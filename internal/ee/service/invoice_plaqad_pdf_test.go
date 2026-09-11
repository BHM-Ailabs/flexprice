package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flexprice/flexprice/internal/config"
	"github.com/flexprice/flexprice/internal/domain/invoice"
	"github.com/flexprice/flexprice/internal/s3"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestPlaqadInvoiceTenantSelection(t *testing.T) {
	for _, tc := range []struct {
		name, configured, tenant string
		ssoEnabled               bool
		want                     bool
	}{
		{"exact tenant with SSO", "tenant_plaqad", "tenant_plaqad", true, true},
		{"exact tenant without SSO", "tenant_plaqad", "tenant_plaqad", false, true},
		{"other tenant", "tenant_plaqad", "tenant_other", true, false},
		{"case mismatch", "tenant_plaqad", "TENANT_PLAQAD", true, false},
		{"empty configuration", "", "", true, false},
		{"empty configuration with tenant", "", "tenant_plaqad", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Configuration{}
			cfg.Auth.Plaqad.TenantID = tc.configured
			cfg.Auth.Plaqad.Enabled = tc.ssoEnabled
			svc := &invoiceService{ServiceParams: ServiceParams{Config: cfg}}
			require.Equal(t, tc.want, svc.isPlaqadInvoiceTenant(tc.tenant))
		})
	}
	t.Run("missing configuration", func(t *testing.T) {
		require.False(t, (&invoiceService{}).isPlaqadInvoiceTenant("tenant_plaqad"))
	})
}

func TestPlaqadInvoiceAccountLink(t *testing.T) {
	url, label := plaqadInvoiceAccountLink("inv_01ABC9", "ws_8c5da40d")
	require.Equal(t, "https://account.plaqad.com/app/billing/invoices/inv_01ABC9?workspaceId=ws_8c5da40d", url)
	require.Equal(t, "View this invoice in your Plaqad account", label)
	for _, tc := range []struct{ invoiceID, workspaceID string }{
		{"", "ws_valid"}, {"inv_valid", ""},
		{"inv_valid", "customer_legacy"}, {"inv_valid", "https://evil.test"},
		{"inv_../evil", "ws_valid"}, {"inv_valid", "ws_valid?next=https://evil.test"},
		{"inv_valid", "ws_valid/path"}, {"inv_valid", "ws_valid#fragment"},
		{"inv_valid", "ws_valid\n"}, {"inv_valid%2fother", "ws_valid"},
		{"inv_" + strings.Repeat("a", 101), "ws_valid"},
		{"inv_valid", "ws_" + strings.Repeat("a", 101)},
	} {
		t.Run(tc.invoiceID+"/"+tc.workspaceID, func(t *testing.T) {
			url, label := plaqadInvoiceAccountLink(tc.invoiceID, tc.workspaceID)
			require.Equal(t, "https://account.plaqad.com", url)
			require.Equal(t, "Open your Plaqad account", label)
		})
	}
}

func plaqadPDFInvoiceFixture() *invoice.Invoice {
	return &invoice.Invoice{
		ID: "inv_01ABC9", CustomerID: "cust_sample", EnvironmentID: "env_production",
		InvoiceType: types.InvoiceTypeOneOff, InvoiceStatus: types.InvoiceStatusFinalized,
		PaymentStatus: types.PaymentStatusPending, Currency: "usd", Version: 1,
		AmountDue: decimal.NewFromInt(10), Total: decimal.NewFromInt(10),
		AmountPaid: decimal.Zero, AmountRemaining: decimal.NewFromInt(10),
		BaseModel: types.BaseModel{TenantID: "tenant_plaqad", UpdatedAt: time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)},
	}
}

