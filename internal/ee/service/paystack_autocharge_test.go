package service

// Tests for renewal-time gateway selection: a subscription carrying a Paystack authorization
// code is charged through Paystack, and everything else keeps the pre-existing Stripe path.

import (
	"testing"

	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/domain/connection"
	"github.com/flexprice/flexprice/internal/domain/invoice"
	"github.com/flexprice/flexprice/internal/domain/payment"
	"github.com/flexprice/flexprice/internal/domain/subscription"
	"github.com/flexprice/flexprice/internal/testutil"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/samber/lo"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type PaystackAutochargeSuite struct {
	testutil.BaseServiceTestSuite
	processor *subscriptionPaymentProcessor
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

// A saved authorization is only usable when the environment also has a Paystack connection.
func (s *PaystackAutochargeSuite) TestPaystackSavedAuthorizationSelection() {
	sub := &subscription.Subscription{ID: "subs_1", GatewayPaymentMethodID: lo.ToPtr("AUTH_reusable123")}

	code, ok := s.processor.paystackSavedAuthorization(s.GetContext(), sub)
	s.False(ok, "no Paystack connection yet")
	s.Empty(code)

	s.publishPaystackConnection()

	code, ok = s.processor.paystackSavedAuthorization(s.GetContext(), sub)
	s.True(ok)
	s.Equal("AUTH_reusable123", code)
}

// Without a saved Paystack authorization nothing changes: the Stripe path runs and, with no
// Stripe connection, the card leg pays nothing so wallet/dunning fallback still applies.
func (s *PaystackAutochargeSuite) TestNoSavedAuthorizationFallsThrough() {
	s.publishPaystackConnection()

	for _, sub := range []*subscription.Subscription{
		{ID: "subs_2"},
		{ID: "subs_3", GatewayPaymentMethodID: lo.ToPtr("pm_1QStripeMethod")},
	} {
		code, ok := s.processor.paystackSavedAuthorization(s.GetContext(), sub)
		s.False(ok)
		s.Empty(code)

		paid := s.processor.processPaymentMethodCharge(
			s.GetContext(),
			sub,
			&dto.InvoiceResponse{Invoice: invoice.Invoice{ID: "inv_1", Currency: "usd"}},
			decimal.NewFromInt(10),
		)
		s.True(paid.IsZero())
	}
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
