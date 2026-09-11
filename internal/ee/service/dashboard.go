package service

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/flexprice/flexprice/internal/api/dto"
	domaininvoice "github.com/flexprice/flexprice/internal/domain/invoice"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/flexprice/flexprice/internal/utils"
	"github.com/shopspring/decimal"
)

// DashboardService provides dashboard functionality
type DashboardService interface {
	GetRevenues(ctx context.Context, req dto.DashboardRevenuesRequest) (*dto.DashboardRevenuesResponse, error)
	GetRevenueDashboard(ctx context.Context, req dto.RevenueDashboardRequest) (*dto.RevenueDashboardResponse, error)
}

type dashboardService struct {
	ServiceParams
}

type revenueDashboardCustomerInfo struct {
	Name       string
	ExternalID string
	Email      string
}

type revenueDashboardPlanInfo struct {
	Name       string
	ProductKey string
}

type revenueDashboardAccumulator struct {
	ID    string
	Label string
	Value decimal.Decimal
}

type revenueDashboardRankingGroups struct {
	Apps       map[string]*revenueDashboardAccumulator
	Users      map[string]*revenueDashboardAccumulator
	Plans      map[string]*revenueDashboardAccumulator
	Workspaces map[string]*revenueDashboardAccumulator
}

type revenueDashboardGraphData struct {
	Recognized map[time.Time]decimal.Decimal
	Invoiced   map[time.Time]decimal.Decimal
	Paid       map[time.Time]decimal.Decimal
}

// NewDashboardService creates a new dashboard service
func NewDashboardService(
	params ServiceParams,
) DashboardService {
	return &dashboardService{
		ServiceParams: params,
	}
}

// GetRevenues returns dashboard revenues data
func (s *dashboardService) GetRevenues(ctx context.Context, req dto.DashboardRevenuesRequest) (*dto.DashboardRevenuesResponse, error) {
	response := &dto.DashboardRevenuesResponse{}

	if err := req.Validate(); err != nil {
		return nil, ierr.WithError(err).
			WithHint("failed to get dashboard revenues").
			Mark(ierr.ErrValidation)
	}

	// Revenue Trend
	if req.RevenueTrend != nil {
		revenueTrend, err := s.getRevenueTrend(ctx, req.RevenueTrend)
		if err != nil {
			s.Logger.Error(ctx, "failed to get revenue trend", "error", err)
			// Continue with other sections even if this fails
		} else {
			response.RevenueTrend = revenueTrend
		}
	}

	// Recent Subscriptions - always fetch
	recentSubs, err := s.getRecentSubscriptions(ctx)
	if err != nil {
		s.Logger.Error(ctx, "failed to get recent subscriptions", "error", err)
		// Continue with other sections even if this fails
	} else {
		response.RecentSubscriptions = recentSubs
	}

	// Invoice Payment Status - always fetch
	paymentStatus, err := s.getInvoicePaymentStatus(ctx)
	if err != nil {
		s.Logger.Error(ctx, "failed to get invoice payment status", "error", err)
		// Continue with other sections even if this fails
	} else {
		response.InvoicePaymentStatus = paymentStatus
	}

	return response, nil
}

// getRevenueTrend calculates revenue trend data using repository
func (s *dashboardService) getRevenueTrend(ctx context.Context, req *dto.RevenueTrendRequest) (*dto.RevenueTrendResponse, error) {
	// Call repository method - always use MONTH window size
	windows, err := s.InvoiceRepo.GetRevenueTrend(ctx, *req.WindowCount)
	if err != nil {
		return nil, err
	}

	// Group by currency
	currencyMap := make(map[string][]types.RevenueWindow)
	for _, w := range windows {
		// Format label for MONTH window size
		label := w.WindowStart.Format("Jan 2006")
		revenueWindow := types.RevenueWindow{
			WindowStart:  w.WindowStart,
			WindowEnd:    w.WindowEnd,
			WindowLabel:  label,
			TotalRevenue: w.Revenue,
		}

		currency := strings.ToLower(strings.TrimSpace(w.Currency))
		if currency == "" {
			return nil, ierr.NewError("currency is missing for revenue data").
				WithHint("Revenue data must include currency information").
				Mark(ierr.ErrValidation)
		}
		currencyMap[currency] = append(currencyMap[currency], revenueWindow)
	}

	// Convert to response structure
	currencyRevenueWindows := make(map[string]dto.CurrencyRevenueWindows)
	for currency, windows := range currencyMap {
		currencyRevenueWindows[currency] = dto.CurrencyRevenueWindows{
			Windows: windows,
		}
	}

	return &dto.RevenueTrendResponse{
		Currency:    currencyRevenueWindows,
		WindowSize:  types.WindowSizeMonth,
		WindowCount: *req.WindowCount,
	}, nil
}

