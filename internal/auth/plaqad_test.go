package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flexprice/flexprice/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExchangePlaqadCode(t *testing.T) {
	t.Run("exchanges the browser code with server-side client credentials", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/api/v1/token/exchange", r.URL.Path)
			var body plaqadCodeExchangeRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, "authorization-code", body.Code)
			assert.Equal(t, "flexprice-client", body.ClientID)
			assert.Equal(t, "server-only-secret", body.ClientSecret)
			assert.Equal(t, "https://billing.example.com/auth/plaqad/callback", body.RedirectURI)
			assert.Equal(t, "pkce-verifier", body.CodeVerifier)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"plaqad-token","expiresAt":1770000000000,"user":{"sub":"admin-id","email":"admin@example.com","name":"Admin"}}`))
		}))
		defer server.Close()

		result, err := ExchangePlaqadCode(context.Background(), config.PlaqadAuthConfig{
			Enabled:        true,
			BaseURL:        server.URL,
			ClientID:       "flexprice-client",
			ClientSecret:   "server-only-secret",
			RedirectURI:    "https://billing.example.com/auth/plaqad/callback",
			TimeoutSeconds: 2,
		}, "authorization-code", "pkce-verifier")

		require.NoError(t, err)
		assert.Equal(t, "plaqad-token", result.Token)
		assert.Equal(t, "admin-id", result.User.ID)
	})

	t.Run("maps rejected or expired codes to unauthorized", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
		}))
		defer server.Close()

		_, err := ExchangePlaqadCode(context.Background(), config.PlaqadAuthConfig{
			Enabled: true, BaseURL: server.URL, ClientID: "client", ClientSecret: "secret", RedirectURI: "https://example.com/callback",
		}, "expired-code", "verifier")

		assert.ErrorIs(t, err, ErrPlaqadUnauthorized)
	})

	t.Run("fails closed when Plaqad OAuth is not fully configured", func(t *testing.T) {
		_, err := ExchangePlaqadCode(context.Background(), config.PlaqadAuthConfig{Enabled: true}, "code", "verifier")

		assert.True(t, errors.Is(err, ErrPlaqadUnavailable))
	})
}

func TestPlaqadAllowedUserIDs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		allowed []string
		userID  string
		isAdmin bool
		wantErr bool
	}{
		{"omitted preserves super admin access", nil, "operator", true, false},
		{"exact central ID allowed", []string{"operator"}, "operator", true, false},
		{"another super admin rejected", []string{"operator"}, "other-admin", true, true},
		{"ID match is case sensitive", []string{"Operator"}, "operator", true, true},
		{"configured whitespace trimmed", []string{" first ", " operator "}, "operator", true, false},
		{"blank entries do not widen access", []string{"", " "}, "operator", true, true},
		{"allowlist does not grant admin role", []string{"operator"}, "operator", false, true},
		{"missing identity rejected", nil, "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"id": tc.userID, "isAdmin": tc.isAdmin}})
			}))
			defer server.Close()
			result, err := VerifyPlaqadSuperAdmin(context.Background(), config.PlaqadAuthConfig{
				BaseURL: server.URL, TenantID: "native-tenant", UserID: "native-user", AllowedUserIDs: tc.allowed,
			}, "verified-token")
			if tc.wantErr {
				require.ErrorIs(t, err, ErrPlaqadForbidden)
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.userID, result.ID)
			}
		})
	}
}

func TestExchangePlaqadCodeRejectsUnlistedIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"token":"other-admin-token","expiresAt":1770000000000,"user":{"sub":"other-admin"}}`))
	}))
	defer server.Close()
	for _, allowed := range [][]string{nil, {"other-admin"}, {"approved-operator"}, {" "}} {
		result, err := ExchangePlaqadCode(context.Background(), config.PlaqadAuthConfig{
			Enabled: true, BaseURL: server.URL, ClientID: "client", ClientSecret: "secret", RedirectURI: "https://example.com/callback", AllowedUserIDs: allowed,
		}, "valid-code", "verifier")
		if len(allowed) == 0 || allowed[0] == "other-admin" {
			require.NoError(t, err)
			assert.NotNil(t, result)
		} else {
			require.ErrorIs(t, err, ErrPlaqadForbidden)
			assert.Nil(t, result, "an unlisted operator must not receive a session token from the callback")
		}
	}
}
