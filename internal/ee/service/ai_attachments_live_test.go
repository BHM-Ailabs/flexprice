package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/config"
)

// Synthetic files only. No billing mutations; provider calls require explicit opt-in.
func TestAIAttachmentsLive(t *testing.T) {
	if os.Getenv("FLEXPRICE_AI_LIVE_SMOKE") != "1" {
		t.Skip("opt-in live conversion")
	}
	s := NewOpenRouterService(&config.Configuration{OpenRouter: config.OpenRouterConfig{APIKey: os.Getenv("FLEXPRICE_OPENROUTER_API_KEY"), DoclingURL: os.Getenv("FLEXPRICE_OPENROUTER_DOCLING_URL"), DoclingAPIKey: os.Getenv("FLEXPRICE_OPENROUTER_DOCLING_API_KEY")}})
	ctx := fileTestContext("synthetic-smoke", "synthetic-env")
	for _, name := range strings.Split(os.Getenv("FLEXPRICE_AI_FIXTURES"), ",") {
		if name == "" {
			continue
		}
		t.Run(filepath.Base(name), func(t *testing.T) {
			file, err := os.Open(name)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			info, err := s.UploadAttachment(ctx, filepath.Base(name), file)
			if err != nil {
				t.Fatal(err)
			}
			defer s.DeleteAttachment(ctx, info.ID)
			deadline := time.Now().Add(4 * time.Minute)
			for info.Status == "processing" && time.Now().Before(deadline) {
				time.Sleep(3 * time.Second)
				info, err = s.AttachmentStatus(ctx, info.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			if info.Status != "ready" {
				t.Fatalf("conversion not ready: %+v", info)
			}
			t.Logf("Converted %s: %s, %d sections", info.Name, info.Kind, info.Sections)
			if info.Kind == "image" || info.Kind == "video" {
				answer, err := s.Chat(ctx, AssistantRequest{Messages: []AIMessage{{Role: "user", Content: "Describe the visible text or color in this file. Be specific."}}, AttachmentIDs: []string{info.ID}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				expected := "PLAQAD-7429"
				if info.Kind == "video" {
					expected = "blue"
				}
				if !strings.Contains(strings.ToLower(answer.Answer), strings.ToLower(expected)) {
					t.Fatalf("media evidence missing: %s", answer.Answer)
				}
				t.Log("Media answer:", answer.Answer)
			} else {
				f, _ := s.findAttachment(ctx, info.ID)
				if !strings.Contains(f.text, "Notebook") && !strings.Contains(f.text, "PLAQAD-7429") {
					t.Fatal("expected fixture evidence missing")
				}
			}
		})
	}
	if schemaPath := os.Getenv("FLEXPRICE_AI_PRICING_FIXTURE"); schemaPath != "" {
		file, err := os.Open(os.Getenv("FLEXPRICE_AI_PRICING_FILE"))
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		info, err := s.UploadAttachment(ctx, "prices.csv", file)
		if err != nil {
			t.Fatal(err)
		}
		defer s.DeleteAttachment(ctx, info.ID)
		raw, err := os.ReadFile(schemaPath)
		if err != nil {
			t.Fatal(err)
		}
		var req dto.ParseGeminiPricingRequest
		if err = json.Unmarshal(raw, &req); err != nil {
			t.Fatal(err)
		}
		req.UserPrompt = "Create the plan exactly as listed in the attached CSV. It has a flat monthly price, no features or usage charges."
		result, err := s.ParsePricing(ctx, &req, info.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(os.Getenv("FLEXPRICE_AI_PRICING_RESULT"), result, 0600); err != nil {
			t.Fatal(err)
		}
		t.Log("Attachment-driven pricing preview returned; no records created")
	}
}
