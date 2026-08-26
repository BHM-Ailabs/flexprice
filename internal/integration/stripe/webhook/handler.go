package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/domain/connection"
	"github.com/flexprice/flexprice/internal/domain/entityintegrationmapping"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/integration/stripe"
	"github.com/flexprice/flexprice/internal/interfaces"
	"github.com/flexprice/flexprice/internal/logger"
	"github.com/flexprice/flexprice/internal/types"
	stripeapi "github.com/stripe/stripe-go/v82"
)

// Handler handles Stripe webhook events
type Handler struct {
	client                       *stripe.Client
	customerSvc                  *stripe.CustomerService
	paymentSvc                   *stripe.PaymentService
	invoiceSyncSvc               *stripe.InvoiceSyncService
	planSvc                      stripe.StripePlanService
	subSvc                       stripe.StripeSubscriptionService
	entityIntegrationMappingRepo entityintegrationmapping.Repository
	connectionRepo               connection.Repository
	logger                       *logger.Logger
}

// NewHandler creates a new Stripe webhook handler
func NewHandler(
	client *stripe.Client,
	customerSvc *stripe.CustomerService,
	paymentSvc *stripe.PaymentService,
	invoiceSyncSvc *stripe.InvoiceSyncService,
	planSvc stripe.StripePlanService,
	subSvc stripe.StripeSubscriptionService,
	entityIntegrationMappingRepo entityintegrationmapping.Repository,
	connectionRepo connection.Repository,
	logger *logger.Logger,
) *Handler {
	return &Handler{
		client:                       client,
		customerSvc:                  customerSvc,
		paymentSvc:                   paymentSvc,
		invoiceSyncSvc:               invoiceSyncSvc,
		planSvc:                      planSvc,
		subSvc:                       subSvc,
		entityIntegrationMappingRepo: entityIntegrationMappingRepo,
		connectionRepo:               connectionRepo,
		logger:                       logger,
	}
}

// HandleWebhookEvent processes a Stripe webhook event
func (h *Handler) HandleWebhookEvent(ctx context.Context, event *stripeapi.Event, environmentID string, services *ServiceDependencies) error {
	h.logger.Info(ctx, "processing Stripe webhook event",
		"event_id", event.ID,
		"event_type", event.Type,
		"environment_id", environmentID,
	)

	switch string(event.Type) {
	case string(types.WebhookEventTypeCustomerCreated):
		return h.handleCustomerCreated(ctx, event, environmentID, services)
	case string(types.WebhookEventTypeCheckoutSessionCompleted):
		return h.handleCheckoutSessionSucceeded(ctx, event, environmentID, services, false)
	case string(types.WebhookEventTypeCheckoutSessionAsyncPaymentSucceeded):
		return h.handleCheckoutSessionSucceeded(ctx, event, environmentID, services, true)
	case string(types.WebhookEventTypeCheckoutSessionAsyncPaymentFailed):
		return h.handleCheckoutSessionTerminalFailure(ctx, event, services, false)
	case string(types.WebhookEventTypeCheckoutSessionExpired):
		return h.handleCheckoutSessionTerminalFailure(ctx, event, services, true)
	case string(types.WebhookEventTypePaymentIntentSucceeded):
		return h.handlePaymentIntentSucceeded(ctx, event, environmentID, services)
	case string(types.WebhookEventTypePaymentIntentPaymentFailed):
		return h.handlePaymentIntentPaymentFailed(ctx, event, environmentID, services)
	case string(types.WebhookEventTypeSetupIntentSucceeded):
		return h.handleSetupIntentSucceeded(ctx, event, environmentID, services)
	case string(types.WebhookEventTypeInvoicePaymentPaid):
		return h.handleInvoicePaymentPaid(ctx, event, environmentID, services)
	case string(types.WebhookEventTypeProductCreated):
		return h.handleProductCreated(ctx, event, environmentID, services)
	case string(types.WebhookEventTypeProductUpdated):
		return h.handleProductUpdated(ctx, event, environmentID, services)
	case string(types.WebhookEventTypeProductDeleted):
		return h.handleProductDeleted(ctx, event, environmentID, services)
	case string(types.WebhookEventTypeSubscriptionCreated):
		return h.handleSubscriptionCreated(ctx, event, environmentID, services)
	case string(types.WebhookEventTypeSubscriptionUpdated):
		return h.handleSubscriptionUpdated(ctx, event, environmentID, services)
	case string(types.WebhookEventTypeSubscriptionDeleted):
		return h.handleSubscriptionCancellation(ctx, event, environmentID, services)

	default:
		h.logger.Info(ctx, "unhandled Stripe webhook event type", "type", event.Type)
		return nil // Not an error, just unhandled
	}
}