// getRecentSubscriptions gets recent subscriptions grouped by plan using repository
func (s *dashboardService) getRecentSubscriptions(ctx context.Context) (*dto.RecentSubscriptionsResponse, error) {
	// Call repository method
	planCounts, err := s.SubRepo.GetRecentSubscriptionsByPlan(ctx)
	if err != nil {
		return nil, err
	}

	// Convert to DTO
	plans := make([]types.SubscriptionPlanCount, 0, len(planCounts))
	totalCount := 0
	for _, pc := range planCounts {
		plans = append(plans, types.SubscriptionPlanCount{
			PlanID:   pc.PlanID,
			PlanName: pc.PlanName,
			Count:    pc.Count,
		})
		totalCount += pc.Count
	}

	now := time.Now().UTC()
	periodStart := now.AddDate(0, 0, -7) // 7 days ago
	return &dto.RecentSubscriptionsResponse{
		TotalCount:  totalCount,
		Plans:       plans,
		PeriodStart: periodStart,
		PeriodEnd:   now,
	}, nil
}

// getInvoicePaymentStatus gets invoice payment status counts using repository
func (s *dashboardService) getInvoicePaymentStatus(ctx context.Context) (*dto.InvoicePaymentStatusResponse, error) {
	// Call repository method
	status, err := s.InvoiceRepo.GetInvoicePaymentStatus(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	periodStart := now.AddDate(0, 0, -7) // T-7 days from now
	return &dto.InvoicePaymentStatusResponse{
		Paid:        status.Succeeded,
		Pending:     status.Pending,
		Failed:      status.Failed,
		PeriodStart: periodStart,
		PeriodEnd:   now,
	}, nil
}

// GetRevenueDashboard returns revenue analytics with summary tiles and per-customer breakdown.
func (s *dashboardService) GetRevenueDashboard(ctx context.Context, req dto.RevenueDashboardRequest) (*dto.RevenueDashboardResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, ierr.WithError(err).
			WithHint("failed to validate revenue dashboard request").
			Mark(ierr.ErrValidation)
	}

	// Step 1: Check custom analytics config for CPM / voice minutes
	meterID, hasCustomAnalytics := s.resolveVoiceMeterID(ctx)

	// Use the same invoice cohort for totals, service-period revenue, graphs,
	// and leaderboards. Undated prepaid lines remain outside earned revenue.
	invoices, err := s.listRevenueDashboardInvoices(ctx, req)
	if err != nil {
		return nil, err
	}
	revenueRows := []domaininvoice.RevenueByCustomerRow{}
	for _, inv := range invoices {
		for _, line := range inv.LineItems {
			if line == nil || !revenueDashboardLineItemInPeriod(line, req.PeriodStart, req.PeriodEnd) {
				continue
			}
			currency := strings.TrimSpace(line.Currency)
			if currency == "" {
				currency = inv.Currency
			}
			priceType := ""
			if line.PriceType != nil {
				priceType = *line.PriceType
			}
			revenueRows = append(revenueRows, domaininvoice.RevenueByCustomerRow{CustomerID: inv.CustomerID, Currency: currency, PriceType: priceType, Amount: line.Amount})
		}
	}

	var voiceRows []domaininvoice.VoiceMinutesRow
	if hasCustomAnalytics && meterID != "" {
		voiceRows, err = s.InvoiceLineItemRepo.GetVoiceMinutesByCustomer(ctx, req.PeriodStart, req.PeriodEnd, meterID, req.CustomerIDs)
		if err != nil {
			return nil, ierr.WithError(err).
				WithHint("failed to fetch voice minutes by customer").
				Mark(ierr.ErrDatabase)
		}
	}

	// Step 3: Aggregate recognized revenue per (customer, currency).
	type customerKey struct {
		customerID string
		currency   string
	}
	type customerData struct {
		usageRevenue decimal.Decimal
		fixedRevenue decimal.Decimal
		voiceMs      decimal.Decimal
	}
	customerMap := make(map[customerKey]*customerData)

	for _, row := range revenueRows {
		key := customerKey{row.CustomerID, strings.ToLower(row.Currency)}
		cd, ok := customerMap[key]
		if !ok {
			cd = &customerData{}
			customerMap[key] = cd
		}
		if row.PriceType == string(types.PRICE_TYPE_USAGE) {
			cd.usageRevenue = cd.usageRevenue.Add(row.Amount)
		} else {
			cd.fixedRevenue = cd.fixedRevenue.Add(row.Amount)
		}
	}

	// Merge voice minutes (voice has no currency; attach the customer's total to every (customer, currency) entry)
	if hasCustomAnalytics {
		voiceByCustomer := make(map[string]decimal.Decimal)
		for _, row := range voiceRows {
			voiceByCustomer[row.CustomerID] = voiceByCustomer[row.CustomerID].Add(row.UsageMs)
		}
		for key, cd := range customerMap {
			cd.voiceMs = voiceByCustomer[key.customerID]
		}
	}

	// Step 4: Bulk-fetch customer details for enrichment. Include customers that
	// have finalized invoices even when their recognized line-item amount is zero.
	uniqueCustomerIDs := make([]string, 0, len(customerMap))
	seen := make(map[string]bool)
	for key := range customerMap {
		if !seen[key.customerID] {
			seen[key.customerID] = true
			uniqueCustomerIDs = append(uniqueCustomerIDs, key.customerID)
		}
	}
	for _, invoice := range invoices {
		if !seen[invoice.CustomerID] {
			seen[invoice.CustomerID] = true
			uniqueCustomerIDs = append(uniqueCustomerIDs, invoice.CustomerID)
		}
	}

	customerInfoMap := make(map[string]revenueDashboardCustomerInfo, len(uniqueCustomerIDs))

	if len(uniqueCustomerIDs) > 0 {
		custFilter := &types.CustomerFilter{
			QueryFilter: types.NewNoLimitQueryFilter(),
			CustomerIDs: uniqueCustomerIDs,
		}
		customers, err := s.CustomerRepo.List(ctx, custFilter)
		if err != nil {
			s.Logger.Info(ctx, "failed to fetch customer details for revenue dashboard", "error", err)
			// Continue without customer details rather than failing the entire request
		} else {
			for _, c := range customers {
				customerInfoMap[c.ID] = revenueDashboardCustomerInfo{
					Name:       c.Name,
					ExternalID: c.ExternalID,
					Email:      c.Email,
				}
			}
		}
	}

	// Step 5: Build response — filter zero-revenue customers
	msPerMinute := decimal.NewFromInt(60000)

	var items []dto.RevenueDashboardCustomer
	summaries := make(map[string]dto.RevenueDashboardSummary)
	summaryVoiceMsByCur := make(map[string]decimal.Decimal)

	for key, cd := range customerMap {
		totalRevenue := cd.usageRevenue.Add(cd.fixedRevenue)
		if totalRevenue.IsZero() {
			continue
		}

		sum := summaries[key.currency]
		sum.TotalRevenue = sum.TotalRevenue.Add(totalRevenue)
		sum.TotalUsageRevenue = sum.TotalUsageRevenue.Add(cd.usageRevenue)
		sum.TotalFixedRevenue = sum.TotalFixedRevenue.Add(cd.fixedRevenue)
		summaries[key.currency] = sum
		summaryVoiceMsByCur[key.currency] = summaryVoiceMsByCur[key.currency].Add(cd.voiceMs)

		info := customerInfoMap[key.customerID]
		cust := dto.RevenueDashboardCustomer{
			CustomerID:         key.customerID,
			CustomerName:       info.Name,
			ExternalCustomerID: info.ExternalID,
			Currency:           key.currency,
			TotalRevenue:       totalRevenue,
			TotalUsageRevenue:  cd.usageRevenue,
			TotalFixedRevenue:  cd.fixedRevenue,
		}

		if hasCustomAnalytics {
			voiceMinutes := cd.voiceMs.Div(msPerMinute)
			cust.VoiceMinutes = &voiceMinutes

			if !cd.voiceMs.IsZero() {
				cpm := cd.usageRevenue.Div(voiceMinutes)
				cust.CPM = &cpm
			}
		}

		items = append(items, cust)
	}

	// Build per-currency summary voice/CPM
	if hasCustomAnalytics {
		for cur, voiceMsTotal := range summaryVoiceMsByCur {
			sum := summaries[cur]
			voiceMinutes := voiceMsTotal.Div(msPerMinute)
			sum.VoiceMinutes = &voiceMinutes
			if !voiceMsTotal.IsZero() {
				cpm := sum.TotalUsageRevenue.Div(voiceMinutes)
				sum.CPM = &cpm
			}
			summaries[cur] = sum
		}
	}

	// Sort by total revenue descending so highest-revenue customers appear first
	sort.Slice(items, func(i, j int) bool {
		return items[i].TotalRevenue.GreaterThan(items[j].TotalRevenue)
	})

	if items == nil {
		items = []dto.RevenueDashboardCustomer{}
	}

	// The legacy graph omitted currency, so only populate it when the result has a
	// single currency. The currency-keyed graphs below are always safe to render.
	var legacyGraph *dto.RevenueDashboardGraph
	if hasCustomAnalytics && meterID != "" && len(summaries) == 1 {
		const dateTruncMonth = "month"

		voiceTS, vErr := s.InvoiceLineItemRepo.GetVoiceMinutesTimeSeries(ctx, req.PeriodStart, req.PeriodEnd, meterID, dateTruncMonth, req.CustomerIDs)
		if vErr != nil {
			return nil, ierr.WithError(vErr).
				WithHint("failed to fetch voice minutes time series for graph").
				Mark(ierr.ErrDatabase)
		}

		legacyGraph = &dto.RevenueDashboardGraph{
			TotalRevenue: []types.RevenueGraphPoint{},
			Invoiced:     []types.RevenueGraphPoint{},
			Paid:         []types.RevenueGraphPoint{},
			VoiceMinutes: buildVoiceMinutesDashboardGraphPoints(voiceTS),
		}
	}

	collections, graphs, aging, leaderboards, err := s.buildRevenueDashboardAnalytics(
		ctx,
		req,
		invoices,
		customerInfoMap,
	)
	if err != nil {
		return nil, err
	}

	if legacyGraph != nil {
		for currency := range summaries {
			legacyGraph.TotalRevenue = graphs[currency].TotalRevenue
		}
	}

	return &dto.RevenueDashboardResponse{
		Summaries:    summaries,
		Items:        items,
		Collections:  collections,
		Graphs:       graphs,
		Aging:        aging,
		Leaderboards: leaderboards,
		Graph:        legacyGraph,
	}, nil
}

