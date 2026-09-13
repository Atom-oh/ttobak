package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func TestResummarySpillsAreImmutableAndRetainedAfterAmbiguousCommit(t *testing.T) {
	exerciseSummarySpillOutcomes(t, func(repo *DynamoDBRepository, snapshot *model.SummarySnapshot, now time.Time) error {
		state := &model.ResummaryState{RunID: "run", Status: "running", OwnerID: "owner", RequestedBy: "owner", SourceHash: "source", LeaseUntil: now.Add(time.Minute).UnixMilli()}
		return repo.CompleteResummary(context.Background(), snapshot, state, strings.Repeat("n", 60*1024), "coverage", "hash", now)
	})
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
