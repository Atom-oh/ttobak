package model

import (
	"testing"
	"time"
)

func TestSummaryPrivacyNeverGrandfathersUnknownDerivedContent(t *testing.T) {
	meeting := &Meeting{}
	if !meeting.CanCreateSummaryGrant() {
		t.Fatal("new empty meeting cannot be shared")
	}
	meeting.Content = "legacy summary"
	if meeting.CanCreateSummaryGrant() {
		t.Fatal("legacy summary was treated as safe")
	}
	meeting.SummaryPrivacy = &SummaryPrivacy{Version: 1, State: SummaryPrivacySafe, GenerationID: "generation", ContentSHA256: SummaryPrivacyHash(meeting.Content)}
	if !meeting.CanCreateSummaryGrant() {
		t.Fatal("current safe publication rejected")
	}
	meeting.Content = "human rewrite"
	if meeting.CanCreateSummaryGrant() {
		t.Fatal("rewritten content inherited a stale clearance")
	}
	meeting.SummaryPrivacy.State = SummaryPrivacyPrivate
	meeting.Content = ""
	if meeting.CanCreateSummaryGrant() {
		t.Fatal("blanking content erased private provenance")
	}
	for _, field := range []string{"tasks", "insights", "tags", "sentiment", "liveSummary"} {
		m := &Meeting{}
		switch field {
		case "tasks":
			m.ActionItems = `[{"text":"legacy task"}]`
		case "insights":
			m.Insights = `[{"text":"legacy insight"}]`
		case "tags":
			m.Tags = []string{"legacy"}
		case "sentiment":
			m.Sentiment = "positive"
		case "liveSummary":
			m.LiveSummary = "legacy derived summary"
		}
		if m.CanCreateSummaryGrant() {
			t.Fatalf("legacy %s silently inherited clearance", field)
		}
	}
}

func TestMembershipPrivacyEligibilityRequiresAddedAtAndActivationFence(t *testing.T) {
	cutoff := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		member  *AccountMember
		cutoff  time.Time
		guarded bool
	}{
		{nil, cutoff, true},
		{&AccountMember{AddedAt: cutoff.Add(-time.Hour)}, cutoff, false},
		{&AccountMember{AddedAt: cutoff}, cutoff, true},
		{&AccountMember{}, cutoff, true},
		{&AccountMember{AddedAt: cutoff.Add(-time.Hour)}, time.Time{}, true},
		{&AccountMember{AddedAt: cutoff.Add(-time.Hour), SummaryPrivacyVersion: 1}, cutoff, true},
		{&AccountMember{AddedAt: cutoff.Add(-time.Hour), SummaryPrivacyVersion: 2}, cutoff, true},
	} {
		if test.member.NeedsSummaryPrivacyGuard(test.cutoff) != test.guarded {
			t.Fatal("new/unknown membership was grandfathered")
		}
	}
}