func (s *dashboardService) listRevenueDashboardInvoices(
	ctx context.Context,
	req dto.RevenueDashboardRequest,
) ([]*domaininvoice.Invoice, error) {
	filter := types.NewNoLimitInvoiceFilter()
	filter.InvoiceStatus = []types.InvoiceStatus{types.InvoiceStatusFinalized}
	filter.ReportingDateGTE = &req.PeriodStart
	filter.ReportingDateLT = &req.PeriodEnd
	filter.SkipLineItems = false

	invoices, err := s.InvoiceRepo.List(ctx, filter)
	if err != nil {
		return nil, ierr.WithError(err).
			WithHint("failed to fetch finalized invoices for revenue dashboard").
			Mark(ierr.ErrDatabase)
	}

	if len(req.CustomerIDs) == 0 {
		return invoices, nil
	}

	allowedCustomers := make(map[string]struct{}, len(req.CustomerIDs))
	for _, id := range req.CustomerIDs {
		allowedCustomers[id] = struct{}{}
	}
	filtered := make([]*domaininvoice.Invoice, 0, len(invoices))
	for _, invoice := range invoices {
		if _, ok := allowedCustomers[invoice.CustomerID]; ok {
			filtered = append(filtered, invoice)
		}
	}
	return filtered, nil
}

