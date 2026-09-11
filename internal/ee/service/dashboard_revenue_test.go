package service

import (
	"context"
	"github.com/flexprice/flexprice/internal/testutil"
	"testing"
	"time"

	"github.com/flexprice/flexprice/internal/api/dto"
	domaininvoice "github.com/flexprice/flexprice/internal/domain/invoice"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRevenueDashboardRequestValidation(t *testing.T) {
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		req     dto.RevenueDashboardRequest
		wantErr bool
	}{
		{
			name: "accepts bounded daily window",
			req: dto.RevenueDashboardRequest{
				PeriodStart: start,
				PeriodEnd:   start.AddDate(0, 3, 0),
				WindowSize:  types.WindowSizeDay,
			},
		},
		{
			name: "accepts bounded monthly window",
			req: dto.RevenueDashboardRequest{
				PeriodStart: start,
				PeriodEnd:   start.AddDate(1, 0, 0),
				WindowSize:  types.WindowSizeMonth,
			},
		},
		{
			name: "rejects unsupported window",
			req: dto.RevenueDashboardRequest{
				PeriodStart: start,
				PeriodEnd:   start.AddDate(0, 1, 0),
				WindowSize:  types.WindowSizeHour,
			},
			wantErr: true,
		},
		{
			name: "rejects unbounded range",
			req: dto.RevenueDashboardRequest{
				PeriodStart: start,
				PeriodEnd:   start.AddDate(1, 0, 2),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestAddRevenueAgingAmountUsesStandardBuckets(t *testing.T) {
	asOf := time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)
	row := &dto.RevenueAgingRow{}
	amount := decimal.NewFromInt(10)
	dueDates := []time.Time{
		asOf.Add(24 * time.Hour),
		asOf.Add(-30 * 24 * time.Hour),
		asOf.Add(-60 * 24 * time.Hour),
		asOf.Add(-90 * 24 * time.Hour),
		asOf.Add(-91 * 24 * time.Hour),
	}

	for i := range dueDates {
		addRevenueAgingAmount(row, amount, &dueDates[i], asOf)
	}

	assert.True(t, row.Current.Equal(decimal.NewFromInt(10)))
	assert.True(t, row.Days1To30.Equal(decimal.NewFromInt(10)))
	assert.True(t, row.Days31To60.Equal(decimal.NewFromInt(10)))
	assert.True(t, row.Days61To90.Equal(decimal.NewFromInt(10)))
	assert.True(t, row.Days91Plus.Equal(decimal.NewFromInt(10)))
	assert.True(t, row.TotalOutstanding.Equal(decimal.NewFromInt(50)))
}

func TestRevenueDashboardAttributionIsExplicit(t *testing.T) {
	planID := "plan_iq"
	planName := "IQ Starter"
	lineItem := &domaininvoice.InvoiceLineItem{
		EntityID:        &planID,
		PlanDisplayName: &planName,
	}
	plans := map[string]revenueDashboardPlanInfo{
		planID: {Name: planName, ProductKey: "iq"},
	}

	gotPlanID, gotPlanLabel, gotProductKey := revenueDashboardLineItemAttribution(lineItem, plans)
	require.Equal(t, planID, gotPlanID)
	assert.Equal(t, planName, gotPlanLabel)
	assert.Equal(t, "iq", gotProductKey)
	assert.Equal(t, "Plaqad IQ", revenueDashboardProductLabel(gotProductKey))
	assert.Equal(t, "Unassigned app", revenueDashboardProductLabel("unassigned"))
	assert.Equal(t, "o•••@example.com", maskRevenueDashboardEmail("owner@example.com"))
}

func TestRevenueDashboardGraphPointsRemainCurrencySafeAndSortable(t *testing.T) {
	jan := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	feb := jan.AddDate(0, 1, 0)
	points := buildRevenueDashboardGraphPointsForWindow(map[time.Time]decimal.Decimal{
		feb: decimal.NewFromInt(20),
		jan: decimal.NewFromInt(10),
	}, types.WindowSizeMonth)

	require.Len(t, points, 2)
	assert.Equal(t, "2026-01", points[0].Label)
	assert.Equal(t, "10", points[0].Value)
	assert.Equal(t, "2026-02", points[1].Label)
}

func TestRevenueDashboardIncludesUndatedReceiptsWithoutInventingEarnedRevenue(t *testing.T) {
	ctx := types.SetEnvironmentID(types.SetTenantID(context.Background(), "tenant-report"), "production")
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	issued := start.Add(10*24*time.Hour + 13*time.Hour)
	paid := issued.Add(5 * time.Minute)
	store := testutil.NewInMemoryInvoiceStore()
	customers := testutil.NewInMemoryCustomerStore()
	service := &dashboardService{ServiceParams: ServiceParams{InvoiceRepo: store, CustomerRepo: customers, SettingsRepo: testutil.NewInMemorySettingsStore()}}
	receipt := &domaininvoice.Invoice{
		ID: "inv_01M289DH22TNWMPDC0GF6TPTMX", CustomerID: "cust_01M1SS60WJMJM3CWPRNWDJ3NEG", InvoiceType: types.InvoiceTypeOneOff,
		InvoiceStatus: types.InvoiceStatusFinalized, PaymentStatus: types.PaymentStatusSucceeded, Currency: "NGN",
		AmountDue: decimal.RequireFromString("331494.06"), AmountPaid: decimal.RequireFromString("331494.06"), AmountRemaining: decimal.Zero,
		FinalizedAt: &issued, PaidAt: &paid, EnvironmentID: "production", BaseModel: types.GetDefaultBaseModel(ctx),
		LineItems: []*domaininvoice.InvoiceLineItem{{Amount: decimal.RequireFromString("331494.06"), Currency: "NGN"}},
	}
	require.NoError(t, store.Create(ctx, receipt))
	// A dated subscription ending at the exact exclusive window boundary belongs
	// to this service period, but an invoice starting at that boundary does not.
	subscription := *receipt
	subscription.ID = "dated-subscription"
	subscription.Currency = "usd"
	subscription.PeriodStart, subscription.PeriodEnd = &start, &end
	subscription.InvoiceType = types.InvoiceTypeSubscription
	subscription.AmountDue, subscription.AmountPaid, subscription.AmountRemaining = decimal.NewFromInt(20), decimal.NewFromInt(5), decimal.NewFromInt(15)
	subscription.PaymentStatus = types.PaymentStatusPartiallyRefunded
	subscription.LineItems = []*domaininvoice.InvoiceLineItem{{Amount: decimal.NewFromInt(20), Currency: "usd", PeriodStart: &start, PeriodEnd: &end}}
	require.NoError(t, store.Create(ctx, &subscription))
	outside := *receipt
	outside.ID, outside.IssueDate = "next-period", &end
	require.NoError(t, store.Create(ctx, &outside))
	draft := *receipt
	draft.ID, draft.InvoiceStatus = "draft", types.InvoiceStatusDraft
	require.NoError(t, store.Create(ctx, &draft))
	req := dto.RevenueDashboardRequest{PeriodStart: start, PeriodEnd: end}
	got, err := service.GetRevenueDashboard(ctx, req)
	require.NoError(t, err)
	require.Equal(t, "331494.06", got.Collections["ngn"].TotalPaid.String())
	require.Equal(t, 1, got.Collections["ngn"].InvoiceCount)
	require.Equal(t, "331494.06", got.Graphs["ngn"].Paid[0].Value)
	require.Empty(t, got.Graphs["ngn"].TotalRevenue)
	require.NotContains(t, got.Summaries, "ngn", "prepaid purchase is not proof of service delivery")
	require.Equal(t, "20", got.Summaries["usd"].TotalRevenue.String())
	require.Equal(t, "20", got.Graphs["usd"].TotalRevenue[0].Value)
	require.Equal(t, "5", got.Collections["usd"].TotalPaid.String(), "use recorded balances without fabricating refund accounting")
	require.Equal(t, "15", got.Collections["usd"].TotalUnpaid.String())
	require.Equal(t, "5", got.Graphs["usd"].Paid[0].Value)
	again, err := service.GetRevenueDashboard(ctx, req)
	require.NoError(t, err)
	require.Equal(t, got, again, "report reads do not change financial records")
	req.CustomerIDs = []string{"absent"}
	filtered, err := service.GetRevenueDashboard(ctx, req)
	require.NoError(t, err)
	require.Empty(t, filtered.Collections)
}

func TestRevenueDashboardCollectionUsesRecordedBalancesIncludingRefunds(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	laterPaid := end.AddDate(0, 0, 5)
	for _, tc := range []struct {
		name            string
		status          types.PaymentStatus
		paid, remaining int64
	}{
		{"unpaid", types.PaymentStatusPending, 0, 20},
		{"part-paid", types.PaymentStatusPending, 5, 15},
		{"paid-after-cohort", types.PaymentStatusSucceeded, 20, 0},
		{"refunded-recorded-balance", types.PaymentStatusRefunded, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv := &domaininvoice.Invoice{ID: "inv-balance", CustomerID: "cust-balance", InvoiceType: types.InvoiceTypeOneOff, IssueDate: &start, PaidAt: &laterPaid, Currency: "NGN", AmountDue: decimal.NewFromInt(20), AmountPaid: decimal.NewFromInt(tc.paid), AmountRemaining: decimal.NewFromInt(tc.remaining), PaymentStatus: tc.status}
			svc := &dashboardService{}
			totals, graphs, _, _, err := svc.buildRevenueDashboardAnalytics(context.Background(), dto.RevenueDashboardRequest{PeriodStart: start, PeriodEnd: end}, []*domaininvoice.Invoice{inv}, nil)
			require.NoError(t, err)
			require.Equal(t, decimal.NewFromInt(tc.paid).String(), totals["ngn"].TotalPaid.String())
			require.Equal(t, decimal.NewFromInt(tc.remaining).String(), totals["ngn"].TotalUnpaid.String())
			require.Equal(t, totals["ngn"].TotalPaid.String(), graphs["ngn"].Paid[0].Value)
			require.Equal(t, "2026-09-01", graphs["ngn"].Paid[0].Label)
		})
	}
}
