package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/ttobak/backend/internal/model"
)

func actionItemsModelResponse(t *testing.T, text, stopReason string) string {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{
		"content":     []map[string]string{{"type": "text", "text": text}},
		"stop_reason": stopReason,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestExtractActionItems_InvalidResponsesAreErrors(t *testing.T) {
	for _, tt := range []struct {
		name, text, stopReason string
	}{
		{"invalid JSON", "not JSON", "end_turn"},
		{"null", "null", "end_turn"},
		{"object", `{"text":"검토"}`, "end_turn"},
		{"missing text", `[{"priority":"high"}]`, "end_turn"},
		{"blank text", `[{"text":" \t\n"}]`, "end_turn"},
		{"invalid priority", `[{"text":"검토","priority":"urgent"}]`, "end_turn"},
		{"invalid date", `[{"text":"검토","dueDate":"2026-02-30"}]`, "end_turn"},
		{"mixed valid and invalid", `[{"text":"준비"},{"text":""}]`, "end_turn"},
		{"token limit even with valid JSON", "[]", "max_tokens"},
		{"nonfinal tool request", "[]", "tool_use"},
		{"missing completion reason", "[]", ""},
		{"blank model text", " \n\t", "end_turn"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			meeting := &model.Meeting{MeetingID: "m1", UserID: "owner", Content: "제안서를 준비합니다."}
			_, got, writes, err := invokeNoteSourceFixture(t, meeting,
				actionItemsModelResponse(t, tt.text, tt.stopReason),
				func(s *BedrockService) (string, error) {
					return s.ExtractActionItems(context.Background(), meeting.MeetingID, meeting.UserID)
				})
			if !errors.Is(err, ErrInvalidAnalysisResponse) || got != "" {
				t.Fatalf("invalid action extraction reported success: output=%q err=%v", got, err)
			}
			if writes != 0 {
				t.Fatalf("extraction persisted results before validation: writes=%d", writes)
			}
		})
	}
}

func TestExtractActionItems_NoSourceIsNotEmptySuccess(t *testing.T) {
	meeting := &model.Meeting{MeetingID: "m1", UserID: "owner"}
	request, got, _, err := invokeNoteSourceFixture(t, meeting,
		actionItemsModelResponse(t, "[]", "end_turn"),
		func(s *BedrockService) (string, error) {
			return s.ExtractActionItems(context.Background(), meeting.MeetingID, meeting.UserID)
		})
	if !errors.Is(err, ErrNoAnalysisSource) || got != "" {
		t.Fatalf("missing source reported success: output=%q err=%v", got, err)
	}
	if len(request.Messages) != 0 {
		t.Fatal("missing source invoked the model")
	}
}

func TestParseActionItems_ValidArrays(t *testing.T) {
	for _, tt := range []struct {
		name, raw string
		want      []ActionItem
	}{
		{"empty is successful", "[]", []ActionItem{}},
		{"fenced empty is successful", "```json\n[]\n```", []ActionItem{}},
		{
			"validated fields and generated completion",
			`[{"id":"untrusted","text":"제안서 준비","completed":true,"assignee":"김담당","priority":"high","dueDate":"2028-02-29"},{"text":"검토","priority":"medium"},{"text":"공유","priority":"low"},{"text":"후속 확인"}]`,
			[]ActionItem{
				{ID: "ai_1", Text: "제안서 준비", Assignee: "김담당", Priority: "high", DueDate: "2028-02-29"},
				{ID: "ai_2", Text: "검토", Priority: "medium"},
				{ID: "ai_3", Text: "공유", Priority: "low"},
				{ID: "ai_4", Text: "후속 확인"},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseActionItems(tt.raw)
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseActionItems = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func TestParseActionItems_RejectsEntireInvalidResult(t *testing.T) {
	for _, raw := range []string{
		"", " ", "null", "{}", `"[]"`, "true", "[", "[] trailing", "[] []",
		"[null]", "[[]]", `["text"]`, "[{}]", `[{"text":null}]`,
		`[{"text":4}]`, `[{"text":" \t\n"}]`,
		`[{"text":"valid"},{"text":""}]`,
		`[{"text":"valid","priority":"urgent"}]`,
		`[{"text":"valid","priority":"HIGH"}]`,
		`[{"text":"valid","priority":""}]`,
		`[{"text":"valid","priority":null}]`,
		`[{"text":"valid","priority":1}]`,
		`[{"text":"valid","dueDate":""}]`,
		`[{"text":"valid","dueDate":null}]`,
		`[{"text":"valid","dueDate":20260912}]`,
		`[{"text":"valid","dueDate":"2026-02-29"}]`,
		`[{"text":"valid","dueDate":"2026-13-01"}]`,
		`[{"text":"valid","dueDate":"2026-9-12"}]`,
		`[{"text":"valid","dueDate":"2026-09-12T00:00:00Z"}]`,
		`[{"text":"valid","dueDate":"tomorrow"}]`,
	} {
		t.Run(raw, func(t *testing.T) {
			got, err := parseActionItems(raw)
			if !errors.Is(err, ErrInvalidAnalysisResponse) || got != nil {
				t.Fatalf("invalid/partial action items returned: %+v, %v", got, err)
			}
		})
	}
}

func TestExtractActionItemsForMeeting_UsesImmutableSuppliedSource(t *testing.T) {
	for _, tt := range []struct {
		name, summary, a, b, selected, want string
	}{
		{"summary preferred", "CURRENT_SUMMARY", "TRANSCRIPT_A", "TRANSCRIPT_B", "B", "CURRENT_SUMMARY"},
		{"selected B", "", "TRANSCRIPT_A", "TRANSCRIPT_B", "B", "TRANSCRIPT_B"},
		{"default A", "", "TRANSCRIPT_A", "TRANSCRIPT_B", "", "TRANSCRIPT_A"},
		{"A unavailable", "", " \n", "TRANSCRIPT_B", "A", "TRANSCRIPT_B"},
		{"B unavailable", "", "TRANSCRIPT_A", " \n", "B", "TRANSCRIPT_A"},
		{"blank summary", " \n", "TRANSCRIPT_A", "TRANSCRIPT_B", "B", "TRANSCRIPT_B"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := &model.Meeting{
				MeetingID: "m1", UserID: "owner", Content: tt.summary,
				TranscriptA: tt.a, TranscriptB: tt.b, SelectedTranscript: tt.selected,
				ActionItems: `[{"id":"user-item","text":"기존","completed":true}]`,
			}
			before := *snapshot
			stored := &model.Meeting{MeetingID: "m1", UserID: "owner", Content: "DATABASE_CONTENT_MUST_NOT_BE_USED"}
			request, got, writes, err := invokeNoteSourceFixture(t, stored,
				actionItemsModelResponse(t, "[]", "end_turn"),
				func(s *BedrockService) (string, error) {
					s.repo = nil // The snapshot API must not reload or persist anything.
					return s.ExtractActionItemsForMeeting(context.Background(), snapshot)
				})
			if err != nil || got != "[]" {
				t.Fatalf("valid no-items response failed: output=%q err=%v", got, err)
			}
			if writes != 0 || !reflect.DeepEqual(*snapshot, before) {
				t.Fatalf("snapshot extraction mutated input or storage: writes=%d snapshot=%+v", writes, snapshot)
			}
			prompt := request.Messages[0].Content[0].Text
			if !strings.HasSuffix(prompt, tt.want) || strings.Contains(prompt, stored.Content) {
				t.Fatalf("wrong extraction source: %q; want suffix %q", prompt, tt.want)
			}
			for _, other := range []string{"CURRENT_SUMMARY", "TRANSCRIPT_A", "TRANSCRIPT_B"} {
				if other != tt.want && strings.Contains(prompt, other) {
					t.Fatalf("unselected source leaked into prompt: %q", prompt)
				}
			}
		})
	}
}

func TestExtractActionItemsForMeeting_NoSource(t *testing.T) {
	for _, meeting := range []*model.Meeting{
		nil, {}, {Content: " \n", TranscriptA: "\t", TranscriptB: " "},
	} {
		// Nil clients prove there is no model or repository call on this branch.
		got, err := (&BedrockService{}).ExtractActionItemsForMeeting(context.Background(), meeting)
		if !errors.Is(err, ErrNoAnalysisSource) || got != "" {
			t.Fatalf("missing source: output=%q err=%v", got, err)
		}
	}
}

func TestExtractActionItemsForMeeting_CompleteResponseRequired(t *testing.T) {
	for _, tt := range incompleteNoteResponses {
		t.Run(tt.name, func(t *testing.T) {
			meeting := &model.Meeting{MeetingID: "m1", UserID: "owner", Content: "검토 필요"}
			_, got, _, err := invokeNoteSourceFixture(t, meeting, tt.body,
				func(s *BedrockService) (string, error) {
					return s.ExtractActionItemsForMeeting(context.Background(), meeting)
				})
			if !errors.Is(err, ErrInvalidAnalysisResponse) || got != "" {
				t.Fatalf("incomplete extraction: output=%q err=%v", got, err)
			}
		})
	}
	for _, reason := range []string{"end_turn", "stop_sequence"} {
		t.Run(reason, func(t *testing.T) {
			meeting := &model.Meeting{MeetingID: "m1", UserID: "owner", Content: "검토 필요"}
			body := `{"content":[{"type":"thinking","thinking":"auxiliary"},{"type":"text","text":"[{\"text\":"},{"type":"text","text":"\"검토\",\"completed\":true}]"}],"stop_reason":"` + reason + `"}`
			_, got, _, err := invokeNoteSourceFixture(t, meeting, body,
				func(s *BedrockService) (string, error) {
					return s.ExtractActionItemsForMeeting(context.Background(), meeting)
				})
			if err != nil || got != `[{"id":"ai_1","text":"검토","completed":false}]` {
				t.Fatalf("complete multi-block extraction: output=%q err=%v", got, err)
			}
		})
	}
}

func TestExtractActionItemsForMeeting_HTTPContract(t *testing.T) {
	transportFailure := errors.New("synthetic transport failure")
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "transport error"}[fail], func(t *testing.T) {
			calls := 0
			client := bedrockruntime.New(bedrockruntime.Options{
				Region: "ap-northeast-2", RetryMaxAttempts: 1,
				Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
					return aws.Credentials{AccessKeyID: "test-key", SecretAccessKey: "test-secret"}, nil
				}),
				HTTPClient: noteSourceHTTPClient(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.URL.Path != "/model/"+ClaudeHaikuModelID+"/invoke" {
						t.Fatalf("action extraction used wrong model: %s", req.URL.Path)
					}
					var request ClaudeRequest
					if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
						t.Fatal(err)
					}
					if request.MaxTokens <= 0 || request.System == "" || len(request.Messages) != 1 ||
						request.Messages[0].Role != "user" || len(request.Messages[0].Content) != 1 ||
						request.Messages[0].Content[0].Type != "text" {
						t.Fatalf("invalid extraction request: %+v", request)
					}
					if fail {
						return nil, transportFailure
					}
					return &http.Response{
						StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
						Body: io.NopCloser(strings.NewReader(actionItemsModelResponse(t, "[]", "end_turn"))),
					}, nil
				}),
			})
			got, err := NewBedrockService(client, nil, nil).ExtractActionItemsForMeeting(
				context.Background(), &model.Meeting{Content: "검토 필요"})
			if calls != 1 {
				t.Fatalf("model invoked %d times", calls)
			}
			if fail {
				if !errors.Is(err, transportFailure) || errors.Is(err, ErrInvalidAnalysisResponse) || got != "" {
					t.Fatalf("transport failure disguised as analysis success/failure: output=%q err=%v", got, err)
				}
			} else if err != nil || got != "[]" {
				t.Fatalf("valid extraction failed: output=%q err=%v", got, err)
			}
		})
	}
}

func TestExtractActionItems_DoesNotTrustGeneratedCompletion(t *testing.T) {
	meeting := &model.Meeting{MeetingID: "m1", UserID: "owner", Content: "검토해야 합니다."}
	_, got, _, err := invokeNoteSourceFixture(t, meeting,
		actionItemsModelResponse(t, `[{"id":"model-chosen","text":"검토","completed":true}]`, "end_turn"),
		func(s *BedrockService) (string, error) {
			return s.ExtractActionItems(context.Background(), meeting.MeetingID, meeting.UserID)
		})
	if err != nil {
		t.Fatal(err)
	}
	var items []ActionItem
	if err := json.Unmarshal([]byte(got), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "ai_1" || items[0].Completed {
		t.Fatalf("model controlled generated identity/completion: %+v", items)
	}
}
