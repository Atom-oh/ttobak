package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

func TestTranscriptUpdateFencesConcurrentInputAndPublishesQualityTogether(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "concurrent-upload-or-edit"}[conflict], func(t *testing.T) {
			writes := 0
			repo := summarySDKRepo(summaryHTTP(func(request *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(request.Body)
				if strings.HasSuffix(request.Header.Get("X-Amz-Target"), ".GetItem") {
					if !strings.Contains(string(body), `"ConsistentRead":true`) {
						t.Fatal("snapshot was not strongly consistent")
					}
					return summaryHTTPResponse(200, `{"Item":{"PK":{"S":"USER#owner"},"SK":{"S":"MEETING#m"},"userId":{"S":"owner"},"meetingId":{"S":"m"},"status":{"S":"transcribing"},"audioKey":{"S":"audio/owner/m/old.wav"},"audioPartsReadySet":{"NS":["0","1"]},"transcriptA":{"S":"prior text"},"transcriptRevisionA":{"S":"prior-revision"},"notes":{"S":"human note"},"updatedAt":{"S":"2026-10-12T00:00:00Z"}}}`, nil), nil
				}
				if !strings.HasSuffix(request.Header.Get("X-Amz-Target"), ".UpdateItem") {
					t.Fatal("unexpected SDK call")
				}
				writes++
				var wire map[string]any
				if err := json.Unmarshal(body, &wire); err != nil {
					t.Fatal(err)
				}
				condition := regexp.MustCompile(`#[0-9]+|:[0-9]+`).ReplaceAllStringFunc(wire["ConditionExpression"].(string), func(alias string) string {
					if strings.HasPrefix(alias, "#") {
						return wire["ExpressionAttributeNames"].(map[string]any)[alias].(string)
					}
					encoded, _ := json.Marshal(wire["ExpressionAttributeValues"].(map[string]any)[alias])
					return string(encoded)
				})
				for _, field := range []string{"audioKey", "audioKeys", "audioCrop", "diarizationSpeakerHint",
					"audioPartsReadySet", "transcriptRevisionA", "transcriptA", "transcriptB", "transcriptSegments", "selectedTranscript", "status"} {
					if !strings.Contains(condition, field) {
						t.Fatalf("missing freshness fence: %s", field)
					}
				}
				for _, value := range []string{"old.wav", "prior text", "prior-revision", `"NS":["0","1"]`} {
					if !strings.Contains(condition, value) {
						t.Fatalf("snapshot observation missing: %s", value)
					}
				}
				for _, unrelated := range []string{"notes", "content", "updatedAt", "title", "participants"} {
					if strings.Contains(condition, unrelated) {
						t.Fatalf("unrelated metadata blocks ASR: %s", unrelated)
					}
				}
				if !strings.Contains(condition, "attribute_not_exists") {
					t.Fatal("absent selected/source fields were not fenced")
				}
				for _, value := range []string{"transcriptQualityA", "new text", "invalid_coverage"} {
					if !strings.Contains(string(body), value) {
						t.Fatal("content/status were not published together")
					}
				}
				if conflict {
					return summaryHTTPResponse(400, `{"__type":"ConditionalCheckFailedException"}`, nil), nil
				}
				return summaryHTTPResponse(200, `{}`, nil), nil
			}))
			// Keep this test focused on the canonical CAS; immutable-spill
			// sizing/cleanup has separate wire coverage.
			repo.s3Client = nil
			snapshot, err := repo.CaptureTranscriptUpdate(context.Background(), "owner", "m")
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := snapshot.Checks[0].Fields["audioPartsReadySet"].Value.(*types.AttributeValueMemberNS); !ok {
				t.Fatal("native Number Set changed type")
			}
			fields := map[string]interface{}{
				"transcriptA": "new text", "transcriptSegments": "",
				"transcriptQualityA": model.TranscriptQuality{Refinement: "raw", Degraded: true, SpeakerProvenance: "unknown", Reasons: []string{"invalid_coverage"}},
			}
			before := map[string]interface{}{}
			for key, value := range fields {
				before[key] = value
			}
			err = repo.SaveTranscriptUpdate(context.Background(), snapshot, fields)
			if !reflect.DeepEqual(fields, before) {
				t.Fatal("conditional write mutated caller map")
			}
			if conflict && !errors.Is(err, ErrConditionFailed) || !conflict && err != nil || writes != 1 {
				t.Fatalf("conditional publication: writes=%d err=%v", writes, err)
			}
		})
	}
}

