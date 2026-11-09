package repository

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func testPeopleLink(origin, toKind, toID string) *model.PeopleLink {
	l := &model.PeopleLink{AccountID: "acc", MeetingID: "owner:m1", ToKind: toKind, ToID: toID, Role: model.PeopleRoleSpeaker,
		Origin: origin, SourceField: model.PeopleSourceSpeakerMap, Mention: "Kim", SourceRef: "spk_0", CreatedAt: "2026-11-09T00:00:00Z"}
	if toKind == model.LinkKindProject || toKind == model.LinkKindAccount {
		l.Role, l.SourceField = model.PeopleRoleMentions, model.PeopleSourceInsights
	}
	if origin == model.LinkOriginManual {
		l.CreatedBy = "actor"
	} else {
		l.Rule = model.PeopleRuleName
	}
	return l
}

// capturePeopleTx returns a repository that records the transaction items.
func capturePeopleTx(t *testing.T, got *[]interface{}) *DynamoDBRepository {
	return indexingWire(t, func(target string, body map[string]interface{}) string {
		if target != "DynamoDB_20120810.TransactWriteItems" {
			t.Fatalf("target %s", target)
		}
		*got = body["TransactItems"].([]interface{})
		return `{}`
	})
}

func txOpKeys(t *testing.T, items []interface{}) []string {
	t.Helper()
	var out []string
	for _, raw := range items {
		item := raw.(wireMap)
		for _, op := range []string{"Put", "Delete", "ConditionCheck"} {
			if _, ok := item[op]; ok {
				pk, sk := wireKey(item, op)
				out = append(out, op+" "+pk+" "+sk)
			}
		}
	}
	return out
}

func TestPutPeopleLink_AutoTransactionShape(t *testing.T) {
	var items []interface{}
	repo := capturePeopleTx(t, &items)
	l := testPeopleLink(model.LinkOriginAuto, model.LinkKindMember, "u7")
	if err := repo.PutPeopleLink(context.Background(), l, ""); err != nil {
		t.Fatal(err)
	}
	k := l.Key()
	want := []string{
		"Put ACCOUNT#acc " + model.PeopleLinkSortKey(k),
		"Put ACCOUNT#acc " + model.PeopleLinkToSortKey(k),
		"ConditionCheck USER#owner MEETING#m1",
		"ConditionCheck ACCOUNT#acc MEMBER#u7",
		"ConditionCheck ACCOUNT#acc " + model.PeopleLinkDenySortKey(k),
		"ConditionCheck ACCOUNT#acc META",
	}
	if got := txOpKeys(t, items); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("items:\n%s", strings.Join(got, "\n"))
	}
	put := items[0].(wireMap)["Put"].(wireMap)
	if c := put["ConditionExpression"].(string); !strings.Contains(c, "attribute_not_exists") || strings.Contains(c, "OR") {
		t.Fatalf("auto forward condition %q", c)
	}
	if wireS(put["Item"], "role") != "speaker" || wireS(put["Item"], "origin") != "auto" || wireS(put["Item"], "entityType") != "ACCOUNT_PEOPLE_LINK" {
		t.Fatalf("forward item %#v", put["Item"])
	}
	meeting := wireValues(items[2].(wireMap)["ConditionCheck"].(wireMap))
	if !meeting["name:sharedToAccount"] || !meeting["value:acc"] || !meeting["value:MEETING"] {
		t.Fatalf("meeting check %#v", meeting)
	}
	if c := items[4].(wireMap)["ConditionCheck"].(wireMap)["ConditionExpression"].(string); !strings.Contains(c, "attribute_not_exists") {
		t.Fatalf("deny check %q", c)
	}
	// The MEMBER# check above is the target, not an actor membership check.
}

