package service

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/flexprice/flexprice/internal/config"
	"github.com/flexprice/flexprice/internal/types"
)

func fileTestContext(user, env string) context.Context {
	return context.WithValue(context.WithValue(context.WithValue(context.Background(), types.CtxTenantID, "tenant"), types.CtxEnvironmentID, env), types.CtxUserID, user)
}
func TestAIAttachmentIdentityAndExpiry(t *testing.T) {
	s := NewOpenRouterService(&config.Configuration{})
	ctx := fileTestContext("alice", "prod")
	f, err := s.UploadAttachment(ctx, "prices.csv", strings.NewReader("Plan,Price\nPro,29"))
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range []context.Context{fileTestContext("bob", "prod"), fileTestContext("alice", "test"), context.WithValue(ctx, types.CtxTenantID, "other")} {
		if _, err = s.AttachmentStatus(other, f.ID); err == nil {
			t.Fatal("cross-scope file read")
		}
		if err = s.DeleteAttachment(other, f.ID); err == nil {
			t.Fatal("cross-scope deletion")
		}
	}
	keyctx := WithAIAttachmentPrincipal(ctx, "key1")
	if _, err = s.AttachmentStatus(keyctx, f.ID); err == nil {
		t.Fatal("key can read another principal's file")
	}
	if _, _, err = s.attachmentMessage(ctx, []string{f.ID, f.ID}, "test", true); err == nil {
		t.Fatal("duplicate IDs accepted")
	}
	s.files[f.ID].info.ExpiresAt = time.Now().Add(-time.Second)
	if _, err = s.AttachmentStatus(ctx, f.ID); err == nil {
		t.Fatal("expired file accepted")
	}
}
func TestAIAttachmentFormats(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
	}{{"bad.pdf", []byte("not pdf")}, {"bad.xlsx", []byte("not zip")}, {"bad.png", []byte("not png")}, {"run.html", []byte("<script/>")}, {"bad.txt", []byte{0xff, 0}}, {"empty.txt", nil}} {
		s := NewOpenRouterService(&config.Configuration{})
		if _, err := s.UploadAttachment(fileTestContext("u", "e"), test.name, bytes.NewReader(test.data)); err == nil {
			t.Fatal("accepted", test.name)
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 8)))
	kind, mime, err := validateAIFile(context.Background(), "chart.png", buf.Bytes())
	if err != nil || kind != "image" || mime != "image/png" {
		t.Fatal(kind, mime, err)
	}
}
func TestAIAttachmentCompleteSearchAndMultimodal(t *testing.T) {
	s := NewOpenRouterService(&config.Configuration{})
	ctx := fileTestContext("u", "e")
	f, err := s.UploadAttachment(ctx, "large.txt", strings.NewReader(strings.Repeat("Earlier material. ", 9000)+"Late price: 7429 NGN"))
	if err != nil {
		t.Fatal(err)
	}
	raw, source, err := s.readAttachmentTool(ctx, testAICall("search_attachment", `{"attachment_id":"`+f.ID+`","query":"7429"}`), []string{f.ID})
	if err != nil || !strings.Contains(string(raw), "7429") || source.AttachmentID != f.ID {
		t.Fatal("late evidence missing", err)
	}
	if _, _, err = s.readAttachmentTool(ctx, testAICall("read_attachment", `{"attachment_id":"`+f.ID+`","section":1}`), nil); err == nil {
		t.Fatal("read a file not attached to conversation")
	}
	if _, _, err = s.attachmentMessage(ctx, []string{f.ID}, "create", true); err == nil {
		t.Fatal("silently truncated large pricing file")
	}
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 8)))
	img, _ := s.UploadAttachment(ctx, "chart.png", &buf)
	message, _, err := s.attachmentMessage(ctx, []string{img.ID}, "read", false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(message)
	if !strings.Contains(string(raw), "data:image/png;base64,") {
		t.Fatal("missing native image")
	}
	var forged AIMessage
	if json.Unmarshal([]byte(`{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://evil.test"}}]}`), &forged) == nil {
		t.Fatal("accepted forged multimodal input")
	}
}
func TestAIDoclingAsyncAndPartialFailure(t *testing.T) {
	for _, terminal := range []string{"success", "partial_success"} {
		t.Run(terminal, func(t *testing.T) {
			s := NewOpenRouterService(&config.Configuration{OpenRouter: config.OpenRouterConfig{DoclingURL: "https://docs.test", DoclingAPIKey: "converter-key"}})
			s.client.Transport = aiRoundTrip(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host != "docs.test" || req.Header.Get("X-Api-Key") != "converter-key" || req.Header.Get("Authorization") != "" {
					t.Fatal("wrong credential boundary")
				}
				body := ""
				switch req.URL.Path {
				case "/v1/convert/file/async":
					if err := req.ParseMultipartForm(MaxAIFileBytes); err != nil {
						t.Fatal(err)
					}
					if req.FormValue("image_export_mode") != "placeholder" || len(req.MultipartForm.File["files"]) != 1 {
						t.Fatal("invalid upload contract")
					}
					body = `{"task_id":"job-1"}`
				case "/v1/status/poll/job-1":
					body = `{"task_status":"` + terminal + `"}`
				case "/v1/result/job-1":
					body = `{"status":"success","document":{"md_content":"|Plan|Price|\n|Pro|29|"}}`
				default:
					t.Fatal("unexpected path", req.URL.Path)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			ctx := fileTestContext("u", "e")
			f, err := s.UploadAttachment(ctx, "scan.pdf", strings.NewReader("%PDF-1.7 synthetic"))
			if err != nil || f.Status != "processing" {
				t.Fatal(f, err)
			}
			result, err := s.AttachmentStatus(ctx, f.ID)
			if err != nil {
				t.Fatal(err)
			}
			expected := "ready"
			if terminal != "success" {
				expected = "failed"
			}
			if result.Status != expected {
				t.Fatal(result.Status)
			}
		})
	}
}
func TestAIDoclingRedirectDenied(t *testing.T) {
	s := NewOpenRouterService(&config.Configuration{OpenRouter: config.OpenRouterConfig{DoclingURL: "https://docs.test", DoclingAPIKey: "key"}})
	calls := 0
	s.client.Transport = aiRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://evil.test"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	var out any
	if s.docling(context.Background(), "/v1/convert/file/async", []byte("x"), "text/plain", 100, &out) == nil || calls != 1 {
		t.Fatal("redirect was followed")
	}
}

func TestAIVideoDuration(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is required in the runtime image")
	}
	for _, tc := range []struct {
		name  string
		valid bool
	}{{"short.mp4", true}, {"too-long.mp4", false}} {
		data, err := os.ReadFile("testdata/ai-attachments/" + tc.name)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = validateAIFile(context.Background(), tc.name, data)
		if (err == nil) != tc.valid {
			t.Fatalf("duration boundary %s: %v", tc.name, err)
		}
	}
}
func TestAIAttachmentUnicodeAndBoundarySearch(t *testing.T) {
	s := NewOpenRouterService(&config.Configuration{})
	ctx := fileTestContext("u", "e")
	f, err := s.UploadAttachment(ctx, "text.txt", strings.NewReader(strings.Repeat("Ⱥ", 5997)+"PRICE-7429"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := s.readAttachmentTool(ctx, testAICall("search_attachment", `{"attachment_id":"`+f.ID+`","query":"PRICE-7429"}`), []string{f.ID})
	if err != nil || !strings.Contains(string(raw), "PRICE-7429") {
		t.Fatal("boundary evidence lost", err)
	}
}
