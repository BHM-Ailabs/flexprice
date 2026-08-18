package paystack

// Tests for off-session Paystack card charging: capturing a reusable authorization from a
// verified charge.success webhook, and charging that authorization at renewal.

import (
	"context"
	"errors"
	"testing"

	"github.com/flexprice/flexprice/internal/api/dto"
	domainCheckout "github.com/flexprice/flexprice/internal/domain/checkout"
	"github.com/flexprice/flexprice/internal/domain/invoice"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/interfaces"
	"github.com/flexprice/flexprice/internal/logger"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/samber/lo"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ── fakes ────────────────────────────────────────────────────────────────────

type fakeClient struct {
	Client
	verified    *TransactionData
	verifyErr   error
	verifyCalls int
	charged     []ChargeAuthorizationRequest
	chargeReply *TransactionData
	chargeErr   error
}

func (c *fakeClient) VerifyTransaction(_ context.Context, _ string) (*TransactionData, error) {
	c.verifyCalls++
	if c.verifyErr != nil {
		return nil, c.verifyErr
	}
	return c.verified, nil
}

func (c *fakeClient) ChargeAuthorization(_ context.Context, req ChargeAuthorizationRequest) (*TransactionData, error) {
	c.charged = append(c.charged, req)
	if c.chargeErr != nil {
		return nil, c.chargeErr
	}
	return c.chargeReply, nil
}

type fakePaymentService struct {
	interfaces.PaymentService
	payment *dto.PaymentResponse
}

func (s *fakePaymentService) GetPayment(_ context.Context, _ string) (*dto.PaymentResponse, error) {
	return s.payment, nil
}

type fakeInvoiceService struct {
	interfaces.InvoiceService
	invoice *dto.InvoiceResponse
}

func (s *fakeInvoiceService) GetInvoice(_ context.Context, _ string) (*dto.InvoiceResponse, error) {
	if s.invoice == nil {
		return nil, ierr.NewError("invoice not found").Mark(ierr.ErrNotFound)
	}
	return s.invoice, nil
}

type savedPaymentMethod struct {
	subscriptionID string
	gatewayID      string
	metadata       map[string]string
}

type fakeSubscriptionService struct {
	interfaces.SubscriptionService
	saved   []savedPaymentMethod
	saveErr error
}

func (s *fakeSubscriptionService) SaveGatewayPaymentMethod(_ context.Context, subscriptionID, gatewayPaymentMethodID string, metadata map[string]string) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.saved = append(s.saved, savedPaymentMethod{subscriptionID, gatewayPaymentMethodID, metadata})
	return nil
}

type fakeCheckoutSessionService struct {
	interfaces.CheckoutSessionService
	completed []string
}

func (s *fakeCheckoutSessionService) List(_ context.Context, _ *types.CheckoutSessionFilter) (*dto.ListCheckoutSessionsResponse, error) {
	return &dto.ListCheckoutSessionsResponse{Items: []*dto.CheckoutSessionResponse{{
		CheckoutSession: &domainCheckout.CheckoutSession{
			ID:             "cs_test",
			CheckoutStatus: types.CheckoutStatusPending,
		},
	}}}, nil
}

