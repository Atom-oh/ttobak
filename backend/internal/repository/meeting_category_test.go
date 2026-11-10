package repository

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func expressionName(body map[string]any, name string) string {
	for token, value := range body["ExpressionAttributeNames"].(map[string]any) {
		if value == name {
			return token
		}
	}
	return ""
}

func TestSetMeetingCategoryConditionalWrite(t *testing.T) {
	for _, tc := range []struct {
		name, category string
		wantRemove     bool
	}{
		{"set", model.MeetingCategoryOneOnOne, false},
		{"clear", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			repo := privacyWriterWire(t, nil, func(target string, body map[string]any) (int, string) {
				calls++
				if !strings.HasSuffix(target, ".UpdateItem") {
					t.Fatalf("unexpected operation %s", target)
				}
				key := body["Key"].(map[string]any)
				if key["PK"].(map[string]any)["S"] != "USER#owner" || key["SK"].(map[string]any)["S"] != "MEETING#meeting" {
					t.Fatalf("category write left the owner's meeting key: %+v", key)
				}
				condition := body["ConditionExpression"].(string)
				account := expressionName(body, "accountId")
				if account == "" || !strings.Contains(condition, "attribute_not_exists ("+account+")") && !strings.Contains(condition, "attribute_not_exists("+account+")") {
					t.Fatalf("category write is not conditional on an absent account: %s", condition)
				}
				category := expressionName(body, "meetingCategory")
				update := body["UpdateExpression"].(string)
				remove := regexp.MustCompile(`(?s)REMOVE\s+(.*)`).FindStringSubmatch(update)
				removed := len(remove) == 2 && strings.Contains(remove[1], category)
				if removed != tc.wantRemove {
					t.Fatalf("update %q remove=%v want %v", update, removed, tc.wantRemove)
				}
				return 200, `{}`
			})
			if err := repo.SetMeetingCategory(context.Background(), "owner", "meeting", tc.category); err != nil || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestSetMeetingCategoryMapsConditionFailure(t *testing.T) {
	repo := privacyWriterWire(t, nil, func(string, map[string]any) (int, string) {
		return 400, `{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","message":"The conditional request failed"}`
	})
	if err := repo.SetMeetingCategory(context.Background(), "owner", "meeting", model.MeetingCategoryTeam); !errors.Is(err, ErrConditionFailed) {
		t.Fatalf("err = %v, want ErrConditionFailed", err)
	}
	if err := repo.SetMeetingCategory(context.Background(), "owner", "meeting", "bogus"); err == nil || errors.Is(err, ErrConditionFailed) {
		t.Fatalf("invalid category reached storage: %v", err)
	}
}

func TestAccountLinkClearsInternalCategory(t *testing.T) {
	t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", "0")
	for name, value := range map[string]any{"link": "account", "unlink": ""} {
		t.Run(name, func(t *testing.T) {
			repo := privacyWriterWire(t, nil, func(_ string, body map[string]any) (int, string) {
				// A classification also carries the target account's open check.
				if items, ok := body["TransactItems"].([]any); ok {
					for _, item := range items {
						if update, ok := item.(map[string]any)["Update"].(map[string]any); ok {
							body = update
						}
					}
				}
				category := expressionName(body, "meetingCategory")
				remove := regexp.MustCompile(`(?s)REMOVE\s+(.*)`).FindStringSubmatch(body["UpdateExpression"].(string))
				removed := category != "" && len(remove) == 2 && strings.Contains(remove[1], category)
				if removed != (value != "") {
					t.Fatalf("accountId=%q removed category=%v", value, removed)
				}
				return 200, `{}`
			})
			if err := repo.UpdateMeetingFields(context.Background(), "owner", "meeting", map[string]interface{}{"accountId": value}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
