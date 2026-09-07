package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/config"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/types"
)

type AIMessage struct {
	Role       string       `json:"role"`
	Content    string       `json:"content,omitempty"`
	ToolCalls  []AIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}
type AIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
type AITool struct {
	Type     string     `json:"type"`
	Function AIFunction `json:"function"`
}
type AIFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}
type AICompletion struct {
	Choices []struct {
		Message      AIMessage `json:"message"`
		FinishReason string    `json:"finish_reason"`
	} `json:"choices"`
}

// OpenRouterService keeps credentials server-side; the assistant's data reader
// forwards the current session only to this API's loopback listener.
type OpenRouterService struct {
	cfg    *config.Configuration
	client *http.Client
	mu     sync.Mutex
	active map[string]int
	total  int
}

func NewOpenRouterService(cfg *config.Configuration) *OpenRouterService {
	return &OpenRouterService{cfg: cfg, client: &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, active: map[string]int{}}
}
func aiError(message string) error {
	return ierr.NewError(message).WithHint(message).Mark(ierr.ErrServiceUnavailable)
}
func (s *OpenRouterService) acquire(ctx context.Context) (func(), error) {
	key := types.GetTenantID(ctx) + ":" + types.GetEnvironmentID(ctx)
	if types.GetTenantID(ctx) == "" || types.GetEnvironmentID(ctx) == "" {
		return nil, aiError("Select an authenticated environment before using AI.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.total >= 8 || s.active[key] >= 2 {
		return nil, ierr.NewError("AI is busy. Please retry shortly.").WithHint("AI is busy. Please retry shortly.").Mark(ierr.ErrTooManyRequests)
	}
	s.total++
	s.active[key]++
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.total--
		s.active[key]--
		if s.active[key] == 0 {
			delete(s.active, key)
		}
	}, nil
}
func (s *OpenRouterService) complete(ctx context.Context, messages []AIMessage, tools []AITool, format any) (AIMessage, error) {
	if s.cfg == nil || strings.TrimSpace(s.cfg.OpenRouter.APIKey) == "" {
		return AIMessage{}, aiError("OpenRouter is not configured on this server.")
	}
	model := strings.TrimSpace(s.cfg.OpenRouter.Model)
	if model == "" {
		model = "google/gemini-2.5-flash"
	}
	payload := map[string]any{"model": model, "messages": messages, "max_tokens": 8192, "temperature": 0.1, "provider": map[string]any{"require_parameters": true, "data_collection": "deny"}}
	if len(tools) > 0 {
		payload["tools"] = tools
	}
	if format != nil {
		payload["response_format"] = format
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return AIMessage{}, aiError("Could not prepare the AI request.")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return AIMessage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.OpenRouter.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Title", "FlexPrice Dashboard")
	resp, err := s.client.Do(req)
	if err != nil {
		return AIMessage{}, aiError("OpenRouter could not be reached. Please retry.")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return AIMessage{}, aiError(fmt.Sprintf("OpenRouter returned HTTP %d. Check the server API key, credit balance, and model access.", resp.StatusCode))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 {
		return AIMessage{}, aiError("AI response exceeded its size limit.")
	}
	var out AICompletion
	if json.Unmarshal(data, &out) != nil || len(out.Choices) != 1 {
		return AIMessage{}, aiError("OpenRouter returned an invalid response.")
	}
	choice := out.Choices[0]
	if choice.FinishReason != "stop" && choice.FinishReason != "tool_calls" {
		return AIMessage{}, aiError("AI response was incomplete. Try a smaller request.")
	}
	if choice.Message.Content == "" && len(choice.Message.ToolCalls) == 0 {
		return AIMessage{}, aiError("OpenRouter returned an empty response.")
	}
	choice.Message.Role = "assistant"
	return choice.Message, nil
}

// jsonSchema converts the existing Gemini nullable schema to standard JSON Schema.
func jsonSchema(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, x := range v {
			if k != "nullable" {
				out[k] = jsonSchema(x)
			}
		}
		if v["nullable"] == true {
			return map[string]any{"anyOf": []any{out, map[string]any{"type": "null"}}}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = jsonSchema(x)
		}
		return out
	default:
		return value
	}
}
func (s *OpenRouterService) ParsePricing(ctx context.Context, req *dto.ParseGeminiPricingRequest) (json.RawMessage, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	var schema map[string]any
	if json.Unmarshal(req.ResponseSchema, &schema) != nil || schema == nil {
		return nil, aiError("Invalid pricing response schema.")
	}
	message, err := s.complete(ctx, []AIMessage{{Role: "system", Content: req.SystemPrompt}, {Role: "user", Content: req.UserPrompt}}, nil, map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "pricing", "schema": jsonSchema(schema)}})
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(message.Content), &obj) != nil || obj == nil {
		return nil, aiError("AI returned invalid pricing. Please retry.")
	}
	var plans []json.RawMessage
	var features []json.RawMessage
	if json.Unmarshal(obj["plans"], &plans) != nil || len(plans) == 0 || len(plans) > 20 || json.Unmarshal(obj["features"], &features) != nil || len(features) > 200 {
		return nil, aiError("AI returned incomplete or oversized pricing. Describe up to 20 plans and 200 features.")
	}
	return json.RawMessage(message.Content), nil
}
