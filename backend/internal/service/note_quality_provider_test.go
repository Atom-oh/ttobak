package service

// These adapters belong only to the opt-in evaluation harness. They preserve
// the production system/user text and store original provider request/response
// bytes as evidence. They do not enable OpenAI in the application transport.
import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
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
	model := "global.openai.gpt-6-sol"
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
		production, _, err := qualityProductionRequest(t, fixture,
			[]byte(`{"content":[{"type":"text","text":"export"}],"stop_reason":"end_turn"}`))
		if err != nil {
			t.Fatal(err)
		}
		adapted, err := qualityProviderRequest("global.openai.gpt-6-sol", production)
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

// Explicit opt-in for the production native transport, without repository
// reads or writes. All input comes from the checked-in synthetic corpus.
func TestQualityLiveSummaryTransport(t *testing.T) {
	if os.Getenv("TTOBAK_NOTE_EVAL_NATIVE_SMOKE") != "1" {
		t.Skip("production transport smoke test is explicit opt-in")
	}
	out := os.Getenv("TTOBAK_NOTE_EVAL_OUT")
	if !filepath.IsAbs(out) {
		t.Fatal("smoke output must be an absolute path")
	}
	region := os.Getenv("BEDROCK_REGION")
	if region == "" {
		region = "us-west-2"
	}
	if os.Getenv("AWS_ENDPOINT_URL") != "" || os.Getenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME") != "" {
		t.Fatal("live smoke does not accept endpoint overrides")
	}
	cfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion(region))
	if err != nil {
		t.Fatal(err)
	}
	client, err := qualityLiveClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fixture := mustQualityCases(t)[0]
	canonical, _, err := qualityProductionRequest(t, fixture,
		[]byte(`{"content":[{"type":"text","text":"export"}],"stop_reason":"end_turn"}`))
	if err != nil {
		t.Fatal(err)
	}
	var request ClaudeRequest
	if err := json.Unmarshal(canonical, &request); err != nil {
		t.Fatal(err)
	}
	svc := NewBedrockService(client, nil, nil)
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	text, err := svc.invokeCompleteSummary(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "[TS:") {
		t.Fatal("native production transport returned no source markers")
	}
	if err := os.Mkdir(out, 0700); err != nil {
		t.Fatal("use a fresh smoke output directory: ", err)
	}
	writeQualityFile(t, out, "note.txt", []byte(text))
	writeQualityFile(t, out, "evidence.json", qualityJSON(t, map[string]any{
		"modelId": svc.summaryModelID, "region": region, "observedAt": time.Now().UTC(),
		"wallSeconds": time.Since(started).Seconds(), "outputSha256": qualityHash([]byte(text)),
		"path":       "BedrockService.invokeCompleteSummary",
		"dataAccess": "synthetic fixture only; no repository",
	}))
}
