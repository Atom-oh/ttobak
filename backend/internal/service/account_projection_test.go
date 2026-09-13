package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

type accountProjectionRepo struct {
	*mockAccountRepo
	sourceError   error
	sourceErrorID string
}

func (r *accountProjectionRepo) GetMeetingPublication(_ context.Context, ownerID, meetingID string) (*model.Meeting, error) {
	if r.sourceError != nil && (r.sourceErrorID == "" || r.sourceErrorID == meetingID) {
		return nil, r.sourceError
	}
	meeting := r.meetings[meetingID]
	if meeting == nil {
		return nil, nil
	}
	copy := *meeting
	return &copy, nil
}

func projectionFixture() (*accountProjectionRepo, *model.Meeting) {
	repo := &accountProjectionRepo{mockAccountRepo: newMockAccountRepo()}
	date := time.Date(2026, 9, 13, 9, 0, 0, 0, time.UTC)
	meeting := &model.Meeting{
		PK: "USER#owner", SK: "MEETING#meeting", EntityType: "MEETING",
		MeetingID: "meeting", UserID: "owner", AccountID: "account-a", SharedToAccount: true,
		Title: "Synthetic customer meeting", Date: date, Status: model.StatusDone,
		Insights: `[{"id":"1","type":"need","text":"Synthetic customer requirement"}]`,
	}
	repo.meetings[meeting.MeetingID] = meeting
	repo.accounts["account-a"] = &model.Account{AccountID: "account-a", Name: "Account A", OwnerUserID: "owner"}
	repo.members[memberKey("account-a", "member")] = &model.AccountMember{
		AccountID: "account-a", UserID: "member", Role: model.RoleSA,
	}
	repo.meetingRefs["account-a"] = []model.MeetingRef{{
		PK: "ACCOUNT#account-a", SK: "MEETINGREF#2026-09-13T09:00:00Z#meeting",
		EntityType: model.EntityTypeMeetingRef, AccountID: "account-a", MeetingID: "meeting",
		OwnerUserID: "owner", Title: meeting.Title, Date: date,
	}}
	repo.insightsByAccount["account-a"] = []model.AccountInsight{{
		PK: "ACCOUNT#account-a", SK: "INSIGHT#2026-09-13T09:00:00Z#meeting#0",
		EntityType: model.EntityTypeInsight, AccountID: "account-a", InsightID: "meeting_0",
		SourceType: "meeting", SourceID: "meeting", SourceUserID: "owner",
		Type: model.InsightNeed, Text: "Synthetic customer requirement", OccurredAt: date,
	}}
	return repo, meeting
}

