package pdf

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	domain "github.com/flexprice/flexprice/internal/domain/pdf"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestPlaqadInvoiceQRData(t *testing.T) {
	for _, name := range []string{"paid", "unpaid", "long"} {
		t.Run(name, func(t *testing.T) {
			data := &domain.InvoiceData{
				PlaqadBranding: true, ID: "inv_preview" + name, InvoiceNumber: "PREVIEW-" + name,
				InvoiceStatus: "FINALIZED", InvoiceType: "ONE_OFF", PaymentStatus: "SUCCEEDED",
				Currency: "₦", Precision: 2, AmountDue: 331494.06, Subtotal: 331494.06, AmountPaid: 331494.06,
				IssuingDate:      domain.CustomTime{Time: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)},
				Biller:           &domain.BillerInfo{Name: "Plaqad", Website: "plaqad.com"},
				Recipient:        &domain.RecipientInfo{Name: "Example customer", Email: "example@example.com"},
				AccountURL:       "https://account.plaqad.com/app/billing/invoices/inv_preview" + name + "?workspaceId=ws_preview",
				AccountLinkLabel: "View this invoice in your Plaqad account",
				LineItems:        []domain.LineItemData{{DisplayName: "25,000 Plaqad credits", Quantity: 25000, Amount: 331494.06}},
			}
			if name == "unpaid" {
				data.AmountPaid, data.AmountRemaining, data.PaymentStatus = 0, 331494.06, "PENDING"
				data.AccountURL, data.AccountLinkLabel = "https://account.plaqad.com", "Open your Plaqad account"
			}
			if name == "long" {
				data.LineItems = nil
				for i := 0; i < 32; i++ {
					data.LineItems = append(data.LineItems, domain.LineItemData{DisplayName: "Example monthly service usage", Quantity: 1, Amount: 10})
				}
				data.Currency, data.AmountDue, data.Subtotal, data.AmountPaid = "$", 320, 320, 320
			}
			compiler := new(MockCompiler)
			compiler.On("CompileTemplate", types.TemplateInvoiceDefault, mock.Anything, mock.Anything).
				Run(func(args mock.Arguments) {
					var rendered struct {
						AccountURL    string  `json:"account_url"`
						AmountPaid    float64 `json:"amount_paid"`
						PaymentStatus string  `json:"payment_status"`
						AccountQRSVG  string  `json:"account_qr_svg"`
					}
					encoded := args.Get(1).([]byte)
					require.NoError(t, json.Unmarshal(encoded, &rendered))
					require.Equal(t, data.AccountURL, rendered.AccountURL)
					require.Equal(t, data.AmountPaid, rendered.AmountPaid)
					require.Equal(t, data.PaymentStatus, rendered.PaymentStatus)
					require.Contains(t, rendered.AccountQRSVG, `<svg xmlns="http://www.w3.org/2000/svg"`)
					require.Contains(t, rendered.AccountQRSVG, `fill="white"`)
					require.Contains(t, rendered.AccountQRSVG, `<g fill="black">`)
					// Optional sanitized inputs for a real Typst/visual/QR smoke.
					if dir := os.Getenv("PLAQAD_PDF_FIXTURE_DIR"); dir != "" {
						require.NoError(t, os.MkdirAll(dir, 0700))
						require.NoError(t, os.WriteFile(filepath.Join(dir, name+".json"), encoded, 0600))
					}
				}).Return([]byte("pdf"), nil)
			_, err := (&service{typst: compiler}).RenderInvoicePdf(context.Background(), data, nil)
			require.NoError(t, err)
			require.Empty(t, data.AccountQRSVG, "render must not mutate caller's invoice snapshot")
			compiler.AssertExpectations(t)
		})
	}
}

func TestUnbrandedInvoiceDoesNotAddAccountQR(t *testing.T) {
	compiler := new(MockCompiler)
	data := &domain.InvoiceData{ID: "other", AccountURL: "https://example.com"}
	encoded, err := json.Marshal(data)
	require.NoError(t, err)
	compiler.On("CompileTemplate", types.TemplateInvoiceDefault, encoded, mock.Anything).Return([]byte("pdf"), nil)
	_, err = (&service{typst: compiler}).RenderInvoicePdf(context.Background(), data, nil)
	require.NoError(t, err)
	compiler.AssertExpectations(t)
}
