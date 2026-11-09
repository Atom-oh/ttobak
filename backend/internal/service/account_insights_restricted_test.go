package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func TestListAccountInsightsPage_SkipsRestrictedMeeting(t *testing.T) {
	prev := accountMemberNeedsSummaryGuard
	accountMemberNeedsSummaryGuard = func(*model.AccountMember) bool { return true }
	defer func() { accountMemberNeedsSummaryGuard = prev }()

	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	ctx := context.Background()
	acc, _ := svc.CreateAccount(ctx, "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "Acme"})
	repo.members[memberKey(acc.AccountID, "reader")] = &model.AccountMember{AccountID: acc.AccountID, UserID: "reader", Role: model.RoleSA}
	d := time.Date(2026, 5, 12, 9, 0, 0, 0, time.UTC)

	mk := func(id string, restricted bool) {
		repo.meetings[id] = &model.Meeting{
			PK: "USER#owner-1", SK: "MEETING#" + id, EntityType: "MEETING",
			UserID: "owner-1", MeetingID: id, AccountID: acc.AccountID, SharedToAccount: true,
			SummaryReadRestricted: restricted, InsightsRevision: "r",
		}
		gen := ""
		if !restricted {
			gen = "g"
			repo.meetings[id].SummaryPrivacy = &model.SummaryPrivacy{
				Version: model.SummaryPrivacyVersion, State: model.SummaryPrivacySafe, GenerationID: "g",
				ContentSHA256: strings.Repeat("a", 64),
			}
		}
		repo.insightsByAccount[acc.AccountID] = append(repo.insightsByAccount[acc.AccountID], model.AccountInsight{
			PK: model.PrefixAccount + acc.AccountID, SK: "INSIGHT#" + d.Format(time.RFC3339) + "#" + id + "#0",
			EntityType: model.EntityTypeInsight, AccountID: acc.AccountID, Type: model.InsightRisk,
			Text: "text-" + id, OccurredAt: d, SourceID: id, SourceUserID: "owner-1", SourceType: "meeting",
			SummaryGeneration: gen, InsightsRevision: "r",
		})
	}
	mk("m-ok", false)
	mk("m-restricted", true)

	page, err := svc.ListAccountInsightsPage(ctx, "reader", acc.AccountID, time.Time{}, time.Time{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if page.RestrictedCount != 1 {
		t.Fatalf("RestrictedCount = %d, want 1", page.RestrictedCount)
	}
	if len(page.Insights) != 1 || page.Insights[0].SourceID != "m-ok" {
		t.Fatalf("insights = %+v, want only m-ok", page.Insights)
	}
}
