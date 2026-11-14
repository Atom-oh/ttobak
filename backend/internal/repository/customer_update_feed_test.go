package repository

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

func feedTestSub() *model.UpdateFeedSub {
	return &model.UpdateFeedSub{
		FeedHash: "fh1", URL: "https://example.com/feed", Kind: model.UpdateSourceKindRSS,
		UserID: "u1", SourceID: "s1", Status: model.UpdateSourceActive,
	}
}

func feedAttrS(t *testing.T, m map[string]types.AttributeValue, k string) string {
	t.Helper()
	v, ok := m[k].(*types.AttributeValueMemberS)
	if !ok {
		t.Fatalf("attribute %s missing or not a string: %v", k, m[k])
	}
	return v.Value
}

func TestPutSourceWithFeedSubItems(t *testing.T) {
	src := &model.UpdateSource{SourceID: "s1", Kind: model.UpdateSourceKindRSS, Label: "B", URL: "https://example.com/feed", Status: model.UpdateSourceActive}
	items, err := putSourceWithFeedSubItems("tbl", "u1", src, feedTestSub())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Put == nil || items[1].Put == nil {
		t.Fatalf("want two puts, got %+v", items)
	}
	srcPut, subPut := items[0].Put, items[1].Put
	if feedAttrS(t, srcPut.Item, "PK") != "USER#u1" || feedAttrS(t, srcPut.Item, "SK") != "CUSRC#s1" {
		t.Fatalf("source keys = %v", srcPut.Item)
	}
	if srcPut.ConditionExpression == nil || !strings.Contains(*srcPut.ConditionExpression, "attribute_not_exists") {
		t.Fatalf("source put must be attribute_not_exists, got %v", srcPut.ConditionExpression)
	}
	if feedAttrS(t, subPut.Item, "PK") != "CUFEEDS" || feedAttrS(t, subPut.Item, "SK") != "fh1#u1#s1" {
		t.Fatalf("registry keys = %v", subPut.Item)
	}
	if feedAttrS(t, subPut.Item, "entityType") != "UPDATE_FEED_SUB" {
		t.Fatalf("entityType = %v", subPut.Item["entityType"])
	}
	for _, k := range []string{"feedHash", "url", "kind", "userId", "sourceId", "status", "fannedAt"} {
		if _, ok := subPut.Item[k]; !ok {
			t.Errorf("registry row lacks %s", k)
		}
	}
}