func TestAccountProjectionsRevalidateCanonicalMeeting(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*accountProjectionRepo, *model.Meeting)
		want   int
	}{
		{"published", func(*accountProjectionRepo, *model.Meeting) {}, 1},
		{"unpublished", func(_ *accountProjectionRepo, m *model.Meeting) { m.SharedToAccount = false }, 0},
		{"repointed", func(_ *accountProjectionRepo, m *model.Meeting) { m.AccountID = "account-b" }, 0},
		{"unclassified", func(_ *accountProjectionRepo, m *model.Meeting) { m.AccountID = "" }, 0},
		{"deleted", func(r *accountProjectionRepo, _ *model.Meeting) { delete(r.meetings, "meeting") }, 0},
		{"wrong meeting ID", func(_ *accountProjectionRepo, m *model.Meeting) { m.MeetingID = "other" }, 0},
		{"wrong owner", func(_ *accountProjectionRepo, m *model.Meeting) { m.UserID = "other" }, 0},
		{"wrong primary key", func(_ *accountProjectionRepo, m *model.Meeting) { m.PK = "USER#other" }, 0},
		{"wrong sort key", func(_ *accountProjectionRepo, m *model.Meeting) { m.SK = "MEETING#other" }, 0},
		{"wrong entity", func(_ *accountProjectionRepo, m *model.Meeting) { m.EntityType = "OTHER" }, 0},
		{"missing source owner", func(r *accountProjectionRepo, _ *model.Meeting) {
			r.meetingRefs["account-a"][0].OwnerUserID = ""
			r.insightsByAccount["account-a"][0].SourceUserID = ""
		}, 0},
		{"wrong projection owner", func(r *accountProjectionRepo, _ *model.Meeting) {
			r.meetingRefs["account-a"][0].OwnerUserID = "other"
			r.insightsByAccount["account-a"][0].SourceUserID = "other"
		}, 0},
		{"wrong projection account", func(r *accountProjectionRepo, _ *model.Meeting) {
			r.meetingRefs["account-a"][0].AccountID = "account-b"
			r.insightsByAccount["account-a"][0].AccountID = "account-b"
		}, 0},
		{"wrong projection partition", func(r *accountProjectionRepo, _ *model.Meeting) {
			r.meetingRefs["account-a"][0].PK = "ACCOUNT#account-b"
			r.insightsByAccount["account-a"][0].PK = "ACCOUNT#account-b"
		}, 0},
		{"projection source key mismatch", func(r *accountProjectionRepo, _ *model.Meeting) {
			r.meetingRefs["account-a"][0].SK = "MEETINGREF#2026-09-13T09:00:00Z#other"
			r.insightsByAccount["account-a"][0].SK = "INSIGHT#2026-09-13T09:00:00Z#other#0"
		}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, meeting := projectionFixture()
			tc.mutate(repo, meeting)
			svc := newAccountServiceWithRepo(repo)
			meetings, err := svc.ListAccountMeetings(context.Background(), "member", "account-a")
			if err != nil || len(meetings) != tc.want {
				t.Errorf("meetings=%+v err=%v want count=%d", meetings, err, tc.want)
			}
			insights, err := svc.ListAccountInsights(context.Background(), "member", "account-a", time.Time{}, time.Time{}, nil)
			if err != nil || len(insights) != tc.want {
				t.Errorf("insights=%+v err=%v want count=%d", insights, err, tc.want)
			}
			brief, err := svc.GetAccountBrief(context.Background(), "member", "account-a", time.Time{}, time.Time{}, nil)
			if err != nil || brief == nil {
				t.Fatalf("brief read failed: %v", err)
			}
			if len(brief.Meetings) != tc.want || len(brief.InsightsByType[model.InsightNeed]) != tc.want {
				t.Errorf("brief exposed stale projections: %+v", brief)
			}
		})
	}
}

func TestAccountProjectionsReshareToBRevokesA(t *testing.T) {
	ctx := context.Background()
	accountRepo, meeting := projectionFixture()
	meetingRepo := newMockMeetingRepo()
	meetingRepo.addMeeting(meeting)
	meetingRepo.addMember("account-a", "owner", model.RoleSA)
	meetingRepo.addMember("account-a", "member", model.RoleTAM)
	meetingRepo.addMember("account-b", "owner", model.RoleSA)
	meetingRepo.addMember("account-b", "member-b", model.RoleTAM)
	meetings := newMeetingServiceWithRepo(meetingRepo)
	for _, accountID := range []string{"account-a", "account-b"} {
		if _, err := meetings.ShareMeetingToAccount(ctx, "owner", "owner@example.invalid", "meeting", accountID); err != nil {
			t.Fatal(err)
		}
	}
	accountRepo.meetingRefs = meetingRepo.meetingRefs
	accountRepo.meetings = meetingRepo.meetingsByID
	accountRepo.members = meetingRepo.members
	accountRepo.accounts["account-b"] = &model.Account{AccountID: "account-b", Name: "Account B", OwnerUserID: "owner"}
	accountRepo.insightsByAccount = map[string][]model.AccountInsight{}
	for _, insight := range meetingRepo.accountInsights {
		accountRepo.insightsByAccount[insight.AccountID] = append(accountRepo.insightsByAccount[insight.AccountID], insight)
	}
	accounts := newAccountServiceWithRepo(accountRepo)
	for _, tc := range []struct {
		accountID, userID string
		want              int
	}{{"account-a", "member", 0}, {"account-b", "member-b", 1}} {
		brief, err := accounts.GetAccountBrief(ctx, tc.userID, tc.accountID, time.Time{}, time.Time{}, nil)
		if err != nil || brief == nil {
			t.Fatalf("account=%s err=%v", tc.accountID, err)
		}
		if len(brief.Meetings) != tc.want || len(brief.InsightsByType[model.InsightNeed]) != tc.want {
			t.Errorf("account=%s brief=%+v want count=%d", tc.accountID, brief, tc.want)
		}
	}
}

