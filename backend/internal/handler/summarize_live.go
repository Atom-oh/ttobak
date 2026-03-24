package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type SummarizeLiveHandler struct {
	bedrockClient *bedrockruntime.Client
}

func NewSummarizeLiveHandler(bedrockClient *bedrockruntime.Client) *SummarizeLiveHandler {
	return &SummarizeLiveHandler{bedrockClient: bedrockClient}
}

// SummarizeLive handles POST /api/meetings/{meetingId}/summarize
func (h *SummarizeLiveHandler) SummarizeLive(w http.ResponseWriter, r *http.Request) {
	_ = chi.URLParam(r, "meetingId")

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

	requestBody, err := json.Marshal(request)
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
