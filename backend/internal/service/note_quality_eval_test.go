package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)

type qualityEvidence struct {
	Transport      string    `json:"transport"`
	ModelID        string    `json:"modelId"`
	Region         string    `json:"region"`
	RequestSHA256  string    `json:"requestSha256"`
	ResponseSHA256 string    `json:"responseSha256"`
	ObservedAt     time.Time `json:"observedAt"`
}

func parseQualityEvidence(proof []byte, modelID, region string, request, response []byte) (qualityEvidence, error) {
	var evidence qualityEvidence
	if err := json.Unmarshal(proof, &evidence); err != nil {
		return evidence, fmt.Errorf("invalid invocation evidence: %w", err)
	}
	if evidence.ModelID == "" || evidence.Region == "" || evidence.RequestSHA256 == "" ||
		evidence.ResponseSHA256 == "" || evidence.Transport == "" || evidence.ObservedAt.IsZero() {
		return evidence, fmt.Errorf("invocation evidence requires every attested field")
	}
	if (evidence.Transport != "aws-sdk" && evidence.Transport != "aws-mcp") ||
		evidence.ModelID != modelID || evidence.Region != region ||
		evidence.RequestSHA256 != qualityHash(request) || evidence.ResponseSHA256 != qualityHash(response) {
		return evidence, fmt.Errorf("invocation evidence does not match the current model/request/response")
	}
	return evidence, nil
}

func TestNoteQualityEvidenceRequiresEveryAttestedField(t *testing.T) {
	request, response := []byte("request"), []byte("response")
	valid := map[string]interface{}{
		"transport": "aws-mcp", "modelId": "model", "region": "us-west-2",
		"requestSha256": qualityHash(request), "responseSha256": qualityHash(response),
		"observedAt": "2026-09-12T12:00:00Z",
	}
	data, _ := json.Marshal(valid)
	if _, err := parseQualityEvidence(data, "model", "us-west-2", request, response); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"transport", "modelId", "region", "requestSha256", "responseSha256", "observedAt"} {
		t.Run(field, func(t *testing.T) {
			incomplete := map[string]interface{}{}
			for key, value := range valid {
				if key != field {
					incomplete[key] = value
				}
			}
			data, _ := json.Marshal(incomplete)
			if _, err := parseQualityEvidence(data, "model", "us-west-2", request, response); err == nil {
				t.Fatalf("missing %s inherited an expected value", field)
			}
		})
	}
	for _, malformed := range []string{"null", "{}", `{"modelId":3}`} {
		if _, err := parseQualityEvidence([]byte(malformed), "model", "us-west-2", request, response); err == nil {
			t.Fatalf("malformed evidence accepted: %s", malformed)
		}
	}
}

type qualityCaseResult struct {
	ID           string           `json:"id"`
	Invoked      bool             `json:"invoked"`
	Completed    bool             `json:"completed"`
	Passed       bool             `json:"passed"`
	Error        string           `json:"error,omitempty"`
	InputTokens  int              `json:"inputTokens,omitempty"`
	OutputTokens int              `json:"outputTokens,omitempty"`
	WallSeconds  float64          `json:"wallSeconds,omitempty"`
	Findings     []qualityFinding `json:"findings,omitempty"`
	Evidence     *qualityEvidence `json:"evidence,omitempty"`
}

func qualityHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeQualityFile(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func qualityJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(body, '\n')
}

func qualityLegacyProductionRequest(t *testing.T, fixture qualityCase, response []byte) ([]byte, string, error) {
	t.Helper()
	meeting := qualityMeeting(fixture)
	request, note, _, err := invokeNoteSourceFixture(t, meeting, string(response), func(s *BedrockService) (string, error) {
		// Historical single-call evidence used this canonical Anthropic
		// envelope. Current acceptance uses qualityRunNative instead.
		s.summaryModelID = "global.anthropic.claude-opus-5"
		return s.SummarizeTranscript(context.Background(), meeting.MeetingID, meeting.UserID, "")
	})
	body, marshalErr := json.Marshal(request)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	return body, note, err
}

func qualityLiveClient(cfg aws.Config) (*bedrockruntime.Client, error) {
	client := bedrockruntime.NewFromConfig(cfg, func(o *bedrockruntime.Options) {
		o.Retryer = aws.NopRetryer{}
		o.RetryMaxAttempts = 1
	})
	if aws.ToString(client.Options().BaseEndpoint) != "" {
		return nil, fmt.Errorf("live evaluation rejects the resolved endpoint override, including shared profiles")
	}
	return client, nil
}

