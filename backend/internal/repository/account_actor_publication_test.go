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
