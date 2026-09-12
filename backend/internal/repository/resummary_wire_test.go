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
	"time"

	"github.com/ttobak/backend/internal/model"
)

func TestResummarySpillsAreImmutableAndRetainedAfterAmbiguousCommit(t *testing.T) {
	for _, outcome := range []string{"success", "condition", "ambiguous"} {
		t.Run(outcome, func(t *testing.T) {
			puts, deletes, transactions := 0, 0, 0
			key := ""
			repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
				if strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".TransactWriteItems") {
					transactions++
					var body map[string]any
					json.NewDecoder(req.Body).Decode(&body)
					update := body["TransactItems"].([]any)[0].(map[string]any)["Update"].(map[string]any)
					raw, _ := json.Marshal(update["ExpressionAttributeValues"])
					if !strings.Contains(string(raw), "s3://bucket/"+strings.TrimPrefix(key, "/bucket/")) {
						t.Fatal("immutable ref not atomically published")
					}
					switch outcome {
					case "condition":
						return summaryHTTPResponse(400, `{"__type":"TransactionCanceledException","CancellationReasons":[{"Code":"ConditionalCheckFailed"},{"Code":"None"}]}`, nil), nil
					case "ambiguous":
						return summaryHTTPResponse(500, `{"__type":"InternalServerError","message":"ambiguous"}`, nil), nil
					default:
						return summaryHTTPResponse(200, `{}`, nil), nil
					}
				}
				switch req.Method {
				case "PUT":
					puts++
					key = req.URL.Path
					if !regexp.MustCompile(`^/bucket/transcripts/meeting/transcriptA\.[a-f0-9]{32}\.txt$`).MatchString(key) {
						t.Fatalf("mutable spill: %s", key)
					}
					body, _ := io.ReadAll(req.Body)
					if len(body) != 290*1024 {
						t.Fatal("spill content changed")
					}
				case "DELETE":
					deletes++
					if req.URL.Path != key {
						t.Fatal("deleted an existing referenced object")
					}
				default:
					t.Fatalf("unexpected request %s", req.Method)
				}
				return summaryHTTPResponse(200, "", http.Header{}), nil
			})
			text := strings.Repeat("x", 290*1024)
			now := time.Now().UTC()
			snapshot := &model.SummarySnapshot{Meeting: &model.Meeting{UserID: "owner", MeetingID: "meeting"},
				Stored: map[string]interface{}{"content": "old", "transcriptA": text},
				Checks: []model.SummaryCheck{{PK: "USER#owner", SK: "MEETING#meeting", Exists: true, Fields: map[string]model.SummaryValue{"content": {Present: true, Value: "old"}, "transcriptA": {Present: true, Value: text}}}}}
			state := &model.ResummaryState{RunID: "run", Status: "running", OwnerID: "owner", RequestedBy: "owner", SourceHash: "source", LeaseUntil: now.Add(time.Minute).UnixMilli()}
			err := repo.CompleteResummary(context.Background(), snapshot, state, strings.Repeat("n", 60*1024), "coverage", "hash", now)
			if (err == nil) != (outcome == "success") {
				t.Fatal(err)
			}
			if outcome == "condition" && !errors.Is(err, ErrConditionFailed) {
				t.Fatal(err)
			}
			if puts != 1 || transactions != 1 || deletes != map[string]int{"success": 0, "condition": 1, "ambiguous": 0}[outcome] {
				t.Fatalf("puts=%d tx=%d deletes=%d", puts, transactions, deletes)
			}
			if snapshot.Stored["transcriptA"] != text {
				t.Fatal("snapshot mutated")
			}
		})
	}
}
func TestResummaryTranscriptUsesExactRefIfMatchAndChecksCurrentETag(t *testing.T) {
	heads, gets := 0, 0
	repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/bucket/transcripts/meeting/transcriptB.0123456789abcdef0123456789abcdef.txt" {
			t.Fatal(req.URL.Path)
		}
		if req.Method == "HEAD" {
			heads++
			etag := `"one"`
			if heads > 1 {
				etag = `"changed"`
			}
			return summaryHTTPResponse(200, "", http.Header{"Etag": {etag}, "Content-Length": {"6"}}), nil
		}
		if req.Method != "GET" || req.Header.Get("If-Match") != `"one"` {
			t.Fatal("unconditional source read")
		}
		gets++
		return summaryHTTPResponse(200, "한글", http.Header{"Etag": {`"one"`}, "Content-Length": {"6"}}), nil
	})
	text, binding, err := repo.ReadResummaryTranscript(context.Background(), "meeting", "transcriptB", "s3://bucket/transcripts/meeting/transcriptB.0123456789abcdef0123456789abcdef.txt")
	if err != nil || text != "한글" || binding == nil || gets != 1 {
		t.Fatalf("%q %+v %v", text, binding, err)
	}
	if err := repo.CheckResummaryObjects(context.Background(), "meeting", []model.SummaryObject{*binding}); !errors.Is(err, ErrConditionFailed) {
		t.Fatal(err)
	}
	if _, _, err := repo.ReadResummaryTranscript(context.Background(), "other", "transcriptB", "s3://bucket/transcripts/meeting/transcriptB.txt"); !errors.Is(err, ErrInvalidTranscriptRef) {
		t.Fatal("cross-meeting source accepted")
	}
}
func TestResummaryCaptureUsesStrongMetadataAndExactEditGrant(t *testing.T) {
	gets, queries := 0, 0
	repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
		var body map[string]any
		json.NewDecoder(req.Body).Decode(&body)
		if body["ConsistentRead"] != true {
			t.Fatal("non-current source/permission read")
		}
		if strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".Query") {
			queries++
			return summaryHTTPResponse(200, `{"Items":[]}`, nil), nil
		}
		gets++
		key := body["Key"].(map[string]any)["SK"].(map[string]any)["S"]
		if key == "MEETING#meeting" {
			return summaryHTTPResponse(200, `{"Item":{"PK":{"S":"USER#owner"},"SK":{"S":"MEETING#meeting"},"meetingId":{"S":"meeting"},"userId":{"S":"owner"},"status":{"S":"done"},"content":{"S":"saved"},"transcriptA":{"S":"s3://bucket/transcripts/meeting/transcriptA.txt"},"updatedAt":{"S":"2026-09-12T12:00:00+00:00"}}}`, nil), nil
		}
		if key != "SHARED#meeting" {
			t.Fatal(key)
		}
		return summaryHTTPResponse(200, `{"Item":{"ownerId":{"S":"owner"},"meetingId":{"S":"meeting"},"permission":{"S":"edit"}}}`, nil), nil
	})
	snapshot, err := repo.CaptureResummary(context.Background(), "owner", "meeting", "editor")
	if err != nil || gets != 2 || queries != 2 {
		t.Fatalf("%v gets=%d queries=%d", err, gets, queries)
	}
	if snapshot.Stored["updatedAt"] != "2026-09-12T12:00:00+00:00" {
		t.Fatal("stored timestamp was normalized")
	}
	if _, pinned := snapshot.Checks[0].Fields["updatedAt"]; pinned {
		t.Fatal("unrelated metadata pinned as generation input")
	}
	if snapshot.Checks[0].Fields["notes"].Present {
		t.Fatal("absent field lost")
	}
	if len(snapshot.Checks) != 2 || snapshot.Checks[1].SK != "SHARED#meeting" {
		t.Fatal("edit grant not pinned")
	}
}
