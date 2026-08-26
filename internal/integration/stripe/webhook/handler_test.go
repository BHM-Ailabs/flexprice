package webhook

import (
	"context"
	"testing"

	"github.com/flexprice/flexprice/internal/logger"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/stretchr/testify/require"
	stripeapi "github.com/stripe/stripe-go/v82"
)

func TestCheckoutSessionPaymentSucceeded(t *testing.T) {
	paid := &stripeapi.CheckoutSession{PaymentStatus: stripeapi.CheckoutSessionPaymentStatusPaid}
	succeeded := &stripeapi.PaymentIntent{Status: stripeapi.PaymentIntentStatusSucceeded}

	require.True(t, checkoutSessionPaymentSucceeded(paid, succeeded))
	require.False(t, checkoutSessionPaymentSucceeded(
		&stripeapi.CheckoutSession{PaymentStatus: stripeapi.CheckoutSessionPaymentStatusUnpaid},
		succeeded,
	))
	require.False(t, checkoutSessionPaymentSucceeded(
		paid,
		&stripeapi.PaymentIntent{Status: stripeapi.PaymentIntentStatusProcessing},
	))
	require.False(t, checkoutSessionPaymentSucceeded(paid, nil))
}

func TestCheckoutSessionEventsAreDispatchedAndMalformedPayloadsRetry(t *testing.T) {
	handler := &Handler{logger: logger.NewNoopLogger()}
	eventTypes := []types.WebhookEventType{
		types.WebhookEventTypeCheckoutSessionCompleted,
		types.WebhookEventTypeCheckoutSessionAsyncPaymentSucceeded,
		types.WebhookEventTypeCheckoutSessionAsyncPaymentFailed,
		types.WebhookEventTypeCheckoutSessionExpired,
	}

	for _, eventType := range eventTypes {
		t.Run(string(eventType), func(t *testing.T) {
			err := handler.HandleWebhookEvent(context.Background(), &stripeapi.Event{
				ID:   "evt_test",
				Type: stripeapi.EventType(eventType),
				Data: &stripeapi.EventData{Raw: []byte("{")},
			}, "env_test", &ServiceDependencies{})

			require.Error(t, err, "processing errors must reach the HTTP handler so Stripe receives non-2xx")
		})
	}
}

func TestCompletedCheckoutSessionWaitsForAsyncSettlement(t *testing.T) {
	handler := &Handler{logger: logger.NewNoopLogger()}
	raw := []byte(`{"id":"cs_test","payment_status":"unpaid","metadata":{"flexprice_payment_id":"payment_test"}}`)

	err := handler.HandleWebhookEvent(context.Background(), &stripeapi.Event{
		ID:   "evt_completed",
		Type: stripeapi.EventType(types.WebhookEventTypeCheckoutSessionCompleted),
		Data: &stripeapi.EventData{Raw: raw},
	}, "env_test", &ServiceDependencies{})
	require.NoError(t, err, "an unpaid completed event must be acknowledged without fulfillment")

	err = handler.HandleWebhookEvent(context.Background(), &stripeapi.Event{
		ID:   "evt_async_success",
		Type: stripeapi.EventType(types.WebhookEventTypeCheckoutSessionAsyncPaymentSucceeded),
		Data: &stripeapi.EventData{Raw: raw},
	}, "env_test", &ServiceDependencies{})
	require.Error(t, err, "an inconsistent async success event must be retried")
}
