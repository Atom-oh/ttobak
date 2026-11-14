package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/sealing"
)

// runTestSealer is a reversible sealer that binds the whole binding map.
type runTestSealer struct{ transportErr error }

func runTestBindingID(b map[string]string) string {
	keys := make([]string, 0, len(b))
	for k := range b {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k + "=" + b[k] + ";")
	}
	return sb.String()
}

func (runTestSealer) Seal(_ context.Context, p []byte, b map[string]string) (string, error) {
	return base64.StdEncoding.EncodeToString([]byte(runTestBindingID(b) + "|" + string(p))), nil
}

func (s runTestSealer) Open(_ context.Context, sealed string, b map[string]string) ([]byte, error) {
	if s.transportErr != nil {
		return nil, s.transportErr
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	prefix := runTestBindingID(b) + "|"
	if err != nil || !strings.HasPrefix(string(raw), prefix) {
		return nil, sealing.ErrInvalid
	}
	return raw[len(prefix):], nil
}

const (
	testRunID   = "0123456789abcdef0123456789abcdef"
	testHashA   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testHashB   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testAccount = "acct-hanbit_1"
)

func sAttr(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }
func nAttr(v string) types.AttributeValue { return &types.AttributeValueMemberN{Value: v} }
func condString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func namesOf(m map[string]string) []string {
	out := []string{}
	for _, n := range m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func hasName(m map[string]string, want string) bool {
	for _, n := range m {
		if n == want {
			return true
		}
	}
	return false
}

func hasValue(m map[string]types.AttributeValue, want string) bool {
	for _, v := range m {
		switch v := v.(type) {
		case *types.AttributeValueMemberS:
			if v.Value == want {
				return true
			}
		case *types.AttributeValueMemberN:
			if v.Value == want {
				return true
			}
		}
	}
	return false
}

func runRow(sk string, attrs map[string]types.AttributeValue) map[string]types.AttributeValue {
	item := map[string]types.AttributeValue{"PK": sAttr(model.UpdateRunPK(testRunID)), "SK": sAttr(sk)}
	for k, v := range attrs {
		item[k] = v
	}
	return item
}

func sealedRunPayload(t *testing.T, sk string, payload any) types.AttributeValue {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return sealedRunBytes(sk, raw)
}

func sealedRunBytes(sk string, raw []byte) types.AttributeValue {
	s, _ := runTestSealer{}.Seal(context.Background(), raw, runRowBinding(testRunID, sk))
	return sAttr(s)
}

func decSK(h, a string) string   { return "DEC#" + h + "#" + a }
func draftSK(a, h string) string { return "DRAFT#" + a + "#" + h }

func decAttrs(t *testing.T, sk, choice string, payload any) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"choice": sAttr(choice), "score": nAttr("0.82"), "backend": sAttr("bedrock"),
		"outcome": sAttr("drafted"), "discardReason": sAttr(""), "sealed": sealedRunPayload(t, sk, payload),
	}
}

func draftPayload() map[string]any {
	return map[string]any{
		"sourceUrl": "https://example.com/a", "sourceTitle": "제목", "why": "이유", "body": "본문 <b>raw</b>",
		"links": []string{"https://example.com/a"}, "opportunityIds": []string{"proj-1"},
		"fetchedUrls": []string{"https://example.com/a", "https://example.com/b"},
	}
}

func TestCustomerUpdateRunReadRunRowsClassifies(t *testing.T) {
	gate := map[string]any{"question": "q", "options": []map[string]string{{"id": "needed", "text": "t1"}, {"id": "not_needed", "text": "t2"}}, "context": "Customer: 한빛"}
	items := []map[string]types.AttributeValue{
		runRow("CLAIM", map[string]types.AttributeValue{"claimedAt": sAttr("2026-11-14T00:00:00Z")}),
		runRow("ITEM#"+testHashA, map[string]types.AttributeValue{"state": sAttr("decided"), "reason": sAttr("")}),
		runRow(decSK(testHashA, testAccount), decAttrs(t, decSK(testHashA, testAccount), "needed", map[string]any{"reason": "관심", "gateInput": gate})),
		runRow(decSK(testHashB, testAccount), decAttrs(t, decSK(testHashB, testAccount), "not_needed", map[string]any{"reason": "무관", "gateInput": gate})),
		runRow(draftSK(testAccount, testHashA), map[string]types.AttributeValue{"sealed": sealedRunPayload(t, draftSK(testAccount, testHashA), draftPayload())}),
		runRow("DONE", map[string]types.AttributeValue{"finishedAt": sAttr("2026-11-14T01:02:03Z"), "stats": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{"drafts": nAttr("1")}}}),
	}
	got, err := classifyRunRows(context.Background(), runTestSealer{}, testRunID, items)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Done || !got.FinishedAt.Equal(time.Date(2026, 11, 14, 1, 2, 3, 0, time.UTC)) {
		t.Fatalf("done = %v %v", got.Done, got.FinishedAt)
	}
	if got.Malformed != 0 || got.Unreadable != 0 {
		t.Fatalf("counts = %d %d", got.Malformed, got.Unreadable)
	}
	if len(got.Items) != 1 || got.Items[0] != (model.RunItemRow{URLHash: testHashA, State: "decided"}) {
		t.Fatalf("items = %+v", got.Items)
	}
	if len(got.Decisions) != 2 {
		t.Fatalf("decisions = %+v", got.Decisions)
	}
	needed, notNeeded := got.Decisions[0], got.Decisions[1]
	if needed.URLHash != testHashA || needed.AccountID != testAccount || needed.Choice != "needed" || needed.Score != 0.82 ||
		needed.Backend != "bedrock" || needed.Outcome != "drafted" || needed.Reason != "관심" || needed.GateInput == nil ||
		needed.GateInput.Question != "q" || len(needed.GateInput.Options) != 2 || needed.GateInput.Options[1].ID != "not_needed" ||
		needed.GateInput.Context != "Customer: 한빛" {
		t.Fatalf("needed = %+v", needed)
	}
	if notNeeded.GateInput != nil || notNeeded.Reason != "무관" {
		t.Fatalf("not_needed must drop gate input: %+v", notNeeded)
	}
	if len(got.Drafts) != 1 {
		t.Fatalf("drafts = %+v", got.Drafts)
	}
	d := got.Drafts[0]
	if d.AccountID != testAccount || d.URLHash != testHashA || d.Body != "본문 <b>raw</b>" || d.SourceTitle != "제목" ||
		len(d.Links) != 1 || len(d.OpportunityIDs) != 1 || len(d.FetchedURLs) != 2 {
		t.Fatalf("draft = %+v", d)
	}
}

