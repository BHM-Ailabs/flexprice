package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/flexprice/flexprice/internal/config"
)

var (
	ErrPlaqadUnauthorized = errors.New("plaqad token is unauthorized")
	ErrPlaqadForbidden    = errors.New("plaqad user is not a super admin")
	ErrPlaqadUnavailable  = errors.New("plaqad authorization is unavailable")
)

const maxPlaqadAdminResponseBytes = 1 << 20

type PlaqadAdminIdentity struct {
	ID      string
	Name    string
	Email   string
	IsAdmin bool
}

type plaqadAdminResponse struct {
	User struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Email   string `json:"email"`
		IsAdmin bool   `json:"isAdmin"`
	} `json:"user"`
}

type PlaqadCodeExchangeResult struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expiresAt"`
	User      struct {
		ID    string `json:"sub"`
		Email string `json:"email"`
		Name  string `json:"name"`
	} `json:"user"`
}

type plaqadCodeExchangeRequest struct {
	Code         string `json:"code"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RedirectURI  string `json:"redirect_uri"`
	CodeVerifier string `json:"code_verifier"`
}

// VerifyPlaqadSuperAdmin performs a live authorization check. It deliberately
// does not cache positive results: revoking isAdmin in Plaqad Auth must revoke
// dashboard access on the next Flexprice request without waiting for token or
// cache expiry.
func VerifyPlaqadSuperAdmin(ctx context.Context, cfg config.PlaqadAuthConfig, token string) (*PlaqadAdminIdentity, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" || strings.TrimSpace(cfg.TenantID) == "" || strings.TrimSpace(cfg.UserID) == "" {
		return nil, fmt.Errorf("%w: incomplete Plaqad auth configuration", ErrPlaqadUnavailable)
	}
	if strings.TrimSpace(token) == "" {
		return nil, ErrPlaqadUnauthorized
	}

	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/admin/me", nil)
	if err != nil {
		return nil, fmt.Errorf("%w: construct admin request: %v", ErrPlaqadUnavailable, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPlaqadUnavailable, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, ErrPlaqadUnauthorized
	case http.StatusForbidden:
		return nil, ErrPlaqadForbidden
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%w: admin endpoint returned %d", ErrPlaqadUnavailable, resp.StatusCode)
	}

	var payload plaqadAdminResponse
	reader := io.LimitReader(resp.Body, maxPlaqadAdminResponseBytes+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("%w: read admin response: %v", ErrPlaqadUnavailable, err)
	}
	if len(data) > maxPlaqadAdminResponseBytes {
		return nil, fmt.Errorf("%w: admin response exceeds limit", ErrPlaqadUnavailable)
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("%w: decode admin response: %v", ErrPlaqadUnavailable, err)
	}
	if payload.User.ID == "" || !payload.User.IsAdmin {
		return nil, ErrPlaqadForbidden
	}

	return &PlaqadAdminIdentity{
		ID:      payload.User.ID,
		Name:    payload.User.Name,
		Email:   payload.User.Email,
		IsAdmin: payload.User.IsAdmin,
	}, nil
}

// ExchangePlaqadCode keeps the OAuth client secret on the server. The browser
// supplies only the single-use authorization code and its PKCE verifier; the
// redirect URI is taken from trusted deployment configuration.
func ExchangePlaqadCode(ctx context.Context, cfg config.PlaqadAuthConfig, code, codeVerifier string) (*PlaqadCodeExchangeResult, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if !cfg.Enabled || baseURL == "" || strings.TrimSpace(cfg.ClientID) == "" || strings.TrimSpace(cfg.ClientSecret) == "" || strings.TrimSpace(cfg.RedirectURI) == "" {
		return nil, fmt.Errorf("%w: incomplete Plaqad OAuth configuration", ErrPlaqadUnavailable)
	}
	if strings.TrimSpace(code) == "" || strings.TrimSpace(codeVerifier) == "" {
		return nil, ErrPlaqadUnauthorized
	}

	payload, err := json.Marshal(plaqadCodeExchangeRequest{
		Code:         code,
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURI:  cfg.RedirectURI,
		CodeVerifier: codeVerifier,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: encode code exchange: %v", ErrPlaqadUnavailable, err)
	}

	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/token/exchange", strings.NewReader(string(payload)))
	if err != nil {
		return nil, fmt.Errorf("%w: construct code exchange: %v", ErrPlaqadUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPlaqadUnavailable, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPlaqadAdminResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read code exchange: %v", ErrPlaqadUnavailable, err)
	}
	if len(data) > maxPlaqadAdminResponseBytes {
		return nil, fmt.Errorf("%w: code exchange response exceeds limit", ErrPlaqadUnavailable)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusBadRequest {
		return nil, ErrPlaqadUnauthorized
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%w: token endpoint returned %d", ErrPlaqadUnavailable, resp.StatusCode)
	}

	var result PlaqadCodeExchangeResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("%w: decode code exchange: %v", ErrPlaqadUnavailable, err)
	}
	if result.Token == "" || result.User.ID == "" {
		return nil, fmt.Errorf("%w: incomplete token response", ErrPlaqadUnavailable)
	}
	return &result, nil
}
