package repository

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

type accountOutputPublisher interface {
	PublishMeetingToAccount(context.Context, string, string, string) error
}

func TestOwnerAccountPublicationPreservesCompletedOutputAndFencesApproval(t *testing.T) {
	privacyWriterEnabled(t)
	t.Setenv("SUMMARY_OWNER_ACCOUNT_SHARE_ENABLED", "1")
	for _, state := range []string{"legacy", model.SummaryPrivacyUnknown, model.SummaryPrivacyPrivate} {
		t.Run(state, func(t *testing.T) {
			parent := privacyParent()
			parent.Content, parent.LiveSummary = "current notes", "current draft"
			parent.ActionItems, parent.Insights = `[{"id":"task","completed":true}]`, `[{"text":"current Insight"}]`
			if state != "legacy" {
				privacyStamp(parent, state, "generation")
			}
			rows := privacyParentRows(parent)
			rows["ACCOUNT#account/MEMBER#owner"] = &model.AccountMember{
				AccountID: "account", UserID: "owner", AddedAt: time.Now().UTC(), SummaryPrivacyVersion: 1,
			}
			writes := 0
			repo := privacyWriterWire(t, rows, func(target string, body map[string]any) (int, string) {
				writes++
				if !strings.HasSuffix(target, ".TransactWriteItems") {
					t.Fatal("approval and membership must publish atomically")
				}
				ops := body["TransactItems"].([]any)
				member := ops[0].(map[string]any)["ConditionCheck"].(map[string]any)
				privacyWriterFences(t, analysisCondition(t, member), "missing membership %s",
					[]string{"accountId", "userId", "addedAt", "summaryPrivacyVersion"})
				update := ops[1].(map[string]any)["Update"].(map[string]any)
				privacyWriterFences(t, analysisCondition(t, update), "missing current-output fence %s",
					[]string{"content", "summaryPrivacy", "summaryAccountApproval", "actionItems", "insights", "liveSummary", "sharedToAccount"})
				approval := transactionSetValue(t, update, "summaryAccountApproval").(map[string]any)["M"].(map[string]any)
				if approval["ownerUserId"].(map[string]any)["S"] != "owner" || approval["accountId"].(map[string]any)["S"] != "account" ||
					approval["contentSha256"].(map[string]any)["S"] != model.SummaryPrivacyHash(parent.Content) ||
					approval["summaryGeneration"].(map[string]any)["S"] != parent.SummaryGeneration() {
					t.Fatal("approval did not bind exact owner/audience/content/generation")
				}
				set := update["UpdateExpression"].(string)
				names := update["ExpressionAttributeNames"].(map[string]any)
				for token, name := range names {
					if name == "summaryPrivacy" || name == "content" || name == "actionItems" || name == "insights" || name == "liveSummary" {
						for _, part := range strings.Split(strings.Split(set, " ADD ")[0], ",") {
							if strings.HasPrefix(strings.TrimPrefix(strings.TrimSpace(part), "SET "), token+" =") {
								t.Fatalf("sharing rewrote existing %s", name)
							}
						}
					}
				}
				return 200, `{}`
			})
			publisher, ok := any(repo).(accountOutputPublisher)
			if !ok {
				t.Fatal("owner Account publication preserving current output is missing")
			}
			if err := publisher.PublishMeetingToAccount(context.Background(), "owner", "meeting", "account"); err != nil || writes != 1 {
				t.Fatalf("share failed: writes=%d err=%v", writes, err)
			}
		})
	}
}

func transactionSetValue(t *testing.T, update map[string]any, field string) any {
	t.Helper()
	names := update["ExpressionAttributeNames"].(map[string]any)
	values := update["ExpressionAttributeValues"].(map[string]any)
	for token, name := range names {
		if name != field {
			continue
		}
		match := regexp.MustCompile(regexp.QuoteMeta(token) + `\s*=\s*(:[0-9]+)`).FindStringSubmatch(update["UpdateExpression"].(string))
		if len(match) == 2 {
			return values[match[1]]
		}
	}
	t.Fatalf("missing trusted assignment of %s", field)
	return nil
}

func TestOwnerAccountApprovalNeverBypassesRolloutOrPartialOutput(t *testing.T) {
	for _, enabled := range []string{"0", "1"} {
		t.Run(enabled, func(t *testing.T) {
			privacyWriterEnabled(t)
			t.Setenv("SUMMARY_OWNER_ACCOUNT_SHARE_ENABLED", enabled)
			parent := privacyParent()
			parent.Content = "private output"
			if enabled == "1" {
				parent.Status = model.StatusRecording
			}
			rows := privacyParentRows(parent)
			rows["ACCOUNT#account/MEMBER#owner"] = &model.AccountMember{AccountID: "account", UserID: "owner"}
			repo := privacyWriterWire(t, rows, func(string, map[string]any) (int, string) {
				t.Fatal("unapproved output reached publication")
				return 200, `{}`
			})
			publisher, ok := any(repo).(accountOutputPublisher)
			if !ok {
				t.Fatal("owner Account publisher missing")
			}
			if err := publisher.PublishMeetingToAccount(context.Background(), "owner", "meeting", "account"); !errors.Is(err, ErrSummarySharingUnsafe) {
				t.Fatalf("gate did not reject: %v", err)
			}
		})
	}
}

func TestAccountAudienceUpdatesInvalidateApprovalDuringWriterRollback(t *testing.T) {
	t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", "0")
	for _, field := range []string{"accountId", "sharedToAccount"} {
		t.Run(field, func(t *testing.T) {
			repo := privacyWriterWire(t, nil, func(_ string, body map[string]any) (int, string) {
				names := body["ExpressionAttributeNames"].(map[string]any)
				remove := regexp.MustCompile(`(?s)REMOVE\s+(.*)`).FindStringSubmatch(body["UpdateExpression"].(string))
				found := false
				if len(remove) == 2 {
					for token, name := range names {
						found = found || name == "summaryAccountApproval" && strings.Contains(remove[1], token)
					}
				}
				if !found {
					t.Fatal("audience update retained a reusable disclosure approval")
				}
				return 200, `{}`
			})
			var value any = "other"
			if field == "sharedToAccount" {
				value = false
			}
			if err := repo.UpdateMeetingFields(context.Background(), "owner", "meeting", map[string]interface{}{field: value}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOwnerApprovalGivesLegacyInsightsFreshIdentityWithoutResettingSummary(t *testing.T) {
	privacyWriterEnabled(t)
	t.Setenv("SUMMARY_OWNER_ACCOUNT_SHARE_ENABLED", "1")
	parent := privacyParent()
	parent.Content, parent.Insights = "current legacy notes", `[{"text":"current legacy Insight"}]`
	rows := privacyParentRows(parent)
	rows["ACCOUNT#account/MEMBER#owner"] = &model.AccountMember{AccountID: "account", UserID: "owner"}
	repo := privacyWriterWire(t, rows, func(_ string, body map[string]any) (int, string) {
		update := body["TransactItems"].([]any)[1].(map[string]any)["Update"].(map[string]any)
		revision := transactionSetValue(t, update, "insightsRevision").(map[string]any)["S"].(string)
		if revision == "" {
			t.Fatal("legacy approval can revive stale unidentified Insight rows")
		}
		return 200, `{}`
	})
	if err := repo.PublishMeetingToAccount(context.Background(), "owner", "meeting", "account"); err != nil {
		t.Fatal(err)
	}
}
