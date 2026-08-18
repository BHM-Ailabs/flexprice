package paystack

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/interfaces"
	"github.com/flexprice/flexprice/internal/logger"
	"github.com/flexprice/flexprice/internal/types"
)

var invalidReferenceCharacters = regexp.MustCompile(`[^A-Za-z0-9.=-]+`)

// authorizationCodePrefix is the fixed prefix Paystack gives every card authorization code.
// It is what distinguishes a saved Paystack card from a Stripe payment method id.
const authorizationCodePrefix = "AUTH_"

// transactionStatusSuccess is Paystack's terminal success status for a transaction.
const transactionStatusSuccess = "success"

// Subscription metadata keys describing the saved Paystack card. The authorization code
// itself lives on subscription.gateway_payment_method_id, the field Stripe already uses.
const (
	MetadataKeyCustomerEmail = "paystack_customer_email"
	MetadataKeyCardLast4     = "paystack_card_last4"
	MetadataKeyCardType      = "paystack_card_type"
	MetadataKeyCardBank      = "paystack_card_bank"
	MetadataKeyCardExpMonth  = "paystack_card_exp_month"
	MetadataKeyCardExpYear   = "paystack_card_exp_year"
)

// IsAuthorizationCode reports whether a stored gateway payment method id is a Paystack
// card authorization code.
func IsAuthorizationCode(value string) bool {
	return len(value) > len(authorizationCodePrefix) && strings.HasPrefix(value, authorizationCodePrefix)
}

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

// ChargeSavedAuthorization charges an invoice against a reusable card authorization captured
// on an earlier payment, with the customer absent (subscription renewals). It is the Paystack
// counterpart of Stripe's off-session ChargeSavedPaymentMethod.
func (s *PaymentService) ChargeSavedAuthorization(
	ctx context.Context,
	req *ChargeAuthorizationParams,
	customerService interfaces.CustomerService,
	invoiceService interfaces.InvoiceService,
) (*ChargeAuthorizationResult, error) {
	if !IsAuthorizationCode(req.AuthorizationCode) {
		return nil, ierr.NewError("a Paystack authorization code is required").
			WithHint("Collect a payment with a reusable card before charging it off-session").
			Mark(ierr.ErrValidation)
	}

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
		return nil, ierr.NewError("charge amount or currency does not match the invoice").
			WithHint("Use the invoice remaining amount and currency").
			Mark(ierr.ErrValidation)
	}
	if invoiceResp.CustomerID != req.CustomerID {
		return nil, ierr.NewError("payment customer does not match the invoice customer").Mark(ierr.ErrValidation)
	}

	email := req.Email
	if email == "" {
		customerResp, err := customerService.GetCustomer(ctx, req.CustomerID)
		if err != nil {
			return nil, ierr.WithError(err).WithHint("Customer not found").Mark(ierr.ErrNotFound)
		}
		email = customerResp.Email
	}
	if email == "" {
		return nil, ierr.NewError("customer email is required to charge a saved Paystack card").
			WithHint("Add an email address to the FlexPrice customer").
			Mark(ierr.ErrValidation)
	}

	reference := referenceForPaymentID(req.PaymentID)
	amountMinor := types.ToSmallestUnit(req.Amount, req.Currency)
	metadata := map[string]any{
		"flexprice_payment_id":  req.PaymentID,
		"flexprice_invoice_id":  req.InvoiceID,
		"flexprice_customer_id": req.CustomerID,
		"payment_source":        "flexprice",
		"payment_type":          "charge",
	}
	for key, value := range req.Metadata {
		metadata[key] = value
	}

	transaction, err := s.client.ChargeAuthorization(ctx, ChargeAuthorizationRequest{
		Email:             email,
		Amount:            amountMinor,
		Currency:          strings.ToUpper(req.Currency),
		AuthorizationCode: req.AuthorizationCode,
		Reference:         reference,
		Metadata:          metadata,
	})
	if err != nil {
		return nil, err
	}

	if transaction.Status != transactionStatusSuccess {
		s.logger.Info(ctx, "Paystack saved card charge was not successful",
			"flexprice_payment_id", req.PaymentID,
			"invoice_id", req.InvoiceID,
			"paystack_reference", transaction.Reference,
			"paystack_status", transaction.Status)
		return nil, ierr.NewError("Paystack declined the saved card charge").
			WithHint("The saved Paystack card could not be charged").
			WithReportableDetails(map[string]any{
				"flexprice_payment_id": req.PaymentID,
				"paystack_reference":   transaction.Reference,
				"paystack_status":      transaction.Status,
			}).
			Mark(ierr.ErrInvalidOperation)
	}
	if transaction.Amount != amountMinor || !strings.EqualFold(transaction.Currency, req.Currency) {
		return nil, ierr.NewError("charged Paystack amount or currency does not match the invoice").
			WithReportableDetails(map[string]any{
				"flexprice_payment_id": req.PaymentID,
				"paystack_reference":   transaction.Reference,
				"charged_amount":       transaction.Amount,
				"charged_currency":     transaction.Currency,
			}).
			Mark(ierr.ErrValidation)
	}

	gatewayPaymentID := transaction.Reference
	if transaction.ID != 0 {
		gatewayPaymentID = fmt.Sprintf("%d", transaction.ID)
	}

	s.logger.Info(ctx, "Paystack saved card charged",
		"flexprice_payment_id", req.PaymentID,
		"invoice_id", req.InvoiceID,
		"paystack_reference", transaction.Reference,
		"gateway_payment_id", gatewayPaymentID)

	return &ChargeAuthorizationResult{
		Reference:        transaction.Reference,
		GatewayPaymentID: gatewayPaymentID,
		Status:           transaction.Status,
		Amount:           req.Amount,
		Currency:         strings.ToUpper(req.Currency),
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
