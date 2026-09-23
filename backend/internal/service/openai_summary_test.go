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
)

func TestOpenAISummaryUsesIndependentModelAndNativeTransport(t *testing.T) {
	const modelID = "global.openai.gpt-6-sol"
	t.Setenv("BEDROCK_SUMMARY_MODEL_ID", modelID)
	request := ClaudeRequest{AnthropicVersion: "bedrock-2023-05-31", MaxTokens: 16000,
		System:   "원문의 숫자와 부정을 보존하세요.",
		Messages: []ClaudeMessage{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "배포 미승인 [TS:12]"}}}}}
	calls := 0
	client := bedrockruntime.New(bedrockruntime.Options{
		Region: "us-west-2",
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "test-key", SecretAccessKey: "test-secret"}, nil
		}),
		HTTPClient: noteSourceHTTPClient(func(req *http.Request) (*http.Response, error) {
			calls++
			raw, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			response := `{"content":[{"type":"text","text":"auxiliary"}],"stop_reason":"end_turn"}`
			if calls == 1 {
				if !strings.Contains(req.URL.Path, modelID) {
					t.Fatalf("wrong final-note model: %s", req.URL.Path)
				}
				want, _ := buildOpenAISummaryRequest(request)
				if string(raw) != string(want) {
					t.Fatal("final note changed its native request")
				}
				response = `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"배포 미승인 [TS:12]"}}]}`
			} else {
				if !strings.Contains(req.URL.Path, ClaudeOpusModelID) {
					t.Fatalf("summary override affected auxiliary model: %s", req.URL.Path)
				}
				var native ClaudeRequest
				if json.Unmarshal(raw, &native) != nil || native.AnthropicVersion != "bedrock-2023-05-31" {
					t.Fatal("auxiliary caller lost Anthropic transport")
				}
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(response))}, nil
		}),
	})
	svc := NewBedrockService(client, nil, nil)
	got, err := svc.invokeCompleteSummary(context.Background(), request)
	if err != nil || got != "배포 미승인 [TS:12]" {
		t.Fatalf("summary failed: %q %v", got, err)
	}
	if got, err = svc.invokeClaudeModel(context.Background(), request); err != nil || got != "auxiliary" {
		t.Fatalf("auxiliary call failed: %q %v", got, err)
	}
	if calls != 2 {
		t.Fatalf("unexpected calls: %d", calls)
	}
}

func TestOpenAISummaryRejectsIncompleteOrUnexpectedResponses(t *testing.T) {
	for _, body := range []string{
		`null`, `{}`, `{"choices":[]}`, `{"choices":[{},{}]}`,
		`{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":"partial"}}]}`,
		`{"choices":[{"finish_reason":"content_filter","message":{"role":"assistant","content":"partial"}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"  "}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"role":"user","content":"text"}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"text","refusal":"refused"}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"text","tool_calls":[{}]}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":[{"text":"unexpected"}]}}]}`,
	} {
		if _, err := parseOpenAISummaryResponse([]byte(body)); err == nil {
			t.Fatalf("accepted incomplete or unexpected response: %s", body)
		}
	}
	if _, err := buildOpenAISummaryRequest(ClaudeRequest{
		Messages: []ClaudeMessage{{Role: "user", Content: []ContentBlock{{Type: "image"}}}},
	}); err == nil {
		t.Fatal("silently discarded a nontext source")
	}
}