// ServiceDependencies contains all service dependencies needed by webhook handlers

type ServiceDependencies = interfaces.ServiceDependencies

// handleCustomerCreated handles customer.created webhook
func (h *Handler) handleCustomerCreated(ctx context.Context, event *stripeapi.Event, environmentID string, services *ServiceDependencies) error {
	// Check sync config first

	// Parse webhook to get customer data
	var stripeCustomer stripeapi.Customer
	err := json.Unmarshal(event.Data.Raw, &stripeCustomer)
	if err != nil {
		h.logger.Error(ctx, "failed to parse customer from webhook", "error", err, "event_id", event.ID)
		return err
	}

	h.logger.Info(ctx, "received customer.created webhook",
		"stripe_customer_id", stripeCustomer.ID,
		"customer_email", stripeCustomer.Email,
		"environment_id", environmentID,
		"event_id", event.ID,
	)

	// Create customer in our system from Stripe data
	err = h.customerSvc.CreateCustomerFromStripe(ctx, &stripeCustomer, environmentID, services.CustomerService)
	if err != nil {
		h.logger.Error(ctx, "failed to create customer from Stripe webhook",
			"error", err,
			"stripe_customer_id", stripeCustomer.ID,
			"event_id", event.ID)
		return err
	}

	h.logger.Info(ctx, "successfully created customer from Stripe webhook",
		"stripe_customer_id", stripeCustomer.ID,
		"customer_email", stripeCustomer.Email,
		"event_id", event.ID)

	return nil
}

// handlePaymentIntentSucceeded handles payment_intent.succeeded webhook
func (h *Handler) handlePaymentIntentSucceeded(ctx context.Context, event *stripeapi.Event, environmentID string, services *ServiceDependencies) error {
	// Parse webhook to get payment intent ID
	var webhookPaymentIntent stripeapi.PaymentIntent
	err := json.Unmarshal(event.Data.Raw, &webhookPaymentIntent)
	if err != nil {
		h.logger.Error(ctx, "failed to parse payment intent from webhook", "error", err, "event_id", event.ID)
		return err
	}

	paymentIntentID := webhookPaymentIntent.ID
	h.logger.Info(ctx, "received payment_intent.succeeded webhook",
		"payment_intent_id", paymentIntentID,
		"environment_id", environmentID,
		"event_id", event.ID,
		"event_type", event.Type,
	)

	// Fetch the latest payment intent data from Stripe API instead of relying on webhook data
	paymentIntent, err := h.paymentSvc.GetPaymentIntent(ctx, paymentIntentID, environmentID)
	if err != nil {
		h.logger.Error(ctx, "failed to fetch payment intent from Stripe API",
			"error", err,
			"payment_intent_id", paymentIntentID,
			"event_id", event.ID)
		return err
	}

	h.logger.Info(ctx, "fetched payment intent from Stripe API",
		"payment_intent_id", paymentIntent.ID,
		"status", paymentIntent.Status,
		"amount", paymentIntent.Amount,
		"currency", paymentIntent.Currency,
		"metadata", paymentIntent.Metadata,
	)

	// Check if this is a FlexPrice-initiated payment by looking for flexprice_payment_id
	flexpricePaymentID := paymentIntent.Metadata["flexprice_payment_id"]

	if flexpricePaymentID != "" {
		// This is a FlexPrice-initiated payment - check its status
		h.logger.Info(ctx, "found FlexPrice payment ID in metadata, checking payment status",
			"payment_intent_id", paymentIntent.ID,
			"flexprice_payment_id", flexpricePaymentID)

		payment, err := services.PaymentService.GetPayment(ctx, flexpricePaymentID)
		if err != nil {
			h.logger.Error(ctx, "failed to get FlexPrice payment",
				"error", err,
				"flexprice_payment_id", flexpricePaymentID,
				"payment_intent_id", paymentIntent.ID,
				"event_id", event.ID)
			return err
		}

		// Skip card payments - they're handled synchronously by charge API
		if payment.PaymentMethodType == types.PaymentMethodTypeCard || payment.PaymentMethodType == types.PaymentMethodTypePaymentLink {
			h.logger.Info(ctx, "payment is a card payment or payment link, skipping the webhook processing",
				"flexprice_payment_id", flexpricePaymentID,
				"payment_intent_id", paymentIntent.ID)
			return nil
		}

		// If payment is already succeeded, skip processing
		if payment.PaymentStatus == types.PaymentStatusSucceeded {
			h.logger.Info(ctx, "FlexPrice payment already succeeded, skipping webhook processing",
				"flexprice_payment_id", flexpricePaymentID,
				"payment_intent_id", paymentIntent.ID,
				"payment_status", payment.PaymentStatus)
			return nil
		}

		return nil // no need to process further
	}

	// No flexprice_payment_id - this is an external Stripe payment
	err = h.paymentSvc.HandleExternalStripePaymentFromWebhook(ctx, paymentIntent, event.Data.Raw, services.PaymentService, services.InvoiceService)
	if err != nil {
		h.logger.Error(ctx, "failed to handle external Stripe payment from webhook",
			"error", err,
			"payment_intent_id", paymentIntent.ID,
			"event_id", event.ID)
		return err
	}
	return nil
}

