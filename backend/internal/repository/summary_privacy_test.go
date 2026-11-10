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
