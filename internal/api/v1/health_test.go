package v1

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

type readinessStub struct {
	ready bool
	calls int
}

func (s *readinessStub) Ready(context.Context) bool { s.calls++; return s.ready }

func TestHealthRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, ready := range []bool{false, true} {
		svc := &readinessStub{ready: ready}
		h := NewHealthHandler(nil, svc)
		r := gin.New()
		r.GET("/health", h.Health)
		r.GET("/health/ready", h.Ready)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
		require.Equal(t, 200, w.Code)
		require.Zero(t, svc.calls)
		w = httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/health/ready", nil))
		if ready {
			require.Equal(t, 200, w.Code)
		} else {
			require.Equal(t, 503, w.Code)
			require.Equal(t, "5", w.Header().Get("Retry-After"))
		}
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		require.Equal(t, 1, svc.calls)
	}
}
