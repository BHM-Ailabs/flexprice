package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/config"
	"github.com/flexprice/flexprice/internal/types"
)

type aiRoundTrip func(*http.Request) (*http.Response, error)

func (f aiRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestOpenRouterTransport(t *testing.T) {
	svc := NewOpenRouterService(&config.Configuration{OpenRouter: config.OpenRouterConfig{APIKey: "server-secret", Model: "test-model"}})
	svc.client.Transport = aiRoundTrip(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://openrouter.ai/api/v1/chat/completions" || req.Header.Get("Authorization") != "Bearer server-secret" {
			t.Fatal("incorrect provider credential routing")
		}
		var body map[string]any
		json.NewDecoder(req.Body).Decode(&body)
		if body["model"] != "test-model" || body["response_format"] == nil {
			t.Fatal("missing model/schema")
		}
		data := `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"{\"features\":[],\"plans\":[{\"name\":\"Pro\"}]}"}}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(data))}, nil
	})
	ctx := context.WithValue(context.WithValue(context.Background(), types.CtxTenantID, "tenant1"), types.CtxEnvironmentID, "env1")
	_, err := svc.ParsePricing(ctx, &dto.ParseGeminiPricingRequest{SystemPrompt: "test", UserPrompt: "test", ResponseSchema: json.RawMessage(`{"type":"object"}`)})
	if err != nil {
		t.Fatal(err)
	}
}
func TestOpenRouterRejectsPartialAndProviderErrors(t *testing.T) {
	for _, data := range []string{`{"choices":[]}`, `{"choices":[{"finish_reason":"length","message":{"content":"partial"}}]}`, `{"choices":[{"finish_reason":"stop","message":{"content":""}}]}`} {
		svc := NewOpenRouterService(&config.Configuration{OpenRouter: config.OpenRouterConfig{APIKey: "secret"}})
		svc.client.Transport = aiRoundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(data))}, nil
		})
		if _, err := svc.complete(context.Background(), nil, nil, nil); err == nil {
			t.Fatal("accepted incomplete response")
		}
	}
}
