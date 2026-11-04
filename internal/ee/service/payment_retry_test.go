package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/domain/payment"
	"github.com/flexprice/flexprice/internal/integration/payments"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/samber/lo"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func retryFixture(ctx context.Context) (*payment.Payment, *paymentRetryProof, dto.UpdatePaymentRequest) {
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	p := &payment.Payment{
		ID: "pay_retry", DestinationType: types.PaymentDestinationTypeInvoice, DestinationID: "inv_retry",
		EnvironmentID: types.GetEnvironmentID(ctx),
		PaymentStatus: types.PaymentStatusFailed, PaymentGateway: lo.ToPtr("paystack"),
		GatewayTrackingID: lo.ToPtr("original-reference"), GatewayPaymentID: lo.ToPtr("42"),
		Amount: decimal.NewFromInt(100), Currency: "NGN", BaseModel: types.GetDefaultBaseModel(ctx),
	}
	proof := &paymentRetryProof{gatewayPaymentID: "42", gatewayTrackingID: "original-reference",
		paymentID: p.ID, amountMinor: 10000, currency: "ngn", succeededAt: at}
	return p, proof, dto.UpdatePaymentRequest{PaymentStatus: lo.ToPtr("SUCCEEDED")}
}

func TestVerifyPaymentRetry(t *testing.T) {
	ctx := types.SetEnvironmentID(types.SetTenantID(context.Background(), "tenant-retry"), "env-retry")
	tests := []struct {
		name   string
		change func(*payment.Payment, *paymentRetryProof, *dto.UpdatePaymentRequest)
	}{
		{"different tenant", func(p *payment.Payment, _ *paymentRetryProof, _ *dto.UpdatePaymentRequest) { p.TenantID = "foreign" }},
		{"different environment", func(p *payment.Payment, _ *paymentRetryProof, _ *dto.UpdatePaymentRequest) {
			p.EnvironmentID = "foreign"
		}},
		{"unsupported gateway", func(p *payment.Payment, _ *paymentRetryProof, _ *dto.UpdatePaymentRequest) {
			p.PaymentGateway = lo.ToPtr("moyasar")
		}},
		{"changed gateway", func(_ *payment.Payment, _ *paymentRetryProof, r *dto.UpdatePaymentRequest) {
			r.PaymentGateway = lo.ToPtr("stripe")
		}},
		{"changed amount", func(_ *payment.Payment, _ *paymentRetryProof, r *dto.UpdatePaymentRequest) {
			r.Amount = lo.ToPtr(decimal.NewFromInt(1))
		}},
		{"wrong received amount", func(_ *payment.Payment, p *paymentRetryProof, _ *dto.UpdatePaymentRequest) { p.amountMinor-- }},
		{"zero received amount", func(_ *payment.Payment, p *paymentRetryProof, _ *dto.UpdatePaymentRequest) { p.amountMinor = 0 }},
		{"wrong currency", func(_ *payment.Payment, p *paymentRetryProof, _ *dto.UpdatePaymentRequest) { p.currency = "USD" }},
		{"foreign metadata", func(_ *payment.Payment, p *paymentRetryProof, _ *dto.UpdatePaymentRequest) { p.paymentID = "foreign" }},
		{"missing payment metadata", func(_ *payment.Payment, p *paymentRetryProof, _ *dto.UpdatePaymentRequest) { p.paymentID = "" }},
		{"wrong reference", func(_ *payment.Payment, p *paymentRetryProof, _ *dto.UpdatePaymentRequest) {
			p.gatewayTrackingID = "foreign"
		}},
		{"wrong transaction", func(_ *payment.Payment, p *paymentRetryProof, _ *dto.UpdatePaymentRequest) {
			p.gatewayPaymentID = "foreign"
		}},
		{"caller supplied transaction", func(_ *payment.Payment, _ *paymentRetryProof, r *dto.UpdatePaymentRequest) {
			r.GatewayPaymentID = lo.ToPtr("foreign")
		}},
		{"missing gateway transaction", func(_ *payment.Payment, p *paymentRetryProof, _ *dto.UpdatePaymentRequest) { p.gatewayPaymentID = "" }},
		{"missing paid time", func(_ *payment.Payment, p *paymentRetryProof, _ *dto.UpdatePaymentRequest) {
			p.succeededAt = time.Time{}
		}},
		{"no original binding", func(p *payment.Payment, _ *paymentRetryProof, _ *dto.UpdatePaymentRequest) {
			p.GatewayPaymentID = nil
			p.GatewayTrackingID = nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, proof, req := retryFixture(ctx)
			tt.change(p, proof, &req)
			svc := &paymentService{retrySuccessReader: func(context.Context, *payment.Payment) (*paymentRetryProof, error) { return proof, nil }}
			_, err := svc.verifyPaymentRetry(ctx, p, req)
			require.Error(t, err)
		})
	}
	t.Run("original verified receipt", func(t *testing.T) {
		p, proof, req := retryFixture(ctx)
		svc := &paymentService{retrySuccessReader: func(ctx context.Context, _ *payment.Payment) (*paymentRetryProof, error) {
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			require.LessOrEqual(t, time.Until(deadline), 5*time.Second)
			return proof, nil
		}}
		got, err := svc.verifyPaymentRetry(ctx, p, req)
		require.NoError(t, err)
		require.Equal(t, proof, got)
	})
	t.Run("unavailable gateway is not success", func(t *testing.T) {
		p, _, req := retryFixture(ctx)
		svc := &paymentService{retrySuccessReader: func(context.Context, *payment.Payment) (*paymentRetryProof, error) { return nil, errors.New("offline") }}
		_, err := svc.verifyPaymentRetry(ctx, p, req)
		require.Error(t, err)
	})
	t.Run("no gateway factory fails closed", func(t *testing.T) {
		p, _, req := retryFixture(ctx)
		_, err := (&paymentService{}).verifyPaymentRetry(ctx, p, req)
		require.Error(t, err)
	})
}

