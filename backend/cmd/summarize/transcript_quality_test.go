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
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
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
	_, hasSegments := fields["transcriptSegments"]
	if err != nil || fields["transcriptA"] != "preserved source text" || hasSegments ||
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

func TestSingleWorkerDiscardsOutputAfterConcurrentSourceChanges(t *testing.T) {
	for _, changedField := range []string{"transcriptA", "audioKey", "selectedTranscript"} {
		t.Run(changedField, func(t *testing.T) {
			current := model.Meeting{PK: "USER#owner", SK: "MEETING#m", MeetingID: "m", UserID: "owner", Status: model.StatusTranscribing,
				AudioKey: "audio/owner/m/source.wav", TranscriptA: "prior transcript", Notes: "prior note", UpdatedAt: time.Now().UTC()}
			modelCalls, attemptedWrites, committedWrites := 0, 0, 0
			client := retryHTTP(func(request *http.Request) (*http.Response, error) {
				switch request.Header.Get("X-Amz-Target") {
				case "DynamoDB_20120810.Query", "DynamoDB_20120810.GetItem":
					item, _ := attributevalue.MarshalMap(current)
					data, _ := attributevalue.MarshalMapJSON(item)
					body := `{"Item":` + string(data) + `}`
					if strings.HasSuffix(request.Header.Get("X-Amz-Target"), ".Query") {
						body = `{"Items":[` + string(data) + `]}`
					}
					return transcriptFixtureResponse(200, body, nil), nil
				case "DynamoDB_20120810.UpdateItem":
					attemptedWrites++
					body, _ := io.ReadAll(request.Body)
					var wire struct {
						ConditionExpression      string
						ExpressionAttributeNames map[string]string
					}
					if err := json.Unmarshal(body, &wire); err != nil {
						t.Fatal(err)
					}
					guarded := false
					for alias, name := range wire.ExpressionAttributeNames {
						if name == changedField && strings.Contains(wire.ConditionExpression, alias) {
							guarded = true
						}
					}
					if !guarded || !strings.Contains(string(body), "transcriptQualityA") || modelCalls != 1 {
						t.Fatal("worker publication was not source-fenced with quality")
					}
					if guarded {
						return transcriptFixtureResponse(400, `{"__type":"ConditionalCheckFailedException"}`, nil), nil
					}
					committedWrites++
					return transcriptFixtureResponse(200, `{}`, nil), nil
				}
				if request.Method == "HEAD" {
					return transcriptFixtureResponse(200, "", http.Header{"Etag": {`"source"`}}), nil
				}
				if request.Method == "GET" {
					return transcriptFixtureResponse(200, `{"results":{"transcripts":[{"transcript":"source words"}]},"whisper_metadata":{"duration_seconds":2,"segments":[{"start":0,"end":2,"text":"source words","speaker":"spk_0"}]}}`, http.Header{"Etag": {`"source"`}}), nil
				}
				modelCalls++
				switch changedField {
				case "transcriptA":
					current.TranscriptA = "human correction"
				case "audioKey":
					current.AudioKey = "audio/owner/m/replacement.wav"
				case "selectedTranscript":
					current.SelectedTranscript = "B"
				}
				current.UpdatedAt = current.UpdatedAt.Add(time.Second)
				return transcriptFixtureResponse(200, `{"stop_reason":"end_turn","content":[{"type":"text","text":"[{\"speaker\":\"spk_0\",\"text\":\"source words\",\"startTime\":0,\"endTime\":2,\"sourceIds\":[0]}]"}]}`, nil), nil
			})
			installTranscriptFixture(t, client)
			priorRepo := repo
			t.Cleanup(func() { repo = priorRepo })
			cfg := aws.Config{Region: "us-west-2", Credentials: aws.AnonymousCredentials{}, HTTPClient: client, Retryer: func() aws.Retryer { return aws.NopRetryer{} }}
			repo = repository.NewDynamoDBRepositoryWithS3(dynamodb.NewFromConfig(cfg), "table", s3Client, "bucket")
			err := handleSingleTranscript(context.Background(), "bucket", "transcripts/m.json", "source")
			needsFreshRetry := changedField == "selectedTranscript"
			if needsFreshRetry && !errors.Is(err, repository.ErrConditionFailed) || !needsFreshRetry && err != nil {
				t.Fatalf("superseded delivery/fresh retry classification: %v", err)
			}
			// The existing sibling-size writer may retry the same immutable
			// source condition up to three times; it must never rebind output.
			if modelCalls != 1 || attemptedWrites < 1 || attemptedWrites > 3 || committedWrites != 0 || current.Status != model.StatusTranscribing {
				t.Fatalf("stale worker changed state or ran summary: models=%d attempts=%d commits=%d", modelCalls, attemptedWrites, committedWrites)
			}
		})
	}
}
