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

// A managing member's direct share or invite is fenced on the member's live
// grant and the meeting's publication in the same transaction.
func TestMemberSharesFenceTheActorsLiveGrant(t *testing.T) {
	t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", "")
	published := `{"PK":{"S":"USER#owner"},"SK":{"S":"MEETING#meeting"},"meetingId":{"S":"meeting"},"userId":{"S":"owner"},"accountId":{"S":"child"},"sharedToAccount":{"BOOL":true}}`
	unpublished := `{"PK":{"S":"USER#owner"},"SK":{"S":"MEETING#meeting"},"meetingId":{"S":"meeting"},"userId":{"S":"owner"},"accountId":{"S":"child"}}`
	member := func(account string) string {
		return `{"PK":{"S":"ACCOUNT#` + account + `"},"SK":{"S":"MEMBER#member"},"accountId":{"S":"` + account + `"},"userId":{"S":"member"}}`
	}
	meta := `{"PK":{"S":"ACCOUNT#child"},"SK":{"S":"META"},"accountId":{"S":"child"},"parentAccountId":{"S":"parent"}}`
	for _, tc := range []struct {
		name        string
		descendants bool
		rows        map[string]string
		want        []string
		err         error
	}{
		{name: "direct member", rows: map[string]string{"USER#owner|MEETING#meeting": published, "ACCOUNT#child|MEMBER#member": member("child")},
			want: []string{"USER#owner|MEETING#meeting", "ACCOUNT#child|MEMBER#member"}},
		{name: "inherited member", descendants: true, rows: map[string]string{"USER#owner|MEETING#meeting": published,
			"ACCOUNT#child|META": meta, "ACCOUNT#parent|MEMBER#member": member("parent")},
			want: []string{"USER#owner|MEETING#meeting", "ACCOUNT#child|META", "ACCOUNT#parent|MEMBER#member"}},
		{name: "former member", rows: map[string]string{"USER#owner|MEETING#meeting": published}, err: ErrSummaryAccess},
		{name: "unpublished meeting", rows: map[string]string{"USER#owner|MEETING#meeting": unpublished,
			"ACCOUNT#child|MEMBER#member": member("child")}, err: ErrSummaryAccess},
	} {
		for _, path := range []string{"direct", "pending"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				if tc.descendants {
					t.Setenv(AccountDescendantReadsEnv, "1")
				} else {
					t.Setenv(AccountDescendantReadsEnv, "")
				}
				var items []any
				repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
					var body map[string]any
					json.NewDecoder(req.Body).Decode(&body)
					target := req.Header.Get("X-Amz-Target")
					if strings.HasSuffix(target, ".TransactWriteItems") {
						items = body["TransactItems"].([]any)
						return summaryHTTPResponse(200, `{}`, nil), nil
					}
					if strings.HasSuffix(target, ".GetItem") {
						key := body["Key"].(map[string]any)
						k := key["PK"].(map[string]any)["S"].(string) + "|" + key["SK"].(map[string]any)["S"].(string)
						if row, ok := tc.rows[k]; ok {
							return summaryHTTPResponse(200, `{"Item":`+row+`}`, nil), nil
						}
					}
					return summaryHTTPResponse(200, `{}`, nil), nil
				})
				var err error
				if path == "direct" {
					_, err = repo.CreateShareBy(context.Background(), "meeting", "owner", "owner@example.com", "guest", "guest@example.com",
						model.PermissionRead, "member", "member@example.com")
				} else {
					err = repo.PutPendingShare(context.Background(), &model.PendingShare{Kind: model.PendingShareKindMeeting, MeetingID: "meeting",
						Email: "guest@example.com", InvitedByUserID: "owner", SharedByUserID: "member", SharedByEmail: "member@example.com"})
				}
				if tc.err != nil {
					if !errors.Is(err, tc.err) || items != nil {
						t.Fatalf("want %v without a write, got %v (items=%d)", tc.err, err, len(items))
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				checks := map[string]bool{}
				for _, op := range items {
					entry := op.(map[string]any)
					if put, ok := entry["Put"].(map[string]any); ok && path == "direct" {
						item := put["Item"].(map[string]any)
						if item["ownerId"].(map[string]any)["S"] != "owner" || item["sharedByUserId"].(map[string]any)["S"] != "member" {
							t.Fatalf("share row: %+v", item)
						}
					}
					if check, ok := entry["ConditionCheck"].(map[string]any); ok {
						key := check["Key"].(map[string]any)
						checks[key["PK"].(map[string]any)["S"].(string)+"|"+key["SK"].(map[string]any)["S"].(string)] = true
					}
				}
				for _, want := range tc.want {
					if !checks[want] {
						t.Fatalf("missing fence %s in %v", want, checks)
					}
				}
			})
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

// The actor's membership pin does not depend on the privacy writer switch.
func TestActorAccountWritesPinMembershipWithPrivacyWritersOff(t *testing.T) {
	t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", "")
	memberRow := `{"PK":{"S":"ACCOUNT#account"},"SK":{"S":"MEMBER#member"},"accountId":{"S":"account"},"userId":{"S":"member"}}`
	for _, tc := range []struct {
		name, actor string
		member      bool
		wantTxn     bool
		err         error
	}{
		{"member", "member", true, true, nil},
		{"former member", "member", false, false, ErrSummaryAccess},
		{"creator", "owner", false, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var targets []string
			var check map[string]any
			repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
				var body map[string]any
				json.NewDecoder(req.Body).Decode(&body)
				target := req.Header.Get("X-Amz-Target")
				targets = append(targets, target[strings.LastIndex(target, ".")+1:])
				switch {
				case strings.HasSuffix(target, ".GetItem") && tc.member:
					return summaryHTTPResponse(200, `{"Item":`+memberRow+`}`, nil), nil
				case strings.HasSuffix(target, ".TransactWriteItems"):
					check = body["TransactItems"].([]any)[0].(map[string]any)["ConditionCheck"].(map[string]any)
				}
				return summaryHTTPResponse(200, `{}`, nil), nil
			})
			err := repo.LinkMeetingToAccountAs(context.Background(), "owner", tc.actor, "meeting", "account")
			if !errors.Is(err, tc.err) {
				t.Fatalf("got %v, want %v (calls %v)", err, tc.err, targets)
			}
			if tc.wantTxn {
				if check == nil || check["Key"].(map[string]any)["SK"].(map[string]any)["S"] != "MEMBER#member" {
					t.Fatalf("member write was not fenced on MEMBER#member: %v", targets)
				}
			} else if check != nil {
				t.Fatalf("unexpected transaction: %v", targets)
			}
		})
	}
}

