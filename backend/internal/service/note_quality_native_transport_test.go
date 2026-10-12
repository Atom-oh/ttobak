package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func nativeTestBinding(t *testing.T, fixture qualityCase) qualityBinding {
	t.Helper()
	source, _ := json.Marshal(qualityMeeting(fixture))
	return qualityBinding{ModelID: qualityEffectiveModel(), Region: "us-west-2", SourceSHA256: qualityHash(source),
		CorpusSHA256: qualityHash([]byte("synthetic corpus binding")), ProductionSHA256: qualityHash([]byte("synthetic source binding")),
		EvaluatorSHA256: qualityHash([]byte("synthetic evaluator binding")), DeploymentSHA256: qualityHash([]byte("synthetic deployment binding")),
		DeployedCodeSHA256: base64.StdEncoding.EncodeToString(make([]byte, 32))}
}

func nativeTestResponse(model, text, reason string) []byte {
	var body any
	if isOpenAISummaryModel(model) {
		body = map[string]any{"choices": []map[string]any{{"finish_reason": reason, "message": map[string]string{"role": "assistant", "content": text}}},
			"usage": map[string]int{"prompt_tokens": 12, "completion_tokens": 23}}
	} else {
		body = map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "stop_reason": reason,
			"usage": map[string]int{"input_tokens": 12, "output_tokens": 23}}
	}
	encoded, _ := json.Marshal(body)
	return encoded
}

func nativeTestRun(t *testing.T, model, scenario string) (qualityCase, *qualityNativeEvidence, []byte, error) {
	t.Helper()
	t.Setenv("BEDROCK_SUMMARY_MODEL_ID", model)
	t.Setenv("BEDROCK_MODEL_ID", "global.anthropic.auxiliary-only")
	fixture := mustQualityCases(t)[0]
	binding := nativeTestBinding(t, fixture)
	exported, err := qualityExportNative(t, fixture, binding)
	if err != nil {
		t.Fatal(err)
	}
	evidence := &qualityNativeEvidence{SchemaVersion: qualityNativeSchema, Transport: "aws-sdk", Binding: binding}
	providerCalls := 0
	text := []rune(fixture.Reference)
	partial, complete := "max_tokens", "end_turn"
	if isOpenAISummaryModel(model) {
		partial, complete = "length", "stop"
	}
	transport := &qualityNativeTransport{evidence: evidence, base: noteSourceHTTPClient(func(request *http.Request) (*http.Response, error) {
		providerCalls++
		if !strings.Contains(request.URL.Path, model) || strings.Contains(request.URL.Path, "auxiliary-only") {
			t.Fatal("evaluated auxiliary model instead of configured final model")
		}
		response := nativeTestResponse(model, string(text[:len(text)/2]), partial)
		if providerCalls > 1 {
			response = nativeTestResponse(model, string(text[len(text)/2:]), complete)
		}
		status, headers := 200, http.Header{"Content-Type": {"application/json"}}
		switch scenario {
		case "exhausted":
			response = nativeTestResponse(model, "unfinished", partial)
		case "http-error":
			status = 400
			headers.Set("X-Amzn-Errortype", "ValidationException")
			response = []byte(`{"message":"synthetic provider rejection"}`)
		case "network-error":
			return nil, errors.New("uncontrolled synthetic transport message")
		case "thinking":
			if providerCalls == 1 {
				if isOpenAISummaryModel(model) {
					response = []byte(`{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":""}}],"usage":{"completion_tokens_details":{"reasoning_tokens":7}}}`)
				} else {
					response = []byte(`{"content":[{"type":"thinking","thinking":"synthetic internal reasoning","signature":"opaque-signature"}],"stop_reason":"max_tokens"}`)
				}
			} else {
				response = nativeTestResponse(model, fixture.Reference, complete)
			}
		}
		return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(string(response)))}, nil
	})}
	note, err := qualityRunNative(t, context.Background(), fixture, transport, nil)
	evidence.Completed = err == nil
	if err == nil {
		evidence.OutputSHA256 = qualityHash([]byte(note))
	}
	if providerCalls != len(evidence.Calls) {
		t.Fatal("native recording dropped a provider attempt")
	}
	if validationErr := qualityValidateNativeEvidence(evidence, binding, exported.Calls[0].Request); validationErr != nil {
		t.Fatal(validationErr)
	}
	return fixture, evidence, exported.Calls[0].Request, err
}