// handlePaymentIntentPaymentFailed handles payment_intent.payment_failed webhook
func (h *Handler) handlePaymentIntentPaymentFailed(ctx context.Context, event *stripeapi.Event, environmentID string, services *ServiceDependencies) error {
	// Parse webhook to get payment intent data
	var paymentIntent stripeapi.PaymentIntent
	err := json.Unmarshal(event.Data.Raw, &paymentIntent)
	if err != nil {
		h.logger.Error(ctx, "failed to parse payment intent from webhook", "error", err, "event_id", event.ID)
		return err
	}

	paymentIntentID := paymentIntent.ID
	h.logger.Info(ctx, "received payment_intent.payment_failed webhook",
		"payment_intent_id", paymentIntentID,
		"environment_id", environmentID,
		"event_id", event.ID)

	// Get flexprice_payment_id from metadata
	flexpricePaymentID := ""
	if paymentID, exists := paymentIntent.Metadata["flexprice_payment_id"]; exists {
		flexpricePaymentID = paymentID
	}

	if flexpricePaymentID == "" {
		h.logger.Info(ctx, "no flexprice_payment_id found in payment intent metadata",
			"payment_intent_id", paymentIntentID)
		return nil
	}

	h.logger.Info(ctx, "processing FlexPrice payment failure",
		"payment_intent_id", paymentIntentID,
		"flexprice_payment_id", flexpricePaymentID)

	// Get payment record by flexprice_payment_id
	payment, err := services.PaymentService.GetPayment(ctx, flexpricePaymentID)
	if err != nil {
		h.logger.Error(ctx, "failed to get payment record",
			"error", err,
			"flexprice_payment_id", flexpricePaymentID,
			"payment_intent_id", paymentIntentID,
			"event_id", event.ID)
		return err
	}

	if payment == nil {
		h.logger.Info(ctx, "no payment record found", "flexprice_payment_id", flexpricePaymentID, "payment_intent_id", paymentIntentID)
		return nil
	}

	// Update payment status to failed
	paymentStatus := string(types.PaymentStatusFailed)
	now := time.Now()
	errorMsg := "Payment failed"
	if paymentIntent.LastPaymentError != nil && paymentIntent.LastPaymentError.Msg != "" {
		errorMsg = paymentIntent.LastPaymentError.Msg
	}

	updateReq := dto.UpdatePaymentRequest{
		PaymentStatus: &paymentStatus,
		FailedAt:      &now,
		ErrorMessage:  &errorMsg,
	}

	_, err = services.PaymentService.UpdatePayment(ctx, payment.ID, updateReq)
	if err != nil {
		h.logger.Error(ctx, "failed to update payment record for failed payment",
			"error", err,
			"payment_id", payment.ID,
			"payment_intent_id", paymentIntentID,
			"event_id", event.ID)
		return err
	}

	h.logger.Info(ctx, "successfully updated payment record for failed payment",
		"payment_id", payment.ID,
		"payment_intent_id", paymentIntentID,
		"new_status", paymentStatus,
		"error_message", errorMsg)

	return nil
}