func TestPutPeopleLink_ManualReplacesAutoOnly(t *testing.T) {
	var items []interface{}
	repo := capturePeopleTx(t, &items)
	l := testPeopleLink(model.LinkOriginManual, model.LinkKindContact, "c1")
	if err := repo.PutPeopleLink(context.Background(), l, "actor"); err != nil {
		t.Fatal(err)
	}
	put := items[0].(wireMap)["Put"].(wireMap)
	seen := wireValues(put)
	c := put["ConditionExpression"].(string)
	if !strings.Contains(c, "attribute_not_exists") || !strings.Contains(c, "OR") || !seen["name:origin"] || !seen["value:auto"] {
		t.Fatalf("manual forward condition %q %#v", c, seen)
	}
	if len(items) != 6 {
		t.Fatalf("manual has no deny check: %d items", len(items))
	}
	for _, key := range txOpKeys(t, items) {
		if strings.Contains(key, "LINKDENY#") {
			t.Fatalf("manual write checks deny: %s", key)
		}
	}
	assertAccountLinkAccessChecks(t, items, "actor")

	// Binding the actor themself pins MEMBER#{actor} once.
	self := testPeopleLink(model.LinkOriginManual, model.LinkKindMember, "actor")
	if err := repo.PutPeopleLink(context.Background(), self, "actor"); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, key := range txOpKeys(t, items) {
		if strings.HasSuffix(key, " MEMBER#actor") {
			count++
		}
	}
	if count != 1 || len(items) != 5 {
		t.Fatalf("self binding items: %v", txOpKeys(t, items))
	}

	calls := 0
	reject := indexingWire(t, func(string, map[string]interface{}) string { calls++; return `{}` })
	other := testPeopleLink(model.LinkOriginManual, model.LinkKindContact, "c1")
	for _, tc := range []struct {
		link  *model.PeopleLink
		actor string
	}{
		{other, ""},
		{other, "someone-else"},
		{testPeopleLink(model.LinkOriginAuto, model.LinkKindContact, "c1"), "actor"},
		{&model.PeopleLink{AccountID: "acc", MeetingID: "m1", ToKind: "contact", ToID: "c1", Role: "speaker", Origin: "auto", SourceField: "x", CreatedAt: "t"}, ""},
		{testPeopleLink(model.LinkOriginAuto, model.LinkKindAccount, "acc"), ""},
	} {
		if err := reject.PutPeopleLink(context.Background(), tc.link, tc.actor); err == nil {
			t.Fatalf("accepted %+v actor=%q", tc.link, tc.actor)
		}
	}
	if calls != 0 {
		t.Fatal("invalid writes must be rejected before any call")
	}
}

func TestPutPeopleLink_ProjectTargetUsesAccountIds(t *testing.T) {
	var items []interface{}
	repo := capturePeopleTx(t, &items)
	if err := repo.PutPeopleLink(context.Background(), testPeopleLink(model.LinkOriginAuto, model.LinkKindProject, "p1"), ""); err != nil {
		t.Fatal(err)
	}
	pk, sk := wireKey(items[3].(wireMap), "ConditionCheck")
	cc := items[3].(wireMap)["ConditionCheck"].(wireMap)
	seen := wireValues(cc)
	if pk != "PROJECT#p1" || sk != "CONFIG" || !strings.Contains(cc["ConditionExpression"].(string), "contains") || !seen["name:accountIds"] || !seen["value:acc"] {
		t.Fatalf("project check %s %s %#v", pk, sk, cc)
	}
	if err := repo.PutPeopleLink(context.Background(), testPeopleLink(model.LinkOriginAuto, model.LinkKindAccount, "child"), ""); err != nil {
		t.Fatal(err)
	}
	if pk, sk := wireKey(items[3].(wireMap), "ConditionCheck"); pk != "ACCOUNT#child" || sk != "META" {
		t.Fatalf("account check %s %s", pk, sk)
	}
}

func TestPutPeopleLink_ContactTargetChecksContactRow(t *testing.T) {
	var items []interface{}
	repo := capturePeopleTx(t, &items)
	if err := repo.PutPeopleLink(context.Background(), testPeopleLink(model.LinkOriginAuto, model.LinkKindContact, "c1"), ""); err != nil {
		t.Fatal(err)
	}
	pk, sk := wireKey(items[3].(wireMap), "ConditionCheck")
	cc := items[3].(wireMap)["ConditionCheck"].(wireMap)
	if pk != "ACCOUNT#acc" || sk != "CONTACT#c1" || !strings.Contains(cc["ConditionExpression"].(string), "attribute_exists") {
		t.Fatalf("contact check %s %s %#v", pk, sk, cc)
	}
}

