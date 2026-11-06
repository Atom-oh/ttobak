package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// memberWire serves the member row when present and captures the
// transaction; cancelled simulates a concurrent unpublish.
func memberWire(member, cancelled bool, items *[]any) *DynamoDBRepository {
	return summarySDKRepo(func(req *http.Request) (*http.Response, error) {
		var body map[string]any
		json.NewDecoder(req.Body).Decode(&body)
		target := req.Header.Get("X-Amz-Target")
		switch {
		case strings.HasSuffix(target, ".GetItem") && member:
			return summaryHTTPResponse(200, `{"Item":{"PK":{"S":"ACCOUNT#acc"},"SK":{"S":"MEMBER#member"},"accountId":{"S":"acc"},"userId":{"S":"member"}}}`, nil), nil
		case strings.HasSuffix(target, ".TransactWriteItems"):
			*items = body["TransactItems"].([]any)
			if cancelled {
				return summaryHTTPResponse(400, `{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException","message":"cancelled","CancellationReasons":[{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"}]}`, nil), nil
			}
		}
		return summaryHTTPResponse(200, `{}`, nil), nil
	})
}

func transactKeys(items []any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, op := range items {
		for kind, raw := range op.(map[string]any) {
			body := raw.(map[string]any)
			key := body["Key"].(map[string]any)
			out[kind+" "+key["PK"].(map[string]any)["S"].(string)+"|"+key["SK"].(map[string]any)["S"].(string)] = body
		}
	}
	return out
}

func TestManagedRediarizeWriteFencesPublicationAndGrant(t *testing.T) {
	t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", "")
	t.Setenv(AccountDescendantReadsEnv, "")
	for _, tc := range []struct {
		name              string
		member, cancelled bool
		err               error
	}{{"success", true, false, nil}, {"removed member", false, false, ErrSummaryAccess}, {"unpublished concurrently", true, true, ErrConditionFailed}} {
		t.Run(tc.name, func(t *testing.T) {
			var items []any
			repo := memberWire(tc.member, tc.cancelled, &items)
			err := repo.UpdateManagedMeetingFieldsIfMatch(context.Background(), "owner", "member", "meeting", "acc",
				map[string]interface{}{"status": "done"}, map[string]interface{}{"status": "transcribing"})
			if !errors.Is(err, tc.err) {
				t.Fatalf("got %v, want %v", err, tc.err)
			}
			if !tc.member {
				if items != nil {
					t.Fatal("removed member reached a write")
				}
				return
			}
			keys := transactKeys(items)
			update := keys["Update USER#owner|MEETING#meeting"]
			if update == nil || keys["ConditionCheck ACCOUNT#acc|MEMBER#member"] == nil {
				t.Fatalf("want the creator's meeting update and the member grant check: %v", keys)
			}
			fenced := conditionNames(t, update)
			if !fenced["accountId"] || !fenced["sharedToAccount"] || !fenced["status"] {
				t.Fatalf("rediarize write not fenced: %v", fenced)
			}
		})
	}
}

func TestMemberProjectUnlinkFencesPublicationProjectAndGrant(t *testing.T) {
	t.Setenv(AccountDescendantReadsEnv, "")
	for _, tc := range []struct {
		name              string
		member, cancelled bool
		err               error
	}{{"success", true, false, nil}, {"removed member", false, false, ErrSummaryAccess}, {"unpublished concurrently", true, true, ErrConditionFailed}} {
		t.Run(tc.name, func(t *testing.T) {
			var items []any
			repo := memberWire(tc.member, tc.cancelled, &items)
			err := repo.MeetingProjectUnlinkInAccount(context.Background(), "owner", "member", "meeting", "project", "acc", "MEETINGREF#x")
			if !errors.Is(err, tc.err) {
				t.Fatalf("got %v, want %v", err, tc.err)
			}
			if !tc.member {
				if items != nil {
					t.Fatal("removed member reached a write")
				}
				return
			}
			keys := transactKeys(items)
			update := keys["Update USER#owner|MEETING#meeting"]
			project := keys["ConditionCheck PROJECT#project|CONFIG"]
			if update == nil || keys["Delete PROJECT#project|MEETINGREF#x"] == nil || keys["ConditionCheck ACCOUNT#acc|MEMBER#member"] == nil {
				t.Fatalf("missing unlink items: %v", keys)
			}
			if fenced := conditionNames(t, update); !fenced["accountId"] || !fenced["sharedToAccount"] {
				t.Fatalf("meeting publication not fenced: %v", fenced)
			}
			if project == nil || !strings.Contains(strings.ReplaceAll(project["ConditionExpression"].(string), " ", ""), "contains(") {
				t.Fatalf("project account link not fenced: %v", project)
			}
		})
	}
}
