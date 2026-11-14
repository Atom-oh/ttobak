package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

func TestItemHoldInputs(t *testing.T) {
	h := strings.Repeat("a", 64)
	until := time.Date(2026, 11, 14, 15, 0, 0, 0, time.UTC)
	set, err := itemHoldUpdateInput("t", "u1", h, until)
	if err != nil || !strings.Contains(condString(set.UpdateExpression), "SET") || !hasName(set.ExpressionAttributeNames, "heldUntil") ||
		!hasValue(set.ExpressionAttributeValues, "2026-11-14T15:00:00Z") || !strings.Contains(condString(set.ConditionExpression), "attribute_exists") {
		t.Fatalf("set = %+v err = %v", set, err)
	}
	clear, err := itemHoldUpdateInput("t", "u1", h, time.Time{})
	if err != nil || !strings.Contains(condString(clear.UpdateExpression), "REMOVE") {
		t.Fatalf("clear = %+v err = %v", clear, err)
	}
	if _, err := itemHoldUpdateInput("t", "u1", "x", until); !errors.Is(err, ErrInvalidUpdateRun) {
		t.Fatalf("bad hash err = %v", err)
	}
	// Leaving state new clears the hold; staying new keeps it.
	done, _ := itemStateUpdateInput("t", "u1", h, model.UpdateItemDecided, 0)
	if !strings.Contains(condString(done.UpdateExpression), "REMOVE") || !hasName(done.ExpressionAttributeNames, "heldUntil") {
		t.Fatalf("decided update = %s", condString(done.UpdateExpression))
	}
	stay, _ := itemStateUpdateInput("t", "u1", h, model.UpdateItemNew, 1)
	if strings.Contains(condString(stay.UpdateExpression), "REMOVE") {
		t.Fatalf("new update = %s", condString(stay.UpdateExpression))
	}
	// The stored form round-trips through the item row.
	row := map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: "USER#u1"}, "SK": &types.AttributeValueMemberS{Value: model.UpdateItemSK(h)},
		"urlHash": &types.AttributeValueMemberS{Value: h}, "heldUntil": &types.AttributeValueMemberS{Value: "2026-11-14T15:00:00Z"},
	}
	if it, _ := itemFromRow("u1", row); it == nil || !it.HeldUntil.Equal(until) {
		t.Fatalf("item = %+v", it)
	}
}

func TestScanNewItemsSkipsHeld(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	f := &fakeItemPager{rows: scanTestRows(6, map[int]bool{0: true, 1: true, 2: true, 3: true}, 0), pageCap: 10}
	f.rows[0].HeldUntil = now.Add(time.Hour)
	f.rows[1].HeldUntil = now.Add(time.Hour)
	f.rows[2].HeldUntil = now.Add(-time.Hour) // hold over
	got, _, held, _, err := scanNewItems(context.Background(), f.page, "", 2, 2000, now)
	if err != nil || held != 2 || len(got) != 2 || got[0].URLHash != f.rows[2].URLHash || got[1].URLHash != f.rows[3].URLHash {
		t.Fatalf("got = %+v held = %d err = %v", got, held, err)
	}
}

func TestGetItemInputAndRow(t *testing.T) {
	h := strings.Repeat("a", 64)
	if _, err := getItemInput("t", "u1", "bad"); !errors.Is(err, ErrInvalidUpdateRun) {
		t.Fatalf("bad hash err = %v", err)
	}
	in, err := getItemInput("t", "u1", h)
	if err != nil || !*in.ConsistentRead {
		t.Fatalf("input = %+v err = %v", in, err)
	}
	if sk := in.Key["SK"].(*types.AttributeValueMemberS).Value; sk != model.UpdateItemSK(h) {
		t.Fatalf("sk = %q", sk)
	}
	row := map[string]types.AttributeValue{
		"PK":       &types.AttributeValueMemberS{Value: "USER#u1"},
		"SK":       &types.AttributeValueMemberS{Value: model.UpdateItemSK(h)},
		"urlHash":  &types.AttributeValueMemberS{Value: h},
		"url":      &types.AttributeValueMemberS{Value: "https://example.com/a"},
		"attempts": &types.AttributeValueMemberN{Value: "2"},
	}
	it, err := itemFromRow("u1", row)
	if err != nil || it == nil || it.Attempts != 2 || it.URL != "https://example.com/a" {
		t.Fatalf("item = %+v err = %v", it, err)
	}
	if it, _ := itemFromRow("u2", row); it != nil {
		t.Fatal("row of another user accepted")
	}
	if it, _ := itemFromRow("u1", nil); it != nil {
		t.Fatal("absent row returned an item")
	}
}