func (s *fakeCheckoutSessionService) CompleteCheckoutSession(_ context.Context, sessionID string, _ *types.CheckoutProviderResult) error {
	s.completed = append(s.completed, sessionID)
	return nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

const (
	testSubscriptionID = "subs_test"
	testInvoiceID      = "inv_test"
	testCustomerID     = "cust_test"
	testPaymentID      = "pay_test"
	testAuthCode       = "AUTH_reusable123"
)

func webhookFixtures(authorization *TransactionAuthorization) (*WebhookHandler, *fakeSubscriptionService, *interfaces.ServiceDependencies, *WebhookEvent) {
	verified := &TransactionData{
		ID:            9911,
		Status:        transactionStatusSuccess,
		Reference:     "fp-pay-test",
		Amount:        types.ToSmallestUnit(decimal.NewFromInt(50), "NGN"),
		Currency:      "NGN",
		Metadata:      map[string]any{"flexprice_payment_id": testPaymentID},
		Authorization: authorization,
		Customer:      &TransactionCustomer{ID: 42, Email: "payer@example.com"},
	}

	subscriptionSvc := &fakeSubscriptionService{}
	services := &interfaces.ServiceDependencies{
		PaymentService: &fakePaymentService{payment: &dto.PaymentResponse{
			ID:              testPaymentID,
			DestinationType: types.PaymentDestinationTypeInvoice,
			DestinationID:   testInvoiceID,
			Amount:          decimal.NewFromInt(50),
			Currency:        "NGN",
		}},
		InvoiceService: &fakeInvoiceService{invoice: &dto.InvoiceResponse{Invoice: invoice.Invoice{
			ID:             testInvoiceID,
			CustomerID:     testCustomerID,
			SubscriptionID: lo.ToPtr(testSubscriptionID),
		}}},
		SubscriptionService:    subscriptionSvc,
		CheckoutSessionService: &fakeCheckoutSessionService{},
	}

	handler := NewWebhookHandler(&fakeClient{verified: verified}, nil, logger.NewNoopLogger())
	event := &WebhookEvent{Event: "charge.success", Data: TransactionData{Reference: verified.Reference}}

	return handler, subscriptionSvc, services, event
}

// ── tests ────────────────────────────────────────────────────────────────────

func TestIsAuthorizationCode(t *testing.T) {
	require.True(t, IsAuthorizationCode("AUTH_abc123"))
	require.False(t, IsAuthorizationCode("AUTH_"))
	require.False(t, IsAuthorizationCode("pm_1QStripeMethod"))
	require.False(t, IsAuthorizationCode(""))
}

func TestWebhookStoresReusableAuthorization(t *testing.T) {
	handler, subscriptionSvc, services, event := webhookFixtures(&TransactionAuthorization{
		AuthorizationCode: testAuthCode,
		Reusable:          true,
		Last4:             "4081",
		CardType:          "visa",
		Bank:              "Test Bank",
		ExpMonth:          "12",
		ExpYear:           "2030",
	})

	require.NoError(t, handler.Handle(context.Background(), event, services))

	require.Len(t, subscriptionSvc.saved, 1)
	saved := subscriptionSvc.saved[0]
	require.Equal(t, testSubscriptionID, saved.subscriptionID)
	require.Equal(t, testAuthCode, saved.gatewayID)
	require.Equal(t, map[string]string{
		MetadataKeyCustomerEmail: "payer@example.com",
		MetadataKeyCardLast4:     "4081",
		MetadataKeyCardType:      "visa",
		MetadataKeyCardBank:      "Test Bank",
		MetadataKeyCardExpMonth:  "12",
		MetadataKeyCardExpYear:   "2030",
	}, saved.metadata)
}

func TestWebhookSkipsNonReusableOrAbsentAuthorization(t *testing.T) {
	tests := []struct {
		name          string
		authorization *TransactionAuthorization
	}{
		{name: "absent", authorization: nil},
		{name: "not reusable", authorization: &TransactionAuthorization{AuthorizationCode: testAuthCode, Reusable: false, Last4: "4081"}},
		{name: "reusable without code", authorization: &TransactionAuthorization{Reusable: true, Last4: "4081"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, subscriptionSvc, services, event := webhookFixtures(test.authorization)

			require.NoError(t, handler.Handle(context.Background(), event, services))
			require.Empty(t, subscriptionSvc.saved)
		})
	}
}

func TestChargeSavedAuthorizationChargesTheSavedCard(t *testing.T) {
	client := &fakeClient{chargeReply: &TransactionData{
		ID:        7788,
		Status:    transactionStatusSuccess,
		Reference: "fp-pay-test",
		Amount:    types.ToSmallestUnit(decimal.NewFromInt(50), "NGN"),
		Currency:  "NGN",
	}}
	svc := NewPaymentService(client, logger.NewNoopLogger())

	result, err := svc.ChargeSavedAuthorization(context.Background(), &ChargeAuthorizationParams{
		InvoiceID:         testInvoiceID,
		CustomerID:        testCustomerID,
		PaymentID:         testPaymentID,
		Amount:            decimal.NewFromInt(50),
		Currency:          "NGN",
		AuthorizationCode: testAuthCode,
		Email:             "payer@example.com",
	}, nil, &fakeInvoiceService{invoice: &dto.InvoiceResponse{Invoice: invoice.Invoice{
		ID:              testInvoiceID,
		CustomerID:      testCustomerID,
		Currency:        "NGN",
		PaymentStatus:   types.PaymentStatusPending,
		InvoiceStatus:   types.InvoiceStatusFinalized,
		AmountRemaining: decimal.NewFromInt(50),
	}}})

	require.NoError(t, err)
	require.Equal(t, "7788", result.GatewayPaymentID)
	require.Equal(t, transactionStatusSuccess, result.Status)

	require.Len(t, client.charged, 1)
	charged := client.charged[0]
	require.Equal(t, testAuthCode, charged.AuthorizationCode)
	require.Equal(t, "payer@example.com", charged.Email)
	require.Equal(t, int64(5000), charged.Amount) // 50 NGN in kobo
	require.Equal(t, "NGN", charged.Currency)
	require.Equal(t, "fp-pay-test", charged.Reference)
	require.Equal(t, testPaymentID, charged.Metadata["flexprice_payment_id"])
}

func TestChargeSavedAuthorizationRejectsDeclineAndMissingCard(t *testing.T) {
	paidInvoice := &fakeInvoiceService{invoice: &dto.InvoiceResponse{Invoice: invoice.Invoice{
		ID:              testInvoiceID,
		CustomerID:      testCustomerID,
		Currency:        "NGN",
		PaymentStatus:   types.PaymentStatusPending,
		InvoiceStatus:   types.InvoiceStatusFinalized,
		AmountRemaining: decimal.NewFromInt(50),
	}}}
	params := &ChargeAuthorizationParams{
		InvoiceID:         testInvoiceID,
		CustomerID:        testCustomerID,
		PaymentID:         testPaymentID,
		Amount:            decimal.NewFromInt(50),
		Currency:          "NGN",
		AuthorizationCode: testAuthCode,
		Email:             "payer@example.com",
	}

	// A declined charge is only terminal once verification confirms a terminal status.
	declined := &TransactionData{
		Status:    "failed",
		Reference: "fp-pay-test",
		Amount:    types.ToSmallestUnit(decimal.NewFromInt(50), "NGN"),
		Currency:  "NGN",
	}
	declining := &fakeClient{chargeReply: declined, verified: declined}
	_, err := NewPaymentService(declining, logger.NewNoopLogger()).
		ChargeSavedAuthorization(context.Background(), params, nil, paidInvoice)
	require.Error(t, err)
	require.False(t, IsChargeOutcomeUnknown(err), "a verified decline is a definite failure")
	require.Len(t, declining.charged, 1)
	require.Equal(t, 1, declining.verifyCalls)

	noCard := &fakeClient{}
	withoutAuthorization := *params
	withoutAuthorization.AuthorizationCode = ""
	_, err = NewPaymentService(noCard, logger.NewNoopLogger()).
		ChargeSavedAuthorization(context.Background(), &withoutAuthorization, nil, paidInvoice)
	require.Error(t, err)
	require.Empty(t, noCard.charged)
}

// A transport failure on the charge call is never a decline: verify by reference first.
func TestChargeSavedAuthorizationVerifiesAfterTransportError(t *testing.T) {
	client := &fakeClient{
		chargeErr: errors.New("post https://api.paystack.co/transaction/charge_authorization: context deadline exceeded"),
		verified: &TransactionData{
			ID:        7788,
			Status:    transactionStatusSuccess,
			Reference: "fp-pay-test",
			Amount:    types.ToSmallestUnit(decimal.NewFromInt(50), "NGN"),
			Currency:  "NGN",
		},
	}

	result, err := NewPaymentService(client, logger.NewNoopLogger()).
		ChargeSavedAuthorization(context.Background(), chargeParams(), nil, pendingInvoiceService())

	require.NoError(t, err)
	require.Equal(t, "7788", result.GatewayPaymentID)
	require.Equal(t, 1, client.verifyCalls)
}

// Charge failed AND verification cannot say what happened: the outcome must be reported as
// unknown so no fallback or retry is attempted.
func TestChargeSavedAuthorizationReportsUnknownOutcome(t *testing.T) {
	tests := []struct {
		name   string
		client *fakeClient
	}{
		{
			name: "verification unavailable after transport error",
			client: &fakeClient{
				chargeErr: errors.New("context deadline exceeded"),
				verifyErr: errors.New("paystack unavailable"),
			},
		},
		{
			name: "charge still pending",
			client: &fakeClient{
				chargeReply: &TransactionData{Status: "pending", Reference: "fp-pay-test"},
				verified:    &TransactionData{Status: "ongoing", Reference: "fp-pay-test"},
			},
		},
		{
			name: "verified success for a different amount",
			client: &fakeClient{
				chargeErr: errors.New("context deadline exceeded"),
				verified: &TransactionData{
					Status:    transactionStatusSuccess,
					Reference: "fp-pay-test",
					Amount:    types.ToSmallestUnit(decimal.NewFromInt(70), "NGN"),
					Currency:  "NGN",
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewPaymentService(test.client, logger.NewNoopLogger()).
				ChargeSavedAuthorization(context.Background(), chargeParams(), nil, pendingInvoiceService())

			require.Error(t, err)
			require.True(t, IsChargeOutcomeUnknown(err))
			require.False(t, IsChargeCollected(err))
		})
	}
}

// Losing a captured authorization silently would leave renewals uncollectable, so a failed
// persistence must surface as an error and let Paystack retry the idempotent webhook.
func TestWebhookPropagatesCapturePersistenceFailure(t *testing.T) {
	handler, subscriptionSvc, services, event := webhookFixtures(&TransactionAuthorization{
		AuthorizationCode: testAuthCode,
		Reusable:          true,
		Last4:             "4081",
	})
	subscriptionSvc.saveErr = errors.New("subscription row locked")

	err := handler.Handle(context.Background(), event, services)

	require.Error(t, err)
	require.Empty(t, subscriptionSvc.saved)
	require.Empty(t, services.CheckoutSessionService.(*fakeCheckoutSessionService).completed,
		"the payment must not be settled on an attempt that lost the saved card")
}

func chargeParams() *ChargeAuthorizationParams {
	return &ChargeAuthorizationParams{
		InvoiceID:         testInvoiceID,
		CustomerID:        testCustomerID,
		PaymentID:         testPaymentID,
		Amount:            decimal.NewFromInt(50),
		Currency:          "NGN",
		AuthorizationCode: testAuthCode,
		Email:             "payer@example.com",
	}
}

func pendingInvoiceService() *fakeInvoiceService {
	return &fakeInvoiceService{invoice: &dto.InvoiceResponse{Invoice: invoice.Invoice{
		ID:              testInvoiceID,
		CustomerID:      testCustomerID,
		Currency:        "NGN",
		PaymentStatus:   types.PaymentStatusPending,
		InvoiceStatus:   types.InvoiceStatusFinalized,
		AmountRemaining: decimal.NewFromInt(50),
	}}}
}
