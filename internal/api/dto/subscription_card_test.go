package dto

import (
	"encoding/json"
	"github.com/flexprice/flexprice/internal/domain/subscription"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSubscriptionCardDescriptorNeverExposesAuthorization(t *testing.T) {
	sub := &subscription.Subscription{GatewayPaymentMethodID: lo.ToPtr("AUTH_test_secret"), BillingCadence: types.BILLING_CADENCE_RECURRING, BillingPeriod: types.BILLING_PERIOD_MONTHLY, Metadata: types.Metadata{
		"paystack_save_card_consent": "true", "paystack_customer_email": "payer@example.com", "paystack_card_last4": "4081", "paystack_card_type": "visa", "paystack_card_exp_month": "12", "paystack_card_exp_year": "2030",
	}}
	for _, response := range []any{SubscriptionResponse{Subscription: sub}, SubscriptionResponseV2{Subscription: sub}} {
		raw, err := json.Marshal(response)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "AUTH_test_secret")
		var data map[string]any
		require.NoError(t, json.Unmarshal(raw, &data))
		require.Equal(t, true, data["payment_method_ready"])
		card := data["payment_method"].(map[string]any)
		require.Equal(t, "4081", card["last4"])
		require.Equal(t, "visa", card["brand"])
	}
	require.Equal(t, "AUTH_test_secret", *sub.GatewayPaymentMethodID)
	delete(sub.Metadata, "paystack_save_card_consent")
	ready, card := subscriptionCardDescriptor(sub)
	require.Equal(t, lo.ToPtr(false), ready)
	require.Nil(t, card)
	sub.GatewayPaymentMethodID = lo.ToPtr("pm_unverified_stripe")
	ready, card = subscriptionCardDescriptor(sub)
	require.Nil(t, ready)
	require.Nil(t, card)
}

func TestSubscriptionCardReadinessUnknownWithoutSupportedLocalEvidence(t *testing.T) {
	for _, sub := range []*subscription.Subscription{nil, {}, {GatewayPaymentMethodID: lo.ToPtr("pm_saved_on_stripe")}} {
		for _, response := range []any{SubscriptionResponse{Subscription: sub}, SubscriptionResponseV2{Subscription: sub}} {
			raw, err := json.Marshal(response)
			require.NoError(t, err)
			var data map[string]any
			require.NoError(t, json.Unmarshal(raw, &data))
			value, exists := data["payment_method_ready"]
			require.True(t, exists)
			require.Nil(t, value)
			require.Nil(t, data["payment_method"])
		}
	}
}
