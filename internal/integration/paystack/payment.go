package paystack

import (
	"context"
	"regexp"
	"strings"

	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/interfaces"
	"github.com/flexprice/flexprice/internal/logger"
	"github.com/flexprice/flexprice/internal/types"
)

var invalidReferenceCharacters = regexp.MustCompile(`[^A-Za-z0-9.=-]+`)

type PaymentService struct {
	client Client
	logger *logger.Logger
}

func NewPaymentService(client Client, logger *logger.Logger) *PaymentService {
	return &PaymentService{client: client, logger: logger}
}

func (s *PaymentService) CreatePaymentLink(
	ctx context.Context,
	req *CreatePaymentLinkRequest,
	customerService interfaces.CustomerService,
	invoiceService interfaces.InvoiceService,
) (*PaymentLinkResponse, error) {
	invoiceResp, err := invoiceService.GetInvoice(ctx, req.InvoiceID)
	if err != nil {
		return nil, ierr.WithError(err).WithHint("Invoice not found").Mark(ierr.ErrNotFound)
	}
	if invoiceResp.PaymentStatus == types.PaymentStatusSucceeded {
		return nil, ierr.NewError("invoice is already paid").Mark(ierr.ErrValidation)
	}
	if invoiceResp.InvoiceStatus == types.InvoiceStatusVoided {
		return nil, ierr.NewError("invoice is voided").Mark(ierr.ErrValidation)
	}
	if req.Amount.GreaterThan(invoiceResp.AmountRemaining) || !strings.EqualFold(req.Currency, invoiceResp.Currency) {
		return nil, ierr.NewError("payment amount or currency does not match the invoice").
			WithHint("Use the invoice remaining amount and currency").
			Mark(ierr.ErrValidation)
	}
	if invoiceResp.CustomerID != req.CustomerID {
		return nil, ierr.NewError("payment customer does not match the invoice customer").Mark(ierr.ErrValidation)
	}

	customerResp, err := customerService.GetCustomer(ctx, req.CustomerID)
	if err != nil {
		return nil, ierr.WithError(err).WithHint("Customer not found").Mark(ierr.ErrNotFound)
	}
	if customerResp.Email == "" {
		return nil, ierr.NewError("customer email is required for Paystack checkout").
			WithHint("Add an email address to the FlexPrice customer").
			Mark(ierr.ErrValidation)
	}

	reference := referenceForPaymentID(req.PaymentID)
	metadata := map[string]any{
		"flexprice_payment_id":  req.PaymentID,
		"flexprice_invoice_id":  req.InvoiceID,
		"flexprice_customer_id": req.CustomerID,
		"payment_source":        "flexprice",
	}
	for key, value := range req.Metadata {
		metadata[key] = value
	}

	result, err := s.client.InitializeTransaction(ctx, InitializeTransactionRequest{
		Email:       customerResp.Email,
		Amount:      types.ToSmallestUnit(req.Amount, req.Currency),
		Currency:    strings.ToUpper(req.Currency),
		Reference:   reference,
		CallbackURL: req.SuccessURL,
		Metadata:    metadata,
	})
	if err != nil {
		return nil, err
	}

	s.logger.Info(ctx, "Paystack payment link created",
		"flexprice_payment_id", req.PaymentID,
		"invoice_id", req.InvoiceID,
		"paystack_reference", result.Reference)

	return &PaymentLinkResponse{
		Reference:  result.Reference,
		PaymentURL: result.AuthorizationURL,
		AccessCode: result.AccessCode,
		Amount:     req.Amount,
		Currency:   strings.ToUpper(req.Currency),
		PaymentID:  req.PaymentID,
	}, nil
}

func referenceForPaymentID(paymentID string) string {
	cleaned := strings.Trim(invalidReferenceCharacters.ReplaceAllString(paymentID, "-"), "-")
	if cleaned == "" {
		cleaned = "payment"
	}
	reference := "fp-" + cleaned
	if len(reference) > 100 {
		return reference[:100]
	}
	return reference
}
