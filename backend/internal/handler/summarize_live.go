package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

// maxLiveSummaryBodyBytes bounds one update. The recorder currently sends the
// whole running transcript plus the previous summary; several hours of Korean
// speech stay well under 1 MiB.
const maxLiveSummaryBodyBytes = 1 << 20

// legacyClientMeetingID matches the browser-only ID (`meeting_<epoch ms>`) that
// recorders sent before they passed the server draft meeting ID. It has no
// server record to authorize against, so it keeps the previous behavior only
// until the updated frontend is deployed; remove it in the follow-up.
var legacyClientMeetingID = regexp.MustCompile(`^meeting_[0-9]{10,16}$`)

type liveSummaryModel interface {
	InvokeModel(context.Context, *bedrockruntime.InvokeModelInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error)
}

type liveSummaryAccess interface {
	AuthorizeEdit(ctx context.Context, userID, meetingID string) error
}

type SummarizeLiveHandler struct {
	bedrockClient liveSummaryModel
	access        liveSummaryAccess
}

func NewSummarizeLiveHandler(bedrockClient liveSummaryModel, access liveSummaryAccess) *SummarizeLiveHandler {
	return &SummarizeLiveHandler{bedrockClient: bedrockClient, access: access}
}

// SummarizeLive handles POST /api/meetings/{meetingId}/summarize. The caller
// must be able to edit the meeting being recorded; the model is never invoked
// for a meeting the caller cannot edit.
func (h *SummarizeLiveHandler) SummarizeLive(w http.ResponseWriter, r *http.Request) {
	meetingID := chi.URLParam(r, "meetingId")
	if meetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "meetingId is required")
		return
	}
	var err error
	if legacyClientMeetingID.MatchString(meetingID) {
		log.Printf("live summary: legacy client meeting id accepted without meeting authorization")
	} else {
		err = h.access.AuthorizeEdit(r.Context(), middleware.GetUserID(r.Context()), meetingID)
	}
	switch {
	case err == nil:
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
		return
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Edit access is required")
		return
	default:
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Failed to check meeting access")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLiveSummaryBodyBytes)

	var req struct {
		Transcript      string `json:"transcript"`
		PreviousSummary string `json:"previousSummary,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	if req.Transcript == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "transcript is required")
		return
	}

	systemPrompt := `You are an expert meeting assistant. Generate a concise real-time meeting summary in Markdown.
Include: key discussion points, decisions made, and action items.
Format in Korean unless the transcript is entirely in English.
Be concise — this is a live summary updated every ~1000 words.`

	userPrompt := fmt.Sprintf("다음 회의 녹취록을 요약해주세요:\n\n%s", req.Transcript)
	if req.PreviousSummary != "" {
		userPrompt = fmt.Sprintf("이전 요약:\n%s\n\n새로운 녹취록 (이전 요약에 이어서 업데이트해주세요):\n%s", req.PreviousSummary, req.Transcript)
	}

	// Use the same pattern as BedrockService.invokeClaudeModel
	request := service.ClaudeRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        2048,
		System:           systemPrompt,
		Messages: []service.ClaudeMessage{
			{
				Role: "user",
				Content: []service.ContentBlock{
					{Type: "text", Text: userPrompt},
				},
			},
		},
	}

	requestBody, err := json.Marshal(service.PrepareClaudeRequest(request, service.ClaudeHaikuModelID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Failed to create request")
		return
	}

	// Use Haiku for live summary (fast, low-cost incremental updates)
	output, err := h.bedrockClient.InvokeModel(r.Context(), &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(service.ClaudeHaikuModelID),
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
		Body:        requestBody,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Summary generation failed")
		return
	}

	var resp service.ClaudeResponse
	if err := json.Unmarshal(output.Body, &resp); err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Failed to parse response")
		return
	}

	summary := ""
	for _, block := range resp.Content {
		if block.Type == "text" {
			summary += block.Text
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"summary": summary,
	})
}
