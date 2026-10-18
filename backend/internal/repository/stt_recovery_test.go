package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

type sttWireCall struct {
	op   string
	body map[string]any
}

// sttS3Listing is the S3 ListObjectsV2 body sttWire serves (empty by default).
var sttS3Listing = `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`

func sttWire(t *testing.T, respond func(op string, body map[string]any) (int, string)) (*DynamoDBRepository, *[]sttWireCall) {
	t.Helper()
	calls := &[]sttWireCall{}
	return summarySDKRepo(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "fixture.invalid" {
			*calls = append(*calls, sttWireCall{op: "S3:" + req.Method + ":" + req.URL.Query().Get("prefix")})
			return summaryHTTPResponse(200, sttS3Listing, http.Header{"Content-Type": {"application/xml"}}), nil
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		op := strings.TrimPrefix(req.Header.Get("X-Amz-Target"), "DynamoDB_20120810.")
		*calls = append(*calls, sttWireCall{op, body})
		status, out := respond(op, body)
		return summaryHTTPResponse(status, out, nil), nil
	}), calls
}

func wireNames(body map[string]any) map[string]bool {
	names := map[string]bool{}
	raw, _ := body["ExpressionAttributeNames"].(map[string]any)
	for _, name := range raw {
		names[name.(string)] = true
	}
	return names
}

// wireSetValue resolves "#alias = :value" in an UpdateExpression to its string value.
func wireSetValue(body map[string]any, field string) string {
	names, _ := body["ExpressionAttributeNames"].(map[string]any)
	values, _ := body["ExpressionAttributeValues"].(map[string]any)
	update, _ := body["UpdateExpression"].(string)
	for alias, name := range names {
		if name != field {
			continue
		}
		idx := strings.Index(update, alias+" = ")
		if idx < 0 {
			continue
		}
		rest := update[idx+len(alias)+3:]
		end := strings.IndexAny(rest, ", \n")
		if end < 0 {
			end = len(rest)
		}
		value, _ := values[rest[:end]].(map[string]any)
		if s, ok := value["S"].(string); ok {
			return s
		}
		if n, ok := value["N"].(string); ok {
			return n
		}
	}
	return ""
}

func sttClaimRow() model.SttRecoveryRow {
	return model.SttRecoveryRow{
		MeetingID: "m", UserID: "owner", Status: model.StatusError, UpdatedAt: "2026-10-17T00:00:00.123Z",
		AudioKey: "audio/owner/m/a.webm", SttRecoveryAttempts: 1, SttRecoveryAudioKey: "audio/owner/m/a.webm",
		Present: map[string]bool{"updatedAt": true, "audioKey": true, "status": true, "sttRecoveryAttempts": true, "sttRecoveryAudioKey": true},
	}
}

func TestClaimSttRecoveryPinsTheObservedRow(t *testing.T) {
	now := time.Date(2026, 10, 17, 1, 2, 3, 4000, time.UTC)
	repo, calls := sttWire(t, func(op string, _ map[string]any) (int, string) {
		if op != "UpdateItem" {
			t.Fatalf("unexpected %s", op)
		}
		return 200, `{}`
	})
	claim, err := repo.ClaimSttRecovery(context.Background(), sttClaimRow(), "audio/owner/m/a.webm", 2, now)
	if err != nil || claim != now.Format(time.RFC3339Nano) {
		t.Fatalf("claim=%q err=%v", claim, err)
	}
	body := (*calls)[0].body
	names := wireNames(body)
	for _, field := range []string{"status", "updatedAt", "audioKey", "audioKeys", "audioPartCount", "audioCrop", "transcriptA", "transcriptB",
		"transcriptSegments", "content", "summarizeRetryClaimedAt", "summaryRetryPending", "summaryConflictCode", "sttFailureCode",
		"sttRecoveryAttempts", "sttRecoveryAudioKey", "sttProvider"} {
		if !names[field] {
			t.Fatalf("claim condition does not pin %s: %v", field, body["ConditionExpression"])
		}
	}
	cond := body["ConditionExpression"].(string)
	if !strings.Contains(cond, "attribute_not_exists") || !strings.Contains(cond, "size") {
		t.Fatalf("claim condition lacks absence/size guards: %s", cond)
	}
	if got := wireSetValue(body, "updatedAt"); got != claim {
		t.Fatalf("updatedAt=%q, want the claim", got)
	}
	if got := wireSetValue(body, "sttRecoveryClaimedAt"); got != claim {
		t.Fatalf("sttRecoveryClaimedAt=%q", got)
	}
	if got := wireSetValue(body, "sttRecoveryAttempts"); got != "2" {
		t.Fatalf("attempt=%q", got)
	}
	if update := body["UpdateExpression"].(string); !strings.Contains(update, "REMOVE") {
		t.Fatalf("claim must clear the retryable failure mark: %s", update)
	}
}

