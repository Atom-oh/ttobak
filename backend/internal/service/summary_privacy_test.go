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
		if user == "reader" {
			if view.Content != "" || len(view.Tags) != 0 || !view.SummarySharing.DerivedReadRestricted {
				t.Fatal("new account membership read old derived content")
			}
		} else if view.Content != meeting.Content || view.SummarySharing.DerivedReadRestricted {
			t.Fatal("owner or existing direct grant lost its independent access")
		}
	}
	if meeting.Content != "PRIVATE_PREDECESSOR" || len(meeting.Tags) != 1 {
		t.Fatal("a reader projection mutated canonical content")
	}
}

func TestSummaryPrivacyInstallationAndFixedCutoffDoNotGrandfatherStampedMembers(t *testing.T) {
	member := &model.AccountMember{AddedAt: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
	t.Setenv(SummaryPrivacyReaderEnv, "0")
	if accountMemberNeedsSummaryGuard(member) {
		t.Fatal("reader installation activated unmarked membership policy")
	}
	member.SummaryPrivacyVersion = 1
	if !accountMemberNeedsSummaryGuard(member) {
		t.Fatal("installation switch disabled a stamped membership's protection")
	}
	member.SummaryPrivacyVersion = 0
	t.Setenv(SummaryPrivacyReaderEnv, "1")
	t.Setenv(SummaryPrivacyCutoffEnv, "")
	if !accountMemberNeedsSummaryGuard(member) {
		t.Fatal("missing rollout cutoff established legacy eligibility")
	}
	t.Setenv(SummaryPrivacyCutoffEnv, "2026-10-12T12:00:00Z")
	if accountMemberNeedsSummaryGuard(member) {
		t.Fatal("valid pre-cutover membership was restricted")
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
		if accountMeetingInsightAllowed(&parent, member, copy, "reader") {
			t.Fatalf("stale or malformed %s authorized an insight body", field)
		}
	}
}

func TestSummaryPrivacyAccountInsightRestrictionIsNotSuccessfulEmptyEvidence(t *testing.T) {
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
	_, err := newAccountServiceWithRepo(repo).ListAccountInsights(
		context.Background(), "reader", "account", time.Time{}, time.Time{}, nil)
	if !errors.Is(err, ErrSummaryReadRestricted) {
		t.Fatal("restricted insight became an empty successful account read")
	}
}