func TestSetSourceStatusWithFeedSubItemsUpsertsRegistry(t *testing.T) {
	sub := feedTestSub()
	sub.Status = model.UpdateSourceDisabled
	items, err := setSourceStatusWithFeedSubItems("tbl", "u1", "s1", sub, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Update == nil || items[1].Update == nil {
		t.Fatalf("want two updates, got %+v", items)
	}
	src := items[0].Update
	if src.ConditionExpression == nil || !strings.Contains(*src.ConditionExpression, "attribute_exists") {
		t.Fatalf("source update must require the row, got %v", src.ConditionExpression)
	}
	if feedAttrS(t, src.Key, "SK") != "CUSRC#s1" {
		t.Fatalf("source key = %v", src.Key)
	}
	if !strings.Contains(*src.UpdateExpression, ", ") || len(src.ExpressionAttributeNames) < 3 {
		t.Fatalf("source update must set status and feedHash: %s %v", *src.UpdateExpression, src.ExpressionAttributeNames)
	}
	reg := items[1].Update
	if feedAttrS(t, reg.Key, "PK") != "CUFEEDS" || feedAttrS(t, reg.Key, "SK") != "fh1#u1#s1" {
		t.Fatalf("registry key = %v", reg.Key)
	}
	if reg.ConditionExpression != nil {
		t.Fatalf("registry upsert must not require the row, condition %s", *reg.ConditionExpression)
	}
	if reg.UpdateExpression == nil || !strings.Contains(*reg.UpdateExpression, "if_not_exists") {
		t.Fatalf("registry upsert must keep an existing fannedAt, got %v", reg.UpdateExpression)
	}
	var sawDisabled bool
	for _, v := range reg.ExpressionAttributeValues {
		if s, ok := v.(*types.AttributeValueMemberS); ok && s.Value == model.UpdateSourceDisabled {
			sawDisabled = true
		}
	}
	if !sawDisabled {
		t.Fatalf("registry status value missing: %v", reg.ExpressionAttributeValues)
	}
}

func TestHealSourceFeedSubItemsKeepsStatusAndRequiresLegacyRow(t *testing.T) {
	items, err := setSourceStatusWithFeedSubItems("tbl", "u1", "s1", feedTestSub(), true)
	if err != nil {
		t.Fatal(err)
	}
	src := items[0].Update
	names := map[string]bool{}
	for _, n := range src.ExpressionAttributeNames {
		names[n] = true
	}
	if !names["feedHash"] || !names["status"] {
		t.Fatalf("names = %v", src.ExpressionAttributeNames)
	}
	if strings.Contains(*src.UpdateExpression, ", ") || !strings.Contains(*src.ConditionExpression, "attribute_not_exists") ||
		!strings.Contains(*src.ConditionExpression, "=") {
		t.Fatalf("heal must only set feedHash under status/legacy conditions: %s / %s", *src.UpdateExpression, *src.ConditionExpression)
	}
	if !strings.Contains(*items[1].Update.UpdateExpression, "if_not_exists") {
		t.Fatal("heal must keep an existing fannedAt")
	}
}

func TestDeleteSourceWithFeedSubItemsDoesNotRequireRegistryRow(t *testing.T) {
	items, err := deleteSourceWithFeedSubItems("tbl", "u1", "s1", "fh1")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Delete == nil || items[1].Delete == nil {
		t.Fatalf("want two deletes, got %+v", items)
	}
	if feedAttrS(t, items[0].Delete.Key, "SK") != "CUSRC#s1" || feedAttrS(t, items[1].Delete.Key, "SK") != "fh1#u1#s1" {
		t.Fatalf("keys = %v / %v", items[0].Delete.Key, items[1].Delete.Key)
	}
	if items[0].Delete.ConditionExpression != nil || items[1].Delete.ConditionExpression != nil {
		t.Fatal("deletes must be unconditional")
	}
}

func TestFeedSubFannedAtUpdateRequiresRow(t *testing.T) {
	in, err := feedSubFannedAtInput("tbl", *feedTestSub(), time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if feedAttrS(t, in.Key, "PK") != "CUFEEDS" || feedAttrS(t, in.Key, "SK") != "fh1#u1#s1" {
		t.Fatalf("key = %v", in.Key)
	}
	if in.ConditionExpression == nil || !strings.Contains(*in.ConditionExpression, "attribute_exists") {
		t.Fatalf("condition = %v", in.ConditionExpression)
	}
}

func TestFeedSubCursorRoundTrip(t *testing.T) {
	sk := model.UpdateFeedSubSK("fh1", "u1", "s1")
	key, err := decodeFeedSubCursor(encodeFeedSubCursor(sk))
	if err != nil {
		t.Fatal(err)
	}
	if feedAttrS(t, key, "PK") != "CUFEEDS" || feedAttrS(t, key, "SK") != sk {
		t.Fatalf("key = %v", key)
	}
	if k, err := decodeFeedSubCursor(""); err != nil || k != nil {
		t.Fatalf("empty cursor = %v, %v", k, err)
	}
	if _, err := decodeFeedSubCursor("!!!"); err == nil {
		t.Fatal("garbage cursor must fail")
	}
}

func TestSelectFeedEntries(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 11, 13, 9, m, 0, 0, time.UTC) }
	entries := []model.UpdateFeedEntry{
		{EntryHash: "e", SeenAt: at(5)},
		{EntryHash: "b", SeenAt: at(2)},
		{EntryHash: "a", SeenAt: at(2)},
		{EntryHash: "old", SeenAt: at(0)},
		{EntryHash: "at-since", SeenAt: at(1)},
		{EntryHash: "d", SeenAt: at(3)},
	}
	got := selectFeedEntries(entries, at(1), 3)
	want := []string{"a", "b", "d"}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, w := range want {
		if got[i].EntryHash != w {
			t.Fatalf("order = %+v, want %v", got, want)
		}
	}
	if all := selectFeedEntries(entries, at(1), 10); len(all) != 4 || all[3].EntryHash != "e" {
		t.Fatalf("all = %+v", all)
	}
	if none := selectFeedEntries(entries, at(9), 10); len(none) != 0 {
		t.Fatalf("none = %+v", none)
	}
}

func TestSelectNewestFeedEntries(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 11, 13, 9, m, 0, 0, time.UTC) }
	entries := []model.UpdateFeedEntry{{EntryHash: "c", SeenAt: at(3)}, {EntryHash: "a", SeenAt: at(1)}, {EntryHash: "d", SeenAt: at(4)}, {EntryHash: "b", SeenAt: at(2)}}
	got := selectNewestFeedEntries(entries, at(0), 2)
	if len(got) != 2 || got[0].EntryHash != "c" || got[1].EntryHash != "d" {
		t.Fatalf("got %+v", got)
	}
}

func TestFeedRowsRoundTripCamelCase(t *testing.T) {
	av, err := attributevalue.MarshalMap(model.UpdateFeedEntry{EntryHash: "e1", ExpiresAt: 7})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := av["pendingShareExpiresAt"]; !ok {
		t.Fatalf("entry TTL attribute missing: %v", av)
	}
	av, _ = attributevalue.MarshalMap(model.UpdateFeedState{FeedHash: "f", Failures: 2, LastError: "x"})
	for _, k := range []string{"feedHash", "resolvedUrl", "updatedAt", "lastFetchedAt", "failures", "lastError"} {
		if _, ok := av[k]; !ok {
			t.Errorf("state lacks %s", k)
		}
	}
}
