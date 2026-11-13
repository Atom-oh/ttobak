package repository

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// assertAbsentOrExpired checks the create condition admits an absent row or an
// expired TTL row (pendingShareExpiresAt <= now).
func assertAbsentOrExpired(t *testing.T, in *dynamodb.PutItemInput, now time.Time) {
	t.Helper()
	if in.ConditionExpression == nil {
		t.Fatal("condition missing")
	}
	c := *in.ConditionExpression
	if !strings.Contains(c, "attribute_not_exists") || !strings.Contains(c, " OR ") || !strings.Contains(c, "<=") {
		t.Fatalf("condition = %s", c)
	}
	var ttlName bool
	for _, n := range in.ExpressionAttributeNames {
		if n == "pendingShareExpiresAt" {
			ttlName = true
		}
	}
	if !ttlName {
		t.Fatalf("TTL attribute not referenced: %v", in.ExpressionAttributeNames)
	}
	var nowValue bool
	for _, v := range in.ExpressionAttributeValues {
		if n, ok := v.(*types.AttributeValueMemberN); ok && n.Value == strconv.FormatInt(now.Unix(), 10) {
			nowValue = true
		}
	}
	if !nowValue {
		t.Fatalf(":now value %d missing: %v", now.Unix(), in.ExpressionAttributeValues)
	}
}

