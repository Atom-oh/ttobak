package service

// These adapters replay historical single-call evidence only. Current model
// evaluation records the actual production provider transport and continuations.
import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func qualityProviderRequest(modelID string, production []byte) ([]byte, error) {
	if !strings.Contains(modelID, ".openai.") {
		return production, nil
	}
	var source ClaudeRequest
	if err := json.Unmarshal(production, &source); err != nil {
		return nil, err
	}
	return buildOpenAISummaryRequest(source)
}

func qualityProviderCompletion(modelID string, original []byte) ([]byte, error) {
	if !strings.Contains(modelID, ".openai.") {
		return original, nil
	}
	text, err := parseOpenAISummaryResponse(original)
	if err != nil {
		return nil, err
	}
	var response openAISummaryResponse
	if err := json.Unmarshal(original, &response); err != nil {
		return nil, err
	}
	// Feed only the completed visible text through the existing production
	// anchor/format postprocessing. The original response remains untouched.
	return json.Marshal(map[string]any{
		"content":     []map[string]string{{"type": "text", "text": text}},
		"stop_reason": "end_turn",
		"usage": map[string]int{
			"input_tokens": response.Usage.Input, "output_tokens": response.Usage.Output,
		},
	})
}

func TestQualityOpenAICompletionRequiresCompleteVisibleText(t *testing.T) {
	model := "global.openai.gpt-6.1-sol"
	for _, response := range []string{
		`{}`, `{"choices":[]}`,
		`{"choices":[{"finish_reason":"length","message":{"content":"partial"}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"content":""}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"content":"text","refusal":"no"}}]}`,
		`{"choices":[{"finish_reason":"content_filter","message":{"content":"partial"}}]}`,
	} {
		if _, err := qualityProviderCompletion(model, []byte(response)); err == nil {
			t.Fatalf("accepted incomplete response: %s", response)
		}
	}
	complete := []byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"요약 [TS:000]"}}],"usage":{"prompt_tokens":12,"completion_tokens":23}}`)
	result, err := qualityProviderCompletion(model, complete)
	if err != nil {
		t.Fatal(err)
	}
	text, err := parseClaudeTextResponse(result)
	if err != nil || text != "요약 [TS:000]" {
		t.Fatalf("visible content changed: %q, %v", text, err)
	}
	var usage ClaudeResponse
	if err := json.Unmarshal(result, &usage); err != nil || usage.Usage.InputTokens != 12 || usage.Usage.OutputTokens != 23 {
		t.Fatal("provider usage changed")
	}
}

func TestQualityOpenAIRequestPreservesProductionPrompt(t *testing.T) {
	for _, fixture := range mustQualityCases(t) {
		production, _, err := qualityLegacyProductionRequest(t, fixture,
			[]byte(`{"content":[{"type":"text","text":"export"}],"stop_reason":"end_turn"}`))
		if err != nil {
			t.Fatal(err)
		}
		adapted, err := qualityProviderRequest("global.openai.gpt-6.1-sol", production)
		if err != nil {
			t.Fatal(err)
		}
		var source ClaudeRequest
		var target struct {
			Messages []struct {
				Role, Content string
			}
			MaxTokens int `json:"max_completion_tokens"`
		}
		if json.Unmarshal(production, &source) != nil || json.Unmarshal(adapted, &target) != nil {
			t.Fatal("invalid request")
		}
		if target.MaxTokens != source.MaxTokens || len(target.Messages) != len(source.Messages)+1 ||
			target.Messages[0].Role != "developer" || target.Messages[0].Content != source.System {
			t.Fatal("system text or token ceiling changed")
		}
		for i, message := range source.Messages {
			if len(message.Content) != 1 || target.Messages[i+1].Role != message.Role ||
				target.Messages[i+1].Content != message.Content[0].Text {
				t.Fatal("user content changed")
			}
		}
	}
}

func mustQualityCases(t *testing.T) []qualityCase {
	t.Helper()
	cases, _ := loadQualityCases(t)
	return cases
}

func TestQualityStressReferences(t *testing.T) {
	data, err := os.ReadFile("testdata/note-quality/stress-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []qualityCase
	if err := json.Unmarshal(data, &cases); err != nil || len(cases) != 3 {
		t.Fatal("invalid stress corpus")
	}
	for _, fixture := range cases {
		t.Run(fixture.ID, func(t *testing.T) {
			for _, finding := range gradeNoteQuality(fixture, fixture.Reference) {
				if !finding.Passed {
					t.Errorf("%s: %s", finding.ID, finding.Detail)
				}
			}
		})
	}
}

// The former single-fixture smoke probe did not retain every native call.
// Reject the old opt-in explicitly instead of publishing incomplete evidence.
func TestQualityLiveSummaryTransport(t *testing.T) {
	if os.Getenv("TTOBAK_NOTE_EVAL_NATIVE_SMOKE") != "1" {
		t.Skip("legacy smoke entry point is disabled")
	}
	t.Fatal("use scripts/eval-note-quality.sh live with a deployment snapshot; it retains every native request/response")
}
