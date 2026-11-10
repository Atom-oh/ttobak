package repository

import (
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestSummaryPrivacyPublicationCannotDeclassifyConsumedPrivateSource(t *testing.T) {
	snapshot := &model.SummarySnapshot{
		Meeting:       &model.Meeting{Content: "unknown legacy"},
		SharingSafe:   true,
		LinkedSources: []model.LinkedSummarySource{{MeetingID: "private", Content: "PRIVATE_PREDECESSOR"}},
	}
	result := summaryPublicationPrivacy(snapshot, "result")
	if result.State != model.SummaryPrivacyPrivate || len(result.Dependencies) != 1 {
		t.Fatal("sharing-safe flag cleared actually consumed private derivation")
	}
	snapshot.LinkedSources = nil
	result = summaryPublicationPrivacy(snapshot, "fresh own source")
	if result.State != model.SummaryPrivacySafe || result.InheritedState != "" {
		t.Fatal("explicit regeneration without private dependencies retained stale derivation")
	}
	snapshot.SharingSafe = false
	result = summaryPublicationPrivacy(snapshot, "ordinary rewrite")
	if result.State != model.SummaryPrivacyUnknown {
		t.Fatal("ordinary rewrite made unknown legacy derivation safe")
	}
}

func TestEditedSummaryPrivacyState(t *testing.T) {
	stamped := func(state, content string, mutate func(*model.Meeting)) *model.Meeting {
		meeting := &model.Meeting{Content: content}
		privacyStamp(meeting, state, "generation")
		if mutate != nil {
			mutate(meeting)
		}
		return meeting
	}
	tests := []struct {
		name   string
		parent *model.Meeting
		want   string
	}{
		{"nil provenance without derivatives", &model.Meeting{}, model.SummaryPrivacySafe},
		{"nil provenance with action items", &model.Meeting{ActionItems: `[{"id":"a"}]`}, model.SummaryPrivacyUnknown},
		{"nil provenance with legacy content", &model.Meeting{Content: "legacy note"}, model.SummaryPrivacyUnknown},
		{"nil provenance with live summary", &model.Meeting{LiveSummary: "draft"}, model.SummaryPrivacyUnknown},
		{"safe with matching hash", stamped(model.SummaryPrivacySafe, "note", nil), model.SummaryPrivacySafe},
		{"unknown stays unknown", stamped(model.SummaryPrivacyUnknown, "note", nil), model.SummaryPrivacyUnknown},
		{"private stays private", stamped(model.SummaryPrivacyPrivate, "note", nil), model.SummaryPrivacyPrivate},
		{"safe with hash mismatch", stamped(model.SummaryPrivacySafe, "note", func(m *model.Meeting) { m.Content = "changed" }), model.SummaryPrivacyUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := editedSummaryPrivacyState(test.parent); got != test.want {
				t.Fatalf("state = %s, want %s", got, test.want)
			}
		})
	}
}
