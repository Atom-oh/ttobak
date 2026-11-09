package repository

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

type wireMap = map[string]interface{}

func wireS(m interface{}, field string) string {
	v, _ := m.(wireMap)[field].(wireMap)
	s, _ := v["S"].(string)
	return s
}

func wireKey(item wireMap, op string) (string, string) {
	body := item[op].(wireMap)
	key, ok := body["Key"].(wireMap)
	if !ok {
		key = body["Item"].(wireMap)
	}
	return wireS(key, "PK"), wireS(key, "SK")
}

func wireValues(body wireMap) map[string]bool {
	seen := map[string]bool{}
	for _, v := range body["ExpressionAttributeNames"].(wireMap) {
		seen["name:"+v.(string)] = true
	}
	if values, ok := body["ExpressionAttributeValues"].(wireMap); ok {
		for _, v := range values {
			av := v.(wireMap)
			if s, ok := av["S"].(string); ok {
				seen["value:"+s] = true
			}
			if b, ok := av["BOOL"].(bool); ok && b {
				seen["value:true"] = true
			}
		}
	}
	return seen
}

func TestPutAccountLink_TransactionShape(t *testing.T) {
	project := &model.AccountLink{AccountID: "acc", FromKind: "event", FromID: "e1", ToKind: "project", ToID: "p1",
		Origin: "manual", CreatedBy: "u", CreatedAt: "2026-11-09T00:00:00Z"}
	auto := &model.AccountLink{AccountID: "acc", FromKind: "news", FromID: "acct_acc_1:0123456789abcdef", ToKind: "meeting", ToID: "owner:m1",
		Origin: "auto", CreatedAt: "2026-11-09T00:00:00Z", Evidence: "quote", ModelID: "haiku"}
	manualMeeting := *auto
	manualMeeting.Origin, manualMeeting.Evidence, manualMeeting.ModelID = "manual", "", ""
	for _, tc := range []struct {
		name  string
		link  *model.AccountLink
		actor string
		count int
		check func(t *testing.T, items []interface{})
	}{
		{"project target", project, "u", 5, func(t *testing.T, items []interface{}) {
			cc := items[2].(wireMap)["ConditionCheck"].(wireMap)
			pk, sk := wireKey(items[2].(wireMap), "ConditionCheck")
			seen := wireValues(cc)
			if pk != "PROJECT#p1" || sk != "CONFIG" || !strings.Contains(cc["ConditionExpression"].(string), "contains") ||
				!seen["name:accountIds"] || !seen["value:acc"] {
				t.Fatalf("project check: %#v", cc)
			}
		}},
		{"auto meeting link", auto, "", 5, func(t *testing.T, items []interface{}) {
			cc := items[2].(wireMap)["ConditionCheck"].(wireMap)
			pk, sk := wireKey(items[2].(wireMap), "ConditionCheck")
			seen := wireValues(cc)
			if pk != "USER#owner" || sk != "MEETING#m1" || !seen["name:accountId"] || !seen["name:sharedToAccount"] ||
				!seen["value:acc"] || !seen["value:true"] || !seen["value:MEETING"] {
				t.Fatalf("meeting check: %#v", cc)
			}
			deny := items[3].(wireMap)["ConditionCheck"].(wireMap)
			pk, sk = wireKey(items[3].(wireMap), "ConditionCheck")
			if pk != "ACCOUNT#acc" || sk != "LINKDENY#news#acct_acc_1:0123456789abcdef#meeting#owner:m1" ||
				!strings.Contains(deny["ConditionExpression"].(string), "attribute_not_exists") {
				t.Fatalf("deny check: %#v", deny)
			}
		}},
		{"manual meeting link has no deny check", &manualMeeting, "u", 5, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			repo := indexingWire(t, func(target string, body map[string]interface{}) string {
				calls++
				if target != "DynamoDB_20120810.TransactWriteItems" {
					t.Fatalf("target %s", target)
				}
				items := body["TransactItems"].([]interface{})
				if len(items) != tc.count {
					t.Fatalf("items: %d", len(items))
				}
				in := tc.link.Input()
				put := items[0].(wireMap)["Put"].(wireMap)
				pk, sk := wireKey(items[0].(wireMap), "Put")
				if pk != "ACCOUNT#acc" || sk != model.AccountLinkSortKey(in) || !strings.Contains(put["ConditionExpression"].(string), "attribute_not_exists") ||
					wireS(put["Item"], "origin") != tc.link.Origin {
					t.Fatalf("forward put: %#v", put)
				}
				pk, sk = wireKey(items[1].(wireMap), "Put")
				if pk != "ACCOUNT#acc" || sk != model.AccountLinkToSortKey(in) {
					t.Fatalf("reverse put: %s %s", pk, sk)
				}
				if tc.check != nil {
					tc.check(t, items)
				}
				assertAccountLinkAccessChecks(t, items, tc.actor)
				return `{}`
			})
			if err := repo.PutAccountLink(context.Background(), tc.link, tc.actor); err != nil || calls != 1 {
				t.Fatalf("put: %d %v", calls, err)
			}
		})
	}
	calls := 0
	repo := indexingWire(t, func(string, map[string]interface{}) string { calls++; return `{}` })
	bad := *project
	bad.FromKind = "meeting"
	if err := repo.PutAccountLink(context.Background(), &bad, "u"); err == nil || calls != 0 {
		t.Fatal("invalid pair must be rejected before any write")
	}
	if err := repo.PutAccountLink(context.Background(), project, ""); err == nil || calls != 0 {
		t.Fatal("a manual link without an acting member must be rejected before any write")
	}
}

