package middleware

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"

	"github.com/flexprice/flexprice/ent"
	"github.com/flexprice/flexprice/internal/domain/customer"
	"github.com/flexprice/flexprice/internal/ee/service"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/logger"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestInfrastructureHTTPResponses(t *testing.T) {
	for _, tt := range []struct {
		name  string
		cause error
	}{
		{"non-timeout net operation", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("synthetic transport failure")}},
		{"dns not found", &net.DNSError{IsNotFound: true, Name: "synthetic.private", Err: "synthetic sensitive text"}},
		{"dns temporary", &net.DNSError{IsTemporary: true, Err: "synthetic sensitive text"}},
		{"host unreachable", syscall.EHOSTUNREACH},
		{"network unreachable", syscall.ENETUNREACH},
		{"connection refused", syscall.ECONNREFUSED},
		{"connection reset", syscall.ECONNRESET},
		{"timed out", syscall.ETIMEDOUT},
		{"pq connection", &pq.Error{Code: "08006"}},
		{"pq insufficient resources", &pq.Error{Code: "53000"}},
		{"pq disk full", &pq.Error{Code: "53100"}},
		{"pq memory exhausted", &pq.Error{Code: "53200"}},
		{"pq connections exhausted", &pq.Error{Code: "53300"}},
		{"pq configuration limit", &pq.Error{Code: "53400"}},
		{"pq other resource error", &pq.Error{Code: "53999"}},
		{"pq system error", &pq.Error{Code: "58000"}},
		{"pq io error", &pq.Error{Code: "58030"}},
		{"pq undefined file", &pq.Error{Code: "58P01"}},
		{"pq duplicate file", &pq.Error{Code: "58P02"}},
		{"pq other system error", &pq.Error{Code: "58999"}},
		{"pq query canceled", &pq.Error{Code: "57014"}},
	} {
		for _, marker := range []error{nil, ierr.ErrNotFound, ierr.ErrValidation} {
			t.Run(fmt.Sprintf("%s/%v", tt.name, marker), func(t *testing.T) {
				err := ierr.WithError(tt.cause).Mark(ierr.ErrDatabase)
				if marker != nil {
					err = ierr.WithError(err).WithHint("synthetic sensitive hint").Mark(marker)
				}
				r := gin.New()
				r.Use(ErrorHandler())
				r.GET("/test", func(c *gin.Context) { _ = c.Error(err) })
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/test", nil))
				require.Equal(t, http.StatusServiceUnavailable, w.Code)
				require.Equal(t, "5", w.Header().Get("Retry-After"))
				require.Contains(t, w.Body.String(), "Service temporarily unavailable")
				require.NotContains(t, w.Body.String(), "synthetic")
			})
		}
	}
}

type unavailableCustomerRepository struct {
	customer.Repository
	err error
}

func (r unavailableCustomerRepository) GetByLookupKey(context.Context, string) (*customer.Customer, error) {
	return nil, r.err
}

func TestCustomerSubscriptionsAvailabilityHTTP(t *testing.T) {
	for _, tt := range []struct {
		name       string
		externalID string
		err        error
		status     int
	}{
		{"previously unrecognized transport outage", "customer", ierr.WithError(&net.OpError{Op: "dial", Err: errors.New("synthetic dial failure")}).Mark(ierr.ErrDatabase), 503},
		{"dns outage", "customer", ierr.WithError(&net.DNSError{IsNotFound: true}).Mark(ierr.ErrDatabase), 503},
		{"disk full", "customer", ierr.WithError(&pq.Error{Code: "53100"}).Mark(ierr.ErrDatabase), 503},
		{"unknown database failure", "customer", ierr.WithError(errors.New("unknown failure")).Mark(ierr.ErrDatabase), 500},
		{"real not found", "customer", ierr.WithError(&ent.NotFoundError{}).Mark(ierr.ErrNotFound), 404},
		{"repository validation", "customer", ierr.WithError(&pq.Error{Code: "22003"}).Mark(ierr.ErrValidation), 400},
		{"request validation", "", nil, 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := service.NewSubscriptionService(service.ServiceParams{
				CustomerRepo: unavailableCustomerRepository{err: tt.err},
				Logger:       &logger.Logger{SugaredLogger: zap.NewNop().Sugar()},
			})
			r := gin.New()
			r.Use(ErrorHandler())
			r.GET("/subscriptions", func(c *gin.Context) {
				response, err := svc.GetSubscriptionsForCustomer(c.Request.Context(), tt.externalID, types.NewExpand(""))
				require.Nil(t, response)
				require.Error(t, err)
				if tt.err != nil {
					require.ErrorIs(t, err, tt.err)
				}
				if tt.status >= 500 {
					require.False(t, ierr.IsNotFound(err), "the service must preserve a lookup failure without re-marking it")
				}
				_ = c.Error(err)
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/subscriptions", nil))
			require.Equal(t, tt.status, w.Code)
			if tt.status == http.StatusServiceUnavailable {
				require.Equal(t, "5", w.Header().Get("Retry-After"))
			} else {
				require.Empty(t, w.Header().Get("Retry-After"))
			}
		})
	}
}