func (s *dashboardService) buildRevenueDashboardAnalytics(
	ctx context.Context,
	req dto.RevenueDashboardRequest,
	invoices []*domaininvoice.Invoice,
	customers map[string]revenueDashboardCustomerInfo,
) (
	map[string]dto.RevenueCollectionSummary,
	map[string]dto.RevenueDashboardGraph,
	map[string][]dto.RevenueAgingRow,
	map[string]dto.RevenueLeaderboards,
	error,
) {
	planIDs := make([]string, 0)
	seenPlanIDs := make(map[string]struct{})
	for _, invoice := range invoices {
		for _, lineItem := range invoice.LineItems {
			if lineItem == nil || lineItem.EntityID == nil || lineItem.EntityType == nil ||
				!strings.EqualFold(*lineItem.EntityType, string(types.InvoiceLineItemEntityTypePlan)) {
				continue
			}
			if _, ok := seenPlanIDs[*lineItem.EntityID]; !ok {
				seenPlanIDs[*lineItem.EntityID] = struct{}{}
				planIDs = append(planIDs, *lineItem.EntityID)
			}
		}
	}

	plans := make(map[string]revenueDashboardPlanInfo, len(planIDs))
	if len(planIDs) > 0 {
		planRows, err := s.PlanRepo.ListByIDs(ctx, planIDs)
		if err != nil {
			return nil, nil, nil, nil, ierr.WithError(err).
				WithHint("failed to fetch plan attribution for revenue dashboard").
				Mark(ierr.ErrDatabase)
		}
		for _, plan := range planRows {
			productKey := strings.TrimSpace(plan.Metadata["plaqad_product"])
			if productKey == "" {
				productKey = strings.TrimSpace(plan.Metadata["product"])
			}
			plans[plan.ID] = revenueDashboardPlanInfo{
				Name:       plan.Name,
				ProductKey: strings.ToLower(productKey),
			}
		}
	}

	windowSize := req.WindowSize
	if windowSize == "" {
		windowSize = types.WindowSizeDay
	}
	asOf := time.Now().UTC()
	if req.PeriodEnd.Before(asOf) {
		asOf = req.PeriodEnd.UTC()
	}

	collections := make(map[string]dto.RevenueCollectionSummary)
	workspaceSets := make(map[string]map[string]struct{})
	graphData := make(map[string]*revenueDashboardGraphData)
	agingRows := make(map[string]map[string]*dto.RevenueAgingRow)
	rankingGroups := make(map[string]*revenueDashboardRankingGroups)

	for _, invoice := range invoices {
		currency := strings.ToLower(strings.TrimSpace(invoice.Currency))
		if currency == "" {
			continue
		}
		collection := collections[currency]
		collection.TotalInvoiced = collection.TotalInvoiced.Add(invoice.AmountDue)
		collection.TotalPaid = collection.TotalPaid.Add(invoice.AmountPaid)
		remaining := invoice.AmountRemaining
		if remaining.IsNegative() {
			remaining = decimal.Zero
		}
		collection.TotalUnpaid = collection.TotalUnpaid.Add(remaining)
		collection.InvoiceCount++
		if invoice.AmountDue.GreaterThan(decimal.Zero) && remaining.IsZero() {
			collection.PaidInvoiceCount++
		}
		if remaining.GreaterThan(decimal.Zero) && invoice.DueDate != nil && invoice.DueDate.Before(asOf) {
			collection.OverdueInvoiceCount++
		}
		collections[currency] = collection

		if workspaceSets[currency] == nil {
			workspaceSets[currency] = make(map[string]struct{})
		}
		workspaceSets[currency][invoice.CustomerID] = struct{}{}

		graph := ensureRevenueDashboardGraphData(graphData, currency)
		if date := invoice.ReportingDate(); date != nil {
			bucket := revenueDashboardBucketStart(*date, windowSize)
			graph.Invoiced[bucket] = graph.Invoiced[bucket].Add(invoice.AmountDue)
			graph.Paid[bucket] = graph.Paid[bucket].Add(invoice.AmountPaid)
		}

		if remaining.GreaterThan(decimal.Zero) {
			if agingRows[currency] == nil {
				agingRows[currency] = make(map[string]*dto.RevenueAgingRow)
			}
			row, ok := agingRows[currency][invoice.CustomerID]
			if !ok {
				info := customers[invoice.CustomerID]
				name := strings.TrimSpace(info.Name)
				if name == "" {
					name = invoice.CustomerID
				}
				row = &dto.RevenueAgingRow{
					WorkspaceID:   invoice.CustomerID,
					WorkspaceName: name,
				}
				agingRows[currency][invoice.CustomerID] = row
			}
			addRevenueAgingAmount(row, remaining, invoice.DueDate, asOf)
		}

		customerInfo := customers[invoice.CustomerID]
		workspaceLabel := strings.TrimSpace(customerInfo.Name)
		if workspaceLabel == "" {
			workspaceLabel = invoice.CustomerID
		}
		for _, lineItem := range invoice.LineItems {
			if lineItem == nil || !revenueDashboardLineItemInPeriod(lineItem, req.PeriodStart, req.PeriodEnd) {
				continue
			}
			amount := lineItem.Amount
			if amount.IsZero() {
				continue
			}
			lineCurrency := strings.ToLower(strings.TrimSpace(lineItem.Currency))
			if lineCurrency == "" {
				lineCurrency = currency
			}
			lineGraph := ensureRevenueDashboardGraphData(graphData, lineCurrency)
			if lineItem.PeriodStart != nil {
				bucket := revenueDashboardBucketStart(*lineItem.PeriodStart, windowSize)
				lineGraph.Recognized[bucket] = lineGraph.Recognized[bucket].Add(amount)
			}

			lineGroups := ensureRevenueDashboardRankingGroups(rankingGroups, lineCurrency)
			addRevenueDashboardRanking(lineGroups.Workspaces, invoice.CustomerID, workspaceLabel, amount)

			ownerKey := strings.ToLower(strings.TrimSpace(customerInfo.Email))
			ownerLabel := maskRevenueDashboardEmail(ownerKey)
			if ownerKey == "" {
				ownerKey = "unassigned"
				ownerLabel = "Unassigned billing owner"
			}
			addRevenueDashboardRanking(lineGroups.Users, ownerKey, ownerLabel, amount)

			planID, planLabel, productKey := revenueDashboardLineItemAttribution(lineItem, plans)
			addRevenueDashboardRanking(lineGroups.Plans, planID, planLabel, amount)
			addRevenueDashboardRanking(lineGroups.Apps, productKey, revenueDashboardProductLabel(productKey), amount)
		}
	}

	for currency, workspaces := range workspaceSets {
		collection := collections[currency]
		collection.TotalWorkspaces = len(workspaces)
		collections[currency] = collection
	}

	graphs := make(map[string]dto.RevenueDashboardGraph, len(graphData))
	for currency, data := range graphData {
		graphs[currency] = dto.RevenueDashboardGraph{
			TotalRevenue: buildRevenueDashboardGraphPointsForWindow(data.Recognized, windowSize),
			Invoiced:     buildRevenueDashboardGraphPointsForWindow(data.Invoiced, windowSize),
			Paid:         buildRevenueDashboardGraphPointsForWindow(data.Paid, windowSize),
		}
	}

	aging := make(map[string][]dto.RevenueAgingRow, len(agingRows))
	for currency, rowsByWorkspace := range agingRows {
		rows := make([]dto.RevenueAgingRow, 0, len(rowsByWorkspace))
		for _, row := range rowsByWorkspace {
			rows = append(rows, *row)
		}
		sort.Slice(rows, func(i, j int) bool {
			return rows[i].TotalOutstanding.GreaterThan(rows[j].TotalOutstanding)
		})
		aging[currency] = rows
	}

	leaderboards := make(map[string]dto.RevenueLeaderboards, len(rankingGroups))
	for currency, groups := range rankingGroups {
		leaderboards[currency] = dto.RevenueLeaderboards{
			Apps:       topRevenueDashboardRankings(groups.Apps, 5),
			Users:      topRevenueDashboardRankings(groups.Users, 5),
			Plans:      topRevenueDashboardRankings(groups.Plans, 5),
			Workspaces: topRevenueDashboardRankings(groups.Workspaces, 5),
		}
	}

	return collections, graphs, aging, leaderboards, nil
}

