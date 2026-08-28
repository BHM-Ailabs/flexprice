package dto

import (
	"time"

	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/shopspring/decimal"
)

// RevenueDashboardRequest represents the request for the revenue dashboard API
type RevenueDashboardRequest struct {
	PeriodStart time.Time        `json:"period_start" binding:"required"`
	PeriodEnd   time.Time        `json:"period_end" binding:"required"`
	CustomerIDs []string         `json:"customer_ids,omitempty"`
	WindowSize  types.WindowSize `json:"window_size,omitempty"`
}

// Validate validates the revenue dashboard request
func (r *RevenueDashboardRequest) Validate() error {
	if r.PeriodStart.IsZero() {
		return ierr.NewError("period_start is required").
			WithHint("period_start must be provided").
			Mark(ierr.ErrValidation)
	}
	if r.PeriodEnd.IsZero() {
		return ierr.NewError("period_end is required").
			WithHint("period_end must be provided").
			Mark(ierr.ErrValidation)
	}
	if !r.PeriodEnd.After(r.PeriodStart) {
		return ierr.NewError("period_end must be after period_start").
			WithHint("period_end must be after period_start").
			WithReportableDetails(map[string]interface{}{
				"period_start": r.PeriodStart,
				"period_end":   r.PeriodEnd,
			}).
			Mark(ierr.ErrValidation)
	}
	const maxDashboardPeriod = 366 * 24 * time.Hour
	if r.PeriodEnd.Sub(r.PeriodStart) > maxDashboardPeriod {
		return ierr.NewError("revenue dashboard period exceeds maximum allowed range").
			WithHint("period_start and period_end must be no more than 366 days apart").
			WithReportableDetails(map[string]interface{}{
				"period_start": r.PeriodStart,
				"period_end":   r.PeriodEnd,
			}).
			Mark(ierr.ErrValidation)
	}
	if r.WindowSize != "" && r.WindowSize != types.WindowSizeDay && r.WindowSize != types.WindowSizeMonth {
		return ierr.NewError("invalid revenue dashboard window_size").
			WithHint("window_size must be DAY or MONTH").
			WithReportableDetails(map[string]interface{}{
				"window_size": r.WindowSize,
			}).
			Mark(ierr.ErrValidation)
	}
	const maxCustomerIDs = 1000
	if len(r.CustomerIDs) > maxCustomerIDs {
		return ierr.NewError("customer_ids exceeds maximum allowed count").
			WithHint("customer_ids must not exceed 1000 entries").
			WithReportableDetails(map[string]interface{}{
				"count": len(r.CustomerIDs),
				"max":   maxCustomerIDs,
			}).
			Mark(ierr.ErrValidation)
	}
	return nil
}

// RevenueDashboardResponse represents the response for the revenue dashboard API.
// Summaries are grouped by lowercase currency code; items carry their own currency.
type RevenueDashboardResponse struct {
	Summaries    map[string]RevenueDashboardSummary  `json:"summaries"`
	Items        []RevenueDashboardCustomer          `json:"items"`
	Collections  map[string]RevenueCollectionSummary `json:"collections"`
	Graphs       map[string]RevenueDashboardGraph    `json:"graphs"`
	Aging        map[string][]RevenueAgingRow        `json:"aging"`
	Leaderboards map[string]RevenueLeaderboards      `json:"leaderboards"`
	// Graph is set only when custom_analytics_config includes the revenue-per-minute rule.
	Graph *RevenueDashboardGraph `json:"graph,omitempty"`
}

// RevenueDashboardGraph holds time-bucketed series for one currency.
type RevenueDashboardGraph struct {
	TotalRevenue []types.RevenueGraphPoint `json:"total_revenue"`
	Invoiced     []types.RevenueGraphPoint `json:"invoiced"`
	Paid         []types.RevenueGraphPoint `json:"paid"`
	VoiceMinutes []types.RevenueGraphPoint `json:"voice_minutes,omitempty"`
}

// RevenueCollectionSummary represents invoice collection totals for one currency.
type RevenueCollectionSummary struct {
	TotalInvoiced       decimal.Decimal `json:"total_invoiced" swaggertype:"string"`
	TotalPaid           decimal.Decimal `json:"total_paid" swaggertype:"string"`
	TotalUnpaid         decimal.Decimal `json:"total_unpaid" swaggertype:"string"`
	TotalWorkspaces     int             `json:"total_workspaces"`
	InvoiceCount        int             `json:"invoice_count"`
	PaidInvoiceCount    int             `json:"paid_invoice_count"`
	OverdueInvoiceCount int             `json:"overdue_invoice_count"`
}

// RevenueAgingRow groups outstanding invoice balances for one workspace.
type RevenueAgingRow struct {
	WorkspaceID      string          `json:"workspace_id"`
	WorkspaceName    string          `json:"workspace_name"`
	Current          decimal.Decimal `json:"current" swaggertype:"string"`
	Days1To30        decimal.Decimal `json:"days_1_30" swaggertype:"string"`
	Days31To60       decimal.Decimal `json:"days_31_60" swaggertype:"string"`
	Days61To90       decimal.Decimal `json:"days_61_90" swaggertype:"string"`
	Days91Plus       decimal.Decimal `json:"days_91_plus" swaggertype:"string"`
	TotalOutstanding decimal.Decimal `json:"total_outstanding" swaggertype:"string"`
}

// RevenueLeaderboardItem is one ranked revenue attribution result.
type RevenueLeaderboardItem struct {
	ID    string          `json:"id"`
	Label string          `json:"label"`
	Value decimal.Decimal `json:"value" swaggertype:"string"`
}

// RevenueLeaderboards contains the top five results for each supported attribution basis.
type RevenueLeaderboards struct {
	Apps       []RevenueLeaderboardItem `json:"apps"`
	Users      []RevenueLeaderboardItem `json:"users"`
	Plans      []RevenueLeaderboardItem `json:"plans"`
	Workspaces []RevenueLeaderboardItem `json:"workspaces"`
}

// RevenueDashboardSummary represents aggregate revenue metrics across all customers
type RevenueDashboardSummary struct {
	TotalRevenue      decimal.Decimal  `json:"total_revenue" swaggertype:"string"`
	TotalUsageRevenue decimal.Decimal  `json:"total_usage_revenue" swaggertype:"string"`
	TotalFixedRevenue decimal.Decimal  `json:"total_fixed_revenue" swaggertype:"string"`
	CPM               *decimal.Decimal `json:"cpm,omitempty" swaggertype:"string"`
	VoiceMinutes      *decimal.Decimal `json:"voice_minutes,omitempty" swaggertype:"string"`
}

// RevenueDashboardCustomer represents per-customer revenue data
type RevenueDashboardCustomer struct {
	CustomerID         string           `json:"customer_id"`
	CustomerName       string           `json:"customer_name"`
	ExternalCustomerID string           `json:"external_customer_id"`
	Currency           string           `json:"currency"`
	TotalRevenue       decimal.Decimal  `json:"total_revenue" swaggertype:"string"`
	TotalUsageRevenue  decimal.Decimal  `json:"total_usage_revenue" swaggertype:"string"`
	TotalFixedRevenue  decimal.Decimal  `json:"total_fixed_revenue" swaggertype:"string"`
	CPM                *decimal.Decimal `json:"cpm,omitempty" swaggertype:"string"`
	VoiceMinutes       *decimal.Decimal `json:"voice_minutes,omitempty" swaggertype:"string"`
}
