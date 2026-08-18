package paystack

import (
	"context"

	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/interfaces"
	"github.com/flexprice/flexprice/internal/types"
)

type CheckoutAdapter struct {
	Svc         *PaymentService
	CustomerSvc interfaces.CustomerService
	InvoiceSvc  interfaces.InvoiceService
}

func (a *CheckoutAdapter) CreatePaymentLink(ctx context.Context, req interfaces.CheckoutProviderRequest) (*interfaces.CheckoutProviderResponse, error) {
	result, err := a.Svc.CreatePaymentLink(ctx, &CreatePaymentLinkRequest{
		InvoiceID: req.InvoiceID, CustomerID: req.CustomerID, Amount: req.Amount,
		Currency: req.Currency, SuccessURL: req.SuccessURL, CancelURL: req.CancelURL,
		Metadata: req.Metadata, PaymentID: req.PaymentID,
	}, a.CustomerSvc, a.InvoiceSvc)
	if err != nil {
		return nil, err
	}
	return &interfaces.CheckoutProviderResponse{
		ProviderSessionID: result.Reference,
		NextAction: types.PaymentAction{
			Type: types.PaymentActionTypePaymentLink,
			URL:  result.PaymentURL,
		},
		ProviderMetadata: map[string]string{"access_code": result.AccessCode},
	}, nil
}

func (a *CheckoutAdapter) CreateAuthorizationLink(context.Context, interfaces.AuthorizationLinkRequest) (*interfaces.CheckoutProviderResponse, error) {
	return nil, ierr.NewError("Paystack saved-payment authorization is not enabled").
		WithHint("Use send-invoice hosted checkout").
		Mark(ierr.ErrNotImplemented)
}

func (a *CheckoutAdapter) TryAutoChargingSavedMethod(context.Context, interfaces.AuthorizationLinkRequest) (*interfaces.CheckoutProviderResponse, bool, error) {
	return nil, false, nil
}
