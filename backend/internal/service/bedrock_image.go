package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	defaultImageModelID   = "global.openai.gpt-6.1-terra"
	maxImageBytes         = 16 * 1024 * 1024
	maxImageResponseBytes = 128 * 1024
)

// Image extraction has its own model and budget, independent of final notes.
// Bedrock's native OpenAI request uses a data URL for the existing image bytes.
func (s *BedrockService) invokeImageModel(ctx context.Context, systemPrompt, userPrompt, imageBase64, mediaType string, maxTokens int) (string, error) {
	modelID := s.imageModelID
	if modelID == "" {
		modelID = defaultImageModelID
	}
	if modelID != defaultImageModelID {
		return "", fmt.Errorf("unsupported image model %q", modelID)
	}
	request := struct {
		Messages  []any  `json:"messages"`
		MaxTokens int    `json:"max_completion_tokens"`
		Reasoning string `json:"reasoning_effort"`
	}{
		Messages: []any{
			map[string]any{"role": "developer", "content": systemPrompt},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:" + mediaType + ";base64," + imageBase64}},
				map[string]string{"type": "text", "text": userPrompt},
			}},
		},
		MaxTokens: maxTokens, Reasoning: "none",
	}
	body, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("marshal image request: %w", err)
	}
	response, err := s.invokeModelBody(ctx, body, modelID)
	if err != nil {
		return "", err
	}
	if len(response) > maxImageResponseBytes {
		return "", fmt.Errorf("image response exceeds byte limit")
	}
	text, _, _, err := decodeOpenAISummaryResponse(response)
	if err != nil {
		return "", fmt.Errorf("invalid image response: %w", err)
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("empty image analysis")
	}
	// Retain usable partial auxiliary analysis; final-note publication has its
	// separate strict completion and continuation contract.
	return text, nil
}