func TestCustomerUpdateRunReadRunRowsWithoutDone(t *testing.T) {
	got, err := classifyRunRows(context.Background(), runTestSealer{}, testRunID, []map[string]types.AttributeValue{
		runRow("CLAIM", nil),
		runRow("DONE", map[string]types.AttributeValue{"finishedAt": sAttr("yesterday")}),
	})
	if err != nil || got.Done || got.Malformed != 1 {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestCustomerUpdateRunReadRunRowsMalformed(t *testing.T) {
	longAcct := strings.Repeat("a", 65)
	tooMany := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "https://example.com/" + strconv.Itoa(i)
		}
		return out
	}
	overDraft := func(field string, n int) map[string]any {
		p := draftPayload()
		p[field] = tooMany(n)
		return p
	}
	manyOptions := make([]map[string]string, 11)
	for i := range manyOptions {
		manyOptions[i] = map[string]string{"id": "o", "text": "t"}
	}
	cases := []struct {
		name string
		item map[string]types.AttributeValue
	}{
		{"unknown sk", runRow("WHAT#x", nil)},
		{"bad item hash", runRow("ITEM#ABC", map[string]types.AttributeValue{"state": sAttr("decided"), "reason": sAttr("")})},
		{"item state number", runRow("ITEM#"+testHashA, map[string]types.AttributeValue{"state": nAttr("1"), "reason": sAttr("")})},
		{"item missing reason", runRow("ITEM#"+testHashA, map[string]types.AttributeValue{"state": sAttr("decided")})},
		{"item unknown state", runRow("ITEM#"+testHashA, map[string]types.AttributeValue{"state": sAttr("weird"), "reason": sAttr("")})},
		{"dec no account", runRow("DEC#"+testHashA, nil)},
		{"dec bad account", runRow(decSK(testHashA, "a.b"), decAttrs(t, decSK(testHashA, "a.b"), "needed", map[string]any{"reason": "r"}))},
		{"dec long account", runRow(decSK(testHashA, longAcct), decAttrs(t, decSK(testHashA, longAcct), "needed", map[string]any{"reason": "r"}))},
		{"dec bad hash", runRow(decSK("ab", testAccount), decAttrs(t, decSK("ab", testAccount), "needed", map[string]any{"reason": "r"}))},
		{"dec score string", runRow(decSK(testHashA, testAccount), func() map[string]types.AttributeValue {
			a := decAttrs(t, decSK(testHashA, testAccount), "needed", map[string]any{"reason": "r"})
			a["score"] = sAttr("0.5")
			return a
		}())},
		{"dec unknown choice", runRow(decSK(testHashA, testAccount), decAttrs(t, decSK(testHashA, testAccount), "maybe", map[string]any{"reason": "r"}))},
		{"dec too many options", runRow(decSK(testHashA, testAccount), decAttrs(t, decSK(testHashA, testAccount), "needed",
			map[string]any{"reason": "r", "gateInput": map[string]any{"question": "q", "options": manyOptions, "context": "c"}}))},
		{"draft bad key", runRow("DRAFT#"+testHashA, map[string]types.AttributeValue{"sealed": sAttr("x")})},
		{"draft swapped key", runRow(draftSK(testHashA, testAccount), map[string]types.AttributeValue{"sealed": sAttr("x")})},
		{"draft too many links", runRow(draftSK(testAccount, testHashA), map[string]types.AttributeValue{"sealed": sealedRunPayload(t, draftSK(testAccount, testHashA), overDraft("links", 21))})},
		{"draft too many fetched", runRow(draftSK(testAccount, testHashA), map[string]types.AttributeValue{"sealed": sealedRunPayload(t, draftSK(testAccount, testHashA), overDraft("fetchedUrls", 21))})},
		{"draft too many opportunities", runRow(draftSK(testAccount, testHashA), map[string]types.AttributeValue{"sealed": sealedRunPayload(t, draftSK(testAccount, testHashA), overDraft("opportunityIds", 11))})},
		{"draft over 64KiB", runRow(draftSK(testAccount, testHashA), map[string]types.AttributeValue{"sealed": sealedRunPayload(t, draftSK(testAccount, testHashA), func() map[string]any {
			p := draftPayload()
			p["body"] = strings.Repeat("x", 64*1024)
			return p
		}())})},
		{"sk not string", map[string]types.AttributeValue{"PK": sAttr(model.UpdateRunPK(testRunID)), "SK": nAttr("1")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := classifyRunRows(context.Background(), runTestSealer{}, testRunID, []map[string]types.AttributeValue{tc.item})
			if err != nil {
				t.Fatal(err)
			}
			if got.Malformed != 1 || got.Unreadable != 0 || len(got.Items)+len(got.Decisions)+len(got.Drafts) != 0 {
				t.Fatalf("got %+v", got)
			}
		})
	}
	// Bounds are inclusive.
	got, err := classifyRunRows(context.Background(), runTestSealer{}, testRunID, []map[string]types.AttributeValue{
		runRow(draftSK(testAccount, testHashA), map[string]types.AttributeValue{"sealed": sealedRunPayload(t, draftSK(testAccount, testHashA), overDraft("links", 20))}),
	})
	if err != nil || len(got.Drafts) != 1 {
		t.Fatalf("20 links must be accepted: %+v %v", got, err)
	}
}

