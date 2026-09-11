package v1

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domain "github.com/flexprice/flexprice/internal/domain/invoice"
	"github.com/flexprice/flexprice/internal/ee/service"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/rest/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type referenceServiceStub struct {
	service.InvoiceService
	err       error
	received  domain.ReferenceRequest
	invoiceID string
	applied   bool
}

func (s *referenceServiceStub) ReservePublicReference(_ context.Context, r domain.ReferenceRequest) (*domain.PublicReference, error) {
	s.received = r
	return &domain.PublicReference{Number: "100001", ReservationKey: r.ReservationKey, Aliases: []string{}}, s.err
}
func (s *referenceServiceStub) BindPublicReference(_ context.Context, id string, r domain.ReferenceRequest) (*domain.PublicReference, error) {
	s.invoiceID = id
	s.received = r
	return &domain.PublicReference{Number: "100001", InvoiceID: &id, CustomerID: &r.ExpectedCustomerID, Aliases: r.Aliases}, s.err
}
func (s *referenceServiceStub) BackfillPublicReferences(_ context.Context, _ string, _ int, apply bool) (map[string]interface{}, error) {
	s.applied = apply
	return map[string]interface{}{"apply": apply}, s.err
}
func TestInvoiceReferenceHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, path, body string
		denied           bool
		status           int
	}{
		{"reserve", "/references/reserve", `{"reservation_key":"plaqad-prepaid:one"}`, false, 200},
		{"bind", "/inv_sample/reference", `{"expected_customer_id":"cust_sample","aliases":["OLD-1"]}`, false, 200},
		{"default dry run", "/references/backfill", `{}`, false, 200},
		{"scope denied", "/references/reserve", `{"reservation_key":"plaqad-prepaid:one"}`, true, 403},
		{"malformed", "/references/reserve", `{`, false, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &referenceServiceStub{}
			if tc.denied {
				stub.err = ierr.NewError("scope denied").Mark(ierr.ErrPermissionDenied)
			}
			h := &InvoiceHandler{invoiceService: stub}
			r := gin.New()
			r.Use(middleware.ErrorHandler())
			r.POST("/references/reserve", h.ReservePublicReference)
			r.POST("/:id/reference", h.BindPublicReference)
			r.POST("/references/backfill", h.BackfillPublicReferences)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code)
			if tc.name == "bind" {
				require.Equal(t, "inv_sample", stub.invoiceID)
				require.Equal(t, "cust_sample", stub.received.ExpectedCustomerID)
				require.Equal(t, []string{"OLD-1"}, stub.received.Aliases)
			}
			require.False(t, stub.applied)
			if tc.status == 200 && tc.name != "default dry run" {
				require.Contains(t, w.Body.String(), `"public_reference":"100001"`)
			}
		})
	}
}