func TestPutPeopleLink_ClassifiesDenied(t *testing.T) {
	for _, tc := range []struct {
		reasons string
		want    error
	}{
		{`[{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"}]`, ErrAccountLinkDenied},
		{`[{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"None"}]`, ErrAccountLinkExists},
		{`[{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"}]`, ErrAccountLinkTarget},
		{`[{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"}]`, ErrAccountLinkAccess},
	} {
		repo := summarySDKRepo(func(*http.Request) (*http.Response, error) {
			return summaryHTTPResponse(400, `{"__type":"TransactionCanceledException","CancellationReasons":`+tc.reasons+`}`, nil), nil
		})
		err := repo.PutPeopleLink(context.Background(), testPeopleLink(model.LinkOriginAuto, model.LinkKindContact, "c1"), "")
		if !errors.Is(err, tc.want) || !errors.Is(err, ErrConditionFailed) {
			t.Fatalf("%s: %v", tc.reasons, err)
		}
	}
}

func TestDeletePeopleLink_SystemCleanupNoDeny(t *testing.T) {
	var items []interface{}
	repo := capturePeopleTx(t, &items)
	k := testPeopleLink(model.LinkOriginAuto, model.LinkKindContact, "c1").Key()
	if err := repo.DeletePeopleLink(context.Background(), "acc", "", k, model.LinkOriginAuto, false); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Delete ACCOUNT#acc " + model.PeopleLinkSortKey(k),
		"Delete ACCOUNT#acc " + model.PeopleLinkToSortKey(k),
		"ConditionCheck ACCOUNT#acc META",
	}
	if got := txOpKeys(t, items); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("items:\n%s", strings.Join(got, "\n"))
	}
	del := items[0].(wireMap)["Delete"].(wireMap)
	if seen := wireValues(del); !strings.Contains(del["ConditionExpression"].(string), "attribute_exists") || !seen["value:auto"] {
		t.Fatalf("forward delete %#v", del)
	}

	if err := repo.DeletePeopleLink(context.Background(), "acc", "actor", k, model.LinkOriginAuto, true); err != nil {
		t.Fatal(err)
	}
	want = []string{
		"Delete ACCOUNT#acc " + model.PeopleLinkSortKey(k),
		"Delete ACCOUNT#acc " + model.PeopleLinkToSortKey(k),
		"Put ACCOUNT#acc " + model.PeopleLinkDenySortKey(k),
		"ConditionCheck ACCOUNT#acc META",
		"ConditionCheck ACCOUNT#acc MEMBER#actor",
	}
	if got := txOpKeys(t, items); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("user delete items:\n%s", strings.Join(got, "\n"))
	}

	calls := 0
	reject := indexingWire(t, func(string, map[string]interface{}) string { calls++; return `{}` })
	for _, tc := range []struct {
		origin string
		deny   bool
	}{{model.LinkOriginAuto, true}, {model.LinkOriginManual, false}} {
		if err := reject.DeletePeopleLink(context.Background(), "acc", "", k, tc.origin, tc.deny); err == nil {
			t.Fatalf("system delete accepted %+v", tc)
		}
	}
	if calls != 0 {
		t.Fatal("invalid deletes must be rejected before any call")
	}

	repo = summarySDKRepo(func(*http.Request) (*http.Response, error) {
		return summaryHTTPResponse(400, `{"__type":"TransactionCanceledException","CancellationReasons":[{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"}]}`, nil), nil
	})
	if err := repo.DeletePeopleLink(context.Background(), "acc", "", k, model.LinkOriginAuto, false); !errors.Is(err, ErrConditionFailed) || errors.Is(err, ErrAccountLinkAccess) {
		t.Fatalf("origin changed: %v", err)
	}
}

