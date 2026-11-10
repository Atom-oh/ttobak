package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

const fenceTable = "accounts-test"

type fenceRequest struct {
	target string
	body   map[string]any
}

// fenceRepo answers every DynamoDB call through respond and records requests.
func fenceRepo(t *testing.T, calls *[]fenceRequest, respond func(fenceRequest) (int, string)) *DynamoDBRepository {
	t.Helper()
	client := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			call := fenceRequest{target: strings.TrimPrefix(req.Header.Get("X-Amz-Target"), "DynamoDB_20120810.")}
			if err := json.NewDecoder(req.Body).Decode(&call.body); err != nil {
				t.Fatal(err)
			}
			*calls = append(*calls, call)
			status, body := respond(call)
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
	})
	return NewDynamoDBRepository(client, fenceTable)
}

// transactItems returns the single operation of each captured transaction item.
func fenceItems(t *testing.T, call fenceRequest) []map[string]any {
	t.Helper()
	raw, _ := call.body["TransactItems"].([]any)
	items := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		for _, op := range entry.(map[string]any) {
			items = append(items, op.(map[string]any))
		}
	}
	return items
}

func resolvedCondition(op map[string]any) string {
	cond, _ := op["ConditionExpression"].(string)
	names, _ := op["ExpressionAttributeNames"].(map[string]any)
	for alias, name := range names {
		cond = strings.ReplaceAll(cond, alias, name.(string))
	}
	return cond
}

func metaIndex(t *testing.T, items []map[string]any, accountID string) int {
	t.Helper()
	found := -1
	for i, op := range items {
		key, _ := op["Key"].(map[string]any)
		pk, _ := key["PK"].(map[string]any)
		sk, _ := key["SK"].(map[string]any)
		if pk["S"] == "ACCOUNT#"+accountID && sk["S"] == "META" {
			if found >= 0 {
				t.Fatalf("account %s META touched twice in one transaction", accountID)
			}
			found = i
		}
	}
	if found < 0 {
		t.Fatalf("no META item for account %s in %d items", accountID, len(items))
	}
	return found
}

func cancelledAt(n, idx int, withMarker bool) string {
	reasons := make([]string, n)
	for i := range reasons {
		reasons[i] = `{"Code":"None"}`
	}
	reasons[idx] = `{"Code":"ConditionalCheckFailed"}`
	if withMarker {
		reasons[idx] = `{"Code":"ConditionalCheckFailed","Item":{"PK":{"S":"ACCOUNT#x"},"mergingInto":{"S":"target"}}}`
	}
	return `{"__type":"TransactionCanceledException","CancellationReasons":[` + strings.Join(reasons, ",") + `]}`
}

