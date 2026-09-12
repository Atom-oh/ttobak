package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestLegacySummarySourceIsByteBoundBeforePublication(t *testing.T) {
	heads, writes := 0, 0
	repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".TransactWriteItems") {
			writes++
			return summaryHTTPResponse(200, `{}`, nil), nil
		}
		if req.Method == "HEAD" {
			heads++
			etag := `"old"`
			if heads > 1 {
				etag = `"changed"`
			}
			return summaryHTTPResponse(200, "", http.Header{"Etag": {etag}, "Content-Length": {"6"}, "X-Amz-Version-Id": {"v1"}}), nil
		}
		if req.Header.Get("If-Match") != `"old"` {
			t.Fatal("legacy source read was not conditional")
		}
		return summaryHTTPResponse(200, "한글", http.Header{"Etag": {`"old"`}, "Content-Length": {"6"}, "X-Amz-Version-Id": {"v1"}}), nil
	})
	text, object, err := repo.ReadResummaryTranscript(context.Background(), "m", "transcriptA", "s3://bucket/transcripts/m/transcriptA.txt")
	if err != nil || text != "한글" || object == nil {
		t.Fatal(text, object, err)
	}
	snapshot := &model.SummarySnapshot{Meeting: &model.Meeting{UserID: "owner", MeetingID: "m"},
		Stored: map[string]interface{}{}, Objects: []model.SummaryObject{*object},
		Checks: []model.SummaryCheck{{PK: "USER#owner", SK: "MEETING#m", Exists: true}}}
	if err := repo.SaveMeetingSummary(context.Background(), snapshot, "old output", ""); !errors.Is(err, ErrConditionFailed) || writes != 0 {
		t.Fatalf("changed legacy bytes were published: writes=%d err=%v", writes, err)
	}
}

func TestSummaryTransactionIncludesCanonicalAttachmentAndResultGuards(t *testing.T) {
	repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".TransactWriteItems") {
			t.Fatal(req.Header.Get("X-Amz-Target"))
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		ops := body["TransactItems"].([]any)
		if len(ops) != 3 || ops[0].(map[string]any)["Update"] == nil {
			t.Fatal("source and output are not one transaction")
		}
		for i, required := range []string{"originalKey", "sourceETag"} {
			op := ops[i+1].(map[string]any)["ConditionCheck"].(map[string]any)
			if !strings.Contains(analysisCondition(t, op), required) {
				t.Fatal("missing evidence condition")
			}
		}
		return summaryHTTPResponse(400, `{"__type":"TransactionCanceledException","CancellationReasons":[{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"}]}`, nil), nil
	})
	snapshot := &model.SummarySnapshot{Meeting: &model.Meeting{UserID: "owner", MeetingID: "m"}, Stored: map[string]interface{}{},
		Checks: []model.SummaryCheck{
			{PK: "USER#owner", SK: "MEETING#m", Exists: true},
			{PK: "MEETING#m", SK: "ATTACH#a", Exists: true, Fields: map[string]model.SummaryValue{"originalKey": {Present: true, Value: "files/owner/m/a.pdf"}}},
			{PK: "MEETING#m", SK: "ATTEXT#a", Exists: true, Fields: map[string]model.SummaryValue{"sourceETag": {Present: true, Value: "old"}}},
		}}
	if err := repo.SaveMeetingSummary(context.Background(), snapshot, "summary", ""); !errors.Is(err, ErrConditionFailed) {
		t.Fatal(err)
	}
}

func TestSummaryBindingNeverRebasesChangedAttachmentMetadata(t *testing.T) {
	repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
		var input map[string]any
		json.NewDecoder(req.Body).Decode(&input)
		if input["ConsistentRead"] != true {
			t.Fatal("evidence read is not current")
		}
		return summaryHTTPResponse(200, `{"Item":{"attachmentId":{"S":"a"},"meetingId":{"S":"m"},"userId":{"S":"owner"},"originalKey":{"S":"files/owner/m/new.pdf"},"type":{"S":"document"},"status":{"S":"done"}}}`, nil), nil
	})
	snapshot := &model.SummarySnapshot{Meeting: &model.Meeting{UserID: "owner", MeetingID: "m"}, Checks: []model.SummaryCheck{{PK: "USER#owner", SK: "MEETING#m", Exists: true}}}
	att := &model.Attachment{AttachmentID: "a", MeetingID: "m", UserID: "owner", OriginalKey: "files/owner/m/old.pdf", Type: "document", Status: "done"}
	if err := repo.BindSummaryAttachment(context.Background(), snapshot, att); !errors.Is(err, ErrConditionFailed) || len(snapshot.Checks) != 1 {
		t.Fatal("old evidence rebound", err)
	}
}
