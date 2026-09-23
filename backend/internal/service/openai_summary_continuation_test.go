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

func openAIContinuationFixture(t *testing.T, responses ...string) (*BedrockService, *[]openAISummaryRequest) {
	t.Helper()
	var requests []openAISummaryRequest
	client := bedrockruntime.New(bedrockruntime.Options{
		Region: "us-west-2", RetryMaxAttempts: 1,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "test-key", SecretAccessKey: "test-secret"}, nil
		}),
		HTTPClient: summaryContinuationHTTP(func(request *http.Request) (*http.Response, error) {
			if !strings.Contains(request.URL.Path, "global.openai.gpt-6-sol") {
				t.Fatal("continuation changed the model")
			}
			var body openAISummaryRequest
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			requests = append(requests, body)
			if len(requests) > len(responses) {
				t.Fatal("unexpected extra invocation")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(responses[len(requests)-1]))}, nil
		}),
	})
	service := NewBedrockService(client, nil, nil)
	service.summaryModelID = "global.openai.gpt-6-sol"
	return service, &requests
}

func openAIResponse(text, reason string, reasoning int) string {
	data, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"finish_reason": reason, "message": map[string]any{"role": "assistant", "content": text}}},
		"usage":   map[string]any{"completion_tokens_details": map[string]any{"reasoning_tokens": reasoning}},
	})
	return string(data)
}

func TestOpenAISummaryContinuesWithSameSourceModelAndLimits(t *testing.T) {
	svc, calls := openAIContinuationFixture(t,
		openAIResponse("# 회의록\n긴 문", "length", 1), openAIResponse("장을 완료.", "stop", 1))
	request := ClaudeRequest{MaxTokens: 16000, System: "Preserve evidence", Messages: []ClaudeMessage{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Selected source"}}},
	}}
	before, _ := json.Marshal(request)
	text, err := svc.invokeCompleteSummary(context.Background(), request)
	if err != nil || text != "# 회의록\n긴 문장을 완료." || len(*calls) != 2 {
		t.Fatalf("incomplete summary: %q, calls=%d, err=%v", text, len(*calls), err)
	}
	first, second := (*calls)[0], (*calls)[1]
	if second.MaxTokens != first.MaxTokens || second.Messages[0] != first.Messages[0] ||
		second.Messages[1] != first.Messages[1] || second.Messages[2].Content != "# 회의록\n긴 문" ||
		second.Messages[3].Content != summaryContinuationPrompt {
		t.Fatal("continuation changed the request contract")
	}
	after, _ := json.Marshal(request)
	if string(before) != string(after) {
		t.Fatal("mutated caller request")
	}
}

func TestOpenAISummaryContinuesReasoningOnlyWithoutInventingVisibleText(t *testing.T) {
	svc, calls := openAIContinuationFixture(t,
		openAIResponse("", "length", 100), openAIResponse("Complete notes", "stop", 1))
	got, err := svc.invokeCompleteSummary(context.Background(), ClaudeRequest{MaxTokens: 16000})
	if err != nil || got != "Complete notes" || len(*calls) != 2 {
		t.Fatalf("reasoning-only continuation failed: %q %v", got, err)
	}
	if len((*calls)[1].Messages) != 2 || (*calls)[1].Messages[1].Role != "user" ||
		(*calls)[1].Messages[1].Content != summaryAfterThinkingPrompt {
		t.Fatal("invented an assistant response or lost the fresh-answer instruction")
	}
}

func TestOpenAISummaryNeverReturnsPartialOrOversizedOutput(t *testing.T) {
	cases := [][]string{
		{openAIResponse("partial", "length", 1), openAIResponse("more", "length", 1), openAIResponse("unfinished", "length", 1)},
		{openAIResponse("partial", "length", 1), openAIResponse("blocked", "content_filter", 0)},
		{openAIResponse("", "length", 0)},
		{openAIResponse(strings.Repeat("x", maxSummaryOutputBytes), "length", 0), openAIResponse("x", "stop", 0)},
		{openAIResponse(strings.Repeat("\x00", maxSummaryOutputBytes), "length", 0)},
	}
	for _, responses := range cases {
		svc, _ := openAIContinuationFixture(t, responses...)
		if text, err := svc.invokeCompleteSummary(context.Background(), ClaudeRequest{MaxTokens: 16000}); err == nil || text != "" {
			t.Fatalf("published incomplete/oversized output: len=%d err=%v", len(text), err)
		}
	}
	svc, calls := openAIContinuationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.invokeCompleteSummary(ctx, ClaudeRequest{}); err == nil || len(*calls) != 0 {
		t.Fatal("cancelled context invoked the model")
	}
}