func TestTranscriptRevisionOnlyFollowsWrittenVariant(t *testing.T) {
	for _, field := range []string{"transcriptA", "transcriptB", "notes"} {
		t.Run(field, func(t *testing.T) {
			repo := summarySDKRepo(summaryHTTP(func(request *http.Request) (*http.Response, error) {
				raw, _ := io.ReadAll(request.Body)
				var wire struct{ ExpressionAttributeNames map[string]string }
				if err := json.Unmarshal(raw, &wire); err != nil {
					t.Fatal(err)
				}
				names := map[string]bool{}
				for _, name := range wire.ExpressionAttributeNames {
					names[name] = true
				}
				for _, variant := range []string{"A", "B"} {
					writes := field == "transcript"+variant
					if names["transcriptRevision"+variant] != writes || names["transcriptQuality"+variant] != writes {
						t.Fatal("revision/quality touched unchanged sibling")
					}
				}
				if field != "notes" && (!strings.Contains(string(raw), `"S":"unknown"`) || !strings.Contains(string(raw), `"S":"unavailable"`)) {
					t.Fatal("legacy text-only write inherited timing")
				}
				return summaryHTTPResponse(200, `{}`, nil), nil
			}))
			repo.s3Client = nil
			fields := map[string]interface{}{field: "current"}
			if err := repo.UpdateMeetingFields(context.Background(), "owner", "m", fields); err != nil {
				t.Fatal(err)
			}
			if len(fields) != 1 || fields[field] != "current" {
				t.Fatal("writer mutated caller")
			}
		})
	}
}

func TestTranscriptTargetRetainsLegacySpillByteFence(t *testing.T) {
	etag := `"before"`
	repo := summarySDKRepo(summaryHTTP(func(request *http.Request) (*http.Response, error) {
		if request.Method == "HEAD" {
			return summaryHTTPResponse(200, ``, http.Header{"Etag": {etag}}), nil
		}
		return summaryHTTPResponse(200, `{"Item":{"PK":{"S":"USER#owner"},"SK":{"S":"MEETING#m"},"userId":{"S":"owner"},"meetingId":{"S":"m"},"status":{"S":"transcribing"},"transcriptA":{"S":"s3://bucket/transcripts/m/transcriptA.txt"}}}`, nil), nil
	}))
	snapshot, err := repo.CaptureTranscriptUpdate(context.Background(), "owner", "m")
	if err != nil || len(snapshot.Objects) != 1 {
		t.Fatal("legacy byte observation missing", err)
	}
	etag = `"human-edit"`
	if same, err := repo.TranscriptTargetsMatch(context.Background(), snapshot); err != nil || same {
		t.Fatal("same ref with new bytes accepted", err)
	}
}

func TestSummaryContentHashPinsPresenceAndExactBytes(t *testing.T) {
	seen := map[string]bool{}
	for _, stored := range []map[string]interface{}{
		{}, {"content": ""}, {"content": " "}, {"content": "saved note"},
		{"content": "saved note\n"}, {"content": "사용자 메모"},
	} {
		snapshot := &model.SummarySnapshot{Stored: stored}
		hash := SummaryContentHash(snapshot)
		if len(hash) != 64 || seen[hash] || SummaryContentHash(snapshot) != hash {
			t.Fatal("content presence/bytes did not create a stable distinct fence")
		}
		seen[hash] = true
	}
}