func TestAccountProjectionsPrivateRelinkLeavesRowsInert(t *testing.T) {
	for _, target := range []string{"account-a", "account-b"} {
		t.Run(target, func(t *testing.T) {
			ctx := context.Background()
			accountRepo, meeting := projectionFixture()
			meetingRepo := newMockMeetingRepo()
			meetingRepo.addMeeting(meeting)
			meetingRepo.addMember("account-a", "owner", model.RoleSA)
			meetingRepo.addMember("account-a", "member", model.RoleTAM)
			meetingRepo.addMember("account-b", "owner", model.RoleSA)
			meetings := newMeetingServiceWithRepo(meetingRepo)
			if _, err := meetings.ShareMeetingToAccount(ctx, "owner", "owner@example.invalid", "meeting", "account-a"); err != nil {
				t.Fatal(err)
			}
			// Use the real publication output and leave it in place after relinking.
			accountRepo.meetingRefs = meetingRepo.meetingRefs
			accountRepo.insightsByAccount["account-a"] = meetingRepo.accountInsights
			accountRepo.meetings = meetingRepo.meetingsByID
			accounts := newAccountServiceWithRepo(accountRepo)
			before, err := accounts.GetAccountBrief(ctx, "member", "account-a", time.Time{}, time.Time{}, nil)
			if err != nil || len(before.Meetings) != 1 || len(before.InsightsByType[model.InsightNeed]) != 1 {
				t.Fatalf("published control not visible: brief=%+v err=%v", before, err)
			}
			if err := meetings.LinkMeetingToAccount(ctx, "owner", "meeting", target); err != nil {
				t.Fatal(err)
			}
			if len(accountRepo.meetingRefs["account-a"]) != 1 || len(accountRepo.insightsByAccount["account-a"]) != 1 {
				t.Fatal("test must retain the stale projection rows")
			}
			after, err := accounts.GetAccountBrief(ctx, "member", "account-a", time.Time{}, time.Time{}, nil)
			if err != nil || len(after.Meetings) != 0 || len(after.InsightsByType) != 0 {
				t.Fatalf("private meeting projections remain visible: brief=%+v err=%v", after, err)
			}
		})
	}
}

func TestAccountProjectionSourceFailuresAreNotEmptySuccess(t *testing.T) {
	repo, _ := projectionFixture()
	sourceError := errors.New("synthetic canonical storage failure")
	repo.sourceError = sourceError
	svc := newAccountServiceWithRepo(repo)
	ctx := context.Background()
	if result, err := svc.ListAccountMeetings(ctx, "member", "account-a"); !errors.Is(err, sourceError) || result != nil {
		t.Errorf("meetings swallowed source failure: result=%+v err=%v", result, err)
	}
	if result, err := svc.ListAccountInsights(ctx, "member", "account-a", time.Time{}, time.Time{}, nil); !errors.Is(err, sourceError) || result != nil {
		t.Errorf("insights swallowed source failure: result=%+v err=%v", result, err)
	}
	if result, err := svc.GetAccountBrief(ctx, "member", "account-a", time.Time{}, time.Time{}, nil); !errors.Is(err, sourceError) || result != nil {
		t.Errorf("brief swallowed meeting source failure: result=%+v err=%v", result, err)
	}
	repo.meetingRefs["account-a"] = nil
	if result, err := svc.GetAccountBrief(ctx, "member", "account-a", time.Time{}, time.Time{}, nil); !errors.Is(err, sourceError) || result != nil {
		t.Errorf("brief swallowed insight source failure: result=%+v err=%v", result, err)
	}
}

