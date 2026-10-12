package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)

func qualityEvaluationDirectories(t *testing.T, mode string) (input, output string) {
	t.Helper()
	input = os.Getenv("TTOBAK_NOTE_EVAL_OUT")
	output = input
	if mode == "grade" || mode == "historical-grade" {
		output = os.Getenv("TTOBAK_NOTE_EVAL_GRADE_OUT")
		if filepath.Clean(input) == filepath.Clean(output) {
			t.Fatal("grade must write to a separate new output directory")
		}
	}
	if !filepath.IsAbs(input) || !filepath.IsAbs(output) {
		t.Fatal("evaluation input/output paths must be absolute")
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(output)
	if err != nil || len(entries) != 0 {
		t.Fatal("use a new empty output directory; existing evidence is immutable")
	}
	// Equal symlink targets must not permit a regrade to overwrite evidence.
	if mode == "grade" || mode == "historical-grade" {
		source, err := filepath.EvalSymlinks(input)
		if err != nil {
			t.Fatal(err)
		}
		target, err := filepath.EvalSymlinks(output)
		if err != nil || source == target {
			t.Fatal("grade output must differ from input")
		}
	}
	return input, output
}

func qualityEvaluationCases(t *testing.T) ([]qualityCase, []byte) {
	t.Helper()
	cases, corpus := loadQualityCases(t)
	if path := os.Getenv("TTOBAK_NOTE_EVAL_EXTRA_CASES"); path != "" {
		extra, err := qualityReadBounded(path)
		if err != nil {
			t.Fatal(err)
		}
		var additional []qualityCase
		if err := json.Unmarshal(extra, &additional); err != nil {
			t.Fatal(err)
		}
		cases, corpus = append(cases, additional...), append(corpus, extra...)
	}
	seen := map[string]bool{}
	for _, fixture := range cases {
		if !regexp.MustCompile(`^[a-z0-9-]+$`).MatchString(fixture.ID) || seen[fixture.ID] || len(fixture.Criteria) == 0 {
			t.Fatal("every evaluation case must have a unique safe ID and criteria")
		}
		seen[fixture.ID] = true
	}
	return cases, corpus
}

type qualityNativeResult struct {
	ID           string           `json:"id"`
	Invoked      bool             `json:"invoked"`
	Completed    bool             `json:"completed"`
	Passed       bool             `json:"passed"`
	Error        string           `json:"error,omitempty"`
	Calls        int              `json:"calls"`
	InputTokens  int              `json:"inputTokens,omitempty"`
	OutputTokens int              `json:"outputTokens,omitempty"`
	WallSeconds  float64          `json:"wallSeconds,omitempty"`
	Findings     []qualityFinding `json:"findings,omitempty"`
}

func qualityNativeUsage(evidence *qualityNativeEvidence) (input, output int) {
	for _, call := range evidence.Calls {
		if call.Status != 200 || call.Failure != "" {
			continue
		}
		if isOpenAISummaryModel(evidence.Binding.ModelID) {
			var response openAISummaryResponse
			if json.Unmarshal(call.Response, &response) == nil {
				input += response.Usage.Input
				output += response.Usage.Output
			}
		} else {
			var response ClaudeResponse
			if json.Unmarshal(call.Response, &response) == nil {
				input += response.Usage.InputTokens
				output += response.Usage.OutputTokens
			}
		}
	}
	return input, output
}

func qualityReplayNative(t *testing.T, fixture qualityCase, evidence *qualityNativeEvidence, expected qualityBinding, firstRequest []byte) (string, error) {
	t.Helper()
	if err := qualityValidateNativeEvidence(evidence, expected, firstRequest); err != nil {
		return "", err
	}
	transport := &qualityNativeTransport{evidence: evidence, replay: true}
	note, err := qualityRunNative(t, context.Background(), fixture, transport, nil)
	if (err == nil) != evidence.Completed {
		return "", errors.New("recorded completion state differs from production replay")
	}
	if err == nil && evidence.OutputSHA256 != qualityHash([]byte(note)) {
		return "", errors.New("replayed note differs from recorded output")
	}
	return note, err
}

// Explicit opt-in. Unit tests use synthetic SDK transports; live mode alone
// contacts Bedrock and still reads/writes no customer repository.
func TestNoteQualityEvaluation(t *testing.T) {
	mode := os.Getenv("TTOBAK_NOTE_EVAL_MODE")
	if mode == "" || mode == "historical-grade" {
		t.Skip("native evaluation is explicit opt-in")
	}
	if mode != "requests" && mode != "live" && mode != "grade" {
		t.Fatal("mode must be requests, live, grade or historical-grade")
	}
	input, out := qualityEvaluationDirectories(t, mode)
	cases, corpus := qualityEvaluationCases(t)
	production, evaluator, err := qualitySourceDigests()
	if err != nil {
		t.Fatal(err)
	}
	binding := qualityBinding{ModelID: qualityEffectiveModel(), Region: qualityRegion(), CorpusSHA256: qualityHash(corpus),
		ProductionSHA256: production, EvaluatorSHA256: evaluator}
	deploymentPath := os.Getenv("TTOBAK_NOTE_EVAL_DEPLOYMENT")
	if mode == "grade" {
		deploymentPath = filepath.Join(input, "deployment.json")
	}
	if deploymentPath != "" {
		data, err := qualityReadBounded(deploymentPath)
		if err != nil {
			if mode == "grade" {
				t.Fatal("native deployment snapshot missing or invalid; use historical-grade for legacy single-call evidence")
			}
			t.Fatal(err)
		}
		deployment, err := qualityValidateDeployment(data)
		if err != nil {
			t.Fatal(err)
		}
		binding.DeploymentSHA256, binding.DeployedCodeSHA256 = qualityHash(data), deployment.CodeSHA256
		writeQualityFile(t, out, "deployment.json", data)
	} else if mode != "requests" {
		t.Fatal("live/grade requires a deployed configuration/code snapshot; set TTOBAK_NOTE_EVAL_DEPLOYMENT for live")
	}
	var live *bedrockruntime.Client
	if mode == "live" {
		if os.Getenv("AWS_ENDPOINT_URL") != "" || os.Getenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME") != "" {
			t.Fatal("live evaluation rejects endpoint overrides")
		}
		cfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion(binding.Region))
		if err != nil {
			t.Fatal(err)
		}
		live, err = qualityLiveClient(cfg)
		if err != nil {
			t.Fatal(err)
		}
	}
	results := make([]qualityNativeResult, 0, len(cases))
	allPassed := mode != "requests"
	for _, fixture := range cases {
		source, _ := json.Marshal(qualityMeeting(fixture))
		caseBinding := binding
		caseBinding.SourceSHA256 = qualityHash(source)
		exported, err := qualityExportNative(t, fixture, caseBinding)
		if err != nil {
			t.Fatal(err)
		}
		firstRequest := exported.Calls[0].Request
		entry := qualityNativeResult{ID: fixture.ID}
		if mode == "requests" {
			qualitySaveNative(t, out, fixture.ID, exported)
			results = append(results, entry)
			continue
		}
		var evidence *qualityNativeEvidence
		var note string
		validEvidence := false
		if mode == "live" {
			evidence = &qualityNativeEvidence{SchemaVersion: qualityNativeSchema, Transport: "aws-sdk", Binding: caseBinding}
			transport := &qualityNativeTransport{evidence: evidence}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			start := time.Now()
			note, err = qualityRunNative(t, ctx, fixture, transport, live)
			entry.WallSeconds = time.Since(start).Seconds()
			cancel()
			evidence.Completed = err == nil
			if err == nil {
				evidence.OutputSHA256 = qualityHash([]byte(note))
			}
			// Preserve every attempt before grading, including failed cases.
			qualitySaveNative(t, out, fixture.ID, evidence)
			if validationErr := qualityValidateNativeEvidence(evidence, caseBinding, firstRequest); validationErr != nil {
				err = validationErr
			} else {
				validEvidence = true
			}
		} else {
			evidence, err = qualityLoadNative(input, fixture.ID)
			if err == nil {
				err = qualityValidateNativeEvidence(evidence, caseBinding, firstRequest)
				if err == nil {
					validEvidence = true
					note, err = qualityReplayNative(t, fixture, evidence, caseBinding, firstRequest)
				}
			}
		}
		if evidence != nil {
			entry.Calls = len(evidence.Calls)
			for _, call := range evidence.Calls {
				entry.Invoked = entry.Invoked || validEvidence && call.Status != 0
			}
			entry.InputTokens, entry.OutputTokens = qualityNativeUsage(evidence)
		}
		if err != nil {
			entry.Error = "native evidence or production completion failed; inspect recorded calls"
			if errors.Is(err, errQualityNativeMissing) {
				entry.Error = "native evidence missing; use historical-grade for legacy single-call evidence"
			}
		} else {
			entry.Completed, entry.Passed = true, true
			entry.Findings = gradeNoteQuality(fixture, note)
			for _, finding := range entry.Findings {
				entry.Passed = entry.Passed && finding.Passed
			}
			if !entry.Passed {
				entry.Error = "quality rubric failed"
			}
			writeQualityFile(t, out, fixture.ID+".md", []byte(note))
		}
		allPassed = allPassed && entry.Passed
		results = append(results, entry)
	}
	commit := "unknown"
	if value, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		commit = string(bytes.TrimSpace(value))
	}
	writeQualityFile(t, out, "report.json", qualityJSON(t, map[string]any{
		"schemaVersion": qualityNativeSchema, "mode": mode, "modelId": binding.ModelID, "region": binding.Region,
		"commit": commit, "binding": binding, "createdAt": time.Now().UTC(), "passed": allPassed, "cases": results,
		"acceptanceScope":         "current source pipeline with a recorded deployment configuration",
		"deployedPackageExecuted": false,
		"limitation":              "Synthetic regression corpus and operator-attested SDK evidence, not cryptographic proof of AWS execution, deployed-package equivalence, general accuracy, or complete semantic citation alignment. Inspect every note.",
	}))
	if mode == "requests" {
		t.Log("Exported native first requests; no model evaluation performed")
		return
	}
	for _, entry := range results {
		t.Logf("%s: calls=%d completed=%v passed=%v", entry.ID, entry.Calls, entry.Completed, entry.Passed)
		if !entry.Passed {
			t.Errorf("%s: %s; inspect output report", entry.ID, entry.Error)
		}
	}
}
