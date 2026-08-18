package paystack

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flexprice/flexprice/internal/api/dto"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/integration/payments"
	"github.com/flexprice/flexprice/internal/interfaces"
	"github.com/flexprice/flexprice/internal/logger"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/samber/lo"
)

type WebhookHandler struct {
	client    Client
	lifecycle *payments.PaymentLifecycle
	logger    *logger.Logger
}

func NewWebhookHandler(client Client, lifecycle *payments.PaymentLifecycle, logger *logger.Logger) *WebhookHandler {
	return &WebhookHandler{client: client, lifecycle: lifecycle, logger: logger}
}

func (h *WebhookHandler) Handle(ctx context.Context, event *WebhookEvent, services *interfaces.ServiceDependencies) error {
	if event.Event != "charge.success" {
		h.logger.Info(ctx, "ignoring unsupported Paystack webhook event", "event_type", event.Event)
		return nil
	}
	if event.Data.Reference == "" {
		return ierr.NewError("Paystack webhook is missing a transaction reference").Mark(ierr.ErrValidation)
	}

	verified, err := h.client.VerifyTransaction(ctx, event.Data.Reference)
	if err != nil {
		return err
	}
	if verified.Status != transactionStatusSuccess || verified.Reference != event.Data.Reference {
		return ierr.NewError("Paystack transaction verification did not succeed").
			WithHint("Wait for a verified charge.success transaction").
			Mark(ierr.ErrValidation)
	}

	paymentID, err := resolveFlexpricePaymentID(ctx, verified, services)
	if err != nil {
		return err
	}
	payment, err := services.PaymentService.GetPayment(ctx, paymentID)
	if err != nil {
		return err
	}
	if types.ToSmallestUnit(payment.Amount, payment.Currency) != verified.Amount || !strings.EqualFold(payment.Currency, verified.Currency) {
		return ierr.NewError("verified Paystack amount or currency does not match the FlexPrice payment").
			WithReportableDetails(map[string]any{"flexprice_payment_id": paymentID, "paystack_reference": verified.Reference}).
			Mark(ierr.ErrValidation)
	}

	gatewayPaymentID := verified.Reference
	if verified.ID != 0 {
		gatewayPaymentID = fmt.Sprintf("%d", verified.ID)
	}

	// A reusable authorization means the customer's card can be charged again off-session at
	// renewal. Capture it before settling the payment so every verified success path stores it.
	// A persistence failure is returned: charge.success is idempotent, so Paystack retrying the
	// webhook is strictly better than silently losing the saved card.
	if err := h.captureReusableAuthorization(ctx, verified, payment, services); err != nil {
		return err
	}

	filter := types.NewDefaultCheckoutSessionFilter()
	filter.CheckoutPaymentIDs = []string{paymentID}
	filter.Limit = lo.ToPtr(1)
	filter.Status = lo.ToPtr(types.StatusPublished)
	sessions, err := services.CheckoutSessionService.List(ctx, filter)
	if err != nil {
		return err
	}
	if sessions != nil && len(sessions.Items) > 0 {
		session := sessions.Items[0]
		switch session.CheckoutStatus {
		case types.CheckoutStatusCompleted:
			return nil
		case types.CheckoutStatusPending:
			return services.CheckoutSessionService.CompleteCheckoutSession(ctx, session.ID, &types.CheckoutProviderResult{
				ProviderSessionID:       verified.Reference,
				ProviderPaymentIntentID: gatewayPaymentID,
			})
		default:
			h.logger.Error(ctx, "Paystack payment succeeded after checkout became non-actionable",
				"error", "manual_refund_required",
				"checkout_session_id", session.ID,
				"checkout_status", session.CheckoutStatus,
				"flexprice_payment_id", paymentID,
				"paystack_reference", verified.Reference)
			return nil
		}
	}

	if h.lifecycle == nil {
		return ierr.NewError("Paystack payment lifecycle is not initialized").Mark(ierr.ErrInternal)
	}
	succeededAt := time.Now().UTC()
	if verified.PaidAt != "" {
		if parsed, parseErr := time.Parse(time.RFC3339, verified.PaidAt); parseErr == nil {
			succeededAt = parsed.UTC()
		}
	}
	return h.lifecycle.RecordPaymentSuccess(ctx, payments.RecordPaymentSuccessParams{
		FlexpricePaymentID: paymentID,
		GatewayPaymentID:   gatewayPaymentID,
		SucceededAt:        succeededAt,
	})
}

