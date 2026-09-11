package ent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/flexprice/flexprice/internal/config"
	domain "github.com/flexprice/flexprice/internal/domain/invoice"
	"github.com/flexprice/flexprice/internal/logger"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func referenceFixture(t *testing.T) (*invoiceRepository, context.Context, string) {
	t.Helper()
	client := newRealPostgresTestClient(t)
	ctx := types.SetTenantID(context.Background(), "tenant_ref_test")
	ctx = types.SetEnvironmentID(ctx, types.GenerateUUIDWithPrefix("env"))
	ctx = context.WithValue(ctx, types.CtxUserID, "operator")
	require.NoError(t, client.Writer(ctx).Schema.Create(ctx))
	log, err := logger.NewLogger(&config.Configuration{Logging: config.LoggingConfig{Level: types.LogLevelInfo}})
	require.NoError(t, err)
	r := NewInvoiceRepository(client, log, noopRedisCache{}, "tenant_ref_test").(*invoiceRepository)
	id := types.GenerateUUIDWithPrefix(types.UUID_PREFIX_CUSTOMER)
	_, err = client.Writer(ctx).Customer.Create().SetID(id).SetTenantID(types.GetTenantID(ctx)).SetEnvironmentID(types.GetEnvironmentID(ctx)).SetExternalID(id).SetName("Reference test").Save(ctx)
	require.NoError(t, err)
	return r, ctx, id
}
func legacyReferenceInvoice(t *testing.T, r *invoiceRepository, ctx context.Context, customer string, metadata types.Metadata) *domain.Invoice {
	t.Helper()
	inv := newTestInvoice(ctx)
	inv.CustomerID = customer
	inv.IdempotencyKey = &inv.ID
	inv.Metadata = metadata
	legacy := "INV-202609-" + inv.ID[len(inv.ID)-5:]
	inv.InvoiceNumber = &legacy
	inv.AmountDue = decimal.RequireFromString("331494.06")
	inv.Total = inv.AmountDue
	inv.Subtotal = inv.AmountDue
	inv.AmountPaid = inv.AmountDue
	inv.AmountRemaining = decimal.Zero
	inv.InvoiceStatus = types.InvoiceStatusFinalized
	inv.PaymentStatus = types.PaymentStatusSucceeded
	old := NewInvoiceRepository(r.client, r.logger, noopRedisCache{})
	require.NoError(t, old.Create(ctx, inv))
	return inv
}
func financialRow(t *testing.T, r *invoiceRepository, ctx context.Context, id string) string {
	t.Helper()
	rows, err := r.client.Writer(ctx).QueryContext(ctx, `SELECT row_to_json(i)::text FROM invoices i WHERE id=$1`, id)
	require.NoError(t, err)
	defer rows.Close()
	require.True(t, rows.Next())
	var value string
	require.NoError(t, rows.Scan(&value))
	return value
}
func TestInvoicePublicReferenceReservations(t *testing.T) {
	r, ctx, customer := referenceFixture(t)
	req := domain.ReferenceRequest{ReservationKey: "plaqad-prepaid:one", Aliases: []string{"PLQ-OLD-ONE"}}
	ref, err := r.ReservePublicReference(ctx, req)
	require.NoError(t, err)
	require.Equal(t, "100001", ref.Number)
	require.Nil(t, ref.InvoiceID)
	require.Nil(t, ref.CustomerID)
	again, err := r.ReservePublicReference(ctx, req)
	require.NoError(t, err)
	require.Equal(t, ref, again)
	req.ExpectedCustomerID = customer
	ref, err = r.ReservePublicReference(ctx, req)
	require.NoError(t, err)
	require.Equal(t, customer, *ref.CustomerID)
	inv := legacyReferenceInvoice(t, r, ctx, customer, types.Metadata{publicReferenceMetadataKey: req.ReservationKey})
	before := financialRow(t, r, ctx, inv.ID)
	got, err := r.Get(ctx, inv.ID)
	require.NoError(t, err)
	require.Equal(t, ref.Number, *got.PublicReference)
	require.Contains(t, got.ReferenceAliases, "PLQ-OLD-ONE")
	require.Equal(t, before, financialRow(t, r, ctx, inv.ID))
	bound, err := r.BindPublicReference(ctx, inv.ID, domain.ReferenceRequest{ReservationKey: req.ReservationKey, ExpectedCustomerID: customer, Aliases: []string{"AUTH-PAID-OLD"}})
	require.NoError(t, err)
	require.Equal(t, inv.ID, *bound.InvoiceID)
	bound2, err := r.BindPublicReference(ctx, inv.ID, domain.ReferenceRequest{ExpectedCustomerID: customer, Aliases: []string{"AUTH-PAID-OLD"}})
	require.NoError(t, err)
	require.Equal(t, bound, bound2)
	other := legacyReferenceInvoice(t, r, ctx, customer, nil)
	_, err = r.BindPublicReference(ctx, other.ID, domain.ReferenceRequest{ReservationKey: req.ReservationKey, ExpectedCustomerID: customer})
	require.ErrorContains(t, err, "already bound")
	_, err = r.BindPublicReference(ctx, inv.ID, domain.ReferenceRequest{ExpectedCustomerID: "cust_WRONG"})
	require.Error(t, err)
	for _, wrong := range []context.Context{types.SetTenantID(ctx, "other"), types.SetEnvironmentID(ctx, "other"), context.WithValue(ctx, types.CtxCustomerID, "cust_WRONG")} {
		_, err = r.Get(wrong, inv.ID)
		require.Error(t, err)
	}
	req.ExpectedCustomerID = "cust_WRONG"
	_, err = r.ReservePublicReference(ctx, req)
	require.Error(t, err)
	require.Equal(t, before, financialRow(t, r, ctx, inv.ID))
}
func TestInvoicePublicReferenceConcurrency(t *testing.T) {
	r, ctx, customer := referenceFixture(t)
	var wg sync.WaitGroup
	results := make(chan string, 16)
	failures := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ref, err := r.ReservePublicReference(ctx, domain.ReferenceRequest{ReservationKey: "plaqad-prepaid:race", ExpectedCustomerID: customer})
			if err != nil {
				failures <- err
			} else {
				results <- ref.Number
			}
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	for number := range results {
		require.Equal(t, "100001", number)
	}
	for i := 0; i < 4; i++ {
		ref, err := r.ReservePublicReference(ctx, domain.ReferenceRequest{ReservationKey: fmt.Sprintf("plaqad-prepaid:next%d", i)})
		require.NoError(t, err)
		require.Equal(t, fmt.Sprint(100002+i), ref.Number)
	}
}
func TestInvoicePublicReferenceFailClosedAndAliases(t *testing.T) {
	r, ctx, customer := referenceFixture(t)
	for _, key := range []string{"missing", "unattached"} {
		req := domain.ReferenceRequest{ReservationKey: "plaqad-prepaid:" + key}
		if key == "unattached" {
			_, err := r.ReservePublicReference(ctx, req)
			require.NoError(t, err)
		}
		inv := legacyReferenceInvoice(t, r, ctx, customer, types.Metadata{publicReferenceMetadataKey: req.ReservationKey})
		before := financialRow(t, r, ctx, inv.ID)
		got, err := r.Get(ctx, inv.ID)
		require.NoError(t, err)
		require.NotNil(t, got.PublicReference)
		require.Empty(t, *got.PublicReference)
		_, err = r.BindPublicReference(ctx, inv.ID, domain.ReferenceRequest{ExpectedCustomerID: customer})
		require.ErrorContains(t, err, "before creation")
		require.Equal(t, before, financialRow(t, r, ctx, inv.ID))
	}
	inv := legacyReferenceInvoice(t, r, ctx, customer, nil)
	ref, err := r.BindPublicReference(ctx, inv.ID, domain.ReferenceRequest{ExpectedCustomerID: customer, Aliases: []string{"Historical-Mixed-Case"}})
	require.NoError(t, err)
	other := legacyReferenceInvoice(t, r, ctx, customer, nil)
	_, err = r.BindPublicReference(ctx, other.ID, domain.ReferenceRequest{ExpectedCustomerID: customer, Aliases: []string{"historical-mixed-case"}})
	require.Error(t, err)
	for _, q := range []string{ref.Number, "historical-mixed-case", *inv.InvoiceNumber, inv.ID} {
		f := types.NewNoLimitInvoiceFilter()
		f.Search = q
		rows, err := r.List(ctx, f)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, inv.ID, rows[0].ID)
		n, err := r.Count(ctx, f)
		require.NoError(t, err)
		require.Equal(t, 1, n)
	}
	for _, op := range []types.FilterOperatorType{types.CONTAINS, types.EQUAL} {
		f := types.NewNoLimitInvoiceFilter()
		field, value, typ := "invoice_reference", "Historical-Mixed-Case", types.DataTypeString
		f.Filters = []*types.FilterCondition{{Field: &field, Operator: &op, DataType: &typ, Value: &types.Value{String: &value}}}
		rows, err := r.List(ctx, f)
		require.NoError(t, err)
		require.Len(t, rows, 1)
	}
}
func TestInvoicePublicReferenceBackfillNoFinancialChanges(t *testing.T) {
	r, ctx, customer := referenceFixture(t)
	inv := legacyReferenceInvoice(t, r, ctx, customer, nil)
	before := financialRow(t, r, ctx, inv.ID)
	plan, err := r.BackfillPublicReferences(ctx, "", 100, false)
	require.NoError(t, err)
	require.Equal(t, 1, plan["count"])
	ref, err := r.referenceBy(ctx, "invoice_id", inv.ID)
	require.NoError(t, err)
	require.Nil(t, ref)
	for i := 0; i < 2; i++ {
		_, err = r.BackfillPublicReferences(ctx, "", 100, true)
		require.NoError(t, err)
	}
	got, err := r.Get(ctx, inv.ID)
	require.NoError(t, err)
	require.Equal(t, "100001", *got.PublicReference)
	require.Equal(t, before, financialRow(t, r, ctx, inv.ID))
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"public_reference":"100001"`)
}

func TestInvoicePublicReferenceOpeningAndRenewal(t *testing.T) {
	r, ctx, customer := referenceFixture(t)
	key := "plaqad-prepaid:monthly"
	reserved, err := r.ReservePublicReference(ctx, domain.ReferenceRequest{ReservationKey: key, ExpectedCustomerID: customer})
	require.NoError(t, err)
	sid := types.GenerateUUIDWithPrefix(types.UUID_PREFIX_SUBSCRIPTION)
	_, err = r.client.Writer(ctx).Subscription.Create().SetID(sid).SetTenantID(types.GetTenantID(ctx)).SetEnvironmentID(types.GetEnvironmentID(ctx)).SetCustomerID(customer).SetPlanID("plan_reference_test").SetCurrency("ngn").SetBillingCadence(types.BILLING_CADENCE_RECURRING).SetBillingPeriod(types.BILLING_PERIOD_MONTHLY).SetMetadata(map[string]string{publicReferenceMetadataKey: key}).Save(ctx)
	require.NoError(t, err)
	numbers := []string{}
	for i, reason := range []types.InvoiceBillingReason{types.InvoiceBillingReasonSubscriptionCreate, types.InvoiceBillingReasonSubscriptionCycle} {
		inv := legacyReferenceInvoice(t, r, ctx, customer, types.Metadata{publicReferenceMetadataKey: key})
		_, err = r.client.Writer(ctx).ExecContext(ctx, `UPDATE invoices SET subscription_id=$1,billing_reason=$2,invoice_type='SUBSCRIPTION' WHERE id=$3`, sid, string(reason), inv.ID)
		require.NoError(t, err)
		// Searching an issued reservation works before Get or Auth's bind retry reaches the old-worker row.
		if i == 0 {
			filter := types.NewNoLimitInvoiceFilter()
			filter.Search = reserved.Number
			count, err := r.Count(ctx, filter)
			require.NoError(t, err)
			require.Equal(t, 1, count)
			matches, err := r.List(ctx, filter)
			require.NoError(t, err)
			require.Len(t, matches, 1)
			require.Equal(t, inv.ID, matches[0].ID)
		}
		// Simulates old worker rows before the current API sees them. Parent metadata binds only opening.
		got, err := r.Get(ctx, inv.ID)
		require.NoError(t, err)
		numbers = append(numbers, *got.PublicReference)
		if i == 0 {
			require.Equal(t, reserved.Number, *got.PublicReference)
		}
	}
	require.NotEqual(t, numbers[0], numbers[1])
}

func TestInvoicePublicReferenceCreateAtomicRetryAndLegacyNamespace(t *testing.T) {
	r, ctx, customer := referenceFixture(t)
	legacy := legacyReferenceInvoice(t, r, ctx, customer, nil)
	_, err := r.ReservePublicReference(ctx, domain.ReferenceRequest{ReservationKey: "plaqad-prepaid:collision", Aliases: []string{*legacy.InvoiceNumber}})
	require.ErrorContains(t, err, "existing invoice number")
	ref, err := r.referenceBy(ctx, "reservation_key", "plaqad-prepaid:collision")
	require.NoError(t, err)
	require.Nil(t, ref)
	got, err := r.Get(ctx, legacy.ID)
	require.NoError(t, err)
	require.Equal(t, "100001", *got.PublicReference)
	require.Contains(t, got.ReferenceAliases, *legacy.InvoiceNumber)

	inv := newTestInvoice(ctx)
	inv.CustomerID = customer
	inv.IdempotencyKey = &inv.ID
	inv.Metadata = types.Metadata{publicReferenceMetadataKey: "plaqad-prepaid:atomic"}
	err = r.Create(ctx, inv)
	require.ErrorContains(t, err, "before creation")
	row, err := r.client.Writer(ctx).Invoice.Get(ctx, inv.ID)
	require.Error(t, err)
	require.Nil(t, row)
	_, err = r.ReservePublicReference(ctx, domain.ReferenceRequest{ReservationKey: "plaqad-prepaid:atomic", ExpectedCustomerID: customer})
	require.NoError(t, err)
	require.NoError(t, r.Create(ctx, inv))
	recovered, err := r.GetByIdempotencyKey(ctx, *inv.IdempotencyKey)
	require.NoError(t, err)
	require.Equal(t, inv.ID, recovered.ID)
	require.Equal(t, "100002", *recovered.PublicReference)
}

func TestInvoicePublicReferenceCrossScopeJobListingDoesNotMaterialize(t *testing.T) {
	r, ctx, customer := referenceFixture(t)
	first := legacyReferenceInvoice(t, r, ctx, customer, nil)
	otherCtx := types.SetEnvironmentID(ctx, "other-"+types.GetEnvironmentID(ctx))
	second := legacyReferenceInvoice(t, r, otherCtx, customer, types.Metadata{publicReferenceMetadataKey: "plaqad-prepaid:unattached"})
	filter := types.NewNoLimitInvoiceFilter()
	filter.InvoiceIDs = []string{first.ID, second.ID}
	got, err := r.ListAllTenant(ctx, filter)
	require.NoError(t, err)
	require.Len(t, got, 2)
	for _, inv := range got {
		require.Nil(t, inv.PublicReference)
	}
}

func TestInvoicePublicReferenceSearchReservedOneOffBeforeBind(t *testing.T) {
	r, ctx, customer := referenceFixture(t)
	key := "plaqad-prepaid:search-recovery"
	reserved, err := r.ReservePublicReference(ctx, domain.ReferenceRequest{ReservationKey: key, ExpectedCustomerID: customer, Aliases: []string{"OLD-PREPAID-REFERENCE"}})
	require.NoError(t, err)
	inv := legacyReferenceInvoice(t, r, ctx, customer, types.Metadata{publicReferenceMetadataKey: key})
	before := financialRow(t, r, ctx, inv.ID)
	filter := types.NewNoLimitInvoiceFilter()
	filter.Search = "old-prepaid-reference"
	count, err := r.Count(ctx, filter)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	unbound, err := r.referenceBy(ctx, "reservation_key", key)
	require.NoError(t, err)
	require.Nil(t, unbound.InvoiceID)
	matches, err := r.List(ctx, filter)
	require.NoError(t, err)
	require.Len(t, matches, 1)
	require.Equal(t, reserved.Number, *matches[0].PublicReference)
	require.Equal(t, before, financialRow(t, r, ctx, inv.ID))
	for _, wrong := range []context.Context{types.SetTenantID(ctx, "other"), types.SetEnvironmentID(ctx, "other")} {
		count, err := r.Count(wrong, filter)
		require.NoError(t, err)
		require.Zero(t, count)
	}
}
