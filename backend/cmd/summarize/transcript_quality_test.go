package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

func transcriptFixtureResponse(status int, body string, headers http.Header) *http.Response {
	if headers == nil {
		headers = http.Header{}
	}
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body))}
}

func installTranscriptFixture(t *testing.T, client retryHTTP) {
	t.Helper()
	priorS3, priorBedrock := s3Client, bedrockService
	t.Cleanup(func() { s3Client, bedrockService = priorS3, priorBedrock })
	cfg := aws.Config{Region: "us-west-2", Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture"}, nil
	}), HTTPClient: client, Retryer: func() aws.Retryer { return aws.NopRetryer{} }}
	s3Client = s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true; o.BaseEndpoint = aws.String("https://fixture.invalid") })
	bedrockService = service.NewBedrockService(bedrockruntime.NewFromConfig(cfg), nil, nil)
}

func TestMergedTranscriptRetainsDegradationProvenanceAndPartOffsets(t *testing.T) {
	installTranscriptFixture(t, retryHTTP(func(request *http.Request) (*http.Response, error) {
		switch {
		case request.URL.Query().Get("list-type") == "2":
			return transcriptFixtureResponse(200, `<ListBucketResult><Contents><Key>transcripts/m_part_000.json</Key></Contents><Contents><Key>transcripts/m_part_001.json</Key></Contents></ListBucketResult>`, nil), nil
		case request.Method == "GET":
			index, label, enabled := 0, `"speaker":"spk_0",`, "true"
			if strings.Contains(request.URL.Path, "_001") {
				index, label, enabled = 1, "", "false"
			}
			body := fmt.Sprintf(`{"results":{"transcripts":[{"transcript":"part %d raw"}]},"whisper_metadata":{"duration_seconds":10,"segments":[{%s"start":0,"end":2,"text":"part %d raw"}],"diarization":{"enabled":%s,"num_speakers_detected":%d}}}`, index, label, index, enabled, 1-index)
			return transcriptFixtureResponse(200, body, http.Header{"Etag": {fmt.Sprintf(`"part-%d"`, index)}}), nil
		case request.Method == "POST":
			var input service.ClaudeRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			stop, text := "end_turn", `[{"speaker":"spk_9","text":"part 0 clean","startTime":0,"endTime":2,"sourceIds":[0]}]`
			if strings.Contains(input.Messages[0].Content[0].Text, "part 1 raw") {
				stop = "max_tokens"
			}
			body, _ := json.Marshal(map[string]any{"stop_reason": stop, "content": []map[string]string{{"type": "text", "text": text}}})
			return transcriptFixtureResponse(200, string(body), nil), nil
		default:
			t.Fatal("unexpected fixture request")
			return nil, nil
		}
	}))
	parsed, err := mergePartTranscripts(context.Background(), "bucket", "m", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Segments) != 2 || parsed.Segments[0].Speaker != "spk_0" ||
		parsed.Segments[1].Speaker != "spk_1000000" || parsed.Segments[1].StartTime != 10 || parsed.Duration != 20 {
		t.Fatalf("part identity/timeline lost: %+v", parsed)
	}
	if parsed.Quality.Refinement != "partial" || parsed.Quality.FallbackChunks != 1 || parsed.Quality.TotalChunks != 2 ||
		!parsed.Quality.Degraded || parsed.Quality.SpeakerProvenance != "mixed" || !strings.Contains(parsed.Text, "part 1 raw") {
		t.Fatalf("merged degradation lost: %+v", parsed.Quality)
	}
	fields, err := transcriptUpdateFields(parsed, false)
	if err != nil {
		t.Fatal(err)
	}
	meeting := &model.Meeting{TranscriptA: fields["transcriptA"].(string), TranscriptSegments: fields["transcriptSegments"].(string)}
	quality := fields["transcriptQualityA"].(model.TranscriptQuality)
	meeting.TranscriptQualityA = &quality
	if model.MatchingTranscriptQuality(meeting, "A").Refinement != "partial" {
		t.Fatal("merged quality fingerprint mismatched published bytes")
	}
	if len(parsed.Objects) != 2 {
		t.Fatal("multipart source versions were not retained")
	}
}

