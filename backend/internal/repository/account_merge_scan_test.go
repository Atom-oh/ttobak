package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const (
	scanTaskRow = `{"PK":{"S":"USER#u1"},"SK":{"S":"SRTASK#e1"},"userId":{"S":"u1"},"accountId":{"S":"src"},"eventId":{"S":"e1"},"version":{"N":"3"}}`
	scanCalRow  = `{"PK":{"S":"USER#u1"},"SK":{"S":"CALEVENT#s1#e2"},"userId":{"S":"u1"},"accountId":{"S":"src"},"sourceId":{"S":"s1"},"eventId":{"S":"e2"},"ciphertext":{"S":"c2"},"revision":{"N":"7"},"shareAccountId":{"S":"src"}}`
	scanMeetRow = `{"PK":{"S":"USER#u1"},"SK":{"S":"MEETING#m1"},"userId":{"S":"u1"},"accountId":{"S":"src"},"meetingId":{"S":"m1"},"title":{"S":"Weekly"},"date":{"S":"2026-11-01T00:00:00Z"}}`
)

func TestScanMergeUserRowsRewritesOnlyA(t *testing.T) {
	ctx := context.Background()
	var calls []fenceRequest
	repo := fenceRepo(t, &calls, func(call fenceRequest) (int, string) {
		if call.target == "Scan" {
			return 200, `{"Items":[` + scanTaskRow + `,` + scanCalRow + `,` + scanMeetRow + `,` +
				`{"PK":{"S":"USER#u1"},"SK":{"S":"MEETING#bad#id"},"userId":{"S":"u1"},"accountId":{"S":"src"},"meetingId":{"S":"bad"}},` +
				`{"PK":{"S":"USER#u1"},"SK":{"S":"MEETING#m9"},"userId":{"S":"u1"},"accountId":{"S":"other"},"meetingId":{"S":"m9"}}],` +
				`"LastEvaluatedKey":{"PK":{"S":"USER#u1"},"SK":{"S":"MEETING#m1"}}}`
		}
		return 200, `{}`
	})

	page, err := repo.ScanMergeUserRowsSegmentPage(ctx, "src", 1, 4, "")
	if err != nil {
		t.Fatal(err)
	}
	scan := calls[0]
	if scan.body["Segment"] != float64(1) || scan.body["TotalSegments"] != float64(4) || scan.body["ConsistentRead"] != true {
		t.Fatalf("scan is not a bounded strongly consistent segment: %v", scan.body)
	}
	filter := resolvedExpression(scan.body, "FilterExpression")
	for _, want := range []string{`accountId = "src"`, `begins_with (PK, "USER#")`, `begins_with (SK, "SRTASK#")`, `begins_with (SK, "CALEVENT#")`, `begins_with (SK, "MEETING#")`} {
		if !strings.Contains(filter, want) {
			t.Errorf("filter %q missing %q", filter, want)
		}
	}
	// A row on another account and a row with a malformed sort key are never returned.
	if len(page.Rows) != 3 || page.Invalid != 2 || page.NextCursor == "" {
		t.Fatalf("rows=%d invalid=%d next=%q", len(page.Rows), page.Invalid, page.NextCursor)
	}
	if got := page.Rows[0]; got.Kind != MergeUserRowSpecReqTask || got.UserID != "u1" || got.EventID != "e1" || got.Version != 3 {
		t.Errorf("task row %+v", got)
	}
	if got := page.Rows[1]; got.Kind != MergeUserRowCalendar || got.SourceID != "s1" || got.EventID != "e2" || got.Revision != 7 || got.ShareAccountID != "src" {
		t.Errorf("calendar row %+v", got)
	}
	if got := page.Rows[2]; got.Kind != MergeUserRowMeeting || got.MeetingID != "m1" || got.SharedToAccount {
		t.Errorf("meeting row %+v", got)
	}
	if ref := MeetingRefForMergeRow("src", page.Rows[2]); ref.SK != "MEETINGREF#2026-11-01T00:00:00Z#m1" || ref.PK != "ACCOUNT#src" || ref.OwnerUserID != "u1" {
		t.Errorf("meeting ref %+v", ref)
	}

	if _, err := repo.ScanMergeUserRowsPage(ctx, "src", "malformed"); err == nil || len(calls) != 1 {
		t.Fatal("malformed cursor reached storage")
	}
	if _, err := repo.ScanMergeUserRowsSegmentPage(ctx, "src", 4, 4, ""); err == nil || len(calls) != 1 {
		t.Fatal("out of range segment reached storage")
	}

	// Task rewrite: A to B, conditioned on A and the observed version.
	calls = calls[:0]
	if err := repo.MoveSpecReqTaskRowForMerge(ctx, "src", "tgt", page.Rows[0]); err != nil {
		t.Fatal(err)
	}
	update := calls[0]
	if update.target != "UpdateItem" || itemKey(update.body) != "USER#u1|SRTASK#e1" {
		t.Fatalf("task update %s %s", update.target, itemKey(update.body))
	}
	if set := resolvedExpression(update.body, "UpdateExpression"); !strings.Contains(set, `accountId = "tgt"`) || !strings.Contains(set, "ADD version 1") {
		t.Errorf("task update %q", set)
	}
	if cond := resolvedExpression(update.body, "ConditionExpression"); !strings.Contains(cond, `accountId = "src"`) || !strings.Contains(cond, "version = 3") {
		t.Errorf("task condition %q", cond)
	}

	// Event rewrite: accountId, shareAccountId (it names A) and the payload move together.
	calls = calls[:0]
	if err := repo.MoveCalendarEventRowForMerge(ctx, "src", "tgt", page.Rows[1], "c2-new"); err != nil {
		t.Fatal(err)
	}
	set := resolvedExpression(calls[0].body, "UpdateExpression")
	for _, want := range []string{`accountId = "tgt"`, `shareAccountId = "tgt"`, `ciphertext = "c2-new"`} {
		if !strings.Contains(set, want) {
			t.Errorf("event update %q missing %q", set, want)
		}
	}
	cond := resolvedExpression(calls[0].body, "ConditionExpression")
	for _, want := range []string{`accountId = "src"`, "revision = 7", `ciphertext = "c2"`, `shareAccountId = "src"`} {
		if !strings.Contains(cond, want) {
			t.Errorf("event condition %q missing %q", cond, want)
		}
	}

	// A share account other than A is neither rewritten nor ignored.
	calls = calls[:0]
	other := page.Rows[1]
	other.ShareAccountID = "elsewhere"
	if err := repo.MoveCalendarEventRowForMerge(ctx, "src", "tgt", other, "c2-new"); err != nil {
		t.Fatal(err)
	}
	if set := resolvedExpression(calls[0].body, "UpdateExpression"); strings.Contains(set, "shareAccountId") {
		t.Errorf("foreign share account rewritten: %q", set)
	}
	if cond := resolvedExpression(calls[0].body, "ConditionExpression"); !strings.Contains(cond, `shareAccountId = "elsewhere"`) {
		t.Errorf("foreign share account not pinned: %q", cond)
	}
}

func TestMoveMergeUserRowConditionFailure(t *testing.T) {
	repo := fenceRepo(t, new([]fenceRequest), func(fenceRequest) (int, string) {
		return 400, `{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","message":"x"}`
	})
	row := MergeUserRow{Kind: MergeUserRowSpecReqTask, PK: "USER#u1", SK: "SRTASK#e1", UserID: "u1", EventID: "e1", Version: 3}
	if err := repo.MoveSpecReqTaskRowForMerge(context.Background(), "src", "tgt", row); !errors.Is(err, ErrConditionFailed) {
		t.Fatalf("got %v, want ErrConditionFailed", err)
	}
	if err := repo.MoveSpecReqTaskRowForMerge(context.Background(), "src", "src", row); err == nil || errors.Is(err, ErrConditionFailed) {
		t.Fatalf("same account must be rejected before storage: %v", err)
	}
}