// handleSetupIntentSucceeded handles setup_intent.succeeded webhook
func (h *Handler) handleSetupIntentSucceeded(ctx context.Context, event *stripeapi.Event, environmentID string, services *ServiceDependencies) error {
	// Parse webhook to get setup intent data
	var setupIntent stripeapi.SetupIntent
	err := json.Unmarshal(event.Data.Raw, &setupIntent)
	if err != nil {
		h.logger.Error(ctx, "failed to parse setup intent from webhook", "error", err, "event_id", event.ID)
		return err
	}

	h.logger.Info(ctx, "received setup_intent.succeeded webhook",
		"setup_intent_id", setupIntent.ID,
		"status", setupIntent.Status,
		"environment_id", environmentID,
		"event_id", event.ID,
		"event_type", event.Type,
		"has_payment_method", setupIntent.PaymentMethod != nil,
		"payment_method_id", func() string {
			if setupIntent.PaymentMethod != nil {
				return setupIntent.PaymentMethod.ID
			}
			return ""
		}(),
		"metadata", setupIntent.Metadata,
	)

	// Check if setup intent succeeded and has a payment method
	if setupIntent.Status != stripeapi.SetupIntentStatusSucceeded || setupIntent.PaymentMethod == nil {
		h.logger.Info(ctx, "setup intent not in succeeded status or no payment method attached",
			"setup_intent_id", setupIntent.ID,
			"status", setupIntent.Status,
			"has_payment_method", setupIntent.PaymentMethod != nil)
		return nil
	}

	// Check if set_default was requested
	setAsDefault, exists := setupIntent.Metadata["set_default"]
	h.logger.Info(ctx, "checking set_default metadata",
		"setup_intent_id", setupIntent.ID,
		"set_default_exists", exists,
		"set_default_value", setAsDefault,
		"all_metadata", setupIntent.Metadata)

	if !exists || setAsDefault != "true" {
		h.logger.Info(ctx, "set_as_default not requested for this setup intent",
			"setup_intent_id", setupIntent.ID,
			"set_default_exists", exists,
			"set_default_value", setAsDefault)
		return nil
	}

	// Get customer ID from metadata
	customerID, exists := setupIntent.Metadata["customer_id"]
	if !exists {
		h.logger.Info(ctx, "customer_id not found in setup intent metadata, skipping event",
			"setup_intent_id", setupIntent.ID,
			"event_id", event.ID)
		return nil
	}

	// Set the payment method as default
	paymentMethodID := setupIntent.PaymentMethod.ID
	h.logger.Info(ctx, "attempting to set payment method as default",
		"setup_intent_id", setupIntent.ID,
		"customer_id", customerID,
		"payment_method_id", paymentMethodID)

	err = h.paymentSvc.SetDefaultPaymentMethod(ctx, customerID, paymentMethodID, services.CustomerService)
	if err != nil {
		h.logger.Error(ctx, "failed to set payment method as default",
			"error", err,
			"error_type", fmt.Sprintf("%T", err),
			"setup_intent_id", setupIntent.ID,
			"customer_id", customerID,
			"payment_method_id", paymentMethodID,
			"event_id", event.ID)
		return err
	}

	h.logger.Info(ctx, "successfully processed setup intent and set payment method as default",
		"setup_intent_id", setupIntent.ID,
		"customer_id", customerID,
		"payment_method_id", paymentMethodID)

	return nil
}

// Helper methods

// getConnection retrieves the Stripe connection for the given environment
func (h *Handler) getConnection(ctx context.Context) (*connection.Connection, error) {
	conn, err := h.connectionRepo.GetByProvider(ctx, types.SecretProviderStripe)
	if err != nil {
		return nil, ierr.WithError(err).
			WithHint("Failed to get Stripe connection").
			Mark(ierr.ErrDatabase)
	}
	return conn, nil
}