func (s *PaymentServiceSuite) TestFailedPaymentVerifiedRetrySettlesOnce() {
	ctx := s.GetContext()
	p, proof, _ := retryFixture(ctx)
	p.DestinationID = s.testData.invoice.ID
	p.Currency, proof.currency = "USD", "usd"
	p.PaymentMethodType = types.PaymentMethodTypePaymentLink
	s.Require().NoError(s.GetStores().PaymentRepo.Create(ctx, p))
	s.service.(*paymentService).retrySuccessReader = func(context.Context, *payment.Payment) (*paymentRetryProof, error) { return proof, nil }
	lifecycle := payments.NewPaymentLifecycle(s.service, NewInvoiceService(s.service.(*paymentService).ServiceParams), s.GetLogger())
	params := payments.RecordPaymentSuccessParams{FlexpricePaymentID: p.ID, GatewayPaymentID: "42", SucceededAt: proof.succeededAt}
	s.Require().NoError(lifecycle.RecordPaymentSuccess(ctx, params))
	s.Require().NoError(lifecycle.RecordPaymentSuccess(ctx, params))
	stored, err := s.GetStores().PaymentRepo.Get(ctx, p.ID)
	s.Require().NoError(err)
	s.Equal(types.PaymentStatusSucceeded, stored.PaymentStatus)
	s.Equal(proof.succeededAt, *stored.SucceededAt)
	inv, err := s.GetStores().InvoiceRepo.Get(ctx, p.DestinationID)
	s.Require().NoError(err)
	s.True(decimal.NewFromInt(100).Equal(inv.AmountPaid), inv.AmountPaid.String())
	s.Equal(types.PaymentStatusSucceeded, inv.PaymentStatus)
	_, err = s.service.UpdatePayment(ctx, p.ID, dto.UpdatePaymentRequest{PaymentStatus: lo.ToPtr("FAILED")})
	s.Error(err)
	s.Equal(types.PaymentStatusSucceeded, stored.PaymentStatus)
}

func (s *PaymentServiceSuite) TestFailedPaymentCannotSettleWithoutGatewayProof() {
	ctx := s.GetContext()
	p, _, req := retryFixture(ctx)
	p.DestinationID = s.testData.invoice.ID
	s.Require().NoError(s.GetStores().PaymentRepo.Create(ctx, p))
	_, err := s.service.UpdatePayment(ctx, p.ID, req)
	s.Require().Error(err)
	stored, err := s.GetStores().PaymentRepo.Get(ctx, p.ID)
	s.Require().NoError(err)
	s.Equal(types.PaymentStatusFailed, stored.PaymentStatus)
}

func (s *PaymentServiceSuite) TestFailedPaymentCannotRebindGatewayBeforeRetry() {
	ctx := s.GetContext()
	p, _, _ := retryFixture(ctx)
	p.DestinationID = s.testData.invoice.ID
	s.Require().NoError(s.GetStores().PaymentRepo.Create(ctx, p))
	for _, req := range []dto.UpdatePaymentRequest{
		{GatewayPaymentID: lo.ToPtr("foreign")},
		{PaymentStatus: lo.ToPtr("FAILED"), GatewayPaymentID: lo.ToPtr("foreign")},
		{PaymentGateway: lo.ToPtr("stripe")},
	} {
		_, err := s.service.UpdatePayment(ctx, p.ID, req)
		s.Require().Error(err)
	}
	stored, err := s.GetStores().PaymentRepo.Get(ctx, p.ID)
	s.Require().NoError(err)
	s.Equal("42", lo.FromPtr(stored.GatewayPaymentID))
	s.Equal("paystack", lo.FromPtr(stored.PaymentGateway))
}

func (s *PaymentServiceSuite) TestFailedHostedPaymentSurvivesForLateSuccess() {
	ctx := s.GetContext()
	p, _, _ := retryFixture(ctx)
	p.DestinationID = s.testData.invoice.ID
	s.Require().NoError(s.GetStores().PaymentRepo.Create(ctx, p))
	s.Require().Error(s.service.DeletePayment(ctx, p.ID))
	_, err := s.GetStores().PaymentRepo.Get(ctx, p.ID)
	s.Require().NoError(err)
}
