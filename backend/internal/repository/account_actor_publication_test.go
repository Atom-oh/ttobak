package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/ttobak/backend/internal/model"
)

// A managing member's classification/publication of the creator's meeting
// fences the actor's direct membership, not the creator's (ADR-016
// 2026-11-06 amendment), and still writes the creator's partition.
func TestActorAccountWritesPinActorMembership(t *testing.T) {
	privacyWriterEnabled(t)
	for _, tc := range []struct {
		name  string
		write func(*DynamoDBRepository) error
	}{
		{"publish", func(r *DynamoDBRepository) error {
			return r.PublishMeetingToAccountAs(context.Background(), "owner", "member", "meeting", "account")
		}},
		{"classify", func(r *DynamoDBRepository) error {
			return r.LinkMeetingToAccountAs(context.Background(), "owner", "member", "meeting", "account")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := privacyParent()
			member := &model.AccountMember{AccountID: "account", UserID: "member", AddedAt: time.Now().UTC(), SummaryPrivacyVersion: 1}
			rows := map[string]interface{}{"USER#owner/MEETING#meeting": parent, "ACCOUNT#account/MEMBER#member": member}
			writes := 0
			repo := privacyWriterWire(t, rows, func(target string, body map[string]any) (int, string) {
				writes++
				ops := body["TransactItems"].([]any)
				check := ops[0].(map[string]any)["ConditionCheck"].(map[string]any)
				if key := check["Key"].(map[string]any)["SK"].(map[string]any)["S"]; key != "MEMBER#member" {
					t.Fatalf("membership fence pinned %v, want the actor", key)
				}
				update := ops[1].(map[string]any)["Update"].(map[string]any)
				if pk := update["Key"].(map[string]any)["PK"].(map[string]any)["S"]; pk != "USER#owner" {
					t.Fatalf("write targeted %v, want the creator partition", pk)
				}
				return 200, `{}`
			})
			if err := tc.write(repo); err != nil || writes != 1 {
				t.Fatalf("actor write: %v writes=%d", err, writes)
			}
			// Without the actor's own membership the write is refused even
			// though the creator may belong to the account.
			delete(rows, "ACCOUNT#account/MEMBER#member")
			rows["ACCOUNT#account/MEMBER#owner"] = &model.AccountMember{AccountID: "account", UserID: "owner", AddedAt: time.Now().UTC(), SummaryPrivacyVersion: 1}
			if err := tc.write(repo); !errors.Is(err, ErrSummaryAccess) || writes != 1 {
				t.Fatalf("non-member actor wrote: %v writes=%d", err, writes)
			}
		})
	}
}

func TestCreateShareByRecordsActorOnBothRows(t *testing.T) {
	t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", "")
	var items []any
	repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
		var body map[string]any
		json.NewDecoder(req.Body).Decode(&body)
		if strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".TransactWriteItems") {
			items = body["TransactItems"].([]any)
		}
		return summaryHTTPResponse(200, `{}`, nil), nil
	})
	if _, err := repo.CreateShareBy(context.Background(), "meeting", "owner", "owner@example.com", "guest", "guest@example.com",
		model.PermissionRead, "member", "member@example.com"); err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("want both share rows, got %d", len(items))
	}
	for _, op := range items {
		item := op.(map[string]any)["Put"].(map[string]any)["Item"].(map[string]any)
		if item["ownerId"].(map[string]any)["S"] != "owner" || item["sharedByUserId"].(map[string]any)["S"] != "member" ||
			item["sharedByEmail"].(map[string]any)["S"] != "member@example.com" {
			t.Fatalf("share row: %+v", item)
		}
	}
}

func TestActorAccountConditionPinsCurrentAccountForNonCreators(t *testing.T) {
	for _, tc := range []struct {
		actor  string
		pinned bool
	}{{"owner", false}, {"member", true}} {
		expr, err := expression.NewBuilder().WithCondition(actorAccountCondition("owner", tc.actor, "account")).Build()
		if err != nil {
			t.Fatal(err)
		}
		pinned := false
		for token, name := range expr.Names() {
			if name == "accountId" && strings.Contains(*expr.Condition(), token) {
				pinned = true
			}
		}
		if pinned != tc.pinned {
			t.Fatalf("%s: accountId pinned=%v want %v (%s)", tc.actor, pinned, tc.pinned, *expr.Condition())
		}
	}
}

