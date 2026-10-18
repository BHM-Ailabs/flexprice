package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flexprice/flexprice/internal/config"
	"github.com/flexprice/flexprice/internal/domain/secret"
	domainUser "github.com/flexprice/flexprice/internal/domain/user"
	"github.com/flexprice/flexprice/internal/ee/service"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type verifyingSecretService struct {
	service.SecretService
	verify func(context.Context) (*secret.Secret, error)
}

func (s *verifyingSecretService) VerifyAPIKey(ctx context.Context, _ string) (*secret.Secret, error) {
	return s.verify(ctx)
}

func TestAPIKeyHTTPAvailability(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mixed := range []bool{false, true} {
		for _, tt := range []struct {
			name   string
			err    error
			status int
		}{
			{"unknown key", ierr.ErrValidation, 401},
			{"not found", ierr.ErrNotFound, 401},
			{"database", ierr.ErrDatabase, 503},
			{"deadline", context.DeadlineExceeded, 503},
			{"unavailable", ierr.ErrServiceUnavailable, 503},
			{"repository error", errors.New("synthetic sensitive driver diagnostic"), 503},
			{"valid", nil, 200},
		} {
			t.Run(tt.name+map[bool]string{true: "/mixed", false: "/key-only"}[mixed], func(t *testing.T) {
				cfg := &config.Configuration{Auth: config.AuthConfig{Provider: types.AuthProviderFlexprice, Secret: testSecret, APIKey: config.APIKeyConfig{Header: "x-api-key"}}}
				svc := &verifyingSecretService{verify: func(ctx context.Context) (*secret.Secret, error) {
					deadline, ok := ctx.Deadline()
					require.True(t, ok)
					require.LessOrEqual(t, time.Until(deadline), authLookupTimeout)
					if tt.err != nil {
						return nil, tt.err
					}
					return &secret.Secret{EnvironmentID: "env_prod", UserID: "usr_dev", BaseModel: types.BaseModel{TenantID: "t_tenant1"}}, nil
				}}
				r := gin.New()
				if mixed {
					r.Use(AuthenticateMiddleware(cfg, svc, nil, nil, newTestLogger(t)))
				} else {
					r.Use(APIKeyAuthMiddleware(cfg, svc, nil, newTestLogger(t)))
				}
				r.GET("/test", func(c *gin.Context) { c.Status(200) })
				req := httptest.NewRequest("GET", "/test", nil)
				req.Header.Set("x-api-key", "synthetic-key")
				// An outage must stop mixed auth before it falls back to this invalid JWT.
				if mixed {
					req.Header.Set("Authorization", "Bearer invalid")
				}
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				require.Equal(t, tt.status, w.Code)
				if tt.status == 503 {
					require.Equal(t, "5", w.Header().Get("Retry-After"))
					require.Contains(t, w.Body.String(), `"code":"service_unavailable"`)
					require.NotContains(t, w.Body.String(), "driver diagnostic")
				}
			})
		}
	}
}

func TestAPIKeyLookupDeadline(t *testing.T) {
	cfg := &config.Configuration{Auth: config.AuthConfig{APIKey: config.APIKeyConfig{Header: "x-api-key"}}}
	svc := &verifyingSecretService{verify: func(ctx context.Context) (*secret.Secret, error) { <-ctx.Done(); return nil, ctx.Err() }}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, _, _, _, valid, err := validateAPIKey(ctx, cfg, svc, "synthetic-key")
	require.False(t, valid)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), time.Second)
}

func TestDependencyErrorsFromHandlers(t *testing.T) {
	for _, err := range []error{ierr.WithError(context.DeadlineExceeded).Mark(ierr.ErrDatabase), context.DeadlineExceeded, ierr.ErrServiceUnavailable} {
		r := gin.New()
		r.Use(ErrorHandler())
		r.GET("/test", func(c *gin.Context) { _ = c.Error(err) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/test", nil))
		require.Equal(t, 503, w.Code)
		require.Equal(t, "5", w.Header().Get("Retry-After"))
		require.Contains(t, w.Body.String(), "Service temporarily unavailable")
	}
}

type unavailableUserRepository struct{ domainUser.Repository }

func (*unavailableUserRepository) GetByID(ctx context.Context, _ string) (*domainUser.User, error) {
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("missing lookup deadline")
	}
	return nil, ierr.ErrDatabase
}

func TestJWTUserLookupUnavailable(t *testing.T) {
	cfg := &config.Configuration{Auth: config.AuthConfig{Provider: types.AuthProviderFlexprice, Secret: testSecret, APIKey: config.APIKeyConfig{Header: "x-api-key"}}}
	r := gin.New()
	r.Use(AuthenticateMiddleware(cfg, nil, nil, &unavailableUserRepository{}, newTestLogger(t)))
	r.GET("/test", func(c *gin.Context) { t.Fatal("handler must not run during authorization outage") })
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT(t, "t_tenant1", "usr_dev", "env_prod", 1))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, 503, w.Code)
	require.Equal(t, "5", w.Header().Get("Retry-After"))
}

func TestDatabaseDomainResponsesAreNotRetryable(t *testing.T) {
	for _, tt := range []struct {
		err    error
		status int
	}{
		{ierr.WithError(ierr.ErrNotFound).Mark(ierr.ErrDatabase), 404},
		{ierr.WithError(ierr.ErrValidation).Mark(ierr.ErrDatabase), 400},
		{ierr.ErrDatabase, 500},
	} {
		r := gin.New()
		r.Use(ErrorHandler())
		r.GET("/test", func(c *gin.Context) { _ = c.Error(tt.err) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/test", nil))
		require.Equal(t, tt.status, w.Code)
		require.Empty(t, w.Header().Get("Retry-After"))
	}
}