// captureReusableAuthorization persists a reusable card authorization on the subscription that
// owns the paid invoice, so renewals can charge the same card off-session. Nothing to capture is
// success; a failed lookup or write is an error so the idempotent webhook is retried.
func (h *WebhookHandler) captureReusableAuthorization(
	ctx context.Context,
	transaction *TransactionData,
	paymentResp *dto.PaymentResponse,
	services *interfaces.ServiceDependencies,
) error {
	authorization := transaction.Authorization
	if authorization == nil || !authorization.Reusable || !IsAuthorizationCode(authorization.AuthorizationCode) {
		return nil
	}
	if paymentResp == nil || paymentResp.DestinationType != types.PaymentDestinationTypeInvoice || paymentResp.DestinationID == "" {
		return nil
	}
	if services == nil || services.InvoiceService == nil || services.SubscriptionService == nil {
		return nil
	}

	invoiceResp, err := services.InvoiceService.GetInvoice(ctx, paymentResp.DestinationID)
	if err != nil {
		h.logger.Error(ctx, "unable to load invoice while capturing Paystack authorization",
			"error", err,
			"flexprice_payment_id", paymentResp.ID,
			"invoice_id", paymentResp.DestinationID)
		return err
	}
	subscriptionID := lo.FromPtr(invoiceResp.SubscriptionID)
	if subscriptionID == "" {
		return nil
	}

	metadata := map[string]string{}
	if transaction.Customer != nil && transaction.Customer.Email != "" {
		metadata[MetadataKeyCustomerEmail] = transaction.Customer.Email
	}
	if authorization.Last4 != "" {
		metadata[MetadataKeyCardLast4] = authorization.Last4
	}
	if authorization.CardType != "" {
		metadata[MetadataKeyCardType] = authorization.CardType
	}
	if authorization.Bank != "" {
		metadata[MetadataKeyCardBank] = authorization.Bank
	}
	if authorization.ExpMonth != "" {
		metadata[MetadataKeyCardExpMonth] = authorization.ExpMonth
	}
	if authorization.ExpYear != "" {
		metadata[MetadataKeyCardExpYear] = authorization.ExpYear
	}

	if err := services.SubscriptionService.SaveGatewayPaymentMethod(ctx, subscriptionID, authorization.AuthorizationCode, metadata); err != nil {
		h.logger.Error(ctx, "failed to save Paystack authorization on subscription",
			"error", err,
			"subscription_id", subscriptionID,
			"flexprice_payment_id", paymentResp.ID)
		return err
	}

	h.logger.Info(ctx, "saved reusable Paystack authorization on subscription",
		"subscription_id", subscriptionID,
		"flexprice_payment_id", paymentResp.ID,
		"card_last4", authorization.Last4)

	return nil
}

func resolveFlexpricePaymentID(ctx context.Context, transaction *TransactionData, services *interfaces.ServiceDependencies) (string, error) {
	if value, ok := transaction.Metadata["flexprice_payment_id"].(string); ok && value != "" {
		return value, nil
	}

	mappings, err := services.EntityIntegrationMappingService.GetEntityIntegrationMappings(ctx, &types.EntityIntegrationMappingFilter{
		ProviderEntityIDs: []string{transaction.Reference},
		ProviderTypes:     []string{string(types.SecretProviderPaystack)},
		EntityType:        types.IntegrationEntityTypePayment,
	})
	if err == nil && mappings != nil && len(mappings.Items) > 0 {
		return mappings.Items[0].EntityID, nil
	}

	payment, lookupErr := services.PaymentService.GetPaymentByGatewayTrackingID(ctx, transaction.Reference, string(types.PaymentGatewayTypePaystack))
	if lookupErr == nil && payment != nil {
		return payment.ID, nil
	}
	return "", ierr.NewError("unable to resolve Paystack transaction to a FlexPrice payment").
		WithReportableDetails(map[string]any{"paystack_reference": transaction.Reference}).
		Mark(ierr.ErrNotFound)
}