func updateSetsField(update map[string]any, field string) bool {
	names := update["ExpressionAttributeNames"].(map[string]any)
	expr := update["UpdateExpression"].(string)
	for token, name := range names {
		if name == field && strings.Contains(expr, token+" =") {
			return true
		}
	}
	return false
}

func conditionNames(t *testing.T, update map[string]any) map[string]bool {
	t.Helper()
	names := update["ExpressionAttributeNames"].(map[string]any)
	condition := update["ConditionExpression"].(string)
	found := map[string]bool{}
	for token, name := range names {
		if strings.Contains(condition, token) {
			found[name.(string)] = true
		}
	}
	return found
}

// ADR-059 approval is the creator's alone. A member republish of a meeting
// already published to the same account publishes without approval, while
// the creator's own republish of private output still mints it.
func TestMemberRepublishNeverMintsOwnerApproval(t *testing.T) {
	privacyWriterEnabled(t)
	t.Setenv("SUMMARY_OWNER_ACCOUNT_SHARE_ENABLED", "1")
	for _, actor := range []string{"member", "owner"} {
		t.Run(actor, func(t *testing.T) {
			parent := privacyParent()
			parent.Content, parent.AccountID, parent.SharedToAccount = "private notes", "account", true
			privacyStamp(parent, model.SummaryPrivacyPrivate, "generation")
			rows := privacyParentRows(parent)
			rows["ACCOUNT#account/MEMBER#"+actor] = &model.AccountMember{AccountID: "account", UserID: actor, AddedAt: time.Now().UTC(), SummaryPrivacyVersion: 1}
			var update map[string]any
			repo := privacyWriterWire(t, rows, func(_ string, body map[string]any) (int, string) {
				ops := body["TransactItems"].([]any)
				update = ops[len(ops)-1].(map[string]any)["Update"].(map[string]any)
				return 200, `{}`
			})
			if err := repo.PublishMeetingToAccountAs(context.Background(), "owner", actor, "meeting", "account"); err != nil {
				t.Fatalf("%s republish: %v", actor, err)
			}
			if minted := updateSetsField(update, "summaryAccountApproval"); minted != (actor == "owner") {
				t.Fatalf("%s: approval minted=%v", actor, minted)
			}
			fenced := conditionNames(t, update)
			if actor == "member" && (!fenced["accountId"] || !fenced["sharedToAccount"]) {
				t.Fatalf("member republish must stay within the current publication: %v", fenced)
			}
		})
	}
}

// Without approval a member can never turn unpublished private output into
// an account disclosure, and a concurrent unpublish rejects the member write.
func TestMemberPublicationCannotWidenDisclosure(t *testing.T) {
	privacyWriterEnabled(t)
	t.Setenv("SUMMARY_OWNER_ACCOUNT_SHARE_ENABLED", "1")
	parent := privacyParent()
	parent.Content, parent.AccountID = "private notes", "account"
	privacyStamp(parent, model.SummaryPrivacyPrivate, "generation")
	rows := privacyParentRows(parent)
	rows["ACCOUNT#account/MEMBER#member"] = &model.AccountMember{AccountID: "account", UserID: "member", AddedAt: time.Now().UTC(), SummaryPrivacyVersion: 1}
	repo := privacyWriterWire(t, rows, func(string, map[string]any) (int, string) {
		t.Fatal("unpublished private output reached a member write")
		return 200, `{}`
	})
	if err := repo.PublishMeetingToAccountAs(context.Background(), "owner", "member", "meeting", "account"); !errors.Is(err, ErrSummarySharingUnsafe) {
		t.Fatalf("member publish of unpublished private output: %v", err)
	}

	parent.SharedToAccount = true
	writes := 0
	repo = privacyWriterWire(t, rows, func(string, map[string]any) (int, string) {
		writes++
		return 400, projectCancellation(2, 1)
	})
	if err := repo.PublishMeetingToAccountAs(context.Background(), "owner", "member", "meeting", "account"); !errors.Is(err, ErrConditionFailed) || writes != 1 {
		t.Fatalf("concurrently unpublished meeting accepted a member write: %v writes=%d", err, writes)
	}
}