func TestAccountProjectionSourceFailureDiscardsPartialResults(t *testing.T) {
	repo, _ := projectionFixture()
	repo.sourceError = errors.New("synthetic later source failure")
	repo.sourceErrorID = "broken"
	ref := repo.meetingRefs["account-a"][0]
	ref.MeetingID, ref.SK = "broken", "MEETINGREF#2026-09-13T09:00:00Z#broken"
	repo.meetingRefs["account-a"] = append(repo.meetingRefs["account-a"], ref)
	insight := repo.insightsByAccount["account-a"][0]
	insight.SourceID, insight.SK = "broken", "INSIGHT#2026-09-13T09:00:00Z#broken#0"
	repo.insightsByAccount["account-a"] = append(repo.insightsByAccount["account-a"], insight)
	svc := newAccountServiceWithRepo(repo)
	ctx := context.Background()
	if result, err := svc.ListAccountMeetings(ctx, "member", "account-a"); !errors.Is(err, repo.sourceError) || result != nil {
		t.Errorf("partial meeting success after source failure: result=%+v err=%v", result, err)
	}
	if result, err := svc.ListAccountInsights(ctx, "member", "account-a", time.Time{}, time.Time{}, nil); !errors.Is(err, repo.sourceError) || result != nil {
		t.Errorf("partial insight success after source failure: result=%+v err=%v", result, err)
	}
}

func TestAccountInsightsAllowOnlyExplicitAccountOwnedNonMeetingSources(t *testing.T) {
	for _, tc := range []struct {
		source string
		want   int
	}{{"news", 1}, {"ingest", 1}, {"", 0}, {"unknown", 0}, {"research", 0}, {"MEETING", 0}} {
		t.Run(tc.source, func(t *testing.T) {
			repo, meeting := projectionFixture()
			meeting.SharedToAccount = false
			insight := &repo.insightsByAccount["account-a"][0]
			insight.SourceType, insight.SourceID, insight.SourceUserID = tc.source, "account-source", ""
			insight.SK = "INSIGHT#2026-09-13T09:00:00Z#account-source#0"
			svc := newAccountServiceWithRepo(repo)
			got, err := svc.ListAccountInsights(context.Background(), "member", "account-a", time.Time{}, time.Time{}, nil)
			if err != nil || len(got) != tc.want {
				t.Fatalf("source=%q result=%+v err=%v want=%d", tc.source, got, err, tc.want)
			}
			insight.AccountID = "account-b"
			got, err = svc.ListAccountInsights(context.Background(), "member", "account-a", time.Time{}, time.Time{}, nil)
			if err != nil || len(got) != 0 {
				t.Fatalf("foreign account-owned source exposed: result=%+v err=%v", got, err)
			}
		})
	}
}

func TestAccountProjectionsKeepMembershipGate(t *testing.T) {
	repo, _ := projectionFixture()
	repo.sourceError = errors.New("must not reach canonical source without membership")
	svc := newAccountServiceWithRepo(repo)
	ctx := context.Background()
	if _, err := svc.ListAccountMeetings(ctx, "stranger", "account-a"); !errors.Is(err, ErrForbidden) {
		t.Errorf("meetings membership gate: %v", err)
	}
	if _, err := svc.ListAccountInsights(ctx, "stranger", "account-a", time.Time{}, time.Time{}, nil); !errors.Is(err, ErrForbidden) {
		t.Errorf("insights membership gate: %v", err)
	}
	if _, err := svc.GetAccountBrief(ctx, "stranger", "account-a", time.Time{}, time.Time{}, nil); !errors.Is(err, ErrForbidden) {
		t.Errorf("brief membership gate: %v", err)
	}
}
