package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)

type summaryContinuationHTTP func(*http.Request) (*http.Response, error)

func (transport summaryContinuationHTTP) Do(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func continuationFixture(t *testing.T, response func(int) (string, string), firstBlocks ...[]map[string]string) (*BedrockService, *[]ClaudeRequest) {
	t.Helper()
	var requests []ClaudeRequest
	client := bedrockruntime.New(bedrockruntime.Options{Region: "us-west-2", RetryMaxAttempts: 1,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "test-key", SecretAccessKey: "test-secret"}, nil
		}),
		HTTPClient: summaryContinuationHTTP(func(request *http.Request) (*http.Response, error) {
			encodedRequest, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			var body ClaudeRequest
			if err := json.Unmarshal(encodedRequest, &body); err != nil {
				t.Fatal(err)
			}
			var raw struct {
				Messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(encodedRequest, &raw); err != nil {
				t.Fatal(err)
			}
			for index, message := range raw.Messages {
				if message.Role == "assistant" {
					body.Messages[index].responseContent = message.Content
				}
			}
			if !strings.Contains(request.URL.Path, ClaudeOpusModelID) {
				t.Fatal("continuation changed the selected model")
			}
			requests = append(requests, body)
			text, reason := response(len(requests))
			content := []map[string]string{{"type": "text", "text": text}}
			if len(requests) == 1 && len(firstBlocks) > 0 {
				content = firstBlocks[0]
			}
			encoded, err := json.Marshal(map[string]interface{}{"content": content, "stop_reason": reason})
			if err != nil {
				t.Fatal(err)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(encoded)))}, nil
		}),
	})
	return NewBedrockService(client, nil, nil), &requests
}

func TestFinalSummaryPreservesSignedThinkingWithoutPublishingIt(t *testing.T) {
	for _, blocks := range [][]map[string]string{
		{{"type": "thinking", "thinking": "", "signature": "opaque-signature"}},
		{{"type": "thinking", "thinking": "synthetic internal work", "signature": "opaque-signature"}, {"type": "text", "text": "First "}},
		{{"type": "redacted_thinking", "data": "opaque-redacted-data"}},
	} {
		service, requests := continuationFixture(t, func(attempt int) (string, string) {
			if attempt == 1 {
				return "", "max_tokens"
			}
			return "complete note", "end_turn"
		}, blocks)
		text, err := service.invokeCompleteSummary(context.Background(), ClaudeRequest{MaxTokens: 16000,
			Messages: []ClaudeMessage{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Original source"}}}}})
		if err != nil || len(*requests) != 2 {
			t.Fatalf("signed thinking could not continue: calls=%d err=%v", len(*requests), err)
		}
		if strings.Contains(text, "opaque") || strings.Contains(text, "internal work") || !strings.HasSuffix(text, "complete note") {
			t.Fatal("thinking escaped into the saved meeting note")
		}
		var replayed []map[string]string
		if err := json.Unmarshal((*requests)[1].Messages[1].responseContent, &replayed); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(replayed, blocks) {
			t.Fatal("thinking signature, empty field or response block order changed")
		}
		if len(blocks) == 1 && (*requests)[1].Messages[2].Content[0].Text != summaryAfterThinkingPrompt {
			t.Fatal("thinking-only response was asked to continue nonexistent visible text")
		}
	}
}

func TestFinalSummaryRejectsInvalidContinuationContext(t *testing.T) {
	for _, blocks := range [][]map[string]string{
		{{"type": "thinking", "thinking": "unsigned"}},
		{{"type": "thinking", "signature": "missing-thinking-field"}},
		{{"type": "tool_use", "text": "not a final note"}},
		{{"type": "redacted_thinking", "data": ""}},
		{{"type": "thinking", "thinking": "", "signature": strings.Repeat("x", maxSummaryContinuationBytes)}},
	} {
		service, requests := continuationFixture(t, func(int) (string, string) { return "", "max_tokens" }, blocks)
		text, err := service.invokeCompleteSummary(context.Background(), ClaudeRequest{MaxTokens: 16000})
		if err == nil || text != "" || len(*requests) != 1 {
			t.Fatal("invalid or unbounded model context was forwarded")
		}
	}
}

func TestFinalSummaryContinuesTokenLimitWithoutChangingSourceModelOrBudget(t *testing.T) {
	request := ClaudeRequest{AnthropicVersion: "bedrock-2023-05-31", MaxTokens: 16000, System: "Keep sources and evidence.",
		Messages: []ClaudeMessage{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Complete transcript and human notes."}}}}}
	original, _ := json.Marshal(request)
	service, requests := continuationFixture(t, func(attempt int) (string, string) {
		switch attempt {
		case 1:
			return "# 회의록\n## 주요 논의\n길어진 문", "max_tokens"
		case 2:
			return "장을 이어 작성합니다.\n## 결정 사항\n합", "max_tokens"
		default:
			return "의된 결정입니다.\n## 액션 아이템\n확정된 액션 아이템이 없습니다.", "end_turn"
		}
	})
	result, err := service.invokeCompleteSummary(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	expected := "# 회의록\n## 주요 논의\n길어진 문장을 이어 작성합니다.\n## 결정 사항\n합의된 결정입니다.\n## 액션 아이템\n확정된 액션 아이템이 없습니다."
	if result != expected || len(*requests) != 3 {
		t.Fatalf("incomplete or repeated summary: requests=%d", len(*requests))
	}
	for index, captured := range *requests {
		if captured.System != request.System || captured.MaxTokens != request.MaxTokens ||
			!reflect.DeepEqual(captured.Messages[0], request.Messages[0]) || len(captured.Messages) != 1+2*index ||
			captured.Messages[len(captured.Messages)-1].Role != "user" {
			t.Fatal("continuation changed the source, budget, prompt or conversation order")
		}
	}
	after, _ := json.Marshal(request)
	if string(original) != string(after) {
		t.Fatal("continuation mutated the caller's request")
	}
}

func TestFinalSummaryNeverReturnsIncompleteOrOversizedAccumulatedText(t *testing.T) {
	for _, failure := range []string{"still_truncated", "tool_use", "empty", "oversized"} {
		t.Run(failure, func(t *testing.T) {
			service, requests := continuationFixture(t, func(attempt int) (string, string) {
				if attempt == 1 {
					return "partial", "max_tokens"
				}
				switch failure {
				case "tool_use":
					return "more text", "tool_use"
				case "empty":
					return "", "end_turn"
				case "oversized":
					return strings.Repeat("x", maxSummaryOutputBytes), "end_turn"
				default:
					return "partial", "max_tokens"
				}
			})
			text, err := service.invokeCompleteSummary(context.Background(), ClaudeRequest{MaxTokens: 16000})
			if err == nil || text != "" || len(*requests) > maxSummaryContinuations+1 {
				t.Fatal("unfinished text escaped the completion gate")
			}
		})
	}
}

func TestFinalSummaryStopsContinuationWhenContextExpires(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service, requests := continuationFixture(t, func(int) (string, string) {
		cancel()
		return "partial", "max_tokens"
	})
	text, err := service.invokeCompleteSummary(ctx, ClaudeRequest{MaxTokens: 16000})
	if err == nil || text != "" || len(*requests) != 1 {
		t.Fatalf("expired context continued: calls=%d error=%v", len(*requests), err)
	}
}