func TestNativeQualityUsesFinalModelAndEveryProductionContinuation(t *testing.T) {
	for _, model := range []string{"global.anthropic.claude-opus-5", "global.openai.gpt-6.1-sol"} {
		for _, scenario := range []string{"complete", "thinking", "exhausted", "http-error", "network-error"} {
			t.Run(model+"/"+scenario, func(t *testing.T) {
				fixture, evidence, first, err := nativeTestRun(t, model, scenario)
				success := scenario == "complete" || scenario == "thinking"
				expectedCalls := 2
				if scenario == "exhausted" {
					expectedCalls = maxSummaryContinuations + 1
				}
				if scenario == "http-error" || scenario == "network-error" {
					expectedCalls = 1
				}
				if (err == nil) != success || evidence.Completed != success || len(evidence.Calls) != expectedCalls {
					t.Fatalf("production result: calls=%d completed=%v err=%v", len(evidence.Calls), evidence.Completed, err)
				}
				dir := t.TempDir()
				qualitySaveNative(t, dir, fixture.ID, evidence)
				loaded, err := qualityLoadNative(dir, fixture.ID)
				if err != nil {
					t.Fatal(err)
				}
				note, err := qualityReplayNative(t, fixture, loaded, evidence.Binding, first)
				if (err == nil) != success {
					t.Fatalf("record/replay completion changed: %v", err)
				}
				if success {
					for _, finding := range gradeNoteQuality(fixture, note) {
						if !finding.Passed {
							t.Fatalf("unchanged reference rubric rejected replay: %+v", finding)
						}
					}
				}
				if scenario == "complete" {
					input, output := qualityNativeUsage(loaded)
					if input != 24 || output != 46 {
						t.Fatalf("continuation usage not aggregated: %d/%d", input, output)
					}
				}
			})
		}
	}
}

func cloneNativeEvidence(source *qualityNativeEvidence) *qualityNativeEvidence {
	copy := *source
	copy.Calls = append([]qualityNativeCall(nil), source.Calls...)
	for index := range copy.Calls {
		copy.Calls[index].Request = append([]byte(nil), source.Calls[index].Request...)
		copy.Calls[index].Response = append([]byte(nil), source.Calls[index].Response...)
	}
	return &copy
}

func TestNativeQualityReplayRejectsTamperedBindingsAndOrderedCalls(t *testing.T) {
	for _, model := range []string{"global.anthropic.claude-opus-5", "global.openai.gpt-6.1-sol"} {
		t.Run(model, func(t *testing.T) {
			fixture, original, first, err := nativeTestRun(t, model, "complete")
			if err != nil {
				t.Fatal(err)
			}
			mutations := map[string]func(*qualityNativeEvidence){
				"model":              func(e *qualityNativeEvidence) { e.Binding.ModelID = "global.anthropic.auxiliary-only" },
				"corpus":             func(e *qualityNativeEvidence) { e.Binding.CorpusSHA256 = qualityHash([]byte("changed")) },
				"source":             func(e *qualityNativeEvidence) { e.Binding.SourceSHA256 = qualityHash([]byte("changed")) },
				"production-code":    func(e *qualityNativeEvidence) { e.Binding.ProductionSHA256 = qualityHash([]byte("changed")) },
				"evaluator-code":     func(e *qualityNativeEvidence) { e.Binding.EvaluatorSHA256 = qualityHash([]byte("changed")) },
				"deployment-package": func(e *qualityNativeEvidence) { e.Binding.DeployedCodeSHA256 = "changed" },
				"deployment-config":  func(e *qualityNativeEvidence) { e.Binding.DeploymentSHA256 = "changed" },
				"prompt":             func(e *qualityNativeEvidence) { e.PromptSHA256 = qualityHash([]byte("changed")) },
				"request-bytes":      func(e *qualityNativeEvidence) { e.Calls[1].Request = append(e.Calls[1].Request, ' ') },
				"response-bytes":     func(e *qualityNativeEvidence) { e.Calls[1].Response = append(e.Calls[1].Response, ' ') },
				"missing-call":       func(e *qualityNativeEvidence) { e.Calls = e.Calls[:1] },
				"reordered-calls":    func(e *qualityNativeEvidence) { e.Calls[0], e.Calls[1] = e.Calls[1], e.Calls[0] },
				"unused-response":    func(e *qualityNativeEvidence) { call := e.Calls[1]; call.Sequence = 3; e.Calls = append(e.Calls, call) },
				"continuation-rehashed": func(e *qualityNativeEvidence) {
					e.Calls[1].Request = append(e.Calls[1].Request, ' ')
					e.Calls[1].RequestSHA256 = qualityHash(e.Calls[1].Request)
				},
				"completion-state": func(e *qualityNativeEvidence) { e.Completed = false },
				"output":           func(e *qualityNativeEvidence) { e.OutputSHA256 = qualityHash([]byte("modified note")) },
				"observation": func(e *qualityNativeEvidence) {
					e.Calls[1].ObservedAt = e.Calls[1].ObservedAt.AddDate(-5000, 0, 0)
					e.Calls[1].Sequence = 9
				},
			}
			for name, mutate := range mutations {
				t.Run(name, func(t *testing.T) {
					evidence := cloneNativeEvidence(original)
					mutate(evidence)
					if _, err := qualityReplayNative(t, fixture, evidence, original.Binding, first); err == nil {
						t.Fatal("tampered evidence was accepted")
					}
				})
			}
		})
	}
}