func TestCustomerUpdateRunReadRunRowsUnreadable(t *testing.T) {
	dsk := draftSK(testAccount, testHashA)
	otherBinding, _ := runTestSealer{}.Seal(context.Background(), []byte(`{"body":"x"}`), runRowBinding(testRunID, draftSK(testAccount, testHashB)))
	otherRun, _ := runTestSealer{}.Seal(context.Background(), []byte(`{"body":"x"}`), runRowBinding("ffffffffffffffffffffffffffffffff", dsk))
	cases := []struct {
		name   string
		sealed types.AttributeValue
	}{
		{"missing", nil},
		{"not string", nAttr("1")},
		{"garbage", sAttr("not-base64!")},
		{"other sk binding", sAttr(otherBinding)},
		{"other run binding", sAttr(otherRun)},
		{"invalid json", sealedRunBytes(dsk, []byte(`{"body":`))},
		{"wrong json type", sealedRunBytes(dsk, []byte(`{"links":"x"}`))},
		{"invalid utf8", sealedRunBytes(dsk, []byte("{\"body\":\"\xff\xfe\"}"))},
		{"lone high surrogate", sealedRunBytes(dsk, []byte(`{"body":"a`+jsonEscape("d800")+`b"}`))},
		{"lone low surrogate", sealedRunBytes(dsk, []byte(`{"body":"`+jsonEscape("dc00")+`"}`))},
		{"high then non-low", sealedRunBytes(dsk, []byte(`{"body":"`+jsonEscape("d800")+jsonEscape("0041")+`"}`))},
		{"high at end", sealedRunBytes(dsk, []byte(`{"body":"`+jsonEscape("D83D")+`"}`))},
		{"trailing brace", sealedRunBytes(dsk, []byte(`{"body":"x"}}`))},
		{"trailing value", sealedRunBytes(dsk, []byte(`{"body":"x"} {"body":"y"}`))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attrs := map[string]types.AttributeValue{}
			if tc.sealed != nil {
				attrs["sealed"] = tc.sealed
			}
			got, err := classifyRunRows(context.Background(), runTestSealer{}, testRunID, []map[string]types.AttributeValue{runRow(dsk, attrs)})
			if err != nil {
				t.Fatal(err)
			}
			if got.Unreadable != 1 || got.Malformed != 0 || len(got.Drafts) != 0 {
				t.Fatalf("got %+v", got)
			}
		})
	}
	// Escaped valid surrogate pairs and an escaped backslash before "ud800"
	// are accepted and decode exactly.
	for _, tc := range []struct{ raw, body string }{
		{`{"body":"` + jsonEscape("d83d") + jsonEscape("de00") + `"}`, string(rune(0x1F600))},
		{`{"body":"` + jsonEscape("DBFF") + jsonEscape("DFFF") + `"}`, string(rune(0x10FFFF))},
		{`{"body":"\` + jsonEscape("d800") + `"}`, `\` + "ud800"},
	} {
		got, err := classifyRunRows(context.Background(), runTestSealer{}, testRunID, []map[string]types.AttributeValue{
			runRow(dsk, map[string]types.AttributeValue{"sealed": sealedRunBytes(dsk, []byte(tc.raw))}),
		})
		if err != nil || len(got.Drafts) != 1 || got.Drafts[0].Body != tc.body {
			t.Fatalf("%s: %+v %v", tc.raw, got, err)
		}
	}
}

func TestCustomerUpdateRunReadRunRowsSealerFailures(t *testing.T) {
	dsk := draftSK(testAccount, testHashA)
	rows := []map[string]types.AttributeValue{runRow(dsk, map[string]types.AttributeValue{"sealed": sealedRunPayload(t, dsk, draftPayload())})}
	if _, err := classifyRunRows(context.Background(), nil, testRunID, rows); !errors.Is(err, ErrUpdateDraftEncryptionUnavailable) {
		t.Fatalf("no sealer: %v", err)
	}
	transport := errors.New("kms timeout")
	if _, err := classifyRunRows(context.Background(), runTestSealer{transportErr: transport}, testRunID, rows); !errors.Is(err, transport) {
		t.Fatalf("transport failure must be fatal: %v", err)
	}
	if _, err := classifyRunRows(context.Background(), runTestSealer{transportErr: sealing.ErrUnavailable}, testRunID, rows); !errors.Is(err, ErrUpdateDraftEncryptionUnavailable) {
		t.Fatalf("unavailable: %v", err)
	}
}

func TestCustomerUpdateRunQueryInputs(t *testing.T) {
	if _, err := runRowsQueryInput("tbl", "ABC"); !errors.Is(err, ErrInvalidUpdateRun) {
		t.Fatalf("bad run id: %v", err)
	}
	in, err := runRowsQueryInput("tbl", testRunID)
	if err != nil {
		t.Fatal(err)
	}
	if in.ConsistentRead == nil || !*in.ConsistentRead || !hasValue(in.ExpressionAttributeValues, "CURUN#"+testRunID) {
		t.Fatalf("query = %+v", in)
	}
	del, err := runRowKeysQueryInput("tbl", testRunID)
	if err != nil || del.ProjectionExpression == nil || !hasName(del.ExpressionAttributeNames, "SK") {
		t.Fatalf("keys query = %+v %v", del, err)
	}
}

