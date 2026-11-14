package repository

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/sealing"
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
	draft, err := draftPutInput(context.Background(), draftTestSealer{}, "tbl", "u1", &model.UpdateDraft{AccountID: "a1", URLHash: "h1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	assertAbsentOrExpired(t, draft, now)
}

// draftTestSealer is a reversible sealer that enforces the full draft binding.
type draftTestSealer struct{}

func draftTestBindingID(b map[string]string) string {
	return b["purpose"] + "/" + b["userId"] + "/" + b["accountId"] + "/" + b["urlHash"]
}

func (draftTestSealer) Seal(_ context.Context, p []byte, b map[string]string) (string, error) {
	return base64.StdEncoding.EncodeToString([]byte(draftTestBindingID(b) + "|" + string(p))), nil
}

func (draftTestSealer) Open(_ context.Context, sealed string, b map[string]string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	prefix := draftTestBindingID(b) + "|"
	if err != nil || !strings.HasPrefix(string(raw), prefix) {
		return nil, sealing.ErrInvalid
	}
	return raw[len(prefix):], nil
}

func secretDraft() *model.UpdateDraft {
	return &model.UpdateDraft{
		AccountID: "a1", URLHash: "h1", SourceURL: "https://example.com/secret-url", SourceTitle: "secret-title",
		Why: "secret-why", Body: "secret-body", EditedBody: "secret-edited", DismissReason: "not_now",
		Opportunities: []model.UpdateDraftOppty{{ProjectID: "p1", Name: "secret-oppty", Stage: "s"}},
		Links:         []string{"https://example.com/secret-link"}, Revision: 2, RunID: "r1",
	}
}

func TestCustomerUpdateDraftPutInputSealsText(t *testing.T) {
	d := secretDraft()
	in, err := draftPutInput(context.Background(), draftTestSealer{}, "tbl", "u1", d, time.Unix(1000, 0))
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"PK": true, "SK": true, "entityType": true, "accountId": true, "urlHash": true, "status": true,
		"revision": true, "runId": true, "createdAt": true, "updatedAt": true, "pendingShareExpiresAt": true, "sealed": true}
	for name, v := range in.Item {
		if !allowed[name] {
			t.Fatalf("plaintext attribute %q stored", name)
		}
		if s, ok := v.(*types.AttributeValueMemberS); ok && name != "sealed" && strings.Contains(s.Value, "secret") {
			t.Fatalf("%s holds draft text", name)
		}
	}
	if in.Item["sealed"] == nil || in.Item["status"].(*types.AttributeValueMemberS).Value != model.UpdateDraftNew {
		t.Fatalf("item = %v", in.Item)
	}
	if d.ExpiresAt != 0 || d.Status != "" {
		t.Fatal("input draft must not be mutated")
	}

	got, err := draftFromItem("u1", in.Item)
	if err != nil || got.Body != "" || got.Sealed == "" {
		t.Fatalf("listed draft must stay unopened: %+v %v", got, err)
	}
	if err := openDraft(context.Background(), draftTestSealer{}, "u1", &got); err != nil {
		t.Fatal(err)
	}
	if got.Body != d.Body || got.EditedBody != d.EditedBody || got.Why != d.Why || got.SourceURL != d.SourceURL ||
		got.SourceTitle != d.SourceTitle || got.DismissReason != d.DismissReason || len(got.Opportunities) != 1 || len(got.Links) != 1 || got.Revision != 2 {
		t.Fatalf("round trip = %+v", got)
	}

	other, _ := draftFromItem("u2", in.Item)
	if err := openDraft(context.Background(), draftTestSealer{}, "u2", &other); !errors.Is(err, ErrUpdateDraftUnreadable) {
		t.Fatalf("row under another user: %v", err)
	}
	moved, _ := draftFromItem("u1", in.Item)
	moved.URLHash = "h2"
	if err := openDraft(context.Background(), draftTestSealer{}, "u1", &moved); !errors.Is(err, ErrUpdateDraftUnreadable) {
		t.Fatalf("wrong binding: %v", err)
	}
	if err := openDraft(context.Background(), nil, "u1", &got); !errors.Is(err, ErrUpdateDraftEncryptionUnavailable) {
		t.Fatalf("open without sealer: %v", err)
	}
	unsealed := model.UpdateDraft{AccountID: "a1", URLHash: "h1", Body: "plain"}
	if err := openDraft(context.Background(), draftTestSealer{}, "u1", &unsealed); !errors.Is(err, ErrUpdateDraftUnreadable) {
		t.Fatalf("unsealed row: %v", err)
	}
	if _, err := draftPutInput(context.Background(), nil, "tbl", "u1", d, time.Unix(1000, 0)); !errors.Is(err, ErrUpdateDraftEncryptionUnavailable) {
		t.Fatalf("write without sealer: %v", err)
	}
}

func TestCustomerUpdateDraftRevisionUpdate(t *testing.T) {
	d := secretDraft()
	sealed, err := sealDraft(context.Background(), draftTestSealer{}, "u1", d)
	if err != nil {
		t.Fatal(err)
	}
	expr, err := draftRevisionUpdate(d, 4, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if expr.Condition() == nil || !strings.Contains(*expr.Condition(), "attribute_exists") {
		t.Fatalf("condition missing existence check: %v", expr.Condition())
	}
	var nums []string
	var resealed bool
	for _, v := range expr.Values() {
		switch v := v.(type) {
		case *types.AttributeValueMemberN:
			nums = append(nums, v.Value)
		case *types.AttributeValueMemberS:
			if v.Value == sealed {
				resealed = true
			} else if strings.Contains(v.Value, "secret") {
				t.Fatalf("plaintext draft text written: %v", v.Value)
			}
		case *types.AttributeValueMemberNULL, *types.AttributeValueMemberL:
			t.Fatalf("unexpected value %T", v)
		}
	}
	if !resealed {
		t.Fatal("sealed payload not written")
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
	names := map[string]bool{}
	for _, n := range expr.Names() {
		names[n] = true
	}
	for _, n := range []string{"revision", "sealed", "status", "updatedAt"} {
		if !names[n] {
			t.Fatalf("%s not referenced: %v", n, expr.Names())
		}
	}
	for _, n := range []string{"body", "editedBody", "why", "opportunities", "links", "dismissReason"} {
		if names[n] {
			t.Fatalf("plaintext %s written", n)
		}
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

func TestCustomerUpdateGetSourceInputIsConsistent(t *testing.T) {
	in := getSourceInput("tbl", "u1", "src1")
	if in.ConsistentRead == nil || !*in.ConsistentRead {
		t.Fatal("ConsistentRead must be true")
	}
	if pk := in.Key["PK"].(*types.AttributeValueMemberS).Value; pk != model.PrefixUser+"u1" {
		t.Fatalf("PK = %s", pk)
	}
	if sk := in.Key["SK"].(*types.AttributeValueMemberS).Value; sk != model.UpdateSourceSK("src1") {
		t.Fatalf("SK = %s", sk)
	}
}
