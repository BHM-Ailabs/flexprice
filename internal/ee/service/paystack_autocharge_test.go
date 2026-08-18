package service

// Tests for renewal-time gateway selection: a subscription carrying a Paystack authorization
// code is charged through Paystack, and everything else keeps the pre-existing Stripe path.

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/domain/connection"
	"github.com/flexprice/flexprice/internal/domain/invoice"
	"github.com/flexprice/flexprice/internal/domain/payment"
	"github.com/flexprice/flexprice/internal/domain/subscription"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/integration/paystack"
	"github.com/flexprice/flexprice/internal/testutil"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/samber/lo"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type PaystackAutochargeSuite struct {
	testutil.BaseServiceTestSuite
	processor    *subscriptionPaymentProcessor
	subscription SubscriptionService
}

func TestPaystackAutocharge(t *testing.T) {
	suite.Run(t, new(PaystackAutochargeSuite))
}

func (s *PaystackAutochargeSuite) SetupTest() {
	s.BaseServiceTestSuite.SetupTest()
	params := ServiceParams{
		Logger:         s.GetLogger(),
		Config:         s.GetConfig(),
		DB:             s.GetDB(),
		SubRepo:        s.GetStores().SubscriptionRepo,
		InvoiceRepo:    s.GetStores().InvoiceRepo,
		PaymentRepo:    s.GetStores().PaymentRepo,
		CustomerRepo:   s.GetStores().CustomerRepo,
		WalletRepo:     s.GetStores().WalletRepo,
		ConnectionRepo: s.GetStores().ConnectionRepo,
	}
	s.processor = &subscriptionPaymentProcessor{ServiceParams: &params}
	s.subscription = NewSubscriptionService(params)
}

func (s *PaystackAutochargeSuite) TearDownTest() {
	s.BaseServiceTestSuite.TearDownTest()
}

func (s *PaystackAutochargeSuite) publishPaystackConnection() {
	s.NoError(s.GetStores().ConnectionRepo.Create(s.GetContext(), &connection.Connection{
		ID:            "conn_paystack",
		Name:          "Paystack",
		ProviderType:  types.SecretProviderPaystack,
		EnvironmentID: types.GetEnvironmentID(s.GetContext()),
		BaseModel:     types.GetDefaultBaseModel(s.GetContext()),
	}))
}

// A saved Paystack authorization is never handed to Stripe: without a Paystack connection the
// card leg collects nothing and does not fall through to Stripe payment-method selection.
func (s *PaystackAutochargeSuite) TestPaystackAuthorizationNeverFallsThroughToStripe() {
	sub := &subscription.Subscription{ID: "subs_1", GatewayPaymentMethodID: lo.ToPtr("AUTH_reusable123")}
	inv := &dto.InvoiceResponse{Invoice: invoice.Invoice{ID: "inv_1", Currency: "usd"}}

	// No Paystack connection: nothing charged, no fallback suppression, and no Stripe lookup
	// (a nil IntegrationFactory would panic if the Stripe branch were entered).
	paid, stopFallback := s.processor.processPaymentMethodCharge(s.GetContext(), sub, inv, decimal.NewFromInt(10))
	s.True(paid.IsZero())
	s.False(stopFallback)

	s.publishPaystackConnection()
	s.True(s.processor.hasPaystackConnection(s.GetContext()))
}

// Without a saved Paystack authorization nothing changes: the Stripe path runs and, with no
// Stripe connection, the card leg pays nothing so wallet/dunning fallback still applies.
func (s *PaystackAutochargeSuite) TestNoSavedAuthorizationFallsThrough() {
	s.publishPaystackConnection()

	for _, sub := range []*subscription.Subscription{
		{ID: "subs_2"},
		{ID: "subs_3", GatewayPaymentMethodID: lo.ToPtr("pm_1QStripeMethod")},
	} {
		paid, stopFallback := s.processor.processPaymentMethodCharge(
			s.GetContext(),
			sub,
			&dto.InvoiceResponse{Invoice: invoice.Invoice{ID: "inv_1", Currency: "usd"}},
			decimal.NewFromInt(10),
		)
		s.True(paid.IsZero())
		s.False(stopFallback, "a plain card failure must still allow wallet fallback")
	}
}

