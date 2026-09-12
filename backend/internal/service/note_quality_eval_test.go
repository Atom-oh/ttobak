package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

type qualityCaseResult struct {
	ID           string           `json:"id"`
	Invoked      bool             `json:"invoked"`
	Completed    bool             `json:"completed"`
	Passed       bool             `json:"passed"`
	Error        string           `json:"error,omitempty"`
	InputTokens  int              `json:"inputTokens,omitempty"`
	OutputTokens int              `json:"outputTokens,omitempty"`
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

func qualityProductionRequest(t *testing.T, fixture qualityCase, response []byte) ([]byte, string, error) {
	t.Helper()
	meeting := qualityMeeting(fixture)
	request, note, _, err := invokeNoteSourceFixture(t, meeting, string(response), func(s *BedrockService) (string, error) {
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

// TestNoteQualityEvaluation is explicit opt-in. All data access remains synthetic;
// only the Bedrock call in live mode uses AWS. Exporting requests never grades a
// synthetic response as a real evaluation.
func TestNoteQualityEvaluation(t *testing.T) {
	mode := os.Getenv("TTOBAK_NOTE_EVAL_MODE")
	if mode == "" {
		t.Skip("real model evaluation is opt-in; use scripts/eval-note-quality.sh")
	}
	if mode != "requests" && mode != "live" && mode != "grade" {
		t.Fatal("mode must be requests, live or grade")
	}
	dir := os.Getenv("TTOBAK_NOTE_EVAL_OUT")
	if !filepath.IsAbs(dir) {
		t.Fatal("TTOBAK_NOTE_EVAL_OUT must be an absolute output directory")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if mode != "grade" {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatal("use a new empty output directory; never mix separate model runs")
		}
	}
	region := os.Getenv("BEDROCK_REGION")
	if region == "" {
		region = "us-west-2" // Same default as cmd/summarize.
	}
	cases, corpus := loadQualityCases(t)
	var client *bedrockruntime.Client
	if mode == "live" {
		if os.Getenv("AWS_ENDPOINT_URL") != "" || os.Getenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME") != "" {
			t.Fatal("live quality evaluation does not accept endpoint overrides")
		}
		cfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion(region))
		if err != nil {
			t.Fatal(err)
		}
		client, err = qualityLiveClient(cfg)
		if err != nil {
			t.Fatal(err)
		}
	}
	results := make([]qualityCaseResult, 0, len(cases))
	allPassed := mode != "requests"
	for _, fixture := range cases {
		if !regexp.MustCompile(`^[a-z0-9-]+$`).MatchString(fixture.ID) {
			t.Fatal("unsafe fixture ID")
		}
		request, _, err := qualityProductionRequest(t, fixture,
			[]byte(`{"content":[{"type":"text","text":"REQUEST_EXPORT_ONLY"}],"stop_reason":"end_turn"}`))
		if err != nil {
			t.Fatalf("construct production request for %s: %v", fixture.ID, err)
		}
		if mode == "grade" {
			original, err := os.ReadFile(filepath.Join(dir, fixture.ID+".request.json"))
			if err != nil || !bytes.Equal(original, request) {
				t.Fatalf("%s: original request does not match current production request", fixture.ID)
			}
		} else {
			writeQualityFile(t, dir, fixture.ID+".request.json", request)
		}
		entry := qualityCaseResult{ID: fixture.ID}
		if mode == "requests" {
			results = append(results, entry)
			continue
		}
		var response []byte
		evidence := qualityEvidence{ModelID: ClaudeOpusModelID, Region: region, RequestSHA256: qualityHash(request)}
		if mode == "live" {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			out, invokeErr := client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
				ModelId: aws.String(ClaudeOpusModelID), ContentType: aws.String("application/json"),
				Accept: aws.String("application/json"), Body: request,
			})
			cancel()
			if invokeErr != nil {
				entry.Error = fmt.Sprintf("model invocation failed: %v", invokeErr)
			} else if out == nil {
				entry.Error = "model invocation returned no response"
			} else {
				entry.Invoked = true
				response = out.Body
				evidence.Transport, evidence.ObservedAt = "aws-sdk", time.Now().UTC()
				evidence.ResponseSHA256 = qualityHash(response)
				writeQualityFile(t, dir, fixture.ID+".response.json", response)
				writeQualityFile(t, dir, fixture.ID+".evidence.json", qualityJSON(t, evidence))
			}
		} else {
			var readErr error
			response, readErr = os.ReadFile(filepath.Join(dir, fixture.ID+".response.json"))
			proof, proofErr := os.ReadFile(filepath.Join(dir, fixture.ID+".evidence.json"))
			if readErr != nil || proofErr != nil || json.Unmarshal(proof, &evidence) != nil {
				entry.Error = "real response and invocation evidence are required"
			} else if (evidence.Transport != "aws-sdk" && evidence.Transport != "aws-mcp") ||
				evidence.ModelID != ClaudeOpusModelID || evidence.Region != region ||
				evidence.RequestSHA256 != qualityHash(request) ||
				evidence.ResponseSHA256 != qualityHash(response) || evidence.ObservedAt.IsZero() {
				entry.Error = "invocation evidence does not match the current model/request/response"
			} else {
				entry.Invoked = true
			}
		}
		if entry.Error == "" {
			// Reuse production completion validation and anchor resolution with
			// the real response. This second model transport is synthetic.
			replayed, note, replayErr := qualityProductionRequest(t, fixture, response)
			if replayErr != nil || !bytes.Equal(request, replayed) {
				entry.Error = fmt.Sprintf("production completion/postprocessing failed: %v", replayErr)
			} else {
				entry.Completed, entry.Passed = true, true
				entry.Evidence = &evidence
				var usage ClaudeResponse
				if err := json.Unmarshal(response, &usage); err != nil {
					t.Fatal(err)
				}
				entry.InputTokens, entry.OutputTokens = usage.Usage.InputTokens, usage.Usage.OutputTokens
				entry.Findings = gradeNoteQuality(fixture, note)
				for _, finding := range entry.Findings {
					entry.Passed = entry.Passed && finding.Passed
				}
				writeQualityFile(t, dir, fixture.ID+".md", []byte(note))
			}
		}
		allPassed = allPassed && entry.Passed
		results = append(results, entry)
	}
	commit := "unknown"
	if value, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		commit = string(bytes.TrimSpace(value))
	}
	report := struct {
		Mode         string              `json:"mode"`
		ModelID      string              `json:"modelId"`
		Region       string              `json:"region"`
		Commit       string              `json:"commit"`
		CorpusSHA256 string              `json:"corpusSha256"`
		CreatedAt    time.Time           `json:"createdAt"`
		Passed       bool                `json:"passed"`
		Cases        []qualityCaseResult `json:"cases"`
		Limitation   string              `json:"limitation"`
	}{mode, ClaudeOpusModelID, region, commit, qualityHash(corpus), time.Now().UTC(), allPassed, results,
		"Small synthetic regression corpus; patterns do not establish general accuracy or complete semantic citation alignment. Inspect every generated note."}
	writeQualityFile(t, dir, "report.json", qualityJSON(t, report))
	if mode == "requests" {
		t.Logf("Exported %d production requests; no real model evaluation performed", len(cases))
		return
	}
	for _, result := range results {
		t.Logf("%s: invoked=%v completed=%v passed=%v", result.ID, result.Invoked, result.Completed, result.Passed)
		if !result.Passed {
			t.Errorf("%s: %s; findings=%+v", result.ID, result.Error, result.Findings)
		}
	}
}
