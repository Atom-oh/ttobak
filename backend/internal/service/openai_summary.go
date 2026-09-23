package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

type openAISummaryMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAISummaryRequest struct {
	Messages  []openAISummaryMessage `json:"messages"`
	MaxTokens int                    `json:"max_completion_tokens"`
}

func isOpenAISummaryModel(modelID string) bool {
	return strings.HasPrefix(modelID, "openai.") || strings.Contains(modelID, ".openai.")
}

// The final-note transport changes by provider; source selection, prompts,
// citation processing and conditional publication remain shared.
func buildOpenAISummaryRequest(source ClaudeRequest) ([]byte, error) {
	messages := []openAISummaryMessage{{Role: "developer", Content: source.System}}
	for _, item := range source.Messages {
		if len(item.responseContent) != 0 {
			return nil, fmt.Errorf("OpenAI summary cannot reuse another provider's response blocks")
		}
		text := make([]string, 0, len(item.Content))
		for _, block := range item.Content {
			if block.Type != "text" {
				return nil, fmt.Errorf("OpenAI summary requires text-only input")
			}
			text = append(text, block.Text)
		}
		messages = append(messages, openAISummaryMessage{Role: item.Role, Content: strings.Join(text, "\n")})
	}
	// Preserve the production output ceiling and provider reasoning default.
	// Bedrock supplies the model from InvokeModel.ModelId.
	return json.Marshal(openAISummaryRequest{messages, source.MaxTokens})
}

type openAISummaryResponse struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Role      string            `json:"role"`
			Content   string            `json:"content"`
			Refusal   string            `json:"refusal"`
			ToolCalls []json.RawMessage `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		Input   int `json:"prompt_tokens"`
		Output  int `json:"completion_tokens"`
		Details struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

func parseOpenAISummaryResponse(body []byte) (string, error) {
	text, reason, _, err := decodeOpenAISummaryResponse(body)
	if err != nil {
		return "", err
	}
	if reason != "stop" {
		return "", fmt.Errorf("incomplete summary response: finish_reason=%q", reason)
	}
	return text, nil
}

func decodeOpenAISummaryResponse(body []byte) (string, string, int, error) {
	var response openAISummaryResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", "", 0, fmt.Errorf("failed to unmarshal summary response: %w", err)
	}
	if len(response.Choices) != 1 {
		return "", "", 0, fmt.Errorf("summary requires exactly one completed choice")
	}
	choice := response.Choices[0]
	if (choice.FinishReason != "stop" && choice.FinishReason != "length") || choice.Message.Role != "assistant" ||
		choice.Message.Refusal != "" || len(choice.Message.ToolCalls) != 0 {
		return "", "", 0, fmt.Errorf("incomplete or refused summary response: finish_reason=%q", choice.FinishReason)
	}
	if strings.TrimSpace(choice.Message.Content) == "" &&
		(choice.FinishReason != "length" || response.Usage.Details.ReasoningTokens <= 0) {
		return "", "", 0, fmt.Errorf("empty text response from summary model")
	}
	return choice.Message.Content, choice.FinishReason, response.Usage.Details.ReasoningTokens, nil
}

// Keep main's bounded continuation behavior when selecting the new provider.
// Only completed visible text may escape this method; reasoning is never replayed.
func (s *BedrockService) invokeOpenAISummary(ctx context.Context, source ClaudeRequest, modelID string) (string, error) {
	encoded, err := buildOpenAISummaryRequest(source)
	if err != nil {
		return "", err
	}
	var request openAISummaryRequest
	if err := json.Unmarshal(encoded, &request); err != nil {
		return "", err
	}
	var content strings.Builder
	continuationBytes := 0
	for continuation := 0; continuation <= maxSummaryContinuations; continuation++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		body, err := s.invokeModelBody(ctx, encoded, modelID)
		if err != nil {
			return "", err
		}
		text, reason, _, err := decodeOpenAISummaryResponse(body)
		if err != nil {
			return "", err
		}
		if len(text) > maxSummaryOutputBytes-content.Len() {
			return "", fmt.Errorf("complete summary exceeds output byte limit")
		}
		content.WriteString(text)
		if reason == "stop" {
			return content.String(), nil
		}
		if continuation == maxSummaryContinuations {
			return "", fmt.Errorf("summary continuation limit exceeded")
		}
		if strings.TrimSpace(text) != "" {
			message := openAISummaryMessage{Role: "assistant", Content: text}
			raw, err := json.Marshal(message)
			if err != nil {
				return "", err
			}
			if len(raw) > maxSummaryContinuationBytes-continuationBytes {
				return "", fmt.Errorf("summary continuation context exceeds byte limit")
			}
			continuationBytes += len(raw)
			request.Messages = append(request.Messages, message)
		}
		prompt := summaryContinuationPrompt
		if strings.TrimSpace(content.String()) == "" {
			prompt = summaryAfterThinkingPrompt
		}
		request.Messages = append(request.Messages, openAISummaryMessage{Role: "user", Content: prompt})
		encoded, err = json.Marshal(request)
		if err != nil {
			return "", err
		}
		log.Printf("Continuing OpenAI final summary after output limit: continuation=%d accumulatedBytes=%d", continuation+1, content.Len())
	}
	return "", fmt.Errorf("summary continuation limit exceeded")
}
