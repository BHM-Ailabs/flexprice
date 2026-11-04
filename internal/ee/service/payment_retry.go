package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/domain/payment"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/samber/lo"
)

type paymentRetryProof struct {
	gatewayPaymentID  string
	gatewayTrackingID string
	paymentID         string
	amountMinor       int64
	currency          string
	succeededAt       time.Time
}

type paymentRetrySuccessReader func(context.Context, *payment.Payment) (*paymentRetryProof, error)

// FAILED remains sealed in the public transition table. This exception reads
// the already-bound gateway transaction itself; request fields are never proof
// of payment and cannot change its provider, amount or identity.
func (s *paymentService) verifyPaymentRetry(ctx context.Context, p *payment.Payment, req dto.UpdatePaymentRequest) (*paymentRetryProof, error) {
	if p.TenantID != types.GetTenantID(ctx) || p.EnvironmentID != types.GetEnvironmentID(ctx) {
		return nil, retryVerificationError()
	}
	gateway := lo.FromPtr(p.PaymentGateway)
	if gateway != string(types.PaymentGatewayTypePaystack) && gateway != string(types.PaymentGatewayTypeStripe) {
		return nil, retryVerificationError()
	}
	if req.PaymentGateway != nil && *req.PaymentGateway != gateway {
		return nil, retryVerificationError()
	}
	if req.Amount != nil {
		return nil, retryVerificationError()
	}
	reader := s.retrySuccessReader
	if reader == nil {
		reader = s.readPaymentRetrySuccess
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	proof, err := reader(verifyCtx, p)
	if err != nil {
		return nil, ierr.WithError(err).WithHint("Unable to verify the successful payment retry").Mark(ierr.ErrSystem)
	}
	if proof == nil || proof.gatewayPaymentID == "" || proof.succeededAt.IsZero() ||
		proof.amountMinor <= 0 || proof.amountMinor != types.ToSmallestUnit(p.Amount, p.Currency) ||
		!strings.EqualFold(proof.currency, p.Currency) || proof.paymentID != p.ID {
		return nil, retryVerificationError()
	}
	trackingID := lo.FromPtr(p.GatewayTrackingID)
	gatewayID := lo.FromPtr(p.GatewayPaymentID)
	if trackingID == "" && gatewayID == "" {
		return nil, retryVerificationError()
	}
	if trackingID != "" && proof.gatewayTrackingID != trackingID {
		return nil, retryVerificationError()
	}
	if gatewayID != "" && gatewayID != proof.gatewayPaymentID && gatewayID != proof.gatewayTrackingID {
		return nil, retryVerificationError()
	}
	if req.GatewayPaymentID != nil && *req.GatewayPaymentID != proof.gatewayPaymentID {
		return nil, retryVerificationError()
	}
	return proof, nil
}

func retryVerificationError() error {
	return ierr.NewError("failed payment retry is not verified").
		WithHint("A failed payment can settle only after its original gateway confirms the matching transaction").
		Mark(ierr.ErrValidation)
}

func (s *paymentService) readPaymentRetrySuccess(ctx context.Context, p *payment.Payment) (*paymentRetryProof, error) {
	if s.IntegrationFactory == nil {
		return nil, retryVerificationError()
	}
	trackingID := lo.FromPtr(p.GatewayTrackingID)
	switch types.PaymentGatewayType(lo.FromPtr(p.PaymentGateway)) {
	case types.PaymentGatewayTypePaystack:
		// A numeric transaction ID is not a Paystack reference. Never substitute
		// a caller's new reference when the original checkout binding is absent.
		if trackingID == "" {
			return nil, retryVerificationError()
		}
		integration, err := s.IntegrationFactory.GetPaystackIntegration(ctx)
		if err != nil {
			return nil, err
		}
		transaction, err := integration.Client.VerifyTransaction(ctx, trackingID)
		if err != nil {
			return nil, err
		}
		if transaction == nil || transaction.Status != "success" || transaction.Reference != trackingID {
			return nil, retryVerificationError()
		}
		paidAt, err := time.Parse(time.RFC3339, transaction.PaidAt)
		if err != nil {
			return nil, retryVerificationError()
		}
		paymentID, _ := transaction.Metadata["flexprice_payment_id"].(string)
		gatewayID := transaction.Reference
		if transaction.ID != 0 {
			gatewayID = fmt.Sprintf("%d", transaction.ID)
		}
		return &paymentRetryProof{gatewayPaymentID: gatewayID, gatewayTrackingID: transaction.Reference,
			paymentID: paymentID, amountMinor: transaction.Amount, currency: transaction.Currency, succeededAt: paidAt.UTC()}, nil
	case types.PaymentGatewayTypeStripe:
		integration, err := s.IntegrationFactory.GetStripeIntegration(ctx)
		if err != nil {
			return nil, err
		}
		intentID := lo.FromPtr(p.GatewayPaymentID)
		if trackingID != "" {
			session, err := integration.PaymentSvc.GetPaymentStatus(ctx, trackingID, types.GetEnvironmentID(ctx))
			if err != nil {
				return nil, err
			}
			if session == nil || session.SessionID != trackingID || session.PaymentIntentID == "" ||
				(intentID != "" && session.PaymentIntentID != intentID) ||
				(session.Metadata["flexprice_payment_id"] != "" && session.Metadata["flexprice_payment_id"] != p.ID) {
				return nil, retryVerificationError()
			}
			intentID = session.PaymentIntentID
		}
		if intentID == "" {
			return nil, retryVerificationError()
		}
		intent, err := integration.PaymentSvc.GetPaymentIntent(ctx, intentID, types.GetEnvironmentID(ctx))
		if err != nil {
			return nil, err
		}
		if intent == nil || intent.ID != intentID || intent.Status != "succeeded" || intent.AmountReceived != intent.Amount {
			return nil, retryVerificationError()
		}
		return &paymentRetryProof{gatewayPaymentID: intent.ID, gatewayTrackingID: trackingID,
			paymentID: intent.Metadata["flexprice_payment_id"], amountMinor: intent.Amount,
			currency: string(intent.Currency), succeededAt: time.Now().UTC()}, nil
	default:
		return nil, retryVerificationError()
	}
}
