package auth

import (
	"context"
	"fmt"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/nedpals/supabase-go"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flexprice/flexprice/internal/config"
	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestSupabaseAuth() *supabaseAuth {
	return &supabaseAuth{
		AuthConfig: config.AuthConfig{
			Secret: "test-secret-key-32-bytes-minimum!",
		},
	}
}

func TestSupabaseDevTokenEnvironmentID(t *testing.T) {
	a := newTestSupabaseAuth()

	t.Run("extracts top-level environment_id", func(t *testing.T) {
		token, _, err := a.GenerateDevToken("tenant_123", "env_123", "user_123", "dev@example.com", 1)
		require.NoError(t, err)

		claims, err := a.ValidateToken(context.Background(), token)
		require.NoError(t, err)
		assert.Equal(t, "env_123", claims.EnvironmentID)
	})

	t.Run("allows an omitted environment_id", func(t *testing.T) {
		token, _, err := a.GenerateDevToken("tenant_123", "", "user_123", "dev@example.com", 1)
		require.NoError(t, err)

		claims, err := a.ValidateToken(context.Background(), token)
		require.NoError(t, err)
		assert.Empty(t, claims.EnvironmentID)
	})

	t.Run("ignores a non-string environment_id", func(t *testing.T) {
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"sub":            "user_123",
			"email":          "dev@example.com",
			"environment_id": 123,
			"app_metadata": map[string]interface{}{
				"tenant_id": "tenant_123",
			},
		})
		signed, err := token.SignedString([]byte(a.AuthConfig.Secret))
		require.NoError(t, err)

		claims, err := a.ValidateToken(context.Background(), signed)
		require.NoError(t, err)
		assert.Empty(t, claims.EnvironmentID)
	})
}

func TestSupabaseLoginAvailability(t *testing.T) {
	for _, status := range []int{400, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error_code":"invalid_credentials","msg":"synthetic denial"}`))
			}))
			defer server.Close()
			client := supabase.CreateClient(server.URL, "synthetic")
			client.HTTPClient = &http.Client{Transport: &authDependencyTransport{base: http.DefaultTransport}, Timeout: time.Second}
			provider := &supabaseAuth{client: client}
			_, err := provider.Login(context.Background(), AuthRequest{Email: "synthetic@example.com", Password: "synthetic"}, nil)
			require.Error(t, err)
			require.Equal(t, status >= 429, ierr.IsServiceUnavailable(err))
			require.Equal(t, status == 400, ierr.IsPermissionDenied(err))
		})
	}
}
