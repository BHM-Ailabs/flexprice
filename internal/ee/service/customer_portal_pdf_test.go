package service

import (
	"context"
	"testing"

	"github.com/cockroachdb/errors"
	"github.com/flexprice/flexprice/internal/domain/customer"
	"github.com/flexprice/flexprice/internal/domain/invoice"
	pdfdata "github.com/flexprice/flexprice/internal/domain/pdf"
	"github.com/flexprice/flexprice/internal/domain/tenant"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/testutil"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Deliberately returns the requested record without filtering: the service must
// also reject a record whose customer, tenant, or environment is wrong.
type portalPDFInvoiceRepo struct {
	invoice.Repository
	inv   *invoice.Invoice
	err   error
	calls int
	ctx   context.Context
	id    string
}

func (r *portalPDFInvoiceRepo) Get(ctx context.Context, id string) (*invoice.Invoice, error) {
	r.calls++
	r.ctx, r.id = ctx, id
	return r.inv, r.err
}

func TestCustomerPortalPDFDeniesBeforeRendering(t *testing.T) {
	repositoryError := errors.New("invoice repository unavailable")
	for _, tc := range []struct {
		name                        string
		customerID, tenantID, envID string
		repoError, wantError        error
		wantReads                   int
	}{
		{"missing customer", "", "tenant_portal", "env_portal", nil, ierr.ErrPermissionDenied, 0},
		{"another customer", "cust_other", "tenant_portal", "env_portal", nil, ierr.ErrNotFound, 1},
		{"another tenant", "cust_portal", "tenant_other", "env_portal", nil, ierr.ErrNotFound, 1},
		{"another environment", "cust_portal", "tenant_portal", "env_other", nil, ierr.ErrNotFound, 1},
		{"missing tenant", "cust_portal", "", "env_portal", nil, ierr.ErrNotFound, 1},
		{"missing environment", "cust_portal", "tenant_portal", "", nil, ierr.ErrNotFound, 1},
		{"missing invoice", "cust_portal", "tenant_portal", "env_portal", ierr.ErrNotFound, ierr.ErrNotFound, 1},
		{"repository error", "cust_portal", "tenant_portal", "env_portal", repositoryError, repositoryError, 1},
	} {
		for _, method := range []string{"binary", "presigned URL"} {
			t.Run(tc.name+"/"+method, func(t *testing.T) {
				ctx := types.SetCustomerID(context.Background(), tc.customerID)
				ctx = types.SetTenantID(ctx, tc.tenantID)
				ctx = types.SetEnvironmentID(ctx, tc.envID)
				repo := &portalPDFInvoiceRepo{
					inv: &invoice.Invoice{
						ID: "inv_portal", CustomerID: "cust_portal", EnvironmentID: "env_portal",
						BaseModel: types.BaseModel{TenantID: "tenant_portal"},
					},
					err: tc.repoError,
				}
				// Rendering/storage dependencies intentionally remain nil. Reaching
				// them before this denial would fail the test rather than leak data.
				svc := &customerPortalService{ServiceParams: ServiceParams{InvoiceRepo: repo}}
				var err error
				if method == "binary" {
					var data []byte
					data, err = svc.GetInvoicePDF(ctx, "inv_portal")
					require.Empty(t, data)
				} else {
					var url string
					url, err = svc.GetInvoicePDFUrl(ctx, "inv_portal")
					require.Empty(t, url)
				}
				require.True(t, errors.Is(err, tc.wantError), "expected %v, got %v", tc.wantError, err)
				require.Equal(t, tc.wantReads, repo.calls)
				if tc.wantReads > 0 {
					require.Equal(t, "inv_portal", repo.id)
					require.Equal(t, tc.tenantID, types.GetTenantID(repo.ctx))
					require.Equal(t, tc.envID, types.GetEnvironmentID(repo.ctx))
				}
			})
		}
	}
}

func TestCustomerPortalPDFBinaryWithoutObjectStorage(t *testing.T) {
	for _, failRender := range []bool{false, true} {
		name := "authorized binary"
		if failRender {
			name = "render error"
		}
		t.Run(name, func(t *testing.T) {
			base := &testutil.BaseServiceTestSuite{}
			base.SetT(t)
			base.SetupSuite()
			base.SetupTest()
			ctx := types.SetEnvironmentID(base.GetContext(), "env_portal")
			ctx = types.SetCustomerID(ctx, "cust_portal")
			stores := base.GetStores()
			base.GetConfig().Auth.Plaqad.TenantID = types.GetTenantID(ctx)
			inv := &invoice.Invoice{
				ID: "inv_portal", CustomerID: "cust_portal", EnvironmentID: "env_portal",
				InvoiceType: types.InvoiceTypeOneOff, InvoiceStatus: types.InvoiceStatusFinalized,
				PaymentStatus: types.PaymentStatusSucceeded, Currency: "usd", Version: 1,
				Subtotal: decimal.NewFromInt(10), Total: decimal.NewFromInt(10),
				AmountDue: decimal.NewFromInt(10), AmountPaid: decimal.NewFromInt(10), AmountRemaining: decimal.Zero,
				BaseModel: types.BaseModel{TenantID: types.GetTenantID(ctx), Status: types.StatusPublished},
			}
			require.NoError(t, stores.InvoiceRepo.Create(ctx, inv))
			require.NoError(t, stores.CustomerRepo.Create(ctx, &customer.Customer{
				ID: "cust_portal", ExternalID: "ws_portal", Name: "Portal Customer", EnvironmentID: "env_portal",
				BaseModel: types.BaseModel{TenantID: types.GetTenantID(ctx), Status: types.StatusPublished},
			}))
			require.NoError(t, stores.TenantRepo.Create(ctx, &tenant.Tenant{ID: types.GetTenantID(ctx), Name: "Plaqad"}))
			generator := base.GetPDFGenerator().(*testutil.MockPDFGenerator)
			want := []byte("%PDF-1.7\nportal fixture\n%%EOF")
			var renderError error
			if failRender {
				want = nil
				renderError = errors.New("PDF rendering unavailable")
			}
			generator.On("RenderInvoicePdf", mock.Anything, mock.Anything, mock.Anything).
				Run(func(args mock.Arguments) {
					data := args.Get(1).(*pdfdata.InvoiceData)
					require.Equal(t, "inv_portal", data.ID)
					require.Equal(t, "Portal Customer", data.Recipient.Name)
					require.True(t, data.PlaqadBranding)
					require.Equal(t, "https://account.plaqad.com/app/billing/invoices/inv_portal?workspaceId=ws_portal", data.AccountURL)
					require.Equal(t, float64(10), data.AmountPaid)
					require.Zero(t, data.AmountRemaining)
				}).Return(want, renderError).Once()
			svc := &customerPortalService{ServiceParams: ServiceParams{
				Config: base.GetConfig(), Logger: base.GetLogger(), PDFGenerator: generator,
				InvoiceRepo: stores.InvoiceRepo, InvoiceLineItemRepo: stores.InvoiceLineItemRepo,
				CustomerRepo: stores.CustomerRepo, TenantRepo: stores.TenantRepo,
				SettingsRepo: stores.SettingsRepo, TaxAppliedRepo: stores.TaxAppliedRepo,
				TaxRateRepo: stores.TaxRateRepo, CouponApplicationRepo: stores.CouponApplicationRepo,
				// No S3, payment, or event publisher dependencies: this path only renders.
			}}
			data, err := svc.GetInvoicePDF(ctx, "inv_portal")
			if failRender {
				require.ErrorIs(t, err, renderError)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, want, data)
			generator.AssertExpectations(t)
			stored, err := stores.InvoiceRepo.Get(ctx, inv.ID)
			require.NoError(t, err)
			require.Equal(t, types.PaymentStatusSucceeded, stored.PaymentStatus)
			require.True(t, stored.AmountPaid.Equal(decimal.NewFromInt(10)))
			require.Equal(t, 1, stored.Version)
		})
	}
}