func TestClaimSttRecoveryFailures(t *testing.T) {
	now := time.Now().UTC()
	repo, calls := sttWire(t, func(string, map[string]any) (int, string) {
		return 400, `{"__type":"ConditionalCheckFailedException"}`
	})
	if _, err := repo.ClaimSttRecovery(context.Background(), sttClaimRow(), "audio/owner/m/a.webm", 2, now); !errors.Is(err, ErrConditionFailed) {
		t.Fatalf("CCF must map to ErrConditionFailed: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatal("a rejected claim must not be retried")
	}
	terminal := sttClaimRow()
	terminal.SttFailureCode = model.SttFailureAudioCorrupt
	if _, err := repo.ClaimSttRecovery(context.Background(), terminal, terminal.AudioKey, 1, now); !errors.Is(err, ErrConditionFailed) || len(*calls) != 1 {
		t.Fatal("a terminal mark must never be claimed")
	}
	if _, err := repo.ClaimSttRecovery(context.Background(), sttClaimRow(), "audio/owner/m/other.webm", 1, now); !errors.Is(err, ErrConditionFailed) || len(*calls) != 1 {
		t.Fatal("a key other than the bound audioKey must never be claimed")
	}

	// An ambiguous failure is never resent; readback decides.
	claim := now.Format(time.RFC3339Nano)
	for _, tc := range []struct {
		stored string
		ok     bool
	}{{claim, true}, {"someone-else", false}} {
		updates := 0
		repo, _ := sttWire(t, func(op string, _ map[string]any) (int, string) {
			if op == "UpdateItem" {
				updates++
				return 500, `{"__type":"InternalServerError"}`
			}
			return 200, `{"Item":{"sttRecoveryClaimedAt":{"S":"` + tc.stored + `"}}}`
		})
		got, err := repo.ClaimSttRecovery(context.Background(), sttClaimRow(), "audio/owner/m/a.webm", 2, now)
		if updates != 1 || (err == nil) != tc.ok || (tc.ok && got != claim) {
			t.Fatalf("stored=%s updates=%d got=%q err=%v", tc.stored, updates, got, err)
		}
	}
}

func TestClaimSttSummaryRedrivePinsItsCounter(t *testing.T) {
	repo, calls := sttWire(t, func(string, map[string]any) (int, string) { return 200, `{}` })
	if _, err := repo.ClaimSttSummaryRedrive(context.Background(), sttClaimRow(), 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	body := (*calls)[0].body
	if !wireNames(body)["sttSummaryRedriveAttempts"] || wireSetValue(body, "status") != model.StatusTranscribing {
		t.Fatalf("summary claim: %v", body)
	}
	if !strings.Contains(body["ConditionExpression"].(string), "attribute_not_exists") {
		t.Fatal("an absent counter must be pinned as absent")
	}
}

func TestMarkSttFailureWritesOneTimestamp(t *testing.T) {
	repo, calls := sttWire(t, func(string, map[string]any) (int, string) { return 200, `{}` })
	if err := repo.MarkSttFailureForClaim(context.Background(), "owner", "m", model.SttFailureRunTaskFailed, "2026-10-17T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	body := (*calls)[0].body
	failed, updated := wireSetValue(body, "sttFailedAt"), wireSetValue(body, "updatedAt")
	if failed == "" || failed != updated {
		t.Fatalf("sttFailedAt=%q updatedAt=%q must be identical", failed, updated)
	}
	if wireSetValue(body, "status") != model.StatusError || wireSetValue(body, "sttFailureCode") != model.SttFailureRunTaskFailed {
		t.Fatal("mark must set status=error and the code")
	}
	if names := wireNames(body); !names["sttRecoveryClaimedAt"] {
		t.Fatal("a release must be fenced on its own claim")
	}
	if _, err := time.Parse(time.RFC3339Nano, failed); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkSttFailureForClaim(context.Background(), "owner", "m", model.SttFailureRunTaskFailed, ""); !errors.Is(err, ErrConditionFailed) {
		t.Fatal("an empty claim must never mark")
	}

	snapshot := &model.SummarySnapshot{Meeting: &model.Meeting{UserID: "owner", MeetingID: "m"}, Checks: []model.SummaryCheck{{
		PK: "USER#owner", SK: "MEETING#m", Exists: true, Fields: map[string]model.SummaryValue{"status": {Present: true, Value: "transcribing"}},
	}}}
	if err := repo.MarkSttNoSpeech(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	body = (*calls)[len(*calls)-1].body
	if wireSetValue(body, "sttFailureCode") != model.SttFailureNoSpeech || !wireNames(body)["status"] {
		t.Fatalf("no-speech mark: %v", body)
	}
	for _, field := range []string{"transcriptA", "content"} {
		if wireSetValue(body, field) != "" {
			t.Fatalf("no-speech mark must not publish %s", field)
		}
	}
}

func TestScanSttRecoveryRowsProjectsNoContent(t *testing.T) {
	repo, calls := sttWire(t, func(string, map[string]any) (int, string) {
		return 200, `{"Items":[{"meetingId":{"S":"m"},"userId":{"S":"u"},"status":{"S":"error"},"updatedAt":{"S":"t"},"sttRecoveryAttempts":{"N":"1"}}]}`
	})
	rows, err := repo.ScanSttRecoveryRows(context.Background())
	if err != nil || len(rows) != 1 || rows[0].SttRecoveryAttempts != 1 || !rows[0].Present["sttRecoveryAttempts"] || rows[0].Present["audioKey"] {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	body := (*calls)[0].body
	projected := map[string]bool{}
	names, _ := body["ExpressionAttributeNames"].(map[string]any)
	for _, part := range strings.Split(body["ProjectionExpression"].(string), ",") {
		projected[names[strings.TrimSpace(part)].(string)] = true
	}
	for _, private := range []string{"notes", "content", "transcriptA", "transcriptB", "transcriptSegments", "title", "liveSummary", "actionItems"} {
		if projected[private] {
			t.Fatalf("scan projects private field %s", private)
		}
	}
	if !projected["sttFailureCode"] || !projected["audioKey"] {
		t.Fatalf("scan projection incomplete: %v", projected)
	}
	if !strings.Contains(body["FilterExpression"].(string), "IN") {
		t.Fatal("scan must filter by status")
	}
}

func sttDeleteMark() model.SttFailureMark {
	return model.SttFailureMark{Code: model.SttFailureAudioCorrupt, FailedAt: "2026-10-01T00:00:00Z"}
}

func TestDeleteSttFailedMeetingBlocksRelatedRows(t *testing.T) {
	for _, sk := range []string{"ATTACH#a", "ATTEXT#a", "SHARE_TO#u2", "SIMRUN", "SOMETHING_NEW#x"} {
		t.Run(sk, func(t *testing.T) {
			repo, calls := sttWire(t, func(op string, body map[string]any) (int, string) {
				if op == "TransactWriteItems" {
					t.Fatal("a blocked meeting must not be deleted")
				}
				if body["ConsistentRead"] != true {
					t.Fatal("child enumeration must be consistent")
				}
				return 200, `{"Items":[{"SK":{"S":"ANALYSIS#summary"}},{"SK":{"S":"` + sk + `"}}]}`
			})
			if err := repo.DeleteSttFailedMeeting(context.Background(), "owner", "m", sttDeleteMark()); !errors.Is(err, ErrSttDeletionBlocked) {
				t.Fatalf("err=%v", err)
			}
			if len(*calls) != 1 {
				t.Fatalf("calls=%d", len(*calls))
			}
		})
	}
}

func TestDeleteSttFailedMeetingKeepsCheckpointRecordings(t *testing.T) {
	original := sttS3Listing
	t.Cleanup(func() { sttS3Listing = original })
	sttS3Listing = `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><IsTruncated>false</IsTruncated>` +
		`<Contents><Key>audio/owner/m/recording_progress.webm</Key><Size>5000</Size></Contents></ListBucketResult>`
	repo, calls := sttWire(t, func(op string, _ map[string]any) (int, string) {
		if op == "TransactWriteItems" {
			t.Fatal("a meeting with a server checkpoint must not be deleted")
		}
		return 200, `{"Items":[]}`
	})
	if err := repo.DeleteSttFailedMeeting(context.Background(), "owner", "m", sttDeleteMark()); !errors.Is(err, ErrSttDeletionBlocked) {
		t.Fatalf("err=%v", err)
	}
	if got := (*calls)[len(*calls)-1].op; got != "S3:GET:audio/owner/m/" {
		t.Fatalf("last call %s", got)
	}
}

func TestDeleteSttFailedMeetingIsConditionalOnTheMark(t *testing.T) {
	var transact map[string]any
	repo, _ := sttWire(t, func(op string, body map[string]any) (int, string) {
		switch op {
		case "Query":
			return 200, `{"Items":[{"SK":{"S":"ANALYSIS#actionItems"}}]}`
		case "TransactWriteItems":
			transact = body
			return 400, `{"__type":"TransactionCanceledException","message":"cancelled","CancellationReasons":[{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"},{"Code":"None"}]}`
		}
		t.Fatalf("unexpected %s", op)
		return 0, ""
	})
	if err := repo.DeleteSttFailedMeeting(context.Background(), "owner", "m", sttDeleteMark()); !errors.Is(err, ErrConditionFailed) {
		t.Fatalf("err=%v", err)
	}
	items := transact["TransactItems"].([]any)
	meeting := items[0].(map[string]any)["Delete"].(map[string]any)
	names := wireNames(meeting)
	for _, field := range []string{"status", "sttFailureCode", "sttFailedAt", "updatedAt", "notes", "content", "transcriptA", "transcriptB",
		"transcriptSegments", "liveSummary", "actionItems", "notionPageId", "accountId", "projectIds", "linkedMeetingIds",
		"participants", "speakerMap", "sharedToAccount", "diarizationSpeakerHint", "audioCrop", "summarizeRetryClaimedAt"} {
		if !names[field] {
			t.Fatalf("meeting delete condition omits %s", field)
		}
	}
	for _, item := range items[1:] {
		if _, conditioned := item.(map[string]any)["Delete"].(map[string]any)["ConditionExpression"]; conditioned {
			t.Fatal("only the meeting row carries the mark condition")
		}
	}
	if err := repo.DeleteSttFailedMeeting(context.Background(), "owner", "m", model.SttFailureMark{Code: model.SttFailureWorkerFailed, FailedAt: "x"}); !errors.Is(err, ErrConditionFailed) {
		t.Fatal("non-deletable codes must be refused before any call")
	}
}

func sttDeletedKeys(body map[string]any) []string {
	var keys []string
	for _, item := range body["TransactItems"].([]any) {
		key := item.(map[string]any)["Delete"].(map[string]any)["Key"].(map[string]any)
		keys = append(keys, key["PK"].(map[string]any)["S"].(string)+"|"+key["SK"].(map[string]any)["S"].(string))
	}
	return keys
}

// Drift guard: the guarded deletion removes exactly DeleteMeeting's singleton set.
func TestDeleteSttFailedMeetingMatchesDeleteMeetingSingletons(t *testing.T) {
	capture := func(run func(*DynamoDBRepository) error) []string {
		var keys []string
		repo, _ := sttWire(t, func(op string, body map[string]any) (int, string) {
			if op == "TransactWriteItems" {
				keys = append(keys, sttDeletedKeys(body)...)
			}
			if op == "Query" {
				return 200, `{"Items":[]}`
			}
			return 200, `{}`
		})
		if err := run(repo); err != nil {
			t.Fatal(err)
		}
		sort.Strings(keys)
		return keys
	}
	base := capture(func(r *DynamoDBRepository) error { return r.DeleteMeeting(context.Background(), "owner", "m") })
	guarded := capture(func(r *DynamoDBRepository) error {
		return r.DeleteSttFailedMeeting(context.Background(), "owner", "m", sttDeleteMark())
	})
	if strings.Join(base, ",") != strings.Join(guarded, ",") || len(base) != 4 {
		t.Fatalf("singleton drift:\nDeleteMeeting=%v\nguarded=%v", base, guarded)
	}
}

func TestIsSttMeetingTranscriptKey(t *testing.T) {
	const id = "0b8f8a8e-1234-4abc-9def-0123456789ab"
	for key, want := range map[string]bool{
		"transcripts/" + id + ".json":                  true,
		"transcripts/" + id + "-nova.json":             true,
		"transcripts/" + id + "_part_002.json":         true,
		"transcripts/" + id + "/transcriptA.txt":       true,
		"transcripts/" + id + "x.json":                 false,
		"transcripts/" + id + "_part_2.json":           false,
		"transcripts/" + id + "/../other/a.txt":        false,
		"transcripts/" + id + "/":                      false,
		"transcripts/other-" + id + ".json":            false,
		"transcripts/" + id + "-nova.json.bak":         false,
		"transcripts/" + id + ".json/../../x":          false,
		"images/u/" + id + "/transcripts/" + id + ".x": false,
	} {
		if got := IsSttMeetingTranscriptKey(id, key); got != want {
			t.Fatalf("%s: %v, want %v", key, got, want)
		}
	}
}