func TestAccountWritesCarryMergeFence(t *testing.T) {
	ctx := context.Background()
	link := &model.AccountLink{AccountID: "acct", FromKind: "event", FromID: "e1", ToKind: "project", ToID: "p1",
		Origin: "manual", CreatedBy: "u", CreatedAt: "2026-11-09T00:00:00Z"}
	cases := []struct {
		name    string
		account string // account whose META must carry the fence
		plain   error  // sentinel kept when the same item fails without a merge marker (nil: any error)
		call    func(r *DynamoDBRepository) error
	}{
		{"contact", "acct", ErrContactAccess, func(r *DynamoDBRepository) error {
			return r.WriteAccountContact(ctx, "acct", "u", &model.AccountContact{ContactID: "c1", Revision: "rev"}, "delete")
		}},
		{"event", "acct", ErrEventAccess, func(r *DynamoDBRepository) error {
			return r.WriteAccountEvent(ctx, "acct", "u", &model.AccountEvent{EventID: "e1"}, "delete", "", "EVENT#x", "rev")
		}},
		{"link put", "acct", ErrAccountLinkAccess, func(r *DynamoDBRepository) error { return r.PutAccountLink(ctx, link, "u") }},
		{"link delete", "acct", ErrAccountLinkAccess, func(r *DynamoDBRepository) error {
			return r.DeleteAccountLink(ctx, "acct", "u", link.Input(), false)
		}},
		{"member add", "acct", nil, func(r *DynamoDBRepository) error {
			return r.PutMember(ctx, &model.AccountMember{PK: "ACCOUNT#acct", SK: "MEMBER#u2", AccountID: "acct", UserID: "u2", Role: model.RoleSA})
		}},
		{"member remove", "acct", ErrConditionFailed, func(r *DynamoDBRepository) error { return r.DeleteMember(ctx, "acct", "owner", "u2") }},
		{"project link", "acct", ErrConditionFailed, func(r *DynamoDBRepository) error {
			return r.ProjectAccountLinkTransactional(ctx, "p1", "acct", &model.ProjectRef{PK: "ACCOUNT#acct", SK: "PROJECTREF#p1"})
		}},
		{"research link", "acct", nil, func(r *DynamoDBRepository) error {
			return NewResearchRepository(r.client, fenceTable).LinkAccountTransactional(ctx, "r1", "acct", &model.ResearchRef{PK: "ACCOUNT#acct", SK: "RESEARCHREF#r1"})
		}},
		{"reparent child", "acct", ErrConditionFailed, func(r *DynamoDBRepository) error {
			return r.UpdateAccountParent(ctx, "acct", "owner", "", "par", []AccountParentLink{{AccountID: "par"}})
		}},
		{"reparent new parent", "par", ErrConditionFailed, func(r *DynamoDBRepository) error {
			return r.UpdateAccountParent(ctx, "acct", "owner", "", "par", []AccountParentLink{{AccountID: "par"}})
		}},
		{"owner transfer", "acct", ErrConditionFailed, func(r *DynamoDBRepository) error {
			return r.TransferAccountOwner(ctx, "acct", "owner", "u2", time.Unix(1700000000, 0))
		}},
		{"meeting publish", "acct", ErrConditionFailed, func(r *DynamoDBRepository) error {
			return r.PublishMeetingToAccount(ctx, "owner", "m1", "acct")
		}},
		{"meeting classify", "acct", ErrConditionFailed, func(r *DynamoDBRepository) error {
			return r.LinkMeetingToAccountAs(ctx, "owner", "owner", "m1", "acct")
		}},
		{"news source create", "acct", ErrAccountNewsAccess, func(r *DynamoDBRepository) error {
			return r.CreateAccountNewsSource(ctx, &model.CrawlerSource{SourceID: "s1", AccountID: "acct"},
				&model.AccountNewsSourceRef{AccountID: "acct", SourceID: "s1", CreatedBy: "u"}, 0, 5)
		}},
		{"people link put", "acct", ErrAccountLinkAccess, func(r *DynamoDBRepository) error {
			l := testPeopleLink(model.LinkOriginAuto, model.LinkKindContact, "c1")
			l.AccountID = "acct"
			return r.PutPeopleLink(ctx, l, "")
		}},
		{"people link delete", "acct", ErrAccountLinkAccess, func(r *DynamoDBRepository) error {
			l := testPeopleLink(model.LinkOriginManual, model.LinkKindContact, "c1")
			return r.DeletePeopleLink(ctx, "acct", "actor", l.Key(), model.LinkOriginManual, false)
		}},
		{"mention ignore", "acct", ErrAccountLinkAccess, func(r *DynamoDBRepository) error {
			return r.PutMentionIgnore(ctx, "acct", "actor", testMentionHash('a'))
		}},
		{"people link alias", "acct", ErrAccountLinkAccess, func(r *DynamoDBRepository) error {
			l := testPeopleLink(model.LinkOriginManual, model.LinkKindContact, "c1")
			return r.AddPeopleLinkAlias(ctx, "acct", "actor", l.Key(), testMentionHash('b'))
		}},
		{"unresolved mentions", "acct", nil, func(r *DynamoDBRepository) error {
			row := testUnresolved(testMentionHash('c'), "participant")
			row.AccountID = "acct"
			return r.ReplaceUnresolvedMentions(ctx, "acct", "o:m1", []model.UnresolvedMention{row}, nil,
				&model.PeopleRun{AccountID: "acct", Scope: "o:m1", LeaseToken: "tok"})
		}},
		{"meeting create", "acct", nil, func(r *DynamoDBRepository) error {
			_, err := r.CreateMeeting(ctx, "u", "t", time.Unix(1700000000, 0), nil, "", model.MeetingPreparation{AccountID: "acct"})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []fenceRequest
			r := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
			if err := tc.call(r); err != nil {
				t.Fatal(err)
			}
			if len(calls) != 1 || calls[0].target != "TransactWriteItems" {
				t.Fatalf("calls=%+v, want one TransactWriteItems", calls)
			}
			items := fenceItems(t, calls[0])
			idx := metaIndex(t, items, tc.account)
			cond := resolvedCondition(items[idx])
			for _, want := range []string{"attribute_not_exists(mergingInto)", "attribute_not_exists(mergedInto)"} {
				if !strings.Contains(strings.ReplaceAll(cond, "attribute_not_exists (", "attribute_not_exists("), want) {
					t.Errorf("META condition %q missing %q", cond, want)
				}
			}
			if items[idx]["ReturnValuesOnConditionCheckFailure"] != "ALL_OLD" {
				t.Errorf("META item must return the stored row on failure: %+v", items[idx])
			}

			fenced := fenceRepo(t, new([]fenceRequest), func(fenceRequest) (int, string) { return 400, cancelledAt(len(items), idx, true) })
			if err := tc.call(fenced); !errors.Is(err, ErrAccountMerging) || errors.Is(err, ErrConditionFailed) {
				t.Fatalf("merge cancellation error=%v, want ErrAccountMerging only", err)
			}
			plain := fenceRepo(t, new([]fenceRequest), func(fenceRequest) (int, string) { return 400, cancelledAt(len(items), idx, false) })
			err := tc.call(plain)
			if err == nil || errors.Is(err, ErrAccountMerging) || (tc.plain != nil && !errors.Is(err, tc.plain)) {
				t.Fatalf("a failed condition without a merge marker must keep its mapping %v, got %v", tc.plain, err)
			}
		})
	}
}

