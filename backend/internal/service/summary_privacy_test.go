package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func TestSummaryPrivacyAccountReadRetainsRawSourceAndExistingDirectGrant(t *testing.T) {
	t.Setenv(SummaryPrivacyReaderEnv, "0")
	repo := newMockMeetingRepo()
	meeting := &model.Meeting{
		MeetingID: "meeting", UserID: "owner", Status: model.StatusDone,
		AccountID: "account", SharedToAccount: true,
		Content: "PRIVATE_PREDECESSOR", ActionItems: `[{"text":"PRIVATE_TASK"}]`,
		LiveSummary: "PRIVATE_DRAFT", Tags: []string{"PRIVATE_TAG"},
		Notes: "parent user notes", TranscriptA: "parent transcript",
		TranscriptQualityA: &model.TranscriptQuality{
			Refinement: "validated", SpeakerProvenance: "acoustic",
			TextSHA256: model.TranscriptHash("parent transcript"),
		},
	}
	repo.addMeeting(meeting)
	repo.members["account|reader"] = &model.AccountMember{
		AccountID: "account", UserID: "reader", SummaryPrivacyVersion: 1,
	}
	repo.shares[shareKey("direct", "meeting")] = &model.Share{
		MeetingID: "meeting", OwnerID: "owner", SharedToID: "direct", Permission: model.PermissionRead,
	}
	svc := newMeetingServiceWithRepo(repo)
	for _, user := range []string{"owner", "reader", "direct"} {
		view, err := svc.GetMeetingDetail(context.Background(), user, "meeting")
		if err != nil {
			t.Fatal(err)
		}
		if view.Notes != meeting.Notes || view.TranscriptA != meeting.TranscriptA {
			t.Fatal("privacy projection removed parent-owned raw source")
		}
		if view.TranscriptQuality == nil || view.TranscriptQuality.Refinement != "validated" ||
			view.TranscriptQuality.SpeakerProvenance != "acoustic" {
			t.Fatal("privacy projection discarded current transcript provenance")
		}
		// Account publication shares current derived content with members.
		if view.Content != meeting.Content || len(view.Tags) != 1 || view.SummarySharing.DerivedReadRestricted {
			t.Fatalf("%s did not read the published derived content", user)
		}
	}
	if meeting.Content != "PRIVATE_PREDECESSOR" || len(meeting.Tags) != 1 {
		t.Fatal("a reader projection mutated canonical content")
	}
}

func TestSummaryPrivacyGuardIsOffForEveryMemberShape(t *testing.T) {
	added := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	for _, env := range []string{"", "0", "1", "unexpected"} {
		t.Setenv(SummaryPrivacyReaderEnv, env)
		for _, cutoff := range []string{"", "2026-10-12T12:00:00Z", "2026-10-01T00:00:00Z"} {
			t.Setenv(SummaryPrivacyCutoffEnv, cutoff)
			for _, member := range []*model.AccountMember{
				nil, {}, {AddedAt: added}, {AddedAt: added, SummaryPrivacyVersion: 1}, {SummaryPrivacyVersion: 2},
			} {
				if accountMemberNeedsSummaryGuard(member) {
					t.Fatalf("env=%q cutoff=%q member=%+v kept the removed summary guard", env, cutoff, member)
				}
			}
		}
	}
}

func TestSummaryPrivacyInsightCopiesRequireCurrentGenerationAndRevision(t *testing.T) {
	meeting := &model.Meeting{
		PK: "USER#owner", SK: "MEETING#meeting", UserID: "owner", MeetingID: "meeting",
		EntityType: "MEETING", AccountID: "account", SharedToAccount: true,
		InsightsRevision: "revision",
		SummaryPrivacy: &model.SummaryPrivacy{
			Version: 1, State: model.SummaryPrivacySafe, GenerationID: "generation",
			ContentSHA256: model.SummaryPrivacyHash("summary"),
		},
	}
	member := &model.AccountMember{SummaryPrivacyVersion: 1}
	insight := model.AccountInsight{
		AccountID: "account", SourceUserID: "owner", SourceID: "meeting",
		SummaryGeneration: "generation", InsightsRevision: "revision",
	}
	if !accountMeetingInsightAllowed(meeting, member, insight, "reader") {
		t.Fatal("current safe insight copy was denied")
	}
	for _, field := range []string{"generation", "revision", "privacy", "hash"} {
		stale := field == "generation" || field == "revision"
		parent, copy := *meeting, insight
		privacy := *meeting.SummaryPrivacy
		parent.SummaryPrivacy = &privacy
		switch field {
		case "generation":
			copy.SummaryGeneration = "old"
		case "revision":
			copy.InsightsRevision = "old"
		case "privacy":
			privacy.State = model.SummaryPrivacyUnknown
		case "hash":
			privacy.ContentSHA256 = strings.ToUpper(privacy.ContentSHA256)
		}
		// Generation/revision freshness still fences copies; privacy state no
		// longer withholds published insights from members.
		if got := accountMeetingInsightAllowed(&parent, member, copy, "reader"); got == stale {
			t.Fatalf("%s change: allowed=%v", field, got)
		}
	}
}

func TestSummaryPrivacyPublishedAccountInsightIsReadableByMembers(t *testing.T) {
	repo := newMockAccountRepo()
	repo.members["account|reader"] = &model.AccountMember{SummaryPrivacyVersion: 1}
	repo.meetings["meeting"] = &model.Meeting{
		PK: "USER#owner", SK: "MEETING#meeting", UserID: "owner", MeetingID: "meeting",
		EntityType: "MEETING", AccountID: "account", SharedToAccount: true,
	}
	date := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	repo.insightsByAccount["account"] = []model.AccountInsight{{
		PK: "ACCOUNT#account", SK: "INSIGHT#" + date.Format(time.RFC3339) + "#meeting#0",
		EntityType: model.EntityTypeInsight, AccountID: "account", SourceType: "meeting",
		SourceID: "meeting", SourceUserID: "owner", OccurredAt: date, Text: "PRIVATE_INSIGHT",
	}}
	insights, err := newAccountServiceWithRepo(repo).ListAccountInsights(
		context.Background(), "reader", "account", time.Time{}, time.Time{}, nil)
	if errors.Is(err, ErrSummaryReadRestricted) || err != nil {
		t.Fatalf("published insight was withheld: %v", err)
	}
	if len(insights) != 1 || insights[0].Text != "PRIVATE_INSIGHT" {
		t.Fatalf("published insight missing: %+v", insights)
	}
	repo.meetings["meeting"].SharedToAccount = false
	insights, err = newAccountServiceWithRepo(repo).ListAccountInsights(
		context.Background(), "reader", "account", time.Time{}, time.Time{}, nil)
	if err != nil || len(insights) != 0 {
		t.Fatalf("unpublished meeting insight leaked: %+v %v", insights, err)
	}
}
