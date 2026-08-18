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
	return types.IsPaystackAuthorizationCode(value)
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

	transaction, chargeErr := s.client.ChargeAuthorization(ctx, ChargeAuthorizationRequest{
		Email:             email,
		Amount:            amountMinor,
		Currency:          strings.ToUpper(req.Currency),
		AuthorizationCode: req.AuthorizationCode,
		Reference:         reference,
		Metadata:          metadata,
	})

	// A transport error or a non-success status does not mean no money moved: the reference is
	// stable, so ask Paystack what actually happened before deciding anything.
	if chargeErr != nil || transaction.Status != transactionStatusSuccess {
		return s.resolveAmbiguousCharge(ctx, req, reference, amountMinor, transaction, chargeErr)
	}

	if transaction.Amount != amountMinor || !strings.EqualFold(transaction.Currency, req.Currency) {
		// Paystack collected something other than what we asked for. Treat the outcome as
		// unknown so nothing else is charged and the mismatch is settled out of band.
		s.logger.Error(ctx, "charged Paystack amount or currency does not match the invoice",
			"flexprice_payment_id", req.PaymentID,
			"paystack_reference", transaction.Reference,
			"charged_amount", transaction.Amount,
			"charged_currency", transaction.Currency,
			"expected_amount", amountMinor,
			"expected_currency", strings.ToUpper(req.Currency))
		return nil, NewChargeOutcomeUnknownError(reference, transaction.Status,
			ierr.NewError("charged Paystack amount or currency does not match the invoice").Mark(ierr.ErrValidation))
	}

	return s.successResult(ctx, req, transaction), nil
}

// resolveAmbiguousCharge decides the real outcome of a charge that errored or came back
// non-success, by verifying the (stable) reference with Paystack.
func (s *PaymentService) resolveAmbiguousCharge(
	ctx context.Context,
	req *ChargeAuthorizationParams,
	reference string,
	amountMinor int64,
	transaction *TransactionData,
	chargeErr error,
) (*ChargeAuthorizationResult, error) {
	chargeStatus := ""
	if transaction != nil {
		chargeStatus = transaction.Status
	}

	s.logger.Info(ctx, "verifying ambiguous Paystack saved card charge",
		"flexprice_payment_id", req.PaymentID,
		"invoice_id", req.InvoiceID,
		"paystack_reference", reference,
		"charge_status", chargeStatus,
		"charge_error", chargeErr)

	verified, verifyErr := s.client.VerifyTransaction(ctx, reference)
	if verifyErr != nil {
		// We cannot tell whether the card was debited. Never fail terminally here.
		cause := chargeErr
		if cause == nil {
			cause = verifyErr
		}
		return nil, NewChargeOutcomeUnknownError(reference, chargeStatus, cause)
	}

	if verified.Status == transactionStatusSuccess {
		if verified.Amount != amountMinor || !strings.EqualFold(verified.Currency, req.Currency) {
			s.logger.Error(ctx, "verified Paystack charge does not match the requested amount",
				"flexprice_payment_id", req.PaymentID,
				"paystack_reference", reference,
				"verified_amount", verified.Amount,
				"verified_currency", verified.Currency)
			return nil, NewChargeOutcomeUnknownError(reference, verified.Status,
				ierr.NewError("verified Paystack amount or currency does not match the invoice").Mark(ierr.ErrValidation))
		}
		return s.successResult(ctx, req, verified), nil
	}

	if isTerminalFailureStatus(verified.Status) {
		s.logger.Info(ctx, "Paystack declined the saved card charge",
			"flexprice_payment_id", req.PaymentID,
			"invoice_id", req.InvoiceID,
			"paystack_reference", reference,
			"paystack_status", verified.Status)
		return nil, ierr.NewError("Paystack declined the saved card charge").
			WithHint("The saved Paystack card could not be charged").
			WithReportableDetails(map[string]any{
				"flexprice_payment_id": req.PaymentID,
				"paystack_reference":   reference,
				"paystack_status":      verified.Status,
			}).
			Mark(ierr.ErrInvalidOperation)
	}

	// Still in flight (pending/ongoing/queued): the money may yet be collected.
	return nil, NewChargeOutcomeUnknownError(reference, verified.Status, chargeErr)
}

func (s *PaymentService) successResult(ctx context.Context, req *ChargeAuthorizationParams, transaction *TransactionData) *ChargeAuthorizationResult {
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
	}
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