func TestMergedTranscribePartsKeepCanonicalPunctuation(t *testing.T) {
	installTranscriptFixture(t, retryHTTP(func(request *http.Request) (*http.Response, error) {
		switch {
		case request.URL.Query().Get("list-type") == "2":
			return transcriptFixtureResponse(200, `<ListBucketResult><Contents><Key>transcripts/m_part_000.json</Key></Contents><Contents><Key>transcripts/m_part_001.json</Key></Contents></ListBucketResult>`, nil), nil
		case request.Method == "GET":
			body := `{"results":{"transcripts":[{"transcript":"No, more tests."}],` +
				`"speaker_labels":{"speakers":1,"segments":[{"start_time":"0.0","end_time":"2.0","speaker_label":"spk_0","items":[` +
				`{"start_time":"0.0","end_time":"0.5","speaker_label":"spk_0"},{"start_time":"0.6","end_time":"1.0","speaker_label":"spk_0"},` +
				`{"start_time":"1.1","end_time":"2.0","speaker_label":"spk_0"}]}]},"items":[` +
				`{"start_time":"0.0","end_time":"0.5","type":"pronunciation","alternatives":[{"content":"No"}]},` +
				`{"type":"punctuation","alternatives":[{"content":","}]},` +
				`{"start_time":"0.6","end_time":"1.0","type":"pronunciation","alternatives":[{"content":"more"}]},` +
				`{"start_time":"1.1","end_time":"2.0","type":"pronunciation","alternatives":[{"content":"tests"}]},` +
				`{"type":"punctuation","alternatives":[{"content":"."}]}]}}`
			return transcriptFixtureResponse(200, body, http.Header{"Etag": {`"part"`}}), nil
		default:
			t.Fatal("Transcribe parts invoked refinement")
			return nil, nil
		}
	}))
	parsed, err := mergePartTranscripts(context.Background(), "bucket", "m", 2)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Text != "No, more tests.\n\nNo, more tests." {
		t.Fatalf("canonical punctuation lost: %q", parsed.Text)
	}
	if len(parsed.Segments) != 2 || parsed.Segments[0].Speaker != "spk_0" || parsed.Segments[1].Speaker != "spk_1000000" ||
		parsed.Segments[1].StartTime != 2 || parsed.Segments[1].EndTime != 4 {
		t.Fatalf("Transcribe part identity/timeline lost: %+v", parsed.Segments)
	}
}

func TestTranscriptObjectReplacementRejectsOldRefinement(t *testing.T) {
	installTranscriptFixture(t, retryHTTP(func(request *http.Request) (*http.Response, error) {
		if request.Method != "HEAD" {
			t.Fatal("version check read source content")
		}
		return transcriptFixtureResponse(200, "", http.Header{"Etag": {`"new"`}, "X-Amz-Version-Id": {"v2"}}), nil
	}))
	if err := checkTranscriptObjects(context.Background(), []model.SummaryObject{{Bucket: "bucket", Key: "transcripts/m.json", ETag: `"old"`, VersionID: "v1"}}); !errors.Is(err, repository.ErrConditionFailed) {
		t.Fatalf("replaced source accepted: %v", err)
	}
	if err := checkTranscriptObjects(context.Background(), []model.SummaryObject{{Bucket: "bucket", Key: "transcripts/m.json", ETag: `"new"`, VersionID: "v1"}}); !errors.Is(err, repository.ErrConditionFailed) {
		t.Fatalf("replaced version with same bytes accepted: %v", err)
	}
}

func TestInvalidRawTimingRemainsReadableWithoutFabricatedSegments(t *testing.T) {
	installTranscriptFixture(t, retryHTTP(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid raw input invoked model")
		return nil, nil
	}))
	parsed := &parsedTranscript{Text: "preserved source text", Whisper: []service.WhisperSegment{{Start: 9999, End: -1, Text: "preserved source text"}},
		Quality: &model.TranscriptQuality{Refinement: "not_requested", SpeakerProvenance: "unknown"}}
	refineParsedTranscript(context.Background(), parsed)
	fields, err := transcriptUpdateFields(parsed, false)
	raw, hasSegments := fields["transcriptSegments"]
	var preserved []TranscriptSegmentOut
	if hasSegments {
		_ = json.Unmarshal([]byte(raw.(string)), &preserved)
	}
	if err != nil || !strings.Contains(fields["transcriptA"].(string), "preserved source text") || !hasSegments || len(preserved) != 1 || !preserved[0].Untimed || preserved[0].StartTime != 0 || preserved[0].EndTime != 0 ||
		!parsed.Quality.Degraded || parsed.Quality.Refinement != "raw" {
		t.Fatal("invalid source lost readable text or gained invented timing")
	}
}

func TestTranscriptUpdateOmitsSegmentsWhenNoneWereTimed(t *testing.T) {
	parsed := &parsedTranscript{Text: "untimed text", Quality: &model.TranscriptQuality{Refinement: "raw", SpeakerProvenance: "unknown"}}
	fields, err := transcriptUpdateFields(parsed, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["transcriptSegments"]; ok {
		t.Fatal("untimed publication blanked the shared segments field")
	}
	if fields["transcriptB"] != "untimed text" {
		t.Fatal("text was not published")
	}
}