func TestCustomerUpdateDecidedAccountsQuery(t *testing.T) {
	if _, err := decidedAccountsQueryInput("tbl", "u1", "AB", time.Unix(100, 0)); !errors.Is(err, ErrInvalidUpdateRun) {
		t.Fatalf("bad hash: %v", err)
	}
	in, err := decidedAccountsQueryInput("tbl", "u1", testHashA, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	filter := condString(in.FilterExpression)
	if in.ConsistentRead == nil || !*in.ConsistentRead || in.IndexName != nil || in.ProjectionExpression == nil ||
		!hasValue(in.ExpressionAttributeValues, "USER#u1") || !hasValue(in.ExpressionAttributeValues, "CUDEC#"+testHashA+"#") ||
		!strings.Contains(condString(in.KeyConditionExpression), "begins_with") {
		t.Fatalf("query = %+v", in)
	}
	for _, v := range []string{"not_needed", "drafted", "discarded", "100"} {
		if !hasValue(in.ExpressionAttributeValues, v) {
			t.Fatalf("filter %s lacks %s", filter, v)
		}
	}
	if hasValue(in.ExpressionAttributeValues, "deferred") || !hasName(in.ExpressionAttributeNames, "pendingShareExpiresAt") {
		t.Fatalf("filter = %s %v", filter, in.ExpressionAttributeValues)
	}
	got := decidedAccountIDs([]map[string]types.AttributeValue{
		{"SK": sAttr("CUDEC#" + testHashA + "#" + testAccount)},
		{"SK": sAttr("CUDEC#" + testHashB + "#" + testAccount)},
		{"SK": sAttr("CUDEC#" + testHashA + "#bad#id")},
		{"SK": nAttr("1")},
	}, testHashA)
	if !reflect.DeepEqual(got, []string{testAccount}) {
		t.Fatalf("ids = %v", got)
	}
}

func TestCustomerUpdateRunDeleteBatches(t *testing.T) {
	keys := make([]map[string]types.AttributeValue, 60)
	for i := range keys {
		keys[i] = map[string]types.AttributeValue{"PK": sAttr("CURUN#" + testRunID), "SK": sAttr("ITEM#" + strconv.Itoa(i))}
	}
	batches := runDeleteBatches("tbl", keys)
	if len(batches) != 3 || len(batches[0].RequestItems["tbl"]) != 25 || len(batches[2].RequestItems["tbl"]) != 10 {
		t.Fatalf("batches = %d", len(batches))
	}
	if batches[0].RequestItems["tbl"][0].DeleteRequest == nil {
		t.Fatal("delete request missing")
	}
	if len(runDeleteBatches("tbl", nil)) != 0 {
		t.Fatal("no keys, no batches")
	}
}

func TestCustomerUpdateRunLeaseInputs(t *testing.T) {
	start := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	run := &model.UpdateRun{RunID: testRunID, StartedAt: start, LeaseUntil: start.Add(15 * time.Minute), ItemHashes: []string{testHashA}, AccountIDs: []string{testAccount}}
	in, err := runLeasePutInput("tbl", "u1", run)
	if err != nil {
		t.Fatal(err)
	}
	c := condString(in.ConditionExpression)
	if !strings.Contains(c, "attribute_not_exists") || strings.Contains(c, " OR ") || !hasName(in.ExpressionAttributeNames, "PK") {
		t.Fatalf("condition = %s %v", c, in.ExpressionAttributeNames)
	}
	if in.Item["PK"].(*types.AttributeValueMemberS).Value != "USER#u1" || in.Item["SK"].(*types.AttributeValueMemberS).Value != "CURUN" {
		t.Fatalf("key = %v", in.Item)
	}
	wantTTL := strconv.FormatInt(start.Add(15*time.Minute+7*24*time.Hour).Unix(), 10)
	if in.Item["pendingShareExpiresAt"].(*types.AttributeValueMemberN).Value != wantTTL {
		t.Fatalf("ttl = %v", in.Item["pendingShareExpiresAt"])
	}
	if in.Item["entityType"] == nil || in.Item["runId"].(*types.AttributeValueMemberS).Value != testRunID {
		t.Fatalf("item = %v", in.Item)
	}
	back, err := runFromItem(in.Item)
	if err != nil || back.RunID != testRunID || !back.LeaseUntil.Equal(run.LeaseUntil) || len(back.ItemHashes) != 1 || back.AccountIDs[0] != testAccount {
		t.Fatalf("round trip = %+v %v", back, err)
	}
	empty := *run
	empty.ItemHashes, empty.AccountIDs = nil, nil
	in, err = runLeasePutInput("tbl", "u1", &empty)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := in.Item["itemHashes"].(*types.AttributeValueMemberL); !ok {
		t.Fatalf("nil hashes must be stored as an empty list: %T", in.Item["itemHashes"])
	}

	many := func(n int, f func(int) string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = f(i)
		}
		return out
	}
	bad := []func(r *model.UpdateRun){
		func(r *model.UpdateRun) { r.RunID = "x" },
		func(r *model.UpdateRun) { r.LeaseUntil = time.Time{} },
		func(r *model.UpdateRun) { r.ItemHashes = many(31, func(int) string { return testHashA }) },
		func(r *model.UpdateRun) { r.AccountIDs = many(41, func(i int) string { return "a" + strconv.Itoa(i) }) },
		func(r *model.UpdateRun) { r.ItemHashes = []string{"short"} },
		func(r *model.UpdateRun) { r.AccountIDs = []string{"a#b"} },
	}
	for i, mutate := range bad {
		r := *run
		mutate(&r)
		if _, err := runLeasePutInput("tbl", "u1", &r); !errors.Is(err, ErrInvalidUpdateRun) {
			t.Fatalf("case %d: %v", i, err)
		}
	}
	if _, err := runLeasePutInput("tbl", "", run); !errors.Is(err, ErrInvalidUpdateRun) {
		t.Fatalf("empty user: %v", err)
	}

	get := getRunInput("tbl", "u1")
	if get.ConsistentRead == nil || !*get.ConsistentRead || get.Key["SK"].(*types.AttributeValueMemberS).Value != "CURUN" {
		t.Fatalf("get = %+v", get)
	}

	del, err := runDeleteIfMatchInput("tbl", "u1", testRunID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(condString(del.ConditionExpression), "=") || !hasName(del.ExpressionAttributeNames, "runId") || !hasValue(del.ExpressionAttributeValues, testRunID) {
		t.Fatalf("delete = %s %v %v", condString(del.ConditionExpression), del.ExpressionAttributeNames, del.ExpressionAttributeValues)
	}
	if _, err := runDeleteIfMatchInput("tbl", "u1", "nope"); !errors.Is(err, ErrInvalidUpdateRun) {
		t.Fatalf("bad run id: %v", err)
	}
}

func TestCustomerUpdateRunDayDraftInput(t *testing.T) {
	in, err := dayDraftUpdateInput("tbl", "u1", "20261114", testAccount, 20, 3)
	if err != nil {
		t.Fatal(err)
	}
	if in.Key["SK"].(*types.AttributeValueMemberS).Value != "CUDAY#20261114" {
		t.Fatalf("key = %v", in.Key)
	}
	u, c := condString(in.UpdateExpression), condString(in.ConditionExpression)
	if !strings.Contains(u, "ADD") || !strings.Contains(c, "attribute_not_exists") || strings.Count(c, "<") != 2 {
		t.Fatalf("update = %s; condition = %s", u, c)
	}
	if !hasName(in.ExpressionAttributeNames, "total") || !hasName(in.ExpressionAttributeNames, "acct#"+testAccount) || !hasName(in.ExpressionAttributeNames, "pendingShareExpiresAt") {
		t.Fatalf("names = %v", namesOf(in.ExpressionAttributeNames))
	}
	if !hasValue(in.ExpressionAttributeValues, "20") || !hasValue(in.ExpressionAttributeValues, "3") || !hasValue(in.ExpressionAttributeValues, "1") {
		t.Fatalf("values = %v", in.ExpressionAttributeValues)
	}
	kst := time.FixedZone("KST", 9*3600)
	wantTTL := strconv.FormatInt(time.Date(2026, 11, 14, 0, 0, 0, 0, kst).Add(model.UpdateDayTTL).Unix(), 10)
	if !hasValue(in.ExpressionAttributeValues, wantTTL) {
		t.Fatalf("ttl %s missing: %v", wantTTL, in.ExpressionAttributeValues)
	}
	for _, tc := range []struct {
		day, acct  string
		total, per int
	}{
		{"2026-11-14", testAccount, 20, 3}, {"20261340", testAccount, 20, 3}, {"20261114", "a.b", 20, 3},
	} {
		if _, err := dayDraftUpdateInput("tbl", "u1", tc.day, tc.acct, tc.total, tc.per); !errors.Is(err, ErrInvalidUpdateRun) {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
	for _, lim := range [][2]int{{0, 3}, {20, 0}, {-1, 3}, {20, -1}} {
		in, err := dayDraftUpdateInput("tbl", "u1", "20261114", testAccount, lim[0], lim[1])
		if in != nil || !errors.Is(err, ErrConditionFailed) {
			t.Fatalf("limits %v: %v", lim, err)
		}
	}
	day := dayFromItem(map[string]types.AttributeValue{
		"PK": sAttr("USER#u1"), "SK": sAttr("CUDAY#20261114"), "total": nAttr("4"),
		"acct#a1": nAttr("3"), "acct#a2": nAttr("1"), "acct#bad": sAttr("x"), "pendingShareExpiresAt": nAttr("9"),
	})
	if day.Total != 4 || len(day.PerAccount) != 2 || day.PerAccount["a1"] != 3 || day.PerAccount["a2"] != 1 {
		t.Fatalf("day = %+v", day)
	}
	if d := dayFromItem(nil); d.Total != 0 || d.PerAccount == nil {
		t.Fatalf("absent day = %+v", d)
	}
}

func TestCustomerUpdateRunDayResearchedInput(t *testing.T) {
	in, err := dayResearchedUpdateInput("tbl", "u1", "20261114", 4)
	if err != nil {
		t.Fatal(err)
	}
	u := condString(in.UpdateExpression)
	if in.Key["SK"].(*types.AttributeValueMemberS).Value != "CUDAY#20261114" || !strings.Contains(u, "ADD") || in.ConditionExpression != nil {
		t.Fatalf("key = %v update = %s", in.Key, u)
	}
	if !hasName(in.ExpressionAttributeNames, "researched") || !hasName(in.ExpressionAttributeNames, "pendingShareExpiresAt") || !hasValue(in.ExpressionAttributeValues, "4") {
		t.Fatalf("names = %v values = %v", namesOf(in.ExpressionAttributeNames), in.ExpressionAttributeValues)
	}
	for _, tc := range []struct {
		day string
		n   int
	}{{"2026-11-14", 1}, {"20261114", 0}, {"20261114", -1}} {
		if _, err := dayResearchedUpdateInput("tbl", "u1", tc.day, tc.n); !errors.Is(err, ErrInvalidUpdateRun) {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
	day := dayFromItem(map[string]types.AttributeValue{"total": nAttr("2"), "researched": nAttr("7"), "acct#a1": nAttr("2")})
	if day.Researched != 7 || day.Total != 2 || len(day.PerAccount) != 1 {
		t.Fatalf("day = %+v", day)
	}
}

func TestCustomerUpdateRunPendingInputs(t *testing.T) {
	now := time.Date(2026, 11, 14, 1, 2, 3, 456789000, time.UTC)
	mark := pendingMarkInput("tbl", "u1", now)
	if mark.Item["updatedAt"].(*types.AttributeValueMemberS).Value != "2026-11-14T01:02:03.456789Z" {
		t.Fatalf("mark = %v", mark.Item)
	}
	clear, err := pendingClearInput("tbl", model.UpdatePendingMark{UserID: "u1", MarkedAt: "2026-11-14T01:02:03.456789Z"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(condString(clear.ConditionExpression), "=") || !hasName(clear.ExpressionAttributeNames, "updatedAt") || !hasValue(clear.ExpressionAttributeValues, "2026-11-14T01:02:03.456789Z") {
		t.Fatalf("clear = %s", condString(clear.ConditionExpression))
	}
	if clear.Key["PK"].(*types.AttributeValueMemberS).Value != "CUPENDING" || clear.Key["SK"].(*types.AttributeValueMemberS).Value != "u1" {
		t.Fatalf("key = %v", clear.Key)
	}
	legacy, err := pendingClearInput("tbl", model.UpdatePendingMark{UserID: "u1"})
	if err != nil || !strings.Contains(condString(legacy.ConditionExpression), "attribute_not_exists") || !hasName(legacy.ExpressionAttributeNames, "updatedAt") {
		t.Fatalf("legacy clear = %s %v", condString(legacy.ConditionExpression), err)
	}
	if _, err := pendingClearInput("tbl", model.UpdatePendingMark{}); err == nil {
		t.Fatal("empty user must be rejected")
	}
	marks := pendingMarksFromItems([]map[string]types.AttributeValue{
		{"PK": sAttr("CUPENDING"), "SK": sAttr("u1"), "updatedAt": sAttr("t1")},
		{"PK": sAttr("CUPENDING"), "SK": sAttr("u2")},
		{"PK": sAttr("CUPENDING"), "SK": sAttr("")},
		{"PK": sAttr("CUPENDING"), "SK": sAttr("#CURSOR"), "lastUserId": sAttr("u1")},
	})
	if len(marks) != 2 || marks[0] != (model.UpdatePendingMark{UserID: "u1", MarkedAt: "t1"}) || marks[1] != (model.UpdatePendingMark{UserID: "u2"}) {
		t.Fatalf("marks = %+v (the cursor row is never a mark)", marks)
	}
}

func TestCustomerUpdateRunPendingCursorInputs(t *testing.T) {
	put, err := pendingCursorPutInput("tbl", "u7")
	if err != nil || put.Item["PK"].(*types.AttributeValueMemberS).Value != "CUPENDING" ||
		put.Item["SK"].(*types.AttributeValueMemberS).Value != "#CURSOR" || put.Item["lastUserId"].(*types.AttributeValueMemberS).Value != "u7" {
		t.Fatalf("put = %+v %v", put, err)
	}
	if _, err := pendingCursorPutInput("tbl", ""); !errors.Is(err, ErrInvalidUpdateRun) {
		t.Fatalf("empty cursor: %v", err)
	}
	q, err := pendingCursorQueryInput("tbl")
	if err != nil || !hasValue(q.ExpressionAttributeValues, "#CURSOR") || !hasValue(q.ExpressionAttributeValues, "CUPENDING") || !*q.ConsistentRead {
		t.Fatalf("query = %+v %v", q, err)
	}
}

func TestCustomerUpdateRunItemInputs(t *testing.T) {
	in, err := itemStateUpdateInput("tbl", "u1", testHashA, model.UpdateItemSkipped, 1)
	if err != nil {
		t.Fatal(err)
	}
	u := condString(in.UpdateExpression)
	if !strings.Contains(u, "SET") || !strings.Contains(u, "ADD") || !strings.Contains(condString(in.ConditionExpression), "attribute_exists") {
		t.Fatalf("update = %s cond = %s", u, condString(in.ConditionExpression))
	}
	if !hasName(in.ExpressionAttributeNames, "state") || !hasName(in.ExpressionAttributeNames, "attempts") || !hasValue(in.ExpressionAttributeValues, "skipped") {
		t.Fatalf("names = %v", in.ExpressionAttributeNames)
	}
	if in.Key["SK"].(*types.AttributeValueMemberS).Value != model.UpdateItemSK(testHashA) {
		t.Fatalf("key = %v", in.Key)
	}
	noAdd, err := itemStateUpdateInput("tbl", "u1", testHashA, model.UpdateItemDecided, 0)
	if err != nil || strings.Contains(condString(noAdd.UpdateExpression), "ADD") {
		t.Fatalf("zero delta = %s %v", condString(noAdd.UpdateExpression), err)
	}
	for _, tc := range []struct {
		hash, state string
		delta       int
	}{
		{"x", model.UpdateItemNew, 0}, {testHashA, "deferred", 0}, {testHashA, model.UpdateItemNew, -1},
	} {
		if _, err := itemStateUpdateInput("tbl", "u1", tc.hash, tc.state, tc.delta); !errors.Is(err, ErrInvalidUpdateRun) {
			t.Fatalf("%+v: %v", tc, err)
		}
	}

	now0 := time.Unix(1_900_000_000, 0)
	q, err := newItemsQueryInput("tbl", "u1", "", "", 40)
	if err != nil {
		t.Fatal(err)
	}
	if !hasValue(q.ExpressionAttributeValues, model.PrefixUpdateItem) || !hasValue(q.ExpressionAttributeValues, model.UpdateItemNew) ||
		q.FilterExpression == nil || !strings.Contains(*q.KeyConditionExpression, "begins_with") || q.ExclusiveStartKey != nil || *q.Limit != 40 ||
		q.ConsistentRead == nil || !*q.ConsistentRead {
		t.Fatalf("query = %+v", q)
	}
	cur := model.UpdateItemSK(testHashA)
	q, err = newItemsQueryInput("tbl", "u1", cur, "", 10)
	if err != nil || q.ExclusiveStartKey["SK"].(*types.AttributeValueMemberS).Value != cur || q.ExclusiveStartKey["PK"].(*types.AttributeValueMemberS).Value != "USER#u1" {
		t.Fatalf("resume query = %+v %v", q, err)
	}
	q, err = newItemsQueryInput("tbl", "u1", "", cur, 10)
	if err != nil || !strings.Contains(*q.KeyConditionExpression, "BETWEEN") || !hasValue(q.ExpressionAttributeValues, cur) || q.ExclusiveStartKey != nil {
		t.Fatalf("wrap query = %+v %v", q, err)
	}
	cu, err := itemCursorUpdateInput("tbl", "u1", cur, now0)
	if err != nil || cu.ConditionExpression != nil || cu.Key["SK"].(*types.AttributeValueMemberS).Value != "CUCURSOR" ||
		!hasName(cu.ExpressionAttributeNames, "itemCursor") || !hasValue(cu.ExpressionAttributeValues, cur) ||
		!hasValue(cu.ExpressionAttributeValues, strconv.FormatInt(now0.Add(30*24*time.Hour).Unix(), 10)) {
		t.Fatalf("cursor update = %+v %v", cu, err)
	}
	if strings.Contains(condString(cu.UpdateExpression), "ADD") {
		t.Fatalf("cursor update = %s", condString(cu.UpdateExpression))
	}
	for c, want := range map[string]string{cur: cur, "CUITEM#xyz": "", "": "", "CUDRAFT#" + testHashA: ""} {
		if got := cursorFromItem(map[string]types.AttributeValue{"itemCursor": sAttr(c)}); got != want {
			t.Fatalf("cursor %q = %q", c, got)
		}
	}
	if cursorFromItem(nil) != "" || cursorFromItem(map[string]types.AttributeValue{"itemCursor": nAttr("1")}) != "" {
		t.Fatal("absent or non-string cursor must be ignored")
	}

	now := time.Unix(2_000_000_000, 0)
	t0 := time.Unix(1_999_000_000, 0)
	rows := []model.UpdateItem{
		{URLHash: testHashB, State: "new", FirstSeenAt: t0, ExpiresAt: now.Unix() + 10},
		{URLHash: testHashA, State: "new", FirstSeenAt: t0, ExpiresAt: now.Unix() + 10},
		{URLHash: "c", State: "new", FirstSeenAt: t0.Add(-time.Hour), ExpiresAt: now.Unix() + 10},
		{URLHash: "d", State: "decided", FirstSeenAt: t0.Add(-2 * time.Hour), ExpiresAt: now.Unix() + 10},
		{URLHash: "e", State: "new", FirstSeenAt: t0.Add(-3 * time.Hour), ExpiresAt: now.Unix()},
	}
	got := oldestNewItems(rows, now, 2)
	if len(got) != 2 || got[0].URLHash != "c" || got[1].URLHash != testHashA {
		t.Fatalf("oldest = %+v", got)
	}
}

func TestCustomerUpdateReplaceOpenDecisionInput(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	d := &model.UpdateDecision{URLHash: testHashA, AccountID: testAccount, Choice: model.UpdateChoiceNotNeeded, Reason: "secret", RunID: testRunID}
	in, err := decisionReplaceOpenInput(context.Background(), runTestSealer{}, "tbl", "u1", d, now)
	if err != nil {
		t.Fatal(err)
	}
	c := condString(in.ConditionExpression)
	if !strings.Contains(c, "attribute_exists") || !strings.Contains(c, "NOT") || strings.Contains(c, "attribute_not_exists") ||
		!hasValue(in.ExpressionAttributeValues, "needed") || !hasValue(in.ExpressionAttributeValues, "drafted") ||
		!hasValue(in.ExpressionAttributeValues, "discarded") || !hasValue(in.ExpressionAttributeValues, "1800000000") {
		t.Fatalf("condition = %s %v", c, in.ExpressionAttributeValues)
	}
	if got, err := decisionFromItem(context.Background(), runTestSealer{}, "u1", in.Item); err != nil || got.Reason != "secret" || got.Choice != d.Choice {
		t.Fatalf("row = %+v %v", got, err)
	}
	if _, err := decisionReplaceOpenInput(context.Background(), nil, "tbl", "u1", d, now); !errors.Is(err, ErrUpdateDraftEncryptionUnavailable) {
		t.Fatalf("no sealer: %v", err)
	}
}

func TestCustomerUpdateRunDecisionRoundTrip(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	d := &model.UpdateDecision{
		URLHash: testHashA, AccountID: testAccount, Choice: model.UpdateChoiceNeeded, Score: 0.82, Reason: "secret-reason",
		GateInput: &model.UpdateGateInput{Question: "q", Options: []model.UpdateGateOption{{ID: "needed", Text: "secret-option"}}, Context: "secret-context"},
		Backend:   "bedrock", RunID: testRunID, Outcome: model.UpdateOutcomeDrafted,
	}
	in, err := decisionPutInput(context.Background(), runTestSealer{}, "tbl", "u1", d, now)
	if err != nil {
		t.Fatal(err)
	}
	assertAbsentOrExpired(t, in, now)
	allowed := map[string]bool{"PK": true, "SK": true, "entityType": true, "urlHash": true, "accountId": true, "choice": true, "score": true,
		"backend": true, "runId": true, "outcome": true, "discardReason": true, "decidedAt": true, "pendingShareExpiresAt": true, "sealed": true}
	for name, v := range in.Item {
		if !allowed[name] {
			t.Fatalf("attribute %q stored", name)
		}
		if s, ok := v.(*types.AttributeValueMemberS); ok && name != "sealed" && strings.Contains(s.Value, "secret") {
			t.Fatalf("%s holds plaintext", name)
		}
	}
	if in.Item["SK"].(*types.AttributeValueMemberS).Value != model.UpdateDecisionSK(testHashA, testAccount) {
		t.Fatalf("SK = %v", in.Item["SK"])
	}
	wantTTL := strconv.FormatInt(now.Add(model.UpdateDecisionTTL).Unix(), 10)
	if in.Item["pendingShareExpiresAt"].(*types.AttributeValueMemberN).Value != wantTTL {
		t.Fatalf("ttl = %v", in.Item["pendingShareExpiresAt"])
	}
	if d.DecidedAt != (time.Time{}) || d.ExpiresAt != 0 {
		t.Fatal("input decision must not be mutated")
	}
	got, err := decisionFromItem(context.Background(), runTestSealer{}, "u1", in.Item)
	if err != nil {
		t.Fatal(err)
	}
	if got.Reason != "secret-reason" || got.GateInput == nil || got.GateInput.Options[0].Text != "secret-option" || got.Score != 0.82 ||
		got.RunID != testRunID || !got.DecidedAt.Equal(now) || got.Outcome != model.UpdateOutcomeDrafted {
		t.Fatalf("round trip = %+v", got)
	}
	if _, err := decisionFromItem(context.Background(), runTestSealer{}, "u2", in.Item); !errors.Is(err, ErrUpdateDecisionUnreadable) {
		t.Fatalf("other user: %v", err)
	}
	if _, err := decisionFromItem(context.Background(), nil, "u1", in.Item); !errors.Is(err, ErrUpdateDraftEncryptionUnavailable) {
		t.Fatalf("no sealer: %v", err)
	}
	moved := map[string]types.AttributeValue{}
	for k, v := range in.Item {
		moved[k] = v
	}
	moved["SK"] = sAttr(model.UpdateDecisionSK(testHashB, testAccount))
	moved["urlHash"] = sAttr(testHashB)
	if _, err := decisionFromItem(context.Background(), runTestSealer{}, "u1", moved); !errors.Is(err, ErrUpdateDecisionUnreadable) {
		t.Fatalf("wrong binding: %v", err)
	}
	if _, err := decisionPutInput(context.Background(), nil, "tbl", "u1", d, now); !errors.Is(err, ErrUpdateDraftEncryptionUnavailable) {
		t.Fatalf("write without sealer: %v", err)
	}

	notNeeded := *d
	notNeeded.Choice = model.UpdateChoiceNotNeeded
	in, err = decisionPutInput(context.Background(), runTestSealer{}, "tbl", "u1", &notNeeded, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err = decisionFromItem(context.Background(), runTestSealer{}, "u1", in.Item)
	if err != nil || got.GateInput != nil || got.Reason != "secret-reason" {
		t.Fatalf("not_needed keeps no gate input: %+v %v", got, err)
	}

	for i, mutate := range []func(*model.UpdateDecision){
		func(x *model.UpdateDecision) { x.URLHash = "x" },
		func(x *model.UpdateDecision) { x.AccountID = "a#b" },
		func(x *model.UpdateDecision) { x.Choice = "maybe" },
		func(x *model.UpdateDecision) { x.Outcome = "weird" },
	} {
		x := *d
		mutate(&x)
		if _, err := decisionPutInput(context.Background(), runTestSealer{}, "tbl", "u1", &x, now); !errors.Is(err, ErrInvalidUpdateRun) {
			t.Fatalf("case %d: %v", i, err)
		}
	}

	up, err := decisionOutcomeUpdateInput("tbl", "u1", testHashA, testAccount, model.UpdateOutcomeDiscarded, "figure")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(condString(up.ConditionExpression), "attribute_exists") || hasName(up.ExpressionAttributeNames, "sealed") ||
		!hasName(up.ExpressionAttributeNames, "outcome") || !hasName(up.ExpressionAttributeNames, "discardReason") || !hasValue(up.ExpressionAttributeValues, "figure") {
		t.Fatalf("outcome update = %s %v", condString(up.UpdateExpression), up.ExpressionAttributeNames)
	}
	if _, err := decisionOutcomeUpdateInput("tbl", "u1", testHashA, testAccount, "weird", ""); !errors.Is(err, ErrInvalidUpdateRun) {
		t.Fatalf("bad outcome: %v", err)
	}
	g, err := getDecisionInput("tbl", "u1", testHashA, testAccount)
	if err != nil || g.ConsistentRead == nil || !*g.ConsistentRead || g.Key["SK"].(*types.AttributeValueMemberS).Value != model.UpdateDecisionSK(testHashA, testAccount) {
		t.Fatalf("get = %+v %v", g, err)
	}
	for _, k := range [][2]string{{"x", testAccount}, {testHashA, "a#b"}, {testHashA, ""}} {
		if g, err := getDecisionInput("tbl", "u1", k[0], k[1]); g != nil || !errors.Is(err, ErrInvalidUpdateRun) {
			t.Fatalf("key %v: %v", k, err)
		}
	}
}

// fakeItemPager serves a sorted in-memory item partition the way Query does:
// it evaluates at most min(limit, pageCap) rows after startAfter and up to upTo
// inclusive, returns only state = new rows, and reports the last evaluated key
// whenever it stopped on a row count (even if nothing follows).
type fakeItemPager struct {
	rows      []updateItemRow
	pageCap   int
	evaluated []string
}

func (f *fakeItemPager) page(_ context.Context, startAfter, upTo string, limit int) (itemScanPage, error) {
	n := min(limit, f.pageCap)
	var p itemScanPage
	for _, row := range f.rows {
		if row.SK <= startAfter && startAfter != "" || upTo != "" && row.SK > upTo {
			continue
		}
		f.evaluated = append(f.evaluated, row.SK)
		p.Scanned++
		if row.State == model.UpdateItemNew {
			p.Rows = append(p.Rows, row)
		}
		if p.Scanned == n {
			p.LastSK = row.SK
			break
		}
	}
	return p, nil
}

func scanTestRows(n int, newAt map[int]bool, expires int64) []updateItemRow {
	rows := make([]updateItemRow, n)
	for i := range rows {
		h := fmt.Sprintf("%064x", i*2) // even hashes, so odd ones are absent keys
		state := model.UpdateItemDecided
		if newAt[i] {
			state = model.UpdateItemNew
		}
		rows[i] = updateItemRow{
			customerUpdateKeys: customerUpdateKeys{PK: "USER#u1", SK: model.UpdateItemSK(h)},
			UpdateItem:         model.UpdateItem{URLHash: h, State: state, ExpiresAt: expires},
		}
	}
	return rows
}

func TestCustomerUpdateRunScanVisitsEveryRowAcrossCalls(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	// 100 rows, one new item at index 90, far past one call's 30-row window.
	f := &fakeItemPager{rows: scanTestRows(100, map[int]bool{90: true}, now.Unix()+100), pageCap: 7}
	cursor, visited := "", map[string]bool{}
	var found []string
	for call := 0; call < 4; call++ {
		f.evaluated = nil
		got, pos, _, _, err := scanNewItems(context.Background(), f.page, cursor, 5, 30, now)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, sk := range f.evaluated {
			if seen[sk] {
				t.Fatalf("call %d evaluated %s twice", call, sk)
			}
			seen[sk], visited[sk] = true, true
		}
		if len(f.evaluated) > 30 {
			t.Fatalf("call %d evaluated %d rows", call, len(f.evaluated))
		}
		for _, row := range got {
			found = append(found, row.URLHash)
		}
		cursor = pos
	}
	if len(visited) != 100 {
		t.Fatalf("4 calls visited %d of 100 rows", len(visited))
	}
	if len(found) != 1 || found[0] != f.rows[90].URLHash {
		t.Fatalf("found = %v", found)
	}
}

func TestCustomerUpdateRunScanFullCycleAndWrap(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	f := &fakeItemPager{rows: scanTestRows(20, map[int]bool{2: true, 15: true, 16: true}, now.Unix()+100), pageCap: 4}
	f.rows[16].ExpiresAt = now.Unix() // expired but unswept: not returned
	// A cursor between rows 9 and 10 that names no stored row (an expired item).
	cursor := model.UpdateItemSK(fmt.Sprintf("%064x", 19))
	got, pos, _, _, err := scanNewItems(context.Background(), f.page, cursor, 10, 2000, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.evaluated) != 20 {
		t.Fatalf("full cycle evaluated %d rows", len(f.evaluated))
	}
	seen := map[string]bool{}
	for _, sk := range f.evaluated {
		if seen[sk] {
			t.Fatalf("%s evaluated twice", sk)
		}
		seen[sk] = true
	}
	if f.evaluated[0] != f.rows[10].SK {
		t.Fatalf("scan must start after the cursor, started at %s", f.evaluated[0])
	}
	if len(got) != 2 || got[0].URLHash != f.rows[15].URLHash || got[1].URLHash != f.rows[2].URLHash {
		t.Fatalf("got = %+v", got)
	}
	if pos != cursor {
		t.Fatalf("a full cycle ends at the cursor, got %s", pos)
	}

	// No cursor: one leg; reaching the end resets the position to the start.
	f.evaluated = nil
	_, pos, _, _, err = scanNewItems(context.Background(), f.page, "", 10, 2000, now)
	if err != nil || pos != "" || len(f.evaluated) != 20 {
		t.Fatalf("pos = %q evaluated %d %v", pos, len(f.evaluated), err)
	}
}

func TestCustomerUpdateRunScanStopsAtWantWithoutSkipping(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	f := &fakeItemPager{rows: scanTestRows(10, map[int]bool{1: true, 2: true, 3: true}, 0), pageCap: 10}
	got, pos, _, _, err := scanNewItems(context.Background(), f.page, "", 2, 2000, now)
	if err != nil || len(got) != 2 || pos != f.rows[2].SK {
		t.Fatalf("got %d pos %s %v", len(got), pos, err)
	}
	got, _, _, _, err = scanNewItems(context.Background(), f.page, pos, 2, 2000, now)
	if err != nil || len(got) < 1 || got[0].URLHash != f.rows[3].URLHash {
		t.Fatalf("row 3, in the same page as the first stop, must not be skipped: %+v %v", got, err)
	}
}

func TestCustomerUpdateRunScanReportsFullCycle(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	scan := func(rows int, newAt map[int]bool, cursor string, want int) (string, bool) {
		f := &fakeItemPager{rows: scanTestRows(rows, newAt, 0), pageCap: 1000}
		_, pos, _, complete, err := scanNewItems(context.Background(), f.page, cursor, want, maxNewItemsScan, now)
		if err != nil {
			t.Fatal(err)
		}
		return pos, complete
	}
	// 2,500 rows with nothing new: no single 2,000-row window is a full cycle.
	pos, complete := scan(2500, nil, "", 30)
	if complete {
		t.Fatal("a 2,000-row window of 2,500 rows reported a full cycle")
	}
	if _, complete = scan(2500, nil, pos, 30); complete {
		t.Fatal("the wrapped window of 2,500 rows reported a full cycle")
	}
	// Fewer rows than the bound: one call covers the prefix, with or without a cursor.
	if _, complete = scan(1500, nil, "", 30); !complete {
		t.Fatal("1,500 rows from the start: want a full cycle")
	}
	mid := model.UpdateItemSK(fmt.Sprintf("%064x", 701))
	if pos, complete = scan(1500, nil, mid, 30); !complete || pos != mid {
		t.Fatalf("1,500 rows from a cursor: complete=%v pos=%s", complete, pos)
	}
	// Stopping at want is not a full cycle.
	if _, complete = scan(10, map[int]bool{1: true, 2: true}, "", 1); complete {
		t.Fatal("a stop at want reported a full cycle")
	}
}

func TestCustomerUpdateRunScanPropagatesPageError(t *testing.T) {
	boom := errors.New("query failed")
	_, _, _, _, err := scanNewItems(context.Background(), func(context.Context, string, string, int) (itemScanPage, error) {
		return itemScanPage{}, boom
	}, "", 5, 2000, time.Now())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

// jsonEscape returns the six-byte JSON escape backslash, 'u', hex. Building it
// at run time keeps the test source free of escapes that editors or tools
// might decode into literal characters.
func jsonEscape(hex string) string { return `\` + "u" + hex }

func TestCustomerUpdateRunLoneSurrogateScanner(t *testing.T) {
	bs := `\`
	for raw, want := range map[string]bool{
		`"plain"`: false,
		`"` + jsonEscape("d83d") + jsonEscape("de00") + `"`:          false, // U+1F600 as a pair
		`"` + jsonEscape("DBFF") + jsonEscape("DFFF") + `"`:          false, // U+10FFFF as a pair
		`"` + bs + jsonEscape("d800") + `"`:                          false, // escaped backslash, then text "ud800"
		`"` + bs + bs + jsonEscape("d800") + `"`:                     true,  // escaped backslash, then a lone escape
		`"` + jsonEscape("d800") + `"`:                               true,
		`"` + jsonEscape("dfff") + `"`:                               true,
		`"` + jsonEscape("d800") + jsonEscape("d800") + `"`:          true,
		`"` + jsonEscape("d800") + jsonEscape("0041") + `"`:          true,
		`"` + jsonEscape("00e9") + `\n\"` + jsonEscape("0041") + `"`: false,
		`"` + jsonEscape("d800") + `x"`:                              true,
		`"` + jsonEscape("d83d"):                                     true, // high surrogate at the end
		`"` + jsonEscape("de00") + jsonEscape("d83d") + `"`:          true, // reversed pair
	} {
		if got := hasLoneSurrogateEscape([]byte(raw)); got != want {
			t.Fatalf("%s: got %v want %v", raw, got, want)
		}
	}
	if !strings.Contains(jsonEscape("d800"), "u") || len(jsonEscape("d800")) != 6 {
		t.Fatal("jsonEscape must build the six-byte escape")
	}
}