// handleInvoicePaymentPaid handles invoice.paid webhook for external Stripe payments without invoice field in payment_intent
func (h *Handler) handleInvoicePaymentPaid(ctx context.Context, event *stripeapi.Event, environmentID string, services *ServiceDependencies) error {
	// Check sync config first
	conn, err := h.getConnection(ctx)
	if err != nil {
		h.logger.Error(ctx, "failed to get connection for sync config check",
			"error", err,
			"environment_id", environmentID,
			"event_id", event.ID)
		return err
	}

	if !conn.IsInvoiceOutboundEnabled() {
		h.logger.Info(ctx, "invoice outbound sync disabled, skipping event",
			"event_id", event.ID,
			"event_type", event.Type,
			"connection_id", conn.ID)
		return nil
	}

	// Parse it manually from the raw JSON since the invoice field is separate from id
	var rawData map[string]interface{}
	if err := json.Unmarshal(event.Data.Raw, &rawData); err != nil {
		h.logger.Error(ctx, "failed to parse raw webhook data",
			"error", err,
			"event_id", event.ID)
		return err
	}

	// Extract invoice ID (the actual invoice ID, not the invoice payment ID)
	stripeInvoiceID := ""
	if invoiceID, exists := rawData["invoice"]; exists {
		if invoiceStr, ok := invoiceID.(string); ok {
			stripeInvoiceID = invoiceStr
		}
	}

	// Extract payment intent ID from nested payment object
	paymentIntentID := ""
	if payment, exists := rawData["payment"]; exists {
		if paymentMap, ok := payment.(map[string]interface{}); ok {
			if piID, exists := paymentMap["payment_intent"]; exists {
				if piStr, ok := piID.(string); ok {
					paymentIntentID = piStr
				}
			}
		}
	}

	h.logger.Info(ctx, "received invoice.paid webhook",
		"event_id", event.ID,
		"stripe_invoice_id", stripeInvoiceID,
		"payment_intent_id", paymentIntentID,
		"environment_id", environmentID)

	if stripeInvoiceID == "" || paymentIntentID == "" {
		h.logger.Info(ctx, "missing invoice ID or payment intent ID in invoice.paid webhook",
			"stripe_invoice_id", stripeInvoiceID,
			"payment_intent_id", paymentIntentID)
		return nil
	}

	h.logger.Info(ctx, "processing invoice.paid webhook",
		"stripe_invoice_id", stripeInvoiceID,
		"payment_intent_id", paymentIntentID,
		"event_id", event.ID)

	// Check if payment already exists with this payment intent ID
	exists, err := h.paymentSvc.PaymentExistsByGatewayPaymentID(ctx, paymentIntentID)
	if err != nil {
		h.logger.Error(ctx, "failed to check if payment exists by gateway payment ID",
			"error", err,
			"payment_intent_id", paymentIntentID)
		return err
	} else if exists {
		h.logger.Info(ctx, "payment already exists for this payment intent, skipping",
			"payment_intent_id", paymentIntentID,
			"stripe_invoice_id", stripeInvoiceID)
		return nil
	}

	// Get payment intent details from Stripe
	paymentIntent, err := h.paymentSvc.GetPaymentIntent(ctx, paymentIntentID, environmentID)
	if err != nil {
		h.logger.Error(ctx, "failed to get payment intent from Stripe",
			"error", err,
			"payment_intent_id", paymentIntentID,
			"event_id", event.ID)
		return err
	}

	// Process external Stripe payment
	h.logger.Info(ctx, "processing external Stripe payment from invoice.paid webhook",
		"payment_intent_id", paymentIntentID,
		"stripe_invoice_id", stripeInvoiceID)

	if err := h.paymentSvc.ProcessExternalStripePayment(ctx, paymentIntent, stripeInvoiceID, services.PaymentService, services.InvoiceService); err != nil {
		h.logger.Error(ctx, "failed to process external Stripe payment",
			"error", err,
			"payment_intent_id", paymentIntentID,
			"stripe_invoice_id", stripeInvoiceID,
			"event_id", event.ID)
		return err
	}

	h.logger.Info(ctx, "successfully processed invoice.paid webhook",
		"payment_intent_id", paymentIntentID,
		"stripe_invoice_id", stripeInvoiceID)

	return nil
}

func (h *Handler) handleProductCreated(ctx context.Context, event *stripeapi.Event, environmentID string, services *ServiceDependencies) error {
	// Check sync config first
	conn, err := h.getConnection(ctx)
	if err != nil {
		h.logger.Error(ctx, "failed to get connection for sync config check",
			"error", err,
			"environment_id", environmentID,
			"event_id", event.ID)
		return err
	}

	if !conn.IsPlanInboundEnabled() {
		h.logger.Info(ctx, "plan inbound sync disabled, skipping event",
			"event_id", event.ID,
			"event_type", event.Type,
			"connection_id", conn.ID)
		return nil
	}

	// Parse webhook to get payment intent data
	var product stripeapi.Product
	err = json.Unmarshal(event.Data.Raw, &product)
	if err != nil {
		h.logger.Error(ctx, "failed to parse product from webhook", "error", err, "event_id", event.ID)
		return err
	}

	h.logger.Info(ctx, "received product.created webhook",
		"product_id", product.ID,
		"environment_id", environmentID,
		"event_id", event.ID,
		"event_type", event.Type,
	)

	// Create plan in FlexPrice
	planID := product.ID

	plan, err := h.planSvc.CreatePlan(ctx, planID, services)
	if err != nil {
		h.logger.Error(ctx, "failed to create plan in FlexPrice", "error", err, "product_id", product.ID, "event_id", event.ID)
		return err
	}

	h.logger.Info(ctx, "successfully created plan in FlexPrice", "plan_id", plan)

	return nil

}

