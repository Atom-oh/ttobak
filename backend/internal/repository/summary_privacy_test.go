package repository

import (
	"context"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestSharingSafeSnapshotNeverReadsPrivatePredecessors(t *testing.T) {
	fields := make(map[string]model.SummaryValue)
	for _, field := range summaryPrivacyFields {
		fields[field] = model.SummaryValue{}
	}
	for _, published := range []bool{false, true} {
		snapshot := &model.SummarySnapshot{
			Meeting: &model.Meeting{
				UserID: "owner", MeetingID: "meeting", LinkedMeetingIDs: []string{"private"},
				SharedToAccount: published,
			},
			SharingSafe: !published,
			Checks:      []model.SummaryCheck{{PK: "USER#owner", SK: "MEETING#meeting", Exists: true, Fields: fields}},
		}
		// A nil client fails the test if the helper attempts any predecessor
		// or grant read in either sharing-safe or account-published mode.
		if err := NewDynamoDBRepository(nil, "synthetic").BindLinkedSummarySources(context.Background(), snapshot); err != nil {
			t.Fatal(err)
		}
		if len(snapshot.LinkedSources) != 0 || len(snapshot.Checks) != 1 {
			t.Fatal("private predecessor became a sharing-safe dependency")
		}
	}
}

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
