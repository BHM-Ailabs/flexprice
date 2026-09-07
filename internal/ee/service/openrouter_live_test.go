package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/config"
	"github.com/flexprice/flexprice/internal/types"
)

// Opt-in, read-only smoke. Credentials are supplied via environment, never fixtures.
func TestOpenRouterLiveSmoke(t *testing.T) {
	if os.Getenv("FLEXPRICE_AI_LIVE_SMOKE") != "1" {
		t.Skip("opt-in live smoke")
	}
	target, err := url.Parse(os.Getenv("FLEXPRICE_API_URL"))
	if err != nil {
		t.Fatal(err)
	}
	target.Path = ""
	reverse := httputil.NewSingleHostReverseProxy(target)
	director := reverse.Director
	reverse.Director = func(r *http.Request) { director(r); r.Host = target.Host }
	proxy := httptest.NewServer(reverse)
	defer proxy.Close()
	cfg := &config.Configuration{Server: config.ServerConfig{Address: strings.TrimPrefix(proxy.URL, "http://")}, OpenRouter: config.OpenRouterConfig{APIKey: os.Getenv("FLEXPRICE_OPENROUTER_API_KEY")}}
	svc := NewOpenRouterService(cfg)
	ctx := context.WithValue(context.WithValue(context.Background(), types.CtxTenantID, "live-smoke"), types.CtxEnvironmentID, os.Getenv("FLEXPRICE_ENVIRONMENT_ID"))
	t.Run("pricing", func(t *testing.T) {
		schema := json.RawMessage(`{"type":"object","properties":{"features":{"type":"array","items":{"type":"object"}},"plans":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"},"amount":{"type":"number"},"currency":{"type":"string"}},"required":["name","amount","currency"]}}},"required":["features","plans"]}`)
		raw, err := svc.ParsePricing(ctx, &dto.ParseGeminiPricingRequest{SystemPrompt: "Return the requested plan as JSON. No features are needed for this flat fee.", UserPrompt: "A Pro plan for 29 USD per month.", ResponseSchema: schema})
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Plans []struct {
				Name     string
				Amount   float64
				Currency string
			}
		}
		json.Unmarshal(raw, &result)
		if len(result.Plans) != 1 || result.Plans[0].Amount != 29 {
			t.Fatal("incorrect pricing result")
		}
		t.Log("OpenRouter returned a valid $29 preview; no records created")
	})
	t.Run("assistant", func(t *testing.T) {
		response, err := svc.Chat(ctx, AssistantRequest{Messages: []AIMessage{{Role: "user", Content: "Find the Intel plans in this account. Give their names and tell me whether any activation restrictions are recorded. Use the live tools."}}}, http.Header{"X-Api-Key": []string{os.Getenv("FLEXPRICE_API_KEY")}})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Sources) == 0 || response.Answer == "" {
			t.Fatalf("missing grounded answer: %s", response.Answer)
		}
		t.Logf("Assistant returned %d source lookups and %d answer bytes", len(response.Sources), len(response.Answer))
	})
}