func (h *Handler) handleProductUpdated(ctx context.Context, event *stripeapi.Event, environmentID string, services *ServiceDependencies) error {
	// Check sync config first
	conn, err := h.getConnection(ctx)
	if err != nil {
		h.logger.Error(ctx, "failed to get connection for sync config check",
			"error", err,
			"environment_id", environmentID,
			"event_id", event.ID)
		return err
	}

	if !conn.IsPlanInboundEnabled() {
		h.logger.Info(ctx, "plan inbound sync disabled, skipping event",
			"event_id", event.ID,
			"event_type", event.Type,
			"connection_id", conn.ID)
		return nil
	}

	// Parse webhook to get product data
	var product stripeapi.Product
	err = json.Unmarshal(event.Data.Raw, &product)
	if err != nil {
		h.logger.Error(ctx, "failed to parse product from webhook", "error", err, "event_id", event.ID)
		return err
	}

	h.logger.Info(ctx, "received product.updated webhook",
		"product_id", product.ID,
		"environment_id", environmentID,
		"event_id", event.ID,
		"event_type", event.Type,
	)

	// Update plan in FlexPrice
	planID := product.ID
	plan, err := h.planSvc.UpdatePlan(ctx, planID, services)
	if err != nil {
		h.logger.Error(ctx, "failed to update plan in FlexPrice", "error", err, "product_id", product.ID, "event_id", event.ID)
		return err
	}

	h.logger.Info(ctx, "successfully updated plan in FlexPrice", "plan_id", plan.ID)

	return nil
}

func (h *Handler) handleProductDeleted(ctx context.Context, event *stripeapi.Event, environmentID string, services *ServiceDependencies) error {
	// Check sync config first
	conn, err := h.getConnection(ctx)
	if err != nil {
		h.logger.Error(ctx, "failed to get connection for sync config check",
			"error", err,
			"environment_id", environmentID,
			"event_id", event.ID)
		return err
	}

	if !conn.IsPlanInboundEnabled() {
		h.logger.Info(ctx, "plan inbound sync disabled, skipping event",
			"event_id", event.ID,
			"event_type", event.Type,
			"connection_id", conn.ID)
		return nil
	}

	// Parse webhook to get product data
	var product stripeapi.Product
	err = json.Unmarshal(event.Data.Raw, &product)
	if err != nil {
		h.logger.Error(ctx, "failed to parse product from webhook", "error", err, "event_id", event.ID)
		return err
	}

	h.logger.Info(ctx, "received product.deleted webhook", "product_id", product.ID)

	// Delete plan in FlexPrice
	planID := product.ID
	err = h.planSvc.DeletePlan(ctx, planID, services)
	if err != nil {
		h.logger.Error(ctx, "failed to delete plan in FlexPrice", "error", err, "product_id", product.ID, "event_id", event.ID)
		return err
	}

	h.logger.Info(ctx, "successfully deleted plan in FlexPrice", "plan_id", planID)

	return nil
}

func (h *Handler) handleSubscriptionCreated(ctx context.Context, event *stripeapi.Event, environmentID string, services *ServiceDependencies) error {
	// Check sync config first
	conn, err := h.getConnection(ctx)
	if err != nil {
		h.logger.Error(ctx, "failed to get connection for sync config check",
			"error", err,
			"environment_id", environmentID,
			"event_id", event.ID)
		return err
	}

	if !conn.IsSubscriptionInboundEnabled() {
		h.logger.Info(ctx, "subscription inbound sync disabled, skipping event",
			"event_id", event.ID,
			"event_type", event.Type,
			"connection_id", conn.ID)
		return nil
	}

	// Parse webhook to get payment intent data
	var subscription stripeapi.Subscription
	err = json.Unmarshal(event.Data.Raw, &subscription)
	if err != nil {
		h.logger.Error(ctx, "failed to parse subscription from webhook", "error", err, "event_id", event.ID)
		return err
	}

	h.logger.Info(ctx, "received customer.subscription.created webhook",
		"subscription_id", subscription.ID,
		"environment_id", environmentID,
		"event_id", event.ID,
		"event_type", event.Type,
	)

	// Create plan in FlexPrice
	subID := subscription.ID

	sub, err := h.subSvc.CreateSubscription(ctx, subID, services)
	if err != nil {
		h.logger.Error(ctx, "failed to create subscription in FlexPrice", "error", err, "subscription_id", subscription.ID, "event_id", event.ID)
		return err
	}

	h.logger.Info(ctx, "successfully created subscription in FlexPrice", "subscription_id", sub.ID)

	return nil

}