func TestNoteQualityRejectsSharedProfileEndpointOverrides(t *testing.T) {
	profiles := []string{
		"[profile quality]\nregion = us-west-2\nendpoint_url = http://127.0.0.1:9999\n",
		"[profile quality]\nregion = us-west-2\nservices = quality-local\n[services quality-local]\nbedrock_runtime =\n  endpoint_url = http://127.0.0.1:9999\n",
	}
	for _, profile := range profiles {
		file := filepath.Join(t.TempDir(), "config")
		if err := os.WriteFile(file, []byte(profile), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.LoadDefaultConfig(context.Background(),
			config.WithSharedConfigFiles([]string{file}),
			config.WithSharedCredentialsFiles([]string{}),
			config.WithSharedConfigProfile("quality"),
			config.WithCredentialsProvider(aws.AnonymousCredentials{}))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := qualityLiveClient(cfg); err == nil {
			t.Fatal("shared-profile emulator endpoint was accepted as a real model")
		}
	}
	if _, err := qualityLiveClient(aws.Config{Region: "us-west-2", Credentials: aws.AnonymousCredentials{}}); err != nil {
		t.Fatalf("standard AWS endpoint rejected: %v", err)
	}
}

// Legacy single-call evidence has a separate, offline-only entry point. It
// never counts as current-production acceptance and never modifies its input.
func TestNoteQualityHistoricalEvaluation(t *testing.T) {
	if os.Getenv("TTOBAK_NOTE_EVAL_MODE") != "historical-grade" {
		t.Skip("historical evaluation is explicit opt-in")
	}
	dir, out := qualityEvaluationDirectories(t, "historical-grade")
	cases, corpus := qualityEvaluationCases(t)
	region := qualityRegion()
	results := make([]qualityCaseResult, 0, len(cases))
	allPassed := true
	for _, fixture := range cases {
		canonical, _, err := qualityLegacyProductionRequest(t, fixture, []byte(`{"content":[{"type":"text","text":"REQUEST_EXPORT_ONLY"}],"stop_reason":"end_turn"}`))
		if err != nil {
			t.Fatal(err)
		}
		request, err := qualityProviderRequest(ClaudeOpusModelID, canonical)
		if err != nil {
			t.Fatal(err)
		}
		entry := qualityCaseResult{ID: fixture.ID}
		original, readErr := qualityReadBounded(filepath.Join(dir, fixture.ID+".request.json"))
		response, responseErr := qualityReadBounded(filepath.Join(dir, fixture.ID+".response.json"))
		proof, proofErr := qualityReadBounded(filepath.Join(dir, fixture.ID+".evidence.json"))
		if readErr != nil || responseErr != nil || proofErr != nil || !bytes.Equal(original, request) {
			entry.Error = "historical request/response evidence missing or different from this checkout"
		} else {
			evidence, err := parseQualityEvidence(proof, ClaudeOpusModelID, region, request, response)
			if err != nil {
				entry.Error = err.Error()
			} else {
				entry.Invoked, entry.Evidence = true, &evidence
				completion, err := qualityProviderCompletion(ClaudeOpusModelID, response)
				if err != nil {
					entry.Error = "historical response incomplete"
				} else {
					replayed, note, err := qualityLegacyProductionRequest(t, fixture, completion)
					if err != nil || !bytes.Equal(canonical, replayed) {
						entry.Error = "historical completion replay failed"
					} else {
						entry.Completed, entry.Passed = true, true
						var usage ClaudeResponse
						if json.Unmarshal(completion, &usage) == nil {
							entry.InputTokens, entry.OutputTokens = usage.Usage.InputTokens, usage.Usage.OutputTokens
						}
						entry.Findings = gradeNoteQuality(fixture, note)
						for _, finding := range entry.Findings {
							entry.Passed = entry.Passed && finding.Passed
						}
						writeQualityFile(t, out, fixture.ID+".md", []byte(note))
					}
				}
			}
		}
		allPassed = allPassed && entry.Passed
		results = append(results, entry)
	}
	writeQualityFile(t, out, "report.json", qualityJSON(t, map[string]any{
		"mode": "historical-grade", "historical": true, "currentAcceptance": false,
		"modelId": ClaudeOpusModelID, "region": region, "corpusSha256": qualityHash(corpus),
		"passed": allPassed, "cases": results,
		"limitation": "Historical single-call evidence only; not current final-model or continuation acceptance.",
	}))
	if !allPassed {
		t.Error("historical evaluation failed; inspect the separate output report")
	}
}
