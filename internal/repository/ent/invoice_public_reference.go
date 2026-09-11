package ent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/flexprice/flexprice/ent/predicate"
	domain "github.com/flexprice/flexprice/internal/domain/invoice"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/types"
)

const publicReferenceMetadataKey = "plaqad_public_reference_key"

var referenceKeyPattern = regexp.MustCompile(`^plaqad-prepaid:[A-Za-z0-9_-]{1,160}$`)
var customerKeyPattern = regexp.MustCompile(`^cust_[A-Za-z0-9]+$`)

func referenceConflict(message string) error {
	return ierr.NewError(message).WithHint(message).Mark(ierr.ErrValidation)
}
func (r *invoiceRepository) referencesEnabled(ctx context.Context) bool {
	return r.referenceTenantID != "" && types.GetTenantID(ctx) == r.referenceTenantID && types.GetEnvironmentID(ctx) != ""
}
func (r *invoiceRepository) referenceScope(ctx context.Context) error {
	if !r.referencesEnabled(ctx) || types.GetCustomerID(ctx) != "" {
		return ierr.NewError("invoice reference administration is unavailable").Mark(ierr.ErrPermissionDenied)
	}
	return nil
}
func validateReferenceRequest(req domain.ReferenceRequest, reserve bool) error {
	if (reserve || req.ReservationKey != "") && !referenceKeyPattern.MatchString(req.ReservationKey) {
		return referenceConflict("invalid invoice reference reservation key")
	}
	if req.ExpectedCustomerID != "" && !customerKeyPattern.MatchString(req.ExpectedCustomerID) {
		return referenceConflict("invalid expected customer")
	}
	if len(req.Aliases) > 20 {
		return referenceConflict("too many invoice reference aliases")
	}
	for _, alias := range req.Aliases {
		if strings.TrimSpace(alias) == "" || len(alias) > 200 || strings.ContainsAny(alias, "\x00\r\n\t") {
			return referenceConflict("invalid invoice reference alias")
		}
	}
	return nil
}
func (r *invoiceRepository) referenceLock(ctx context.Context) error {
	rows, err := r.client.Writer(ctx).QueryContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "invoice-public-reference:"+types.GetTenantID(ctx)+":"+types.GetEnvironmentID(ctx))
	if err != nil {
		return err
	}
	rows.Close()
	return nil
}
func (r *invoiceRepository) referenceBy(ctx context.Context, column, value string) (*domain.PublicReference, error) {
	if column != "invoice_id" && column != "reservation_key" {
		return nil, referenceConflict("invalid reference lookup")
	}
	rows, err := r.client.Writer(ctx).QueryContext(ctx, `SELECT public_reference,reservation_key,invoice_id,customer_id FROM invoice_public_references WHERE tenant_id=$1 AND environment_id=$2 AND `+column+`=$3`, types.GetTenantID(ctx), types.GetEnvironmentID(ctx), value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	out := &domain.PublicReference{Aliases: []string{}}
	err = rows.Scan(&out.Number, &out.ReservationKey, &out.InvoiceID, &out.CustomerID)
	return out, err
}
func (r *invoiceRepository) referenceAliases(ctx context.Context, ref *domain.PublicReference, aliases []string) error {
	tenant, environment := types.GetTenantID(ctx), types.GetEnvironmentID(ctx)
	// The native number and every alias share one lookup namespace.
	for _, alias := range append([]string{ref.Number}, aliases...) {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		normalized := strings.ToUpper(alias)
		legacyRows, err := r.client.Writer(ctx).QueryContext(ctx, `SELECT id FROM invoices WHERE tenant_id=$1 AND environment_id=$2 AND UPPER(invoice_number)=$3`, tenant, environment, normalized)
		if err != nil {
			return err
		}
		legacyConflict := false
		for legacyRows.Next() {
			var id string
			if err = legacyRows.Scan(&id); err != nil {
				legacyRows.Close()
				return err
			}
			if ref.InvoiceID == nil || *ref.InvoiceID != id {
				legacyConflict = true
			}
		}
		err = legacyRows.Err()
		legacyRows.Close()
		if err != nil {
			return err
		}
		if legacyConflict {
			return referenceConflict("invoice reference alias belongs to an existing invoice number")
		}
		rows, err := r.client.Writer(ctx).QueryContext(ctx, `SELECT public_reference FROM invoice_reference_alias WHERE tenant_id=$1 AND environment_id=$2 AND normalized_alias=$3 UNION SELECT public_reference FROM invoice_public_references WHERE tenant_id=$1 AND environment_id=$2 AND public_reference=$3`, tenant, environment, normalized)
		if err != nil {
			return err
		}
		conflict := false
		for rows.Next() {
			var number string
			if err = rows.Scan(&number); err != nil {
				rows.Close()
				return err
			}
			if number != ref.Number {
				conflict = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if conflict {
			return referenceConflict("invoice reference alias belongs to another invoice")
		}
		_, err = r.client.Writer(ctx).ExecContext(ctx, `INSERT INTO invoice_reference_alias(tenant_id,environment_id,public_reference,alias,normalized_alias,created_by,created_at) VALUES($1,$2,$3,$4,$5,$6,CURRENT_TIMESTAMP) ON CONFLICT(tenant_id,environment_id,normalized_alias) DO NOTHING`, tenant, environment, ref.Number, alias, normalized, types.GetUserID(ctx))
		if err != nil {
			return err
		}
	}
	return r.readReferenceAliases(ctx, ref)
}
func (r *invoiceRepository) readReferenceAliases(ctx context.Context, ref *domain.PublicReference) error {
	rows, err := r.client.Writer(ctx).QueryContext(ctx, `SELECT alias FROM invoice_reference_alias WHERE tenant_id=$1 AND environment_id=$2 AND public_reference=$3 AND normalized_alias<>$3 ORDER BY id`, types.GetTenantID(ctx), types.GetEnvironmentID(ctx), ref.Number)
	if err != nil {
		return err
	}
	defer rows.Close()
	ref.Aliases = []string{}
	for rows.Next() {
		var alias string
		if err = rows.Scan(&alias); err != nil {
			return err
		}
		ref.Aliases = append(ref.Aliases, alias)
	}
	return rows.Err()
}
func (r *invoiceRepository) reserveReferenceLocked(ctx context.Context, key, customer string) (*domain.PublicReference, error) {
	ref, err := r.referenceBy(ctx, "reservation_key", key)
	if err != nil {
		return nil, err
	}
	tenant, environment := types.GetTenantID(ctx), types.GetEnvironmentID(ctx)
	if customer != "" {
		rows, e := r.client.Writer(ctx).QueryContext(ctx, `SELECT id FROM customers WHERE id=$1 AND tenant_id=$2 AND environment_id=$3`, customer, tenant, environment)
		if e != nil {
			return nil, e
		}
		exists := rows.Next()
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		if !exists {
			return nil, referenceConflict("expected billing customer was not found")
		}
	}
	if ref == nil {
		// PUBLIC is a permanent, non-date bucket. It must never be reset or reclaimed.
		for attempts := 0; attempts < 100; attempts++ {
			rows, e := r.client.Writer(ctx).QueryContext(ctx, `INSERT INTO invoice_sequences(tenant_id,environment_id,year_month,last_value,created_at,updated_at) VALUES($1,$2,'PUBLIC',100001,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP) ON CONFLICT(tenant_id,environment_id,year_month) DO UPDATE SET last_value=invoice_sequences.last_value+1,updated_at=CURRENT_TIMESTAMP RETURNING last_value`, tenant, environment)
			if e != nil {
				return nil, e
			}
			var seq int64
			if !rows.Next() {
				rows.Close()
				return nil, referenceConflict("reference sequence unavailable")
			}
			e = rows.Scan(&seq)
			rows.Close()
			if e != nil {
				return nil, e
			}
			number := fmt.Sprint(seq)
			rows, e = r.client.Writer(ctx).QueryContext(ctx, `SELECT 1 FROM invoice_reference_alias WHERE tenant_id=$1 AND environment_id=$2 AND normalized_alias=$3 UNION SELECT 1 FROM invoices WHERE tenant_id=$1 AND environment_id=$2 AND invoice_number=$3`, tenant, environment, number)
			if e != nil {
				return nil, e
			}
			used := rows.Next()
			e = rows.Err()
			rows.Close()
			if e != nil {
				return nil, e
			}
			if used {
				continue
			}
			_, e = r.client.Writer(ctx).ExecContext(ctx, `INSERT INTO invoice_public_references(tenant_id,environment_id,public_reference,reservation_key,customer_id,created_by,created_at) VALUES($1,$2,$3,$4,NULLIF($5,''),$6,CURRENT_TIMESTAMP)`, tenant, environment, number, key, customer, types.GetUserID(ctx))
			if e != nil {
				return nil, e
			}
			ref = &domain.PublicReference{Number: number, ReservationKey: key, Aliases: []string{}}
			if customer != "" {
				ref.CustomerID = &customer
			}
			break
		}
		if ref == nil {
			return nil, referenceConflict("reference sequence collision requires review")
		}
	} else if customer != "" {
		if ref.CustomerID != nil && *ref.CustomerID != customer {
			return nil, referenceConflict("reference is reserved for a different customer")
		}
		if ref.CustomerID == nil {
			_, err = r.client.Writer(ctx).ExecContext(ctx, `UPDATE invoice_public_references SET customer_id=$1 WHERE tenant_id=$2 AND environment_id=$3 AND reservation_key=$4 AND customer_id IS NULL`, customer, tenant, environment, key)
			if err != nil {
				return nil, err
			}
			ref.CustomerID = &customer
		}
	}
	return ref, nil
}
func (r *invoiceRepository) ReservePublicReference(ctx context.Context, req domain.ReferenceRequest) (*domain.PublicReference, error) {
	if err := r.referenceScope(ctx); err != nil {
		return nil, err
	}
	if err := validateReferenceRequest(req, true); err != nil {
		return nil, err
	}
	var ref *domain.PublicReference
	err := r.client.WithTx(ctx, func(tx context.Context) error {
		if e := r.referenceLock(tx); e != nil {
			return e
		}
		var e error
		ref, e = r.reserveReferenceLocked(tx, req.ReservationKey, req.ExpectedCustomerID)
		if e != nil {
			return e
		}
		return r.referenceAliases(tx, ref, req.Aliases)
	})
	return ref, err
}

// Invoice evidence is always re-read on the writer; cached/ caller-supplied metadata cannot adopt a reservation.
func (r *invoiceRepository) bindReferenceLocked(ctx context.Context, id string, req domain.ReferenceRequest) (*domain.PublicReference, error) {
	tenant, environment := types.GetTenantID(ctx), types.GetEnvironmentID(ctx)
	rows, err := r.client.Writer(ctx).QueryContext(ctx, `SELECT customer_id,invoice_number,metadata,subscription_id,COALESCE(billing_reason,'') FROM invoices WHERE id=$1 AND tenant_id=$2 AND environment_id=$3`, id, tenant, environment)
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		rows.Close()
		return nil, ierr.NewError("invoice not found").Mark(ierr.ErrNotFound)
	}
	var customer, reason string
	var legacy, subscription *string
	var metadata []byte
	err = rows.Scan(&customer, &legacy, &metadata, &subscription, &reason)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if req.ExpectedCustomerID != "" && req.ExpectedCustomerID != customer {
		return nil, referenceConflict("invoice does not belong to expected customer")
	}
	if scopeCustomer := types.GetCustomerID(ctx); scopeCustomer != "" && scopeCustomer != customer {
		return nil, ierr.NewError("invoice not found").Mark(ierr.ErrNotFound)
	}
	values := map[string]string{}
	if len(metadata) > 0 {
		if err = json.Unmarshal(metadata, &values); err != nil {
			return nil, err
		}
	}
	key := values[publicReferenceMetadataKey]
	if subscription != nil {
		key = ""
		if reason == string(types.InvoiceBillingReasonSubscriptionCreate) {
			rows, err = r.client.Writer(ctx).QueryContext(ctx, `SELECT metadata FROM subscriptions WHERE id=$1 AND tenant_id=$2 AND environment_id=$3 AND COALESCE(invoicing_customer_id,customer_id)=$4`, *subscription, tenant, environment, customer)
			if err != nil {
				return nil, err
			}
			if !rows.Next() {
				rows.Close()
				return nil, referenceConflict("opening invoice subscription attribution unavailable")
			}
			err = rows.Scan(&metadata)
			rows.Close()
			if err != nil {
				return nil, err
			}
			values = map[string]string{}
			if len(metadata) > 0 {
				if err = json.Unmarshal(metadata, &values); err != nil {
					return nil, err
				}
			}
			key = values[publicReferenceMetadataKey]
		}
	}
	if req.ReservationKey != "" {
		if key != "" && key != req.ReservationKey {
			return nil, referenceConflict("invoice reservation evidence differs")
		}
		key = req.ReservationKey
	}
	existing, err := r.referenceBy(ctx, "invoice_id", id)
	if err != nil {
		return nil, err
	}
	var ref *domain.PublicReference
	if key != "" {
		if !referenceKeyPattern.MatchString(key) {
			return nil, referenceConflict("invalid invoice reservation metadata")
		}
		ref, err = r.referenceBy(ctx, "reservation_key", key)
		if err != nil {
			return nil, err
		}
		if ref == nil || ref.CustomerID == nil || *ref.CustomerID != customer {
			return nil, referenceConflict("invoice reference must be reserved for this customer before creation")
		}
		if ref.InvoiceID != nil && *ref.InvoiceID != id {
			return nil, referenceConflict("invoice reference is already bound")
		}
		if existing != nil && existing.Number != ref.Number {
			return nil, referenceConflict("invoice already has a different public reference")
		}
	} else if existing != nil {
		ref = existing
	} else {
		ref, err = r.reserveReferenceLocked(ctx, "invoice:"+id, customer)
		if err != nil {
			return nil, err
		}
	}
	if ref.InvoiceID == nil {
		_, err = r.client.Writer(ctx).ExecContext(ctx, `UPDATE invoice_public_references SET invoice_id=$1,bound_at=CURRENT_TIMESTAMP WHERE tenant_id=$2 AND environment_id=$3 AND reservation_key=$4 AND invoice_id IS NULL`, id, tenant, environment, ref.ReservationKey)
		if err != nil {
			return nil, err
		}
		ref.InvoiceID = &id
	}
	aliases := append([]string{}, req.Aliases...)
	if legacy != nil && *legacy != "" {
		aliases = append(aliases, *legacy)
	}
	if err = r.referenceAliases(ctx, ref, aliases); err != nil {
		return nil, err
	}
	return ref, nil
}
func (r *invoiceRepository) BindPublicReference(ctx context.Context, id string, req domain.ReferenceRequest) (*domain.PublicReference, error) {
	if err := r.referenceScope(ctx); err != nil {
		return nil, err
	}
	if req.ExpectedCustomerID == "" {
		return nil, referenceConflict("expected_customer_id is required")
	}
	if err := validateReferenceRequest(req, false); err != nil {
		return nil, err
	}
	var ref *domain.PublicReference
	err := r.client.WithTx(ctx, func(tx context.Context) error {
		if e := r.referenceLock(tx); e != nil {
			return e
		}
		var e error
		ref, e = r.bindReferenceLocked(tx, id, req)
		return e
	})
	return ref, err
}
func (r *invoiceRepository) materializeReference(ctx context.Context, inv *domain.Invoice) (*domain.Invoice, error) {
	got, err := r.materializeReferenceValue(ctx, inv, false)
	if ierr.IsValidation(err) {
		// Bad historical reservation metadata must not hide the financial invoice or its neighbours.
		r.logger.Info(ctx, "invoice public reference is unavailable", "invoice_id", inv.ID)
		copy := *inv
		pending := ""
		copy.PublicReference, copy.ReferenceAliases = &pending, nil
		return &copy, nil
	}
	return got, err
}

func (r *invoiceRepository) boundReference(ctx context.Context, inv *domain.Invoice) (*domain.PublicReference, error) {
	// Read authoritative ownership from the writer, without taking an allocation or financial row lock.
	rows, err := r.client.Writer(ctx).QueryContext(ctx, `SELECT pr.public_reference,pr.reservation_key,pr.invoice_id,pr.customer_id,i.invoice_number FROM invoice_public_references pr JOIN invoices i ON i.id=pr.invoice_id AND i.tenant_id=pr.tenant_id AND i.environment_id=pr.environment_id AND i.customer_id=pr.customer_id WHERE pr.tenant_id=$1 AND pr.environment_id=$2 AND pr.invoice_id=$3 AND pr.customer_id=$4`, types.GetTenantID(ctx), types.GetEnvironmentID(ctx), inv.ID, inv.CustomerID)
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		err = rows.Err()
		rows.Close()
		return nil, err
	}
	ref := &domain.PublicReference{}
	var legacy *string
	err = rows.Scan(&ref.Number, &ref.ReservationKey, &ref.InvoiceID, &ref.CustomerID, &legacy)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = r.readReferenceAliases(ctx, ref); err != nil {
		return nil, err
	}
	// Finalization may have added the legacy number after the draft was first assigned.
	if legacy != nil && *legacy != "" && *legacy != ref.Number {
		found := false
		for _, alias := range ref.Aliases {
			if strings.EqualFold(alias, *legacy) {
				found = true
				break
			}
		}
		if !found {
			return nil, nil
		}
	}
	return ref, nil
}

func (r *invoiceRepository) materializeReferenceStrict(ctx context.Context, inv *domain.Invoice) (*domain.Invoice, error) {
	return r.materializeReferenceValue(ctx, inv, true)
}

func (r *invoiceRepository) materializeReferenceValue(ctx context.Context, inv *domain.Invoice, allowAllocationInTx bool) (*domain.Invoice, error) {
	if inv == nil || !r.referencesEnabled(ctx) {
		return inv, nil
	}
	if inv.TenantID != types.GetTenantID(ctx) || inv.EnvironmentID != types.GetEnvironmentID(ctx) {
		return nil, ierr.NewError("invoice reference scope differs").Mark(ierr.ErrPermissionDenied)
	}
	if customer := types.GetCustomerID(ctx); customer != "" && customer != inv.CustomerID {
		return nil, ierr.NewError("invoice not found").Mark(ierr.ErrNotFound)
	}
	ref, err := r.boundReference(ctx, inv)
	if err != nil {
		return nil, err
	}
	if ref == nil && !allowAllocationInTx && r.client.TxFromContext(ctx) != nil {
		// Read projections never acquire the allocation lock inside another financial transaction.
		copy := *inv
		pending := ""
		copy.PublicReference, copy.ReferenceAliases = &pending, nil
		return &copy, nil
	}
	if ref == nil {
		err = r.client.WithTx(ctx, func(tx context.Context) error {
			if e := r.referenceLock(tx); e != nil {
				return e
			}
			var e error
			ref, e = r.bindReferenceLocked(tx, inv.ID, domain.ReferenceRequest{ExpectedCustomerID: inv.CustomerID})
			return e
		})
	}
	if err != nil {
		return nil, err
	}
	copy := *inv
	copy.PublicReference = &ref.Number
	copy.ReferenceAliases = ref.Aliases
	return &copy, nil
}
func referenceSearchPredicate(ctx context.Context, value string, exact bool) predicate.Invoice {
	return func(s *entsql.Selector) {
		// Correlated scope prevents alias lookup from crossing tenant or environment boundaries.
		match := strings.ToUpper(value)
		op := "LIKE"
		if exact {
			op = "="
		} else {
			match = "%" + strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(match) + "%"
		}
		// An older worker may have persisted a reserved opening invoice before Auth's bind retry.
		// Match only the same authoritative metadata and customer that binding itself validates.
		reservationKey := `CASE WHEN ` + s.C("subscription_id") + ` IS NULL THEN ` + s.C("metadata") + `->>'plaqad_public_reference_key' WHEN ` + s.C("billing_reason") + `='SUBSCRIPTION_CREATE' THEN (SELECT sr.metadata->>'plaqad_public_reference_key' FROM subscriptions sr WHERE sr.id=` + s.C("subscription_id") + ` AND sr.tenant_id=` + s.C("tenant_id") + ` AND sr.environment_id=` + s.C("environment_id") + ` AND COALESCE(sr.invoicing_customer_id,sr.customer_id)=` + s.C("customer_id") + `) ELSE NULL END`
		binding := `(pr.invoice_id=` + s.C("id") + ` OR (pr.invoice_id IS NULL AND pr.customer_id=` + s.C("customer_id") + ` AND pr.reservation_key=(` + reservationKey + `)))`
		s.Where(entsql.P(func(b *entsql.Builder) {
			b.WriteString(`EXISTS (SELECT 1 FROM invoice_public_references pr JOIN invoice_reference_alias pa ON pa.tenant_id=pr.tenant_id AND pa.environment_id=pr.environment_id AND pa.public_reference=pr.public_reference WHERE pr.tenant_id=` + s.C("tenant_id") + ` AND pr.environment_id=` + s.C("environment_id") + ` AND ` + binding + ` AND pa.normalized_alias ` + op + ` `).Arg(match).WriteString(")")
		}))
	}
}
func (r *invoiceRepository) BackfillPublicReferences(ctx context.Context, after string, limit int, apply bool) (map[string]interface{}, error) {
	if err := r.referenceScope(ctx); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 200 {
		return nil, referenceConflict("limit must be 1..200")
	}
	rows, err := r.client.Writer(ctx).QueryContext(ctx, `SELECT id FROM invoices WHERE tenant_id=$1 AND environment_id=$2 AND id>$3 ORDER BY id LIMIT $4`, types.GetTenantID(ctx), types.GetEnvironmentID(ctx), after, limit)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	conflicts := []map[string]string{}
	for _, id := range ids {
		if apply {
			err = r.client.WithTx(ctx, func(tx context.Context) error {
				if e := r.referenceLock(tx); e != nil {
					return e
				}
				_, e := r.bindReferenceLocked(tx, id, domain.ReferenceRequest{})
				return e
			})
			if ierr.IsValidation(err) {
				conflicts = append(conflicts, map[string]string{"invoice_id": id, "reason": err.Error()})
			} else if err != nil {
				return nil, err
			}
		}
	}
	next := ""
	if len(ids) > 0 {
		next = ids[len(ids)-1]
	}
	return map[string]interface{}{"apply": apply, "invoice_ids": ids, "next_cursor": next, "count": len(ids), "conflicts": conflicts, "financial_rows_changed": false}, nil
}