func TestCustomerUpdateCreatesTreatExpiredRowsAsAbsent(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	item, err := itemPutInput("tbl", "u1", &model.UpdateItem{URLHash: "h1", SourceIDs: []string{"s1"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	assertAbsentOrExpired(t, item, now)
	url, err := urlPendingInput("tbl", "h1", "https://example.com/a", now)
	if err != nil {
		t.Fatal(err)
	}
	assertAbsentOrExpired(t, url, now)
	draft, err := draftPutInput("tbl", "u1", &model.UpdateDraft{AccountID: "a1", URLHash: "h1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	assertAbsentOrExpired(t, draft, now)
}

func TestCustomerUpdateDraftPutInputStoresEmptyLists(t *testing.T) {
	d := &model.UpdateDraft{AccountID: "a1", URLHash: "h1"}
	in, err := draftPutInput("tbl", "u1", d, time.Unix(1000, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"opportunities", "links"} {
		l, ok := in.Item[name].(*types.AttributeValueMemberL)
		if !ok || len(l.Value) != 0 {
			t.Fatalf("%s = %#v, want empty list", name, in.Item[name])
		}
	}
	if d.Opportunities != nil || d.Links != nil || d.ExpiresAt != 0 || d.Status != "" {
		t.Fatal("input draft must not be mutated")
	}
	if got := in.Item["status"].(*types.AttributeValueMemberS).Value; got != model.UpdateDraftNew {
		t.Fatalf("status = %q", got)
	}
}

func TestCustomerUpdateDraftRevisionUpdateNeverWritesNull(t *testing.T) {
	expr, err := draftRevisionUpdate(&model.UpdateDraft{AccountID: "a1", URLHash: "h"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	empties := 0
	for _, v := range expr.Values() {
		if _, ok := v.(*types.AttributeValueMemberNULL); ok {
			t.Fatalf("NULL value written: %v", expr.Values())
		}
		if l, ok := v.(*types.AttributeValueMemberL); ok && len(l.Value) == 0 {
			empties++
		}
	}
	if empties != 2 {
		t.Fatalf("want 2 empty lists (opportunities, links), got %d: %v", empties, expr.Values())
	}
}

func TestCustomerUpdateDraftRevisionUpdate(t *testing.T) {
	d := &model.UpdateDraft{AccountID: "acc1", URLHash: "h", EditedBody: "x", Status: model.UpdateDraftNew}
	expr, err := draftRevisionUpdate(d, 4)
	if err != nil {
		t.Fatal(err)
	}
	if expr.Condition() == nil || !strings.Contains(*expr.Condition(), "attribute_exists") {
		t.Fatalf("condition missing existence check: %v", expr.Condition())
	}
	var nums []string
	for _, v := range expr.Values() {
		if n, ok := v.(*types.AttributeValueMemberN); ok {
			nums = append(nums, n.Value)
		}
	}
	has := func(want string) bool {
		for _, n := range nums {
			if n == want {
				return true
			}
		}
		return false
	}
	if !has("4") || !has("5") {
		t.Fatalf("expected values 4 (condition) and 5 (new revision), got %v", nums)
	}
	foundRevision := false
	for _, n := range expr.Names() {
		if n == "revision" {
			foundRevision = true
		}
	}
	if !foundRevision {
		t.Fatalf("revision attribute not referenced: %v", expr.Names())
	}
}

func TestCustomerUpdateItemPutInputRequiresAbsent(t *testing.T) {
	it := model.UpdateItem{URLHash: "h1", URL: "https://example.com/a", SourceIDs: []string{"s1"}}
	in, err := itemPutInput("tbl", "u1", &it, time.Unix(1000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if in.ConditionExpression == nil || !strings.Contains(*in.ConditionExpression, "attribute_not_exists") {
		t.Fatalf("condition = %v", in.ConditionExpression)
	}
	if got := in.Item["PK"].(*types.AttributeValueMemberS).Value; got != "USER#u1" {
		t.Fatalf("PK = %q", got)
	}
	if got := in.Item["SK"].(*types.AttributeValueMemberS).Value; got != model.UpdateItemSK("h1") {
		t.Fatalf("SK = %q", got)
	}
	if _, ok := in.Item["sourceIds"].(*types.AttributeValueMemberSS); !ok {
		t.Fatalf("sourceIds must be a string set, got %T", in.Item["sourceIds"])
	}
	if _, ok := in.Item["pendingShareExpiresAt"].(*types.AttributeValueMemberN); !ok {
		t.Fatalf("TTL attribute missing: %v", in.Item)
	}
	if it.ExpiresAt != 0 {
		t.Fatal("input item must not be mutated")
	}
}

func TestCustomerUpdateListDraftsKeyCondition(t *testing.T) {
	for _, tc := range []struct{ account, prefix string }{
		{"acc1", "CUDRAFT#acc1#"},
		{"", "CUDRAFT#"},
	} {
		expr, err := draftListExpression("u1", tc.account)
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, v := range expr.Values() {
			if s, ok := v.(*types.AttributeValueMemberS); ok && s.Value == tc.prefix {
				found = true
			}
		}
		if !found {
			t.Fatalf("prefix %q not in values %v", tc.prefix, expr.Values())
		}
		if !strings.Contains(*expr.KeyCondition(), "begins_with") {
			t.Fatalf("key condition = %s", *expr.KeyCondition())
		}
	}
}

func TestCustomerUpdateAddSourceExpression(t *testing.T) {
	expr, err := itemAddSourceExpression("s2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*expr.Update(), "ADD") {
		t.Fatalf("update = %s", *expr.Update())
	}
	var ss bool
	for _, v := range expr.Values() {
		if set, ok := v.(*types.AttributeValueMemberSS); ok && len(set.Value) == 1 && set.Value[0] == "s2" {
			ss = true
		}
	}
	if !ss {
		t.Fatalf("string set value missing: %v", expr.Values())
	}
}

func TestCustomerUpdateURLPendingKey(t *testing.T) {
	in, err := urlPendingInput("tbl", "abc", "https://example.com/a", time.Unix(1000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := in.Item["PK"].(*types.AttributeValueMemberS).Value; got != "CUURL#abc" {
		t.Fatalf("PK = %q", got)
	}
	if got := in.Item["SK"].(*types.AttributeValueMemberS).Value; got != "META" {
		t.Fatalf("SK = %q", got)
	}
	if in.ConditionExpression == nil || !strings.Contains(*in.ConditionExpression, "attribute_not_exists") {
		t.Fatalf("condition = %v", in.ConditionExpression)
	}
}