// assertAccountLinkAccessChecks verifies the trailing META and (for a member
// actor) MEMBER# existence checks that pin the account and membership.
func assertAccountLinkAccessChecks(t *testing.T, items []interface{}, actor string) {
	t.Helper()
	want := []string{"META"}
	if actor != "" {
		want = append(want, "MEMBER#"+actor)
	}
	tail := items[len(items)-len(want):]
	for i, sk := range want {
		cc, ok := tail[i].(wireMap)["ConditionCheck"].(wireMap)
		if !ok {
			t.Fatalf("access check %d missing: %#v", i, tail[i])
		}
		pk, got := wireKey(tail[i].(wireMap), "ConditionCheck")
		if pk != "ACCOUNT#acc" || got != sk || !strings.Contains(cc["ConditionExpression"].(string), "attribute_exists") {
			t.Fatalf("access check %d: %s %s %#v", i, pk, got, cc)
		}
	}
	if actor == "" {
		for _, item := range items {
			if cc, ok := item.(wireMap)["ConditionCheck"].(wireMap); ok {
				if _, sk := wireKey(item.(wireMap), "ConditionCheck"); strings.HasPrefix(sk, "MEMBER#") {
					t.Fatalf("system link must not check a member row: %#v", cc)
				}
			}
		}
	}
}

func TestPutAccountLinkCancellationMapping(t *testing.T) {
	auto := &model.AccountLink{AccountID: "acc", FromKind: "news", FromID: "s:h", ToKind: "meeting", ToID: "o:m", Origin: "auto", CreatedAt: "t"}
	for _, tc := range []struct {
		reasons string
		want    error
	}{
		{`[{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"None"}]`, ErrAccountLinkExists},
		{`[{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"}]`, ErrAccountLinkTarget},
		{`[{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"}]`, ErrAccountLinkDenied},
		{`[{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"}]`, ErrAccountLinkDenied},
		{`[{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"}]`, ErrAccountLinkTarget},
		{`[{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"ConditionalCheckFailed"}]`, ErrAccountLinkAccess},
		{`[{"Code":"None"},{"Code":"TransactionConflict"},{"Code":"None"},{"Code":"None"},{"Code":"None"}]`, nil},
	} {
		repo := summarySDKRepo(func(*http.Request) (*http.Response, error) {
			return summaryHTTPResponse(400, `{"__type":"TransactionCanceledException","CancellationReasons":`+tc.reasons+`}`, nil), nil
		})
		err := repo.PutAccountLink(context.Background(), auto, "")
		if tc.want == nil {
			if err == nil || errors.Is(err, ErrConditionFailed) {
				t.Fatalf("conflict must stay a transport error: %v", err)
			}
			continue
		}
		if !errors.Is(err, tc.want) || !errors.Is(err, ErrConditionFailed) {
			t.Fatalf("%s: %v", tc.reasons, err)
		}
	}
}

