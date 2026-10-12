package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

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
					return summaryHTTPResponse(200, `{"Item":{"PK":{"S":"USER#owner"},"SK":{"S":"MEETING#m"},"userId":{"S":"owner"},"meetingId":{"S":"m"},"status":{"S":"transcribing"},"audioKey":{"S":"audio/owner/m/old.wav"},"transcriptA":{"S":"prior text"},"notes":{"S":"human note"},"updatedAt":{"S":"2026-10-12T00:00:00Z"}}}`, nil), nil
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
					"updatedAt", "transcriptA", "transcriptB", "transcriptSegments", "selectedTranscript", "notes", "content", "status"} {
					if !strings.Contains(condition, field) {
						t.Fatalf("missing freshness fence: %s", field)
					}
				}
				for _, value := range []string{"old.wav", "prior text", "human note", "2026-10-12"} {
					if !strings.Contains(condition, value) {
						t.Fatalf("snapshot observation missing: %s", value)
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
			err = repo.SaveTranscriptUpdate(context.Background(), snapshot, map[string]interface{}{
				"transcriptA": "new text", "transcriptSegments": "",
				"transcriptQualityA": model.TranscriptQuality{Refinement: "raw", Degraded: true, SpeakerProvenance: "unknown", Reasons: []string{"invalid_coverage"}},
			})
			if conflict && !errors.Is(err, ErrConditionFailed) || !conflict && err != nil || writes != 1 {
				t.Fatalf("conditional publication: writes=%d err=%v", writes, err)
			}
		})
	}
}
