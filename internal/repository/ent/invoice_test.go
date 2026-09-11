package ent

import (
	"context"
	"testing"
	"time"

	"github.com/flexprice/flexprice/internal/config"
	domainInvoice "github.com/flexprice/flexprice/internal/domain/invoice"
	"github.com/flexprice/flexprice/internal/logger"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// newTestInvoiceRepository needs a real Postgres instance (see coupon_test.go) -
// an in-memory implementation wouldn't exercise this file's SQL update chain.
func newTestInvoiceRepository(t *testing.T) domainInvoice.Repository {
	t.Helper()
	client := newRealPostgresTestClient(t)
	log, err := logger.NewLogger(&config.Configuration{
		Logging: config.LoggingConfig{Level: types.LogLevelInfo},
	})
	require.NoError(t, err)
	return NewInvoiceRepository(client, log, noopRedisCache{})
}

func testInvoiceContext() context.Context {
	ctx := context.Background()
	ctx = types.SetTenantID(ctx, types.DefaultTenantID)
	ctx = context.WithValue(ctx, types.CtxUserID, types.DefaultUserID)
	return ctx
}

func newTestInvoice(ctx context.Context) *domainInvoice.Invoice {
	zero := decimal.Zero
	return &domainInvoice.Invoice{
		ID:              types.GenerateUUIDWithPrefix(types.UUID_PREFIX_INVOICE),
		CustomerID:      types.GenerateUUIDWithPrefix(types.UUID_PREFIX_CUSTOMER),
		InvoiceType:     types.InvoiceTypeOneOff,
		InvoiceStatus:   types.InvoiceStatusDraft,
		PaymentStatus:   types.PaymentStatusPending,
		Currency:        "usd",
		AmountDue:       zero,
		AmountPaid:      zero,
		AmountRemaining: zero,
		Subtotal:        zero,
		Total:           zero,
		TotalDiscount:   zero,
		TotalTax:        zero,
		EnvironmentID:   types.GetEnvironmentID(ctx),
		BaseModel:       types.GetDefaultBaseModel(ctx),
	}
}

func TestInvoiceRepository_Update_PersistsIsManuallyEdited(t *testing.T) {
	repo := newTestInvoiceRepository(t)
	ctx := testInvoiceContext()

	inv := newTestInvoice(ctx)
	require.NoError(t, repo.Create(ctx, inv))

	got, err := repo.Get(ctx, inv.ID)
	require.NoError(t, err)
	require.False(t, got.IsManuallyEdited, "expected default is_manually_edited to be false")

	got.IsManuallyEdited = true
	require.NoError(t, repo.Update(ctx, got))

	reloaded, err := repo.Get(ctx, inv.ID)
	require.NoError(t, err)
	require.True(t, reloaded.IsManuallyEdited, "is_manually_edited should persist true after Update")
}

func TestInvoiceRepository_ReportingDateIncludesUndatedOneOff(t *testing.T) {
	client := newRealPostgresTestClient(t)
	ctx := types.SetEnvironmentID(testInvoiceContext(), "env_reporting_test")
	require.NoError(t, client.Writer(ctx).Schema.Create(ctx))
	log, err := logger.NewLogger(&config.Configuration{Logging: config.LoggingConfig{Level: types.LogLevelInfo}})
	require.NoError(t, err)
	repo := NewInvoiceRepository(client, log, noopRedisCache{})
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	mid := start.AddDate(0, 0, 10)
	cases := []struct {
		name                     string
		kind                     types.InvoiceType
		period, issue, finalized *time.Time
		want                     bool
	}{
		{"oneoff-issued", types.InvoiceTypeOneOff, nil, &mid, &end, true},
		{"oneoff-finalized", types.InvoiceTypeOneOff, nil, nil, &mid, true},
		{"oneoff-created", types.InvoiceTypeOneOff, nil, nil, nil, true},
		{"dated-subscription", types.InvoiceTypeSubscription, &start, nil, nil, true},
		{"end-exclusive", types.InvoiceTypeOneOff, nil, &end, &mid, false},
		{"undated-subscription", types.InvoiceTypeSubscription, nil, &mid, &mid, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inv := newTestInvoice(ctx)
			inv.IdempotencyKey = &inv.ID
			inv.CreatedAt = mid
			inv.InvoiceType, inv.InvoiceStatus = tc.kind, types.InvoiceStatusFinalized
			inv.PeriodStart, inv.IssueDate, inv.FinalizedAt = tc.period, tc.issue, tc.finalized
			require.NoError(t, repo.Create(ctx, inv))
			filter := types.NewNoLimitInvoiceFilter()
			filter.InvoiceIDs = []string{inv.ID}
			filter.ReportingDateGTE, filter.ReportingDateLT = &start, &end
			rows, err := repo.List(ctx, filter)
			require.NoError(t, err)
			if tc.want {
				require.Len(t, rows, 1)
			} else {
				require.Empty(t, rows)
			}
			other := types.SetEnvironmentID(ctx, "other-env")
			rows, err = repo.List(other, filter)
			require.NoError(t, err)
			require.Empty(t, rows)
		})
	}
}
