package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
)

func TestImageCallsSendTerraMultimodalRequests(t *testing.T) {
	t.Setenv("BEDROCK_IMAGE_MODEL_ID", "")
	t.Setenv("BEDROCK_SUMMARY_MODEL_ID", "global.anthropic.claude-sonnet-5-5")
	calls := 0
	client := bedrockruntime.New(bedrockruntime.Options{
		Region: "us-west-2",
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "test-key", SecretAccessKey: "test-secret"}, nil
		}),
		HTTPClient: noteSourceHTTPClient(func(req *http.Request) (*http.Response, error) {
			calls++
			if !strings.Contains(req.URL.Path, "global.openai.gpt-6.1-terra") {
				t.Errorf("wrong image model: %s", req.URL.Path)
			}
			var payload struct {
				Messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
				MaxTokens        int    `json:"max_completion_tokens"`
				Reasoning        string `json:"reasoning_effort"`
				AnthropicVersion string `json:"anthropic_version"`
			}
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			wantTokens := 50
			text := "whiteboard"
			if calls == 2 {
				wantTokens = 4096
				text = "## Extracted notes"
			}
			if payload.MaxTokens != wantTokens || payload.Reasoning != "none" || payload.AnthropicVersion != "" {
				t.Errorf("wrong provider payload: %+v", payload)
			}
			if len(payload.Messages) != 2 {
				t.Fatalf("wrong messages: %+v", payload.Messages)
			}
			if payload.Messages[0].Role != "developer" || payload.Messages[1].Role != "user" {
				t.Error("missing instructions or image user message")
			}
			var parts []struct {
				Type     string `json:"type"`
				ImageURL struct {
					URL string `json:"url"`
				} `json:"image_url"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(payload.Messages[1].Content, &parts); err != nil {
				t.Fatal(err)
			}
			if len(parts) != 2 || parts[0].Type != "image_url" || parts[0].ImageURL.URL != "data:image/png;base64,aW1hZ2U=" || parts[1].Type != "text" || parts[1].Text == "" {
				t.Fatalf("image bytes/instructions lost: %+v", parts)
			}
			body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": text}}}})
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
		}),
	})
	svc := NewBedrockService(client, nil, nil)
	category, err := svc.classifyImage(context.Background(), "aW1hZ2U=", "image/png")
	if err != nil || category != model.AttachTypeWhiteboard {
		t.Fatalf("classification=%q err=%v", category, err)
	}
	analysis, err := svc.analyzeByClassification(context.Background(), "aW1hZ2U=", "image/png", category)
	if err != nil || analysis != "## Extracted notes" {
		t.Fatalf("analysis=%q err=%v", analysis, err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestAnalyzeImageRejectsInvalidSizeBeforeModelInvocation(t *testing.T) {
	for _, tt := range []struct {
		name, key string
		size      int
		wantReads int
	}{
		{"unsupported media", "upload.pdf", 8, 0},
		{"empty image", "upload.png", 0, 1},
		{"oversized image", "upload.png", 16*1024*1024 + 1, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reads := 0
			client := s3.New(s3.Options{
				Region: "ap-northeast-2",
				Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
					return aws.Credentials{AccessKeyID: "test-key", SecretAccessKey: "test-secret"}, nil
				}),
				HTTPClient: noteSourceHTTPClient(func(*http.Request) (*http.Response, error) {
					reads++
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", tt.size)))}, nil
				}),
			})
			// A nil model client would panic if a rejected upload reached it.
			svc := NewBedrockService(nil, client, nil)
			category, text, err := svc.AnalyzeImage(context.Background(), "test-bucket", tt.key)
			if err == nil || category != "" || text != "" || reads != tt.wantReads {
				t.Fatalf("category=%q text=%q err=%v reads=%d", category, text, err, reads)
			}
		})
	}
}