func ensureRevenueDashboardGraphData(
	graphs map[string]*revenueDashboardGraphData,
	currency string,
) *revenueDashboardGraphData {
	graph, ok := graphs[currency]
	if !ok {
		graph = &revenueDashboardGraphData{
			Recognized: make(map[time.Time]decimal.Decimal),
			Invoiced:   make(map[time.Time]decimal.Decimal),
			Paid:       make(map[time.Time]decimal.Decimal),
		}
		graphs[currency] = graph
	}
	return graph
}

func ensureRevenueDashboardRankingGroups(
	groups map[string]*revenueDashboardRankingGroups,
	currency string,
) *revenueDashboardRankingGroups {
	group, ok := groups[currency]
	if !ok {
		group = &revenueDashboardRankingGroups{
			Apps:       make(map[string]*revenueDashboardAccumulator),
			Users:      make(map[string]*revenueDashboardAccumulator),
			Plans:      make(map[string]*revenueDashboardAccumulator),
			Workspaces: make(map[string]*revenueDashboardAccumulator),
		}
		groups[currency] = group
	}
	return group
}

func addRevenueDashboardRanking(
	rankings map[string]*revenueDashboardAccumulator,
	id string,
	label string,
	value decimal.Decimal,
) {
	item, ok := rankings[id]
	if !ok {
		item = &revenueDashboardAccumulator{ID: id, Label: label}
		rankings[id] = item
	}
	item.Value = item.Value.Add(value)
}