// A member's predecessor links commit with the parent's publication and the
// member's live grant in one transaction.
func TestMemberPredecessorLinksFenceParentPublicationAndGrant(t *testing.T) {
	t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", "")
	t.Setenv(AccountDescendantReadsEnv, "")
	memberRow := `{"PK":{"S":"ACCOUNT#acc"},"SK":{"S":"MEMBER#member"},"accountId":{"S":"acc"},"userId":{"S":"member"}}`
	for _, tc := range []struct {
		name        string
		member      bool
		unpublished bool
		err         error
	}{{"success", true, false, nil}, {"removed member", false, false, ErrSummaryAccess}, {"parent unpublished concurrently", true, true, ErrConditionFailed}} {
		t.Run(tc.name, func(t *testing.T) {
			var items []any
			repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
				var body map[string]any
				json.NewDecoder(req.Body).Decode(&body)
				target := req.Header.Get("X-Amz-Target")
				switch {
				case strings.HasSuffix(target, ".GetItem") && tc.member:
					return summaryHTTPResponse(200, `{"Item":`+memberRow+`}`, nil), nil
				case strings.HasSuffix(target, ".TransactWriteItems"):
					items = body["TransactItems"].([]any)
					if tc.unpublished {
						return summaryHTTPResponse(400, `{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException","message":"cancelled","CancellationReasons":[{"Code":"ConditionalCheckFailed"},{"Code":"None"}]}`, nil), nil
					}
				}
				return summaryHTTPResponse(200, `{}`, nil), nil
			})
			err := repo.LinkPredecessorsAs(context.Background(), "owner", "member", "meeting", "acc", []string{"p-team"})
			if !errors.Is(err, tc.err) {
				t.Fatalf("got %v, want %v", err, tc.err)
			}
			if !tc.member {
				if items != nil {
					t.Fatal("removed member reached a write")
				}
				return
			}
			if len(items) != 2 {
				t.Fatalf("want meeting update and member check, got %d", len(items))
			}
			update := items[0].(map[string]any)["Update"].(map[string]any)
			if update["Key"].(map[string]any)["PK"].(map[string]any)["S"] != "USER#owner" {
				t.Fatal("links must be written on the creator's meeting")
			}
			fenced := conditionNames(t, update)
			if !fenced["accountId"] || !fenced["sharedToAccount"] {
				t.Fatalf("parent publication not fenced: %v", fenced)
			}
			check := items[1].(map[string]any)["ConditionCheck"].(map[string]any)
			if check["Key"].(map[string]any)["SK"].(map[string]any)["S"] != "MEMBER#member" {
				t.Fatal("member grant not fenced")
			}
		})
	}
}

func TestMeetingProjectLinkInAccountFencesMeetingAndProjectAccount(t *testing.T) {
	var items []any
	repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
		var body map[string]any
		json.NewDecoder(req.Body).Decode(&body)
		if strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".TransactWriteItems") {
			items = body["TransactItems"].([]any)
		}
		return summaryHTTPResponse(200, `{}`, nil), nil
	})
	ref := &model.ProjectMeetingRef{PK: model.PrefixProject + "project", SK: "MEETINGREF#x", ProjectID: "project", MeetingID: "meeting", OwnerUserID: "owner"}
	if err := repo.MeetingProjectLinkInAccount(context.Background(), "owner", "meeting", "project", "account", ref); err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("want meeting update, ref put and project check, got %d", len(items))
	}
	update := items[0].(map[string]any)["Update"].(map[string]any)
	names := update["ExpressionAttributeNames"].(map[string]any)
	condition := update["ConditionExpression"].(string)
	for _, field := range []string{"accountId", "sharedToAccount"} {
		found := false
		for token, name := range names {
			if name == field && strings.Contains(condition, token) {
				found = true
			}
		}
		if !found {
			t.Fatalf("meeting link not fenced on %s: %s", field, condition)
		}
	}
	check := items[2].(map[string]any)["ConditionCheck"].(map[string]any)
	if !strings.Contains(strings.ReplaceAll(check["ConditionExpression"].(string), " ", ""), "contains(") {
		t.Fatalf("project account membership not fenced: %v", check["ConditionExpression"])
	}
	if err := repo.MeetingProjectLinkInAccount(context.Background(), "owner", "meeting", "project", "", ref); !errors.Is(err, ErrConditionFailed) {
		t.Fatalf("an empty account must not fall back to the creator path: %v", err)
	}
}