func (h *Handler) handleSubscriptionUpdated(ctx context.Context, event *stripeapi.Event, environmentID string, services *ServiceDependencies) error {
	// Check sync config first
	conn, err := h.getConnection(ctx)
	if err != nil {
		h.logger.Error(ctx, "failed to get connection for sync config check",
			"error", err,
			"environment_id", environmentID,
			"event_id", event.ID)
		return err
	}

	if !conn.IsSubscriptionInboundEnabled() {
		h.logger.Info(ctx, "subscription inbound sync disabled, skipping event",
			"event_id", event.ID,
			"event_type", event.Type,
			"connection_id", conn.ID)
		return nil
	}

	// Parse webhook to get product data
	var subscription stripeapi.Subscription
	err = json.Unmarshal(event.Data.Raw, &subscription)
	if err != nil {
		h.logger.Error(ctx, "failed to parse subscription from webhook", "error", err, "event_id", event.ID)
		return err
	}

	h.logger.Info(ctx, "received customer.subscription.updated webhook",
		"subscription_id", subscription.ID,
		"environment_id", environmentID,
		"event_id", event.ID,
		"event_type", event.Type,
	)

	// Update plan in FlexPrice
	subscriptionID := subscription.ID
	err = h.subSvc.UpdateSubscription(ctx, subscriptionID, services)
	if err != nil {
		h.logger.Error(ctx, "failed to update subscription in FlexPrice", "error", err, "subscription_id", subscription.ID, "event_id", event.ID)
		return err
	}

	h.logger.Info(ctx, "successfully updated subscription in FlexPrice", "subscription_id", subscriptionID)

	return nil
}

func (h *Handler) handleSubscriptionCancellation(ctx context.Context, event *stripeapi.Event, environmentID string, services *ServiceDependencies) error {
	// Check sync config first
	conn, err := h.getConnection(ctx)
	if err != nil {
		h.logger.Error(ctx, "failed to get connection for sync config check",
			"error", err,
			"environment_id", environmentID,
			"event_id", event.ID)
		return err
	}

	if !conn.IsSubscriptionInboundEnabled() {
		h.logger.Info(ctx, "subscription inbound sync disabled, skipping event",
			"event_id", event.ID,
			"event_type", event.Type,
			"connection_id", conn.ID)
		return nil
	}

	// Parse webhook to get product data
	var subscription stripeapi.Subscription
	err = json.Unmarshal(event.Data.Raw, &subscription)
	if err != nil {
		h.logger.Error(ctx, "failed to parse subscription from webhook", "error", err, "event_id", event.ID)
		return err
	}

	h.logger.Info(ctx, "received customer.subscription.deleted webhook", "subscription_id", subscription.ID)

	// Delete plan in FlexPrice
	subID := subscription.ID
	err = h.subSvc.CancelSubscription(ctx, subID, services)
	if err != nil {
		h.logger.Error(ctx, "failed to delete subscription in FlexPrice", "error", err, "subscription_id", subscription.ID, "event_id", event.ID)
		return err
	}

	h.logger.Info(ctx, "successfully deleted subscription in FlexPrice", "subscription_id", subID)

	return nil
}

func checkoutSessionPaymentSucceeded(session *stripeapi.CheckoutSession, paymentIntent *stripeapi.PaymentIntent) bool {
	return session != nil &&
		session.PaymentStatus == stripeapi.CheckoutSessionPaymentStatusPaid &&
		paymentIntent != nil &&
		paymentIntent.Status == stripeapi.PaymentIntentStatusSucceeded
}