func topRevenueDashboardRankings(
	rankings map[string]*revenueDashboardAccumulator,
	limit int,
) []dto.RevenueLeaderboardItem {
	items := make([]dto.RevenueLeaderboardItem, 0, len(rankings))
	for _, item := range rankings {
		items = append(items, dto.RevenueLeaderboardItem{
			ID:    item.ID,
			Label: item.Label,
			Value: item.Value,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Value.Equal(items[j].Value) {
			return items[i].Label < items[j].Label
		}
		return items[i].Value.GreaterThan(items[j].Value)
	})
	if len(items) > limit {
		items = items[:limit]
	}
	if items == nil {
		return []dto.RevenueLeaderboardItem{}
	}
	return items
}

func revenueDashboardLineItemInPeriod(
	lineItem *domaininvoice.InvoiceLineItem,
	periodStart time.Time,
	periodEnd time.Time,
) bool {
	if lineItem.PeriodStart == nil || lineItem.PeriodEnd == nil {
		return false
	}
	return !lineItem.PeriodStart.Before(periodStart) && lineItem.PeriodStart.Before(periodEnd) && !lineItem.PeriodEnd.After(periodEnd)
}

func revenueDashboardBucketStart(t time.Time, windowSize types.WindowSize) time.Time {
	t = t.UTC()
	if windowSize == types.WindowSizeMonth {
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func buildRevenueDashboardGraphPointsForWindow(
	agg map[time.Time]decimal.Decimal,
	windowSize types.WindowSize,
) []types.RevenueGraphPoint {
	if len(agg) == 0 {
		return []types.RevenueGraphPoint{}
	}
	keys := make([]time.Time, 0, len(agg))
	for key := range agg {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Before(keys[j]) })

	format := "2006-01-02"
	if windowSize == types.WindowSizeMonth {
		format = "2006-01"
	}
	points := make([]types.RevenueGraphPoint, 0, len(keys))
	for _, key := range keys {
		points = append(points, types.RevenueGraphPoint{
			Label: key.Format(format),
			Value: agg[key].String(),
		})
	}
	return points
}

func addRevenueAgingAmount(
	row *dto.RevenueAgingRow,
	amount decimal.Decimal,
	dueDate *time.Time,
	asOf time.Time,
) {
	row.TotalOutstanding = row.TotalOutstanding.Add(amount)
	if dueDate == nil || !dueDate.Before(asOf) {
		row.Current = row.Current.Add(amount)
		return
	}
	daysOverdue := int(math.Ceil(asOf.Sub(dueDate.UTC()).Hours() / 24))
	switch {
	case daysOverdue <= 30:
		row.Days1To30 = row.Days1To30.Add(amount)
	case daysOverdue <= 60:
		row.Days31To60 = row.Days31To60.Add(amount)
	case daysOverdue <= 90:
		row.Days61To90 = row.Days61To90.Add(amount)
	default:
		row.Days91Plus = row.Days91Plus.Add(amount)
	}
}

func revenueDashboardLineItemAttribution(
	lineItem *domaininvoice.InvoiceLineItem,
	plans map[string]revenueDashboardPlanInfo,
) (string, string, string) {
	planID := "unassigned"
	planLabel := "Unassigned plan"
	productKey := "unassigned"
	if lineItem.EntityID != nil && strings.TrimSpace(*lineItem.EntityID) != "" {
		planID = *lineItem.EntityID
	}
	if lineItem.PlanDisplayName != nil && strings.TrimSpace(*lineItem.PlanDisplayName) != "" {
		planLabel = strings.TrimSpace(*lineItem.PlanDisplayName)
	}
	if plan, ok := plans[planID]; ok {
		if strings.TrimSpace(plan.Name) != "" {
			planLabel = strings.TrimSpace(plan.Name)
		}
		if plan.ProductKey != "" {
			productKey = plan.ProductKey
		}
	}
	return planID, planLabel, productKey
}

func revenueDashboardProductLabel(productKey string) string {
	labels := map[string]string{
		"iq":      "Plaqad IQ",
		"suite":   "Plaqad Suite",
		"pa":      "Plaqad PA",
		"studio":  "Plaqad Studio",
		"maestro": "Plaqad Studio Plus",
		"os":      "Plaqad OS",
		"talent":  "Plaqad Talent",
		"intel":   "Plaqad Intel",
		"spark":   "Plaqad Intel",
	}
	if label, ok := labels[strings.ToLower(productKey)]; ok {
		return label
	}
	if productKey == "" || productKey == "unassigned" {
		return "Unassigned app"
	}
	return productKey
}

func maskRevenueDashboardEmail(email string) string {
	parts := strings.Split(strings.TrimSpace(email), "@")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return string([]rune(parts[0])[0]) + "•••@" + parts[1]
}

func aggregateRevenueDashboardByWindow(rows []domaininvoice.RevenueTimeSeriesRow) map[time.Time]decimal.Decimal {
	out := make(map[time.Time]decimal.Decimal)
	for _, row := range rows {
		out[row.WindowStart.UTC()] = out[row.WindowStart.UTC()].Add(row.Amount)
	}
	return out
}

func buildRevenueDashboardGraphPoints(agg map[time.Time]decimal.Decimal) []types.RevenueGraphPoint {
	if len(agg) == 0 {
		return []types.RevenueGraphPoint{}
	}
	keys := make([]time.Time, 0, len(agg))
	for k := range agg {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Before(keys[j]) })

	points := make([]types.RevenueGraphPoint, 0, len(keys))
	for _, k := range keys {
		points = append(points, types.RevenueGraphPoint{
			Label: revenueDashboardGraphMonthLabel(k),
			Value: agg[k].String(),
		})
	}
	return points
}

func buildVoiceMinutesDashboardGraphPoints(rows []domaininvoice.VoiceMinutesTimeSeriesRow) []types.RevenueGraphPoint {
	if len(rows) == 0 {
		return []types.RevenueGraphPoint{}
	}
	msPerMinute := decimal.NewFromInt(60000)
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].WindowStart.UTC().Before(rows[j].WindowStart.UTC())
	})
	points := make([]types.RevenueGraphPoint, 0, len(rows))
	for _, row := range rows {
		minutes := row.UsageMs.Div(msPerMinute)
		points = append(points, types.RevenueGraphPoint{
			Label: revenueDashboardGraphMonthLabel(row.WindowStart),
			Value: minutes.String(),
		})
	}
	return points
}

