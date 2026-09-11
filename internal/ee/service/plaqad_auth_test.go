package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/config"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/stretchr/testify/require"
)

func TestPlaqadCallbackAllowlistDenialIsForbidden(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"token":"other-admin-token","expiresAt":1770000000000,"user":{"sub":"other-admin"}}`))
	}))
	defer server.Close()
	svc := &authService{ServiceParams: ServiceParams{Config: &config.Configuration{Auth: config.AuthConfig{Plaqad: config.PlaqadAuthConfig{
		Enabled: true, BaseURL: server.URL, ClientID: "client", ClientSecret: "secret", RedirectURI: "https://example.com/callback", AllowedUserIDs: []string{"approved-operator"},
	}}}}}
	result, err := svc.ExchangePlaqadCode(context.Background(), &dto.PlaqadCallbackRequest{Code: "valid-code", CodeVerifier: "verifier"})
	require.Error(t, err)
	require.True(t, ierr.IsPermissionDenied(err))
	require.Nil(t, result)
}