func (h *Handler) handleCheckoutSessionSucceeded(ctx context.Context, event *stripeapi.Event, environmentID string, services *ServiceDependencies, async bool) error {
	// Parse webhook to get checkout session data
	var checkoutSession stripeapi.CheckoutSession
	err := json.Unmarshal(event.Data.Raw, &checkoutSession)
	if err != nil {
		h.logger.Error(ctx, "failed to parse checkout session from webhook", "error", err, "event_id", event.ID)
		return err
	}

	// get flexprice_payment_id from metadata
	flexpricePaymentID := checkoutSession.Metadata["flexprice_payment_id"]
	if flexpricePaymentID == "" {
		h.logger.Info(ctx, "no flexprice_payment_id found in checkout session metadata", "event_id", event.ID)
		return nil
	}
	// A completed Checkout Session can still be unpaid for delayed methods. Ack
	// that event and wait for async_payment_succeeded rather than fulfilling it.
	if checkoutSession.PaymentStatus != stripeapi.CheckoutSessionPaymentStatusPaid {
		if async {
			return ierr.NewError("Stripe reported async checkout success without paid status").
				WithHint("Retry after Stripe marks the Checkout Session paid").
				Mark(ierr.ErrSystem)
		}
		h.logger.Info(ctx, "checkout session completed before payment settled",
			"checkout_session_id", checkoutSession.ID,
			"payment_status", checkoutSession.PaymentStatus,
			"event_id", event.ID)
		return nil
	}

	// get payment from database
	payment, err := services.PaymentService.GetPayment(ctx, flexpricePaymentID)
	if err != nil {
		h.logger.Error(ctx, "failed to get payment from database", "error", err, "event_id", event.ID)
		return err
	}

	// check if payment is already succeeded
	if payment.PaymentStatus == types.PaymentStatusSucceeded {
		h.logger.Info(ctx, "payment already succeeded, skipping event", "event_id", event.ID)
		return nil
	}

	if checkoutSession.PaymentIntent == nil || checkoutSession.PaymentIntent.ID == "" {
		return ierr.NewError("paid Stripe checkout session has no payment intent").
			WithHint("Retry after Stripe attaches the PaymentIntent").
			Mark(ierr.ErrSystem)
	}
	paymentIntentID := checkoutSession.PaymentIntent.ID
	paymentIntent, err := h.paymentSvc.GetPaymentIntent(ctx, paymentIntentID, environmentID)
	if err != nil {
		h.logger.Error(ctx, "failed to fetch checkout payment intent",
			"error", err,
			"payment_intent_id", paymentIntentID,
			"event_id", event.ID)
		return err
	}
	if !checkoutSessionPaymentSucceeded(&checkoutSession, paymentIntent) {
		return ierr.NewError("Stripe checkout payment is not settled").
			WithHint("Retry after the PaymentIntent succeeds").
			WithReportableDetails(map[string]interface{}{
				"checkout_session_id":   checkoutSession.ID,
				"payment_intent_id":     paymentIntentID,
				"payment_status":        checkoutSession.PaymentStatus,
				"payment_intent_status": paymentIntent.Status,
			}).
			Mark(ierr.ErrSystem)
	}

	// Fulfillment is gated on both Stripe settlement signals.
	err = h.paymentSvc.HandleFlexPriceCheckoutPayment(ctx, &checkoutSession, paymentIntent, payment, services.CustomerService, services.InvoiceService, services.PaymentService)
	if err != nil {
		h.logger.Error(ctx, "failed to handle FlexPrice checkout payment",
			"error", err,
			"flexprice_payment_id", flexpricePaymentID,
			"event_id", event.ID)
		return err
	}

	return nil
}

func (h *Handler) handleCheckoutSessionTerminalFailure(ctx context.Context, event *stripeapi.Event, services *ServiceDependencies, expired bool) error {
	var checkoutSession stripeapi.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &checkoutSession); err != nil {
		h.logger.Error(ctx, "failed to parse terminal checkout session webhook", "error", err, "event_id", event.ID)
		return err
	}

	flexpricePaymentID := checkoutSession.Metadata["flexprice_payment_id"]
	if flexpricePaymentID == "" {
		h.logger.Info(ctx, "no flexprice_payment_id found in checkout session metadata", "event_id", event.ID)
		return nil
	}
	payment, err := services.PaymentService.GetPayment(ctx, flexpricePaymentID)
	if err != nil {
		h.logger.Error(ctx, "failed to get payment for terminal checkout event", "error", err, "event_id", event.ID)
		return err
	}
	if payment.PaymentStatus == types.PaymentStatusSucceeded || payment.PaymentStatus == types.PaymentStatusFailed {
		return nil
	}

	paymentStatus := string(types.PaymentStatusFailed)
	failedAt := time.Now()
	errorMessage := "Asynchronous checkout payment failed"
	if expired {
		errorMessage = "Checkout session expired before payment"
	}
	_, err = services.PaymentService.UpdatePayment(ctx, payment.ID, dto.UpdatePaymentRequest{
		PaymentStatus: &paymentStatus,
		FailedAt:      &failedAt,
		ErrorMessage:  &errorMessage,
	})
	if err != nil {
		h.logger.Error(ctx, "failed to record terminal checkout event", "error", err, "payment_id", payment.ID, "event_id", event.ID)
		return err
	}
	return nil
}
