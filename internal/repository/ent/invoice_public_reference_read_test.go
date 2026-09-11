package ent

import (
	"context"
	"sync"
	"testing"

	domain "github.com/flexprice/flexprice/internal/domain/invoice"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/stretchr/testify/require"
)

func assertNoReferenceAdvisoryLock(t *testing.T, r *invoiceRepository, ctx context.Context) {
	t.Helper()
	rows, err := r.client.Writer(ctx).QueryContext(ctx, `SELECT count(*) FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory'`)
	require.NoError(t, err)
	defer rows.Close()
	require.True(t, rows.Next())
	var count int
	require.NoError(t, rows.Scan(&count))
	require.Zero(t, count)
}
func TestInvoicePublicReferenceReadsDoNotLockFinancialTransactions(t *testing.T) {
	r, ctx, customer := referenceFixture(t)
	inv := legacyReferenceInvoice(t, r, ctx, customer, nil)
	got, err := r.Get(ctx, inv.ID)
	require.NoError(t, err)
	require.NotEmpty(t, *got.PublicReference)
	require.NoError(t, r.client.WithTx(ctx, func(tx context.Context) error {
		got, err := r.Get(tx, inv.ID)
		require.NoError(t, err)
		require.NotEmpty(t, *got.PublicReference)
		assertNoReferenceAdvisoryLock(t, r, tx)
		return nil
	}))
	unbound := legacyReferenceInvoice(t, r, ctx, customer, nil)
	require.NoError(t, r.client.WithTx(ctx, func(tx context.Context) error {
		got, err := r.GetForUpdate(tx, unbound.ID)
		require.NoError(t, err)
		require.Nil(t, got.PublicReference)
		projected, err := r.Get(tx, unbound.ID)
		require.NoError(t, err)
		require.Empty(t, *projected.PublicReference)
		assertNoReferenceAdvisoryLock(t, r, tx)
		return nil
	}))
	ref, err := r.referenceBy(ctx, "invoice_id", unbound.ID)
	require.NoError(t, err)
	require.Nil(t, ref)
	var wg sync.WaitGroup
	errors := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := r.Get(ctx, inv.ID); errors <- err }()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
}
func TestInvoicePublicReferenceConflictKeepsOtherInvoicesReadable(t *testing.T) {
	r, ctx, customer := referenceFixture(t)
	key := "plaqad-prepaid:unattached-read"
	_, err := r.ReservePublicReference(ctx, domain.ReferenceRequest{ReservationKey: key})
	require.NoError(t, err)
	bad := legacyReferenceInvoice(t, r, ctx, customer, types.Metadata{publicReferenceMetadataKey: key})
	good := legacyReferenceInvoice(t, r, ctx, customer, nil)
	before := financialRow(t, r, ctx, bad.ID)
	got, err := r.Get(ctx, bad.ID)
	require.NoError(t, err)
	require.NotNil(t, got.PublicReference)
	require.Empty(t, *got.PublicReference)
	rows, err := r.List(ctx, types.NewNoLimitInvoiceFilter())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	_, err = r.BindPublicReference(ctx, bad.ID, domain.ReferenceRequest{ExpectedCustomerID: customer})
	require.Error(t, err)
	ref, err := r.referenceBy(ctx, "invoice_id", bad.ID)
	require.NoError(t, err)
	require.Nil(t, ref)
	result, err := r.BackfillPublicReferences(ctx, "", 100, true)
	require.NoError(t, err)
	conflicts := result["conflicts"].([]map[string]string)
	require.Len(t, conflicts, 1)
	require.Equal(t, bad.ID, conflicts[0]["invoice_id"])
	ref, err = r.referenceBy(ctx, "invoice_id", good.ID)
	require.NoError(t, err)
	require.NotNil(t, ref)
	require.Equal(t, before, financialRow(t, r, ctx, bad.ID))
}

func TestInvoicePublicReferenceWrongOrUsedReservationRemainsReadable(t *testing.T) {
	for _, scenario := range []string{"wrong_customer", "already_bound"} {
		t.Run(scenario, func(t *testing.T) {
			r, ctx, customer := referenceFixture(t)
			key := "plaqad-prepaid:conflicting-read"
			_, err := r.ReservePublicReference(ctx, domain.ReferenceRequest{ReservationKey: key, ExpectedCustomerID: customer})
			require.NoError(t, err)
			badCustomer := customer
			if scenario == "wrong_customer" {
				badCustomer = types.GenerateUUIDWithPrefix(types.UUID_PREFIX_CUSTOMER)
				_, err = r.client.Writer(ctx).Customer.Create().SetID(badCustomer).SetTenantID(types.GetTenantID(ctx)).SetEnvironmentID(types.GetEnvironmentID(ctx)).SetExternalID(badCustomer).SetName("Other reference test").Save(ctx)
				require.NoError(t, err)
			} else {
				first := legacyReferenceInvoice(t, r, ctx, customer, types.Metadata{publicReferenceMetadataKey: key})
				_, err = r.Get(ctx, first.ID)
				require.NoError(t, err)
			}
			bad := legacyReferenceInvoice(t, r, ctx, badCustomer, types.Metadata{publicReferenceMetadataKey: key})
			before := financialRow(t, r, ctx, bad.ID)
			got, err := r.Get(ctx, bad.ID)
			require.NoError(t, err)
			require.NotNil(t, got.PublicReference)
			require.Empty(t, *got.PublicReference)
			filter := types.NewNoLimitInvoiceFilter()
			filter.InvoiceIDs = []string{bad.ID}
			rows, err := r.List(ctx, filter)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Empty(t, *rows[0].PublicReference)
			_, err = r.BindPublicReference(ctx, bad.ID, domain.ReferenceRequest{ExpectedCustomerID: badCustomer})
			require.Error(t, err)
			ref, err := r.referenceBy(ctx, "invoice_id", bad.ID)
			require.NoError(t, err)
			require.Nil(t, ref)
			require.Equal(t, before, financialRow(t, r, ctx, bad.ID))
		})
	}
}