// The capture writes the authorization and the card descriptors on the locked row, and repeating
// it is a no-op.
func (s *PaystackAutochargeSuite) TestSaveGatewayPaymentMethodPersistsUnderLock() {
	sub := &subscription.Subscription{
		ID:                 "subs_capture",
		CustomerID:         "cust_1",
		SubscriptionStatus: types.SubscriptionStatusActive,
		Currency:           "ngn",
		BaseModel:          types.GetDefaultBaseModel(s.GetContext()),
	}
	s.NoError(s.GetStores().SubscriptionRepo.Create(s.GetContext(), sub))

	metadata := map[string]string{
		paystack.MetadataKeyCustomerEmail: "payer@example.com",
		paystack.MetadataKeyCardLast4:     "4081",
	}
	s.NoError(s.subscription.SaveGatewayPaymentMethod(s.GetContext(), sub.ID, "AUTH_reusable123", metadata))

	stored, err := s.GetStores().SubscriptionRepo.Get(s.GetContext(), sub.ID)
	s.NoError(err)
	s.Equal("AUTH_reusable123", lo.FromPtr(stored.GatewayPaymentMethodID))
	s.Equal("payer@example.com", stored.Metadata[paystack.MetadataKeyCustomerEmail])
	s.Equal("4081", stored.Metadata[paystack.MetadataKeyCardLast4])

	// Repeated webhook deliveries must not churn the row.
	s.NoError(s.subscription.SaveGatewayPaymentMethod(s.GetContext(), sub.ID, "AUTH_reusable123", metadata))
	again, err := s.GetStores().SubscriptionRepo.Get(s.GetContext(), sub.ID)
	s.NoError(err)
	s.Equal(stored.UpdatedAt, again.UpdatedAt)

	s.Error(s.subscription.SaveGatewayPaymentMethod(s.GetContext(), "", "AUTH_reusable123", nil))
}

// The money semantics of a card-charge error: collected counts as paid, unknown counts as nothing,
// and both forbid any fallback. A plain decline keeps today's wallet/dunning behaviour.
func TestClassifyCardChargeOutcome(t *testing.T) {
	countAsPaid, stopFallback, classified := classifyCardChargeOutcome(paystack.NewChargeCollectedError("fp-pay-1", errors.New("db down")))
	require.True(t, classified)
	require.True(t, countAsPaid)
	require.True(t, stopFallback)

	countAsPaid, stopFallback, classified = classifyCardChargeOutcome(paystack.NewChargeOutcomeUnknownError("fp-pay-1", "pending", nil))
	require.True(t, classified)
	require.False(t, countAsPaid)
	require.True(t, stopFallback)

	countAsPaid, stopFallback, classified = classifyCardChargeOutcome(errors.New("card declined"))
	require.False(t, classified)
	require.False(t, countAsPaid)
	require.False(t, stopFallback)
}

// An unresolved gateway outcome must never become a terminal FAILED payment, and a collected
// charge that failed to persist must not either — both would invite a second collection.
func TestUnresolvedGatewayOutcome(t *testing.T) {
	require.True(t, isUnresolvedGatewayOutcome(paystack.NewChargeOutcomeUnknownError("fp-pay-1", "", errors.New("timeout"))))
	require.True(t, isUnresolvedGatewayOutcome(paystack.NewChargeCollectedError("fp-pay-1", errors.New("db down"))))
	require.True(t, isUnresolvedGatewayOutcome(ierr.WithError(paystack.NewChargeOutcomeUnknownError("fp-pay-1", "pending", nil)).
		WithHint("Failed to process payment").Mark(ierr.ErrInvalidOperation)), "must survive the ierr wrapping CreatePayment applies")
	require.False(t, isUnresolvedGatewayOutcome(errors.New("card declined")))
}

// Paystack authorization codes must not be serialized to API clients.
func TestSubscriptionResponseMasksPaystackAuthorization(t *testing.T) {
	sub := &subscription.Subscription{ID: "subs_1", GatewayPaymentMethodID: lo.ToPtr("AUTH_reusable123")}

	encoded, err := json.Marshal(&dto.SubscriptionResponse{Subscription: sub})
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "AUTH_reusable123")
	require.Contains(t, string(encoded), "[paystack authorization]")

	encodedV2, err := json.Marshal(&dto.SubscriptionResponseV2{Subscription: sub})
	require.NoError(t, err)
	require.NotContains(t, string(encodedV2), "AUTH_reusable123")

	// The domain object the charge path reads is untouched.
	require.Equal(t, "AUTH_reusable123", lo.FromPtr(sub.GatewayPaymentMethodID))

	stripeSub := &subscription.Subscription{ID: "subs_2", GatewayPaymentMethodID: lo.ToPtr("pm_1QStripeMethod")}
	encodedStripe, err := json.Marshal(&dto.SubscriptionResponse{Subscription: stripeSub})
	require.NoError(t, err)
	require.Contains(t, string(encodedStripe), "pm_1QStripeMethod")
}

func TestIsPaystackCardPayment(t *testing.T) {
	tests := []struct {
		name    string
		payment *payment.Payment
		want    bool
	}{
		{
			name:    "paystack gateway",
			payment: &payment.Payment{PaymentGateway: lo.ToPtr(string(types.PaymentGatewayTypePaystack))},
			want:    true,
		},
		{
			name:    "paystack authorization code without gateway",
			payment: &payment.Payment{PaymentMethodID: "AUTH_reusable123"},
			want:    true,
		},
		{
			name:    "stripe payment method",
			payment: &payment.Payment{PaymentGateway: lo.ToPtr(string(types.PaymentGatewayTypeStripe)), PaymentMethodID: "pm_1QStripe"},
			want:    false,
		},
		{
			name:    "no gateway and no saved card",
			payment: &payment.Payment{},
			want:    false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, isPaystackCardPayment(test.payment))
		})
	}
}