func TestPutMemberTransaction(t *testing.T) {
	var calls []fenceRequest
	r := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	member := &model.AccountMember{PK: "ACCOUNT#acct", SK: "MEMBER#u2", AccountID: "acct", UserID: "u2", Role: model.RoleSA}
	if err := r.PutMember(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	items := fenceItems(t, calls[0])
	if len(items) != 2 {
		t.Fatalf("items=%d, want 2", len(items))
	}
	if cond := resolvedCondition(items[0]); !strings.Contains(strings.ReplaceAll(cond, "attribute_not_exists (", "attribute_not_exists("), "attribute_not_exists(PK)") {
		t.Fatalf("member put must stay create-only: %q", cond)
	}
	// A duplicate add still reports a plain condition failure.
	dup := fenceRepo(t, new([]fenceRequest), func(fenceRequest) (int, string) { return 400, cancelledAt(2, 0, false) })
	if err := dup.PutMember(context.Background(), member); !errors.Is(err, ErrConditionFailed) || errors.Is(err, ErrAccountMerging) {
		t.Fatalf("duplicate add error=%v, want ErrConditionFailed", err)
	}
}

func TestMeetingPublicationFencedByTargetAccount(t *testing.T) {
	ctx := context.Background()
	respond := func(failAt int, marker bool) func(fenceRequest) (int, string) {
		return func(call fenceRequest) (int, string) {
			switch call.target {
			case "GetItem":
				return 200, `{"Item":{"accountId":{"S":"acct"}}}`
			case "TransactWriteItems":
				if failAt >= 0 {
					return 400, cancelledAt(len(call.body["TransactItems"].([]any)), failAt, marker)
				}
			}
			return 200, `{}`
		}
	}
	t.Run("publish with an account in the write", func(t *testing.T) {
		var calls []fenceRequest
		r := fenceRepo(t, &calls, respond(-1, false))
		if err := r.PublishMeetingToAccount(ctx, "owner", "m1", "acct"); err != nil {
			t.Fatal(err)
		}
		idx := metaIndex(t, fenceItems(t, calls[len(calls)-1]), "acct")
		fenced := fenceRepo(t, new([]fenceRequest), respond(idx, true))
		if err := fenced.PublishMeetingToAccount(ctx, "owner", "m1", "acct"); !errors.Is(err, ErrAccountMerging) {
			t.Fatalf("error=%v, want ErrAccountMerging", err)
		}
	})
	t.Run("publish naming the stored account", func(t *testing.T) {
		var calls []fenceRequest
		r := fenceRepo(t, &calls, respond(-1, false))
		if err := r.UpdateMeetingFields(ctx, "owner", "m1", map[string]interface{}{"sharedToAccount": true}); err != nil {
			t.Fatal(err)
		}
		if len(calls) != 2 || calls[0].target != "GetItem" || calls[1].target != "TransactWriteItems" {
			t.Fatalf("calls=%+v, want GetItem then TransactWriteItems", calls)
		}
		items := fenceItems(t, calls[1])
		idx := metaIndex(t, items, "acct")
		meeting := items[1-idx]
		if cond := resolvedCondition(meeting); !strings.Contains(cond, "accountId = ") {
			t.Errorf("publish must stay bound to the observed account: %q", cond)
		}
		fenced := fenceRepo(t, new([]fenceRequest), respond(idx, true))
		err := fenced.UpdateMeetingFields(ctx, "owner", "m1", map[string]interface{}{"sharedToAccount": true})
		if !errors.Is(err, ErrAccountMerging) {
			t.Fatalf("error=%v, want ErrAccountMerging", err)
		}
	})
	t.Run("unrelated writes add no META check", func(t *testing.T) {
		for _, fields := range []map[string]interface{}{
			{"notes": "text"},
			{"title": "t"},
			{"sharedToAccount": false},
			{"accountId": ""},
		} {
			var calls []fenceRequest
			r := fenceRepo(t, &calls, respond(-1, false))
			if err := r.UpdateMeetingFields(ctx, "owner", "m1", fields); err != nil {
				t.Fatalf("%v: %v", fields, err)
			}
			for _, call := range calls {
				if call.target == "TransactWriteItems" {
					t.Errorf("%v: unexpected transaction %+v", fields, call.body)
				}
				if call.target == "GetItem" {
					t.Errorf("%v: unexpected account read", fields)
				}
			}
		}
	})
}

func TestFoldOpenIntoExtraKeepsOneMetaItem(t *testing.T) {
	r := fenceRepo(t, new([]fenceRequest), nil)
	extra, err := r.hierarchyCheck("acct", model.SKAccountMeta, accountParentCondition(""))
	if err != nil {
		t.Fatal(err)
	}
	open, err := r.accountOpenCheck("acct")
	if err != nil {
		t.Fatal(err)
	}
	pending := &open
	folded := foldOpenIntoExtra([]types.TransactWriteItem{extra}, "acct", &pending)
	if pending != nil {
		t.Fatal("the separate open item must be dropped once folded")
	}
	cond := aws.ToString(folded[0].ConditionCheck.ConditionExpression)
	if !strings.Contains(cond, "attribute_not_exists(#mergeFenceA)") || folded[0].ConditionCheck == extra.ConditionCheck {
		t.Fatalf("folded=%q, want a copy carrying the fence", cond)
	}
	if strings.Contains(aws.ToString(extra.ConditionCheck.ConditionExpression), "mergeFence") {
		t.Fatal("the caller's check must not be mutated")
	}
}

// wireUpdateOp returns the Update operation of a captured transaction. The
// target account's META open check shares the transaction with account writes,
// so tests must not rely on the meeting update's position.
func wireUpdateOp(items []any) map[string]any {
	for _, item := range items {
		if update, ok := item.(map[string]any)["Update"].(map[string]any); ok {
			return update
		}
	}
	return nil
}

// wireMemberCheck returns the first ConditionCheck on a MEMBER# row.
func wireMemberCheck(items []any) map[string]any {
	for _, item := range items {
		if check, ok := item.(map[string]any)["ConditionCheck"].(map[string]any); ok &&
			strings.HasPrefix(check["Key"].(map[string]any)["SK"].(map[string]any)["S"].(string), "MEMBER#") {
			return check
		}
	}
	return nil
}

// wireMeetingWrite returns the meeting update body of a captured write, which
// is a plain UpdateItem or the Update inside a fenced transaction.
func wireMeetingWrite(body map[string]any) map[string]any {
	if items, ok := body["TransactItems"].([]any); ok {
		return wireUpdateOp(items)
	}
	return body
}

// Connector creates reach the same classifier through connectorWriteError.
func TestConnectorWriteErrorMapsMergeFence(t *testing.T) {
	cancelled := func(item map[string]types.AttributeValue) error {
		return &types.TransactionCanceledException{CancellationReasons: []types.CancellationReason{
			{Code: aws.String("None")}, {Code: aws.String("None")}, {Code: aws.String("ConditionalCheckFailed"), Item: item},
		}}
	}
	marked := map[string]types.AttributeValue{"mergedInto": &types.AttributeValueMemberS{Value: "target"}}
	if err := connectorWriteError(cancelled(marked)); !errors.Is(err, ErrAccountMerging) {
		t.Fatalf("marked cancellation=%v, want ErrAccountMerging", err)
	}
	if err := connectorWriteError(cancelled(nil)); !errors.Is(err, ErrConditionFailed) || errors.Is(err, ErrAccountMerging) {
		t.Fatalf("plain cancellation=%v, want ErrConditionFailed", err)
	}
	// A replay with an existing receipt is never a definitive merge refusal.
	if err := connectorReplayError(cancelled(marked)); !errors.Is(err, ErrConnectorReplayAccountMerging) || errors.Is(err, ErrAccountMerging) {
		t.Fatalf("replay fence trip=%v, want ErrConnectorReplayAccountMerging only", err)
	}
	if err := connectorReplayError(cancelled(nil)); !errors.Is(err, ErrConditionFailed) {
		t.Fatalf("replay plain cancellation=%v, want ErrConditionFailed", err)
	}
	// A probe fence trip re-reads the receipt before reporting it missing.
	none := func() (string, bool, error) { return "", false, nil }
	if _, err := connectorProbeError(cancelled(marked), none); !errors.Is(err, ErrConnectorReceiptMissing) || errors.Is(err, ErrAccountMerging) {
		t.Fatalf("probe fence trip=%v, want ErrConnectorReceiptMissing", err)
	}
	committed := func() (string, bool, error) { return "", true, ErrConnectorReplayAccountMerging }
	if _, err := connectorProbeError(cancelled(marked), committed); !errors.Is(err, ErrConnectorReplayAccountMerging) {
		t.Fatalf("probe with a late receipt=%v, want ErrConnectorReplayAccountMerging", err)
	}
	reads := 0
	counted := func() (string, bool, error) { reads++; return "", false, nil }
	if _, err := connectorProbeError(cancelled(nil), counted); !errors.Is(err, ErrConditionFailed) || reads != 0 {
		t.Fatalf("probe plain cancellation=%v reads=%d, want ErrConditionFailed without a re-read", err, reads)
	}
}