func TestListPeopleLinksByTarget_ResumesAfterSK(t *testing.T) {
	after := "LINKTO#contact#c1#meeting#o:m1#speaker"
	calls := 0
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		calls++
		values := wireValues(body)
		if target != "DynamoDB_20120810.Query" || body["ConsistentRead"] != true || body["Limit"].(float64) != 3 ||
			!strings.Contains(body["KeyConditionExpression"].(string), "BETWEEN") ||
			!values["value:"+after] || !values["value:LINKTO#contact$"] {
			t.Fatalf("query %#v", body)
		}
		return `{"Items":[
{"PK":{"S":"ACCOUNT#acc"},"SK":{"S":"` + after + `"},"origin":{"S":"auto"}},
{"PK":{"S":"ACCOUNT#acc"},"SK":{"S":"LINKTO#contact#c1#meeting#o:m2#participant"},"toId":{"S":"spoofed"},"origin":{"S":"manual"},"createdBy":{"S":"u"}},
{"PK":{"S":"ACCOUNT#acc"},"SK":{"S":"LINKTO#contact#c2#meeting#o:m3#speaker"},"origin":{"S":"other"}}],
"LastEvaluatedKey":{"PK":{"S":"ACCOUNT#acc"},"SK":{"S":"LINKTO#contact#c2#meeting#o:m3#speaker"}}}`
	})
	links, last, err := repo.ListPeopleLinksByTarget(context.Background(), "acc", model.LinkKindContact, after, 3)
	if err != nil || calls != 1 || len(links) != 1 || last != "LINKTO#contact#c2#meeting#o:m3#speaker" {
		t.Fatalf("links=%+v last=%q err=%v calls=%d", links, last, err, calls)
	}
	if l := links[0]; l.MeetingID != "o:m2" || l.ToKind != "contact" || l.ToID != "c1" || l.Role != "participant" || l.AccountID != "acc" {
		t.Fatalf("key must be canonical: %+v", l)
	}
	if _, _, err := repo.ListPeopleLinksByTarget(context.Background(), "acc", model.LinkKindContact, "LINKTO#member#x", 3); err == nil {
		t.Fatal("cursor outside the kind range accepted")
	}

	repo = indexingWire(t, func(string, map[string]interface{}) string { return `{"Items":[]}` })
	if links, last, err := repo.ListPeopleLinksByTarget(context.Background(), "acc", model.LinkKindMember, "", 10); err != nil || len(links) != 0 || last != "" {
		t.Fatalf("exhausted range: %+v %q %v", links, last, err)
	}
}

func TestListPeopleLinksForMeetingAndTarget(t *testing.T) {
	var prefixes []string
	repo := indexingWire(t, func(_ string, body map[string]interface{}) string {
		for v := range wireValues(body) {
			if strings.HasPrefix(v, "value:LINK") {
				prefixes = append(prefixes, strings.TrimPrefix(v, "value:"))
			}
		}
		return `{"Items":[{"PK":{"S":"ACCOUNT#acc"},"SK":{"S":"LINK#meeting#o:m1#member#u1#assignee"},"origin":{"S":"auto"},"sourceRef":{"S":"ai1"}}]}`
	})
	links, truncated, err := repo.ListPeopleLinksForMeeting(context.Background(), "acc", "o:m1", 50)
	if err != nil || truncated || len(links) != 1 || links[0].SourceRef != "ai1" || links[0].Role != "assignee" {
		t.Fatalf("meeting links %+v %v %v", links, truncated, err)
	}
	if _, _, err := repo.ListPeopleLinksToTarget(context.Background(), "acc", "member", "u1", 50); err != nil {
		t.Fatal(err)
	}
	if strings.Join(prefixes, ",") != "LINK#meeting#o:m1#,LINKTO#member#u1#meeting#" {
		t.Fatalf("prefixes %v", prefixes)
	}
	if _, _, err := repo.ListPeopleLinksForMeeting(context.Background(), "acc", "m1", 50); err == nil {
		t.Fatal("meeting ID without owner accepted")
	}
}
