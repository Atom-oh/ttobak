package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ttobak/backend/internal/model"
)

var (
	// ErrInvalidAnalysisResponse means the model did not return a complete,
	// valid analysis. It must not be treated as a successful empty result.
	ErrInvalidAnalysisResponse = errors.New("invalid analysis response")
	// ErrNoAnalysisSource means no summary or transcript is available to analyze.
	ErrNoAnalysisSource = errors.New("no analysis source")
)

// parseActionItems validates the entire generated array without dropping bad
// entries. Optional fields may be omitted; when present they must be valid.
// IDs and completion are application-owned, never taken from the model.
func parseActionItems(raw string) ([]ActionItem, error) {
	var generated []struct {
		Text     string          `json:"text"`
		Assignee string          `json:"assignee"`
		Priority json.RawMessage `json:"priority"`
		DueDate  json.RawMessage `json:"dueDate"`
	}
	if err := json.Unmarshal([]byte(stripCodeFences(raw)), &generated); err != nil {
		return nil, fmt.Errorf("%w: expected an action item array", ErrInvalidAnalysisResponse)
	}
	if generated == nil {
		return nil, fmt.Errorf("%w: expected an action item array, not null", ErrInvalidAnalysisResponse)
	}

	items := make([]ActionItem, 0, len(generated))
	for i, item := range generated {
		if strings.TrimSpace(item.Text) == "" {
			return nil, fmt.Errorf("%w: action item %d requires text", ErrInvalidAnalysisResponse, i+1)
		}
		var priority, dueDate string
		if len(item.Priority) > 0 {
			if err := json.Unmarshal(item.Priority, &priority); err != nil {
				return nil, fmt.Errorf("%w: action item %d has invalid priority", ErrInvalidAnalysisResponse, i+1)
			}
			switch priority {
			case "high", "medium", "low":
			default:
				return nil, fmt.Errorf("%w: action item %d has invalid priority", ErrInvalidAnalysisResponse, i+1)
			}
		}
		if len(item.DueDate) > 0 {
			if err := json.Unmarshal(item.DueDate, &dueDate); err != nil {
				return nil, fmt.Errorf("%w: action item %d has invalid dueDate", ErrInvalidAnalysisResponse, i+1)
			}
			if _, err := time.Parse(time.DateOnly, dueDate); err != nil {
				return nil, fmt.Errorf("%w: action item %d requires a YYYY-MM-DD dueDate", ErrInvalidAnalysisResponse, i+1)
			}
		}
		items = append(items, ActionItem{
			ID: fmt.Sprintf("ai_%d", i+1), Text: item.Text, Completed: false,
			Assignee: item.Assignee, Priority: priority, DueDate: dueDate,
		})
	}
	return items, nil
}

// ExtractActionItemsForMeeting analyzes the caller's immutable snapshot without
// reloading or mutating it. Persistence and prior completion reconciliation are
// the caller's responsibility.
func (s *BedrockService) ExtractActionItemsForMeeting(ctx context.Context, meeting *model.Meeting) (string, error) {
	if meeting == nil {
		return "", ErrNoAnalysisSource
	}
	// Prefer the current summary; preserve the selected-transcript fallback.
	source := meeting.Content
	if strings.TrimSpace(source) == "" {
		source, _ = selectMeetingTranscript(meeting)
	}
	if strings.TrimSpace(source) == "" {
		return "", ErrNoAnalysisSource
	}

	systemPrompt := `회의 요약 또는 트랜스크립트에서 액션 아이템(해야 할 일, 후속 조치)을 추출하세요.
각 액션 아이템에 대해 아래를 식별하세요:
- text: 할 일 설명 (한국어로 작성, 필수)
- assignee: 담당자 (이름 또는 화자 라벨)
- priority: high, medium, low (중요도/긴급도 기준)
- dueDate: 명시적으로 언급된 경우만 (ISO 형식 YYYY-MM-DD)

"~하기로 했다", "~할 예정", "~를 준비", "팔로업", "확인 필요" 등의 표현에서 액션을 추출하세요.
유효한 JSON 배열만 반환하세요. 액션 아이템이 없으면 []를 반환하세요.
예시:
[{"text":"PoC 환경 구축 제안서 준비","assignee":"spk_1","priority":"high","completed":false}]`

	request := ClaudeRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        1024,
		System:           systemPrompt,
		Messages: []ClaudeMessage{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "text", Text: fmt.Sprintf("다음 회의 내용에서 액션 아이템을 추출하세요:\n\n%s", source)},
				},
			},
		},
	}
	response, err := s.invokeCompleteAnalysisWithID(ctx, request, ClaudeHaikuModelID)
	if err != nil {
		return "", fmt.Errorf("failed to extract action items: %w", err)
	}
	items, err := parseActionItems(response)
	if err != nil {
		return "", err
	}
	result, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("failed to marshal action items: %w", err)
	}
	return string(result), nil
}

// invokeCompleteAnalysisWithID shares the final-note text gate without changing
// permissive image/refinement callers. Transport failures retain their cause;
// invalid model responses carry the analysis sentinel.
func (s *BedrockService) invokeCompleteAnalysisWithID(ctx context.Context, request ClaudeRequest, modelID string) (string, error) {
	body, err := s.invokeClaudeResponseBody(ctx, request, modelID)
	if err != nil {
		return "", err
	}
	text, err := parseClaudeTextResponse(body)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidAnalysisResponse, err)
	}
	return text, nil
}