func TestDeleteAccountLinkShape(t *testing.T) {
	in := model.AccountLinkInput{FromKind: "news", FromID: "s:h", ToKind: "meeting", ToID: "o:m"}
	for _, deny := range []bool{false, true} {
		repo := indexingWire(t, func(target string, body map[string]interface{}) string {
			items := body["TransactItems"].([]interface{})
			want := 4
			if deny {
				want = 5
			}
			if target != "DynamoDB_20120810.TransactWriteItems" || len(items) != want {
				t.Fatalf("deny=%v: %s %d", deny, target, len(items))
			}
			del := items[0].(wireMap)["Delete"].(wireMap)
			_, sk := wireKey(items[0].(wireMap), "Delete")
			seen := wireValues(del)
			origin := "manual"
			if deny {
				origin = "auto"
			}
			if sk != model.AccountLinkSortKey(in) || !strings.Contains(del["ConditionExpression"].(string), "attribute_exists") ||
				!seen["name:origin"] || !seen["value:"+origin] {
				t.Fatalf("forward delete: %#v", del)
			}
			if _, sk := wireKey(items[1].(wireMap), "Delete"); sk != model.AccountLinkToSortKey(in) {
				t.Fatalf("reverse delete %s", sk)
			}
			if deny {
				if pk, sk := wireKey(items[2].(wireMap), "Put"); pk != "ACCOUNT#acc" || sk != model.AccountLinkDenySortKey(in) {
					t.Fatalf("deny put %s %s", pk, sk)
				}
			}
			assertAccountLinkAccessChecks(t, items, "u")
			return `{}`
		})
		if err := repo.DeleteAccountLink(context.Background(), "acc", "u", in, deny); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		reasons string
		revoked bool
	}{
		{`[{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"},{"Code":"None"}]`, false},
		{`[{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"}]`, true},
	} {
		repo := summarySDKRepo(func(*http.Request) (*http.Response, error) {
			return summaryHTTPResponse(400, `{"__type":"TransactionCanceledException","CancellationReasons":`+tc.reasons+`}`, nil), nil
		})
		err := repo.DeleteAccountLink(context.Background(), "acc", "u", in, false)
		if !errors.Is(err, ErrConditionFailed) || errors.Is(err, ErrAccountLinkAccess) != tc.revoked {
			t.Fatalf("%s: %v", tc.reasons, err)
		}
	}
	calls := 0
	repo := indexingWire(t, func(string, map[string]interface{}) string { calls++; return `{}` })
	if err := repo.DeleteAccountLink(context.Background(), "acc", "", in, false); err == nil || calls != 0 {
		t.Fatal("delete without an acting member must be rejected before any write")
	}
}

func TestListAccountLinksDecodesKeysAndPaginates(t *testing.T) {
	calls := 0
	var cursor *string
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		calls++
		if target != "DynamoDB_20120810.Query" || body["ConsistentRead"] != true || body["Limit"].(float64) != 100 {
			t.Fatalf("query: %#v", body)
		}
		if calls == 1 {
			if body["ExclusiveStartKey"] != nil {
				t.Fatal("first page has no start key")
			}
			return `{"Items":[
{"PK":{"S":"ACCOUNT#acc"},"SK":{"S":"LINK#event#e1#project#p1"},"fromKind":{"S":"news"},"fromId":{"S":"spoofed"},"toKind":{"S":"project"},"toId":{"S":"spoofed"},"origin":{"S":"manual"},"createdBy":{"S":"u"},"createdAt":{"S":"t"}},
{"PK":{"S":"ACCOUNT#acc"},"SK":{"S":"LINK#meeting#o:m#project#p1"},"origin":{"S":"manual"}},
{"PK":{"S":"ACCOUNT#acc"},"SK":{"S":"LINK#event#e2#project#p1"},"origin":{"S":"other"}}],
"LastEvaluatedKey":{"PK":{"S":"ACCOUNT#acc"},"SK":{"S":"LINK#event#e1#project#p1"}}}`
		}
		start := body["ExclusiveStartKey"].(wireMap)
		if wireS(start, "PK") != "ACCOUNT#acc" || wireS(start, "SK") != "LINK#event#e1#project#p1" {
			t.Fatalf("start key %#v", start)
		}
		return `{"Items":[]}`
	})
	links, next, err := repo.ListAccountLinks(context.Background(), "acc", "", 100)
	if err != nil || len(links) != 1 || next == nil {
		t.Fatalf("page 1: %#v %v %v", links, next, err)
	}
	if l := links[0]; l.AccountID != "acc" || l.FromKind != "event" || l.FromID != "e1" || l.ToID != "p1" || l.CreatedBy != "u" {
		t.Fatalf("key must be canonical: %#v", l)
	}
	cursor = next
	links, next, err = repo.ListAccountLinks(context.Background(), "acc", *cursor, 100)
	if err != nil || len(links) != 0 || next != nil {
		t.Fatalf("page 2: %#v %v %v", links, next, err)
	}
	for _, bad := range []string{"!!", "TElOS1RPI3g"} { // invalid base64; "LINKTO#x"
		if _, _, err := repo.ListAccountLinks(context.Background(), "acc", bad, 100); !errors.Is(err, ErrInvalidAccountLinkCursor) {
			t.Fatalf("cursor %q: %v", bad, err)
		}
	}
	if calls != 2 {
		t.Fatalf("bad cursors must not query: %d", calls)
	}
}