func TestNativeQualityResolvesProductionFallbackWithoutAuxiliaryOverride(t *testing.T) {
	prior := ClaudeOpusModelID
	t.Cleanup(func() { ClaudeOpusModelID = prior })
	ClaudeOpusModelID = "global.anthropic.synthetic-auxiliary"
	t.Setenv("BEDROCK_SUMMARY_MODEL_ID", "global.openai.synthetic-final")
	if qualityEffectiveModel() != "global.openai.synthetic-final" {
		t.Fatal("explicit final model ignored")
	}
	t.Setenv("BEDROCK_SUMMARY_MODEL_ID", "")
	if qualityEffectiveModel() != ClaudeOpusModelID {
		t.Fatal("production auxiliary fallback changed")
	}
}

func TestNativeQualityDeploymentSnapshotRequiresMatchingConfiguration(t *testing.T) {
	t.Setenv("BEDROCK_SUMMARY_MODEL_ID", "global.openai.synthetic-final")
	t.Setenv("BEDROCK_MODEL_ID", "global.anthropic.synthetic-auxiliary")
	t.Setenv("BEDROCK_REGION", "us-west-2")
	record := qualityDeployment{SummaryModelID: os.Getenv("BEDROCK_SUMMARY_MODEL_ID"), AuxiliaryModelID: os.Getenv("BEDROCK_MODEL_ID"),
		Region: "us-west-2", CodeSHA256: base64.StdEncoding.EncodeToString(make([]byte, 32)), State: "Active", LastUpdateStatus: "Successful", RevisionID: "fixture-revision"}
	data, _ := json.Marshal(record)
	if _, err := qualityValidateDeployment(data); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"summary", "auxiliary", "region", "code", "state", "revision"} {
		changed := record
		switch field {
		case "summary":
			changed.SummaryModelID = ""
		case "auxiliary":
			changed.AuxiliaryModelID = "other"
		case "region":
			changed.Region = "us-east-1"
		case "code":
			changed.CodeSHA256 = ""
		case "state":
			changed.State = "Pending"
		case "revision":
			changed.RevisionID = ""
		}
		body, _ := json.Marshal(changed)
		if _, err := qualityValidateDeployment(body); err == nil {
			t.Fatalf("invalid deployment %s accepted", field)
		}
	}
}

func TestNativeQualityEvidenceFilesAreBoundedRegularFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	link := path + ".link"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := qualityReadBounded(link); err == nil {
		t.Fatal("symlink artifact followed")
	}
	if err := os.WriteFile(path, make([]byte, qualityWireLimit+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := qualityReadBounded(path); err == nil {
		t.Fatal("oversized artifact accepted")
	}
}

func TestNativeQualityWrapsConfiguredClientAndRecordsRetryableErrorsOnce(t *testing.T) {
	for _, status := range []int{200, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Setenv("BEDROCK_SUMMARY_MODEL_ID", "global.openai.synthetic-final")
			fixture := mustQualityCases(t)[0]
			binding := nativeTestBinding(t, fixture)
			providerCalls := 0
			body := nativeTestResponse(binding.ModelID, fixture.Reference, "stop")
			if status == 503 {
				body = []byte(`{"message":"synthetic retryable provider failure"}`)
			}
			client, err := qualityLiveClient(aws.Config{
				Region: binding.Region,
				Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
					return aws.Credentials{AccessKeyID: "live-fixture-key", SecretAccessKey: "live-fixture-secret"}, nil
				}),
				HTTPClient: noteSourceHTTPClient(func(*http.Request) (*http.Response, error) {
					providerCalls++
					return &http.Response{StatusCode: status, Header: http.Header{
						"Content-Type": {"application/json"}, "X-Amzn-Errortype": {"ServiceUnavailableException"}},
						Body: io.NopCloser(strings.NewReader(string(body)))}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			evidence := &qualityNativeEvidence{SchemaVersion: qualityNativeSchema, Transport: "aws-sdk", Binding: binding}
			transport := &qualityNativeTransport{evidence: evidence}
			_, err = qualityRunNative(t, context.Background(), fixture, transport, client)
			if (err == nil) != (status == 200) || providerCalls != 1 || len(evidence.Calls) != 1 ||
				evidence.Calls[0].Status != status || string(evidence.Calls[0].Response) != string(body) {
				t.Fatalf("configured transport/status/retry evidence changed: calls=%d err=%v", providerCalls, err)
			}
			encoded, _ := json.Marshal(evidence)
			if strings.Contains(string(encoded), "live-fixture") || strings.Contains(string(encoded), "Authorization") {
				t.Fatal("SDK authentication material entered evidence")
			}
		})
	}
}