func revenueDashboardGraphMonthLabel(t time.Time) string {
	return t.UTC().Format("Jan 2006")
}

// resolveVoiceMeterID checks the custom_analytics_config setting for the
// "revenue-per-minute" rule and resolves the target feature to its meter ID.
// Returns (meterID, true) when custom analytics are active, ("", false) otherwise.
func (s *dashboardService) resolveVoiceMeterID(ctx context.Context) (string, bool) {
	setting, err := s.SettingsRepo.GetByKey(ctx, types.SettingKeyCustomAnalytics)
	if err != nil || setting == nil || setting.Value == nil {
		return "", false
	}

	config, err := utils.ToStruct[types.CustomAnalyticsConfig](setting.Value)
	if err != nil {
		s.Logger.Info(ctx, "failed to parse custom analytics config", "error", err)
		return "", false
	}

	// Find the revenue-per-minute rule
	for _, rule := range config.Rules {
		if types.CustomAnalyticsRuleID(rule.ID) != types.CustomAnalyticsRuleRevenuePerMinute {
			continue
		}

		if rule.TargetType == "feature" {
			feature, err := s.FeatureRepo.Get(ctx, rule.TargetID)
			if err != nil || feature == nil {
				s.Logger.Info(ctx, "failed to resolve feature for revenue-per-minute rule",
					"error", err,
					"target_id", rule.TargetID,
				)
				return "", false
			}
			if feature.MeterID == "" {
				s.Logger.Info(ctx, "feature has no meter_id for revenue-per-minute rule",
					"feature_id", feature.ID,
				)
				return "", false
			}
			return feature.MeterID, true
		}

		if rule.TargetType == "meter" {
			return rule.TargetID, true
		}

		return "", false
	}

	return "", false
}
