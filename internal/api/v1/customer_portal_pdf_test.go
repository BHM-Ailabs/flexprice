package v1

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flexprice/flexprice/internal/ee/service"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/rest/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type portalPDFResponseStub struct {
	service.CustomerPortalService
	err error
}

func (s portalPDFResponseStub) GetInvoicePDF(context.Context, string) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	return []byte("%PDF-1.7\nowned invoice"), nil
}

func TestCustomerPortalPDFContentResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"owned PDF", nil, http.StatusOK},
		{"wrong customer", ierr.NewError("invoice not found").Mark(ierr.ErrNotFound), http.StatusNotFound},
		{"missing customer", ierr.NewError("customer required").Mark(ierr.ErrPermissionDenied), http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &CustomerPortalHandler{portalService: portalPDFResponseStub{err: tc.err}}
			router := gin.New()
			router.Use(middleware.ErrorHandler())
			router.GET("/invoices/:id/pdf/content", h.GetInvoicePDFContent)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/invoices/inv_sample/pdf/content", nil))
			require.Equal(t, tc.status, response.Code)
			if tc.err == nil {
				require.Equal(t, "application/pdf", response.Header().Get("Content-Type"))
				require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
				require.Equal(t, "attachment; filename=invoice-inv_sample.pdf", response.Header().Get("Content-Disposition"))
				require.Equal(t, "%PDF-1.7\nowned invoice", response.Body.String())
			} else {
				require.NotContains(t, response.Body.String(), "%PDF")
				require.Empty(t, response.Header().Get("Content-Disposition"))
			}
		})
	}
}