func TestPlaqadInvoicePDFCacheKey(t *testing.T) {
	baseline := plaqadPDFInvoiceFixture()
	key := plaqadInvoicePDFCacheKey(baseline)
	require.Contains(t, key, "tenant_plaqad/env_production/plaqad-invoice-v1/inv_01ABC9/")
	require.Len(t, key[strings.LastIndex(key, "/")+1:], 32)
	require.NotEqual(t, "tenant_plaqad/inv_01ABC9", key)
	require.Equal(t, key, plaqadInvoicePDFCacheKey(plaqadPDFInvoiceFixture()))
	for _, tc := range []struct {
		name   string
		change func(*invoice.Invoice)
	}{
		{"partial balance", func(i *invoice.Invoice) {
			i.AmountPaid = decimal.NewFromInt(3)
			i.AmountRemaining = decimal.NewFromInt(7)
		}},
		{"paid status", func(i *invoice.Invoice) { i.PaymentStatus = types.PaymentStatusSucceeded }},
		{"void status", func(i *invoice.Invoice) { i.InvoiceStatus = types.InvoiceStatusVoided }},
		{"version", func(i *invoice.Invoice) { i.Version++ }},
		{"updated time", func(i *invoice.Invoice) { i.UpdatedAt = i.UpdatedAt.Add(time.Second) }},
		{"environment", func(i *invoice.Invoice) { i.EnvironmentID = "env_sandbox" }},
		{"tenant", func(i *invoice.Invoice) { i.TenantID = "tenant_other" }},
		{"description", func(i *invoice.Invoice) { i.Description = "Corrected description" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := plaqadPDFInvoiceFixture()
			tc.change(next)
			require.NotEqual(t, key, plaqadInvoicePDFCacheKey(next))
		})
	}
}

type plaqadPDFInvoiceRepo struct {
	invoice.Repository
	inv *invoice.Invoice
}

func (r *plaqadPDFInvoiceRepo) Get(context.Context, string) (*invoice.Invoice, error) {
	return r.inv, nil
}

type plaqadPDFCache struct {
	s3.Service
	checked []string
	signed  []string
}

func (s *plaqadPDFCache) Exists(_ context.Context, key string, kind s3.DocumentType) (bool, error) {
	s.checked = append(s.checked, key)
	return kind == s3.DocumentTypeInvoice, nil
}

func (s *plaqadPDFCache) GetPresignedUrl(_ context.Context, key string, _ s3.DocumentType) (string, error) {
	s.signed = append(s.signed, key)
	return "https://storage.example.test/branded-invoice", nil
}

func TestPlaqadInvoicePDFURLIgnoresLegacyCache(t *testing.T) {
	inv := plaqadPDFInvoiceFixture()
	legacy := "https://storage.example.test/legacy-unbranded.pdf"
	inv.InvoicePDFURL = &legacy
	cfg := &config.Configuration{}
	cfg.Auth.Plaqad.TenantID = inv.TenantID
	cache := &plaqadPDFCache{}
	svc := &invoiceService{ServiceParams: ServiceParams{
		Config: cfg, InvoiceRepo: &plaqadPDFInvoiceRepo{inv: inv}, S3: cache,
	}}
	url, err := svc.GetInvoicePDFUrl(context.Background(), inv.ID, false)
	require.NoError(t, err)
	require.Equal(t, "https://storage.example.test/branded-invoice", url)
	require.Equal(t, []string{plaqadInvoicePDFCacheKey(inv)}, cache.checked)
	require.Equal(t, cache.checked, cache.signed)
	require.Equal(t, legacy, *inv.InvoicePDFURL, "historical evidence must remain unchanged")

	inv.TenantID = "tenant_other"
	cache.checked, cache.signed = nil, nil
	url, err = svc.GetInvoicePDFUrl(context.Background(), inv.ID, false)
	require.NoError(t, err)
	require.Equal(t, legacy, url)
	require.Empty(t, cache.checked)
	require.Empty(t, cache.signed)

	inv.InvoicePDFURL = nil
	url, err = svc.GetInvoicePDFUrl(context.Background(), inv.ID, false)
	require.NoError(t, err)
	require.Equal(t, "https://storage.example.test/branded-invoice", url)
	require.Equal(t, []string{"tenant_other/inv_01ABC9"}, cache.checked,
		"non-Plaqad cache contract must stay unchanged")
}
