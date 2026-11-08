package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

const testEventID = "01234567-89ab-4cde-8123-0123456789ab"

func eventFixture() *model.AccountEvent {
	return &model.AccountEvent{EventID: testEventID, Revision: "r2", Title: "아키텍처 리뷰", StartAt: "2026-11-10T05:00:00Z", Location: "본사 3층",
		Attendees: []model.EventAttendee{{Name: "홍길동", Email: "hong@example.com"}}, Notes: "비공개 메모"}
}

func transactItems(t *testing.T, body map[string]interface{}) []map[string]interface{} {
	t.Helper()
	raw := body["TransactItems"].([]interface{})
	out := make([]map[string]interface{}, len(raw))
	for i, item := range raw {
		out[i] = item.(map[string]interface{})
	}
	return out
}

func TestEventWritesAreSealedAtomicAndConditional(t *testing.T) {
	newSK := EventSortKey("2026-11-10T05:00:00Z", testEventID)
	movedSK := EventSortKey("2026-11-11T05:00:00Z", testEventID)
	for _, tc := range []struct {
		op, newSK, previousSK string
		kinds                 []string
	}{
		{"create", newSK, "", []string{"Put", "Put"}},
		{"update", newSK, newSK, []string{"Put", "ConditionCheck"}},
		{"update", movedSK, newSK, []string{"Delete", "Put", "Update"}},
		{"delete", "", newSK, []string{"Delete", "Delete"}},
	} {
		t.Run(tc.op+tc.newSK, func(t *testing.T) {
			calls := 0
			repo := contactWire(t, func(target string, body map[string]interface{}) string {
				calls++
				items := transactItems(t, body)
				if len(items) != 2+len(tc.kinds) {
					t.Fatalf("items: %d", len(items))
				}
				for i, sk := range []string{"META", "MEMBER#user"} {
					key := items[i]["ConditionCheck"].(map[string]interface{})["Key"].(map[string]interface{})
					if key["SK"].(map[string]interface{})["S"] != sk {
						t.Fatalf("missing access check %s", sk)
					}
				}
				for i, kind := range tc.kinds {
					action, ok := items[2+i][kind].(map[string]interface{})
					if !ok || action["ConditionExpression"] == nil {
						t.Fatalf("item %d must be a conditional %s: %#v", 2+i, kind, items[2+i])
					}
					if row, ok := action["Item"].(map[string]interface{}); ok && strings.HasPrefix(row["SK"].(map[string]interface{})["S"].(string), "EVENT#") {
						text := fmt.Sprint(row)
						for _, secret := range []string{"아키텍처", "홍길동", "hong@example.com", "본사", "비공개"} {
							if strings.Contains(text, secret) {
								t.Fatalf("event field %q stored in plaintext", secret)
							}
						}
						if row["sealed"] == nil || row["startAt"].(map[string]interface{})["S"] != "2026-11-10T05:00:00Z" {
							t.Fatalf("event row: %v", row)
						}
					}
				}
				if tc.op != "create" && !strings.Contains(fmt.Sprint(items), "r1") {
					t.Fatal("update/delete must pin the observed revision")
				}
				return `{}`
			})
			if err := repo.WriteAccountEvent(context.Background(), "account", "user", eventFixture(), tc.op, tc.newSK, tc.previousSK, "r1"); err != nil || calls != 1 {
				t.Fatalf("write: %v calls=%d", err, calls)
			}
		})
	}
}

func TestEventsFailClosedWithoutKeyAndRoundTripSealed(t *testing.T) {
	calls := 0
	repo := indexingWire(t, func(string, map[string]interface{}) string { calls++; return `{}` })
	err := repo.WriteAccountEvent(context.Background(), "account", "user", eventFixture(), "create", EventSortKey("2026-11-10T05:00:00Z", testEventID), "", "")
	if !errors.Is(err, ErrEventEncryptionUnavailable) || calls != 0 {
		t.Fatalf("unconfigured key: %v calls=%d", err, calls)
	}
	sealedRepo := contactWire(t, func(string, map[string]interface{}) string { return `{}` })
	item, err := sealedRepo.sealedEventItem(context.Background(), "account", EventSortKey("2026-11-10T05:00:00Z", testEventID), eventFixture())
	if err != nil {
		t.Fatal(err)
	}
	got, sk, err := sealedRepo.openEvent(context.Background(), "account", item)
	if err != nil || got.Title != "아키텍처 리뷰" || got.Attendees[0].Name != "홍길동" || got.Revision != "r2" || !strings.HasSuffix(sk, testEventID) {
		t.Fatalf("round trip: %#v %v", got, err)
	}
	if _, _, err := sealedRepo.openEvent(context.Background(), "other", item); err == nil {
		t.Fatal("an event row opened under another account")
	}
}

func TestEventListQueriesTheRequestedRange(t *testing.T) {
	repo := contactWire(t, func(target string, body map[string]interface{}) string {
		if target != "DynamoDB_20120810.Query" || body["ConsistentRead"] != true || body["ScanIndexForward"] != false || body["Limit"] != float64(5) {
			t.Fatalf("query: %#v", body)
		}
		values := fmt.Sprint(body["ExpressionAttributeValues"])
		if !strings.Contains(values, "EVENT#2026-11-01T00:00:00Z") || !strings.Contains(values, "EVENT#2026-12-01T00:00:00Z") || !strings.Contains(strings.ToUpper(body["KeyConditionExpression"].(string)), "BETWEEN") {
			t.Fatalf("range: %s %s", values, body["KeyConditionExpression"])
		}
		return `{"Items":[]}`
	})
	if _, _, err := repo.ListAccountEvents(context.Background(), "account", "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z", true, 5, EventSortKey("2026-11-20T00:00:00Z", testEventID)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.ListAccountEvents(context.Background(), "account", "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z", true, 5, EventSortKey("2027-01-01T00:00:00Z", testEventID)); err == nil {
		t.Fatal("a cursor outside the range was accepted")
	}
}

func TestContactReferenceCheckReadsKeysOnly(t *testing.T) {
	repo := contactWire(t, func(target string, body map[string]interface{}) string {
		request := body["RequestItems"].(map[string]interface{})
		for _, v := range request {
			spec := v.(map[string]interface{})
			if spec["ProjectionExpression"] != "contactId" || spec["ConsistentRead"] != true || len(spec["Keys"].([]interface{})) != 1 {
				t.Fatalf("reference check must read keys only: %#v", spec)
			}
		}
		return `{"Responses":{"synthetic":[{"contactId":{"S":"` + testEventID + `"}}]}}`
	})
	found, err := repo.ExistingAccountContactIDs(context.Background(), "account", []string{testEventID, testEventID})
	if err != nil {
		t.Fatal(err)
	}
	if !found[testEventID] || len(found) != 1 {
		t.Fatalf("found: %v", found)
	}
}
