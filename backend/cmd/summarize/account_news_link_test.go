package main

import (
	"context"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func withNewsLinkHooks(t *testing.T) (*[]string, *[]string) {
	t.Helper()
	restoreTerms, restoreLinks := enqueueTerms, linkAccountNews
	t.Cleanup(func() { enqueueTerms, linkAccountNews = restoreTerms, restoreLinks })
	var order, linked []string
	enqueueTerms = func(_ context.Context, owner, meeting, reason string) {
		order = append(order, "terms")
	}
	linkAccountNews = func(_ context.Context, m *model.Meeting) {
		order = append(order, "links")
		linked = append(linked, m.UserID+":"+m.MeetingID)
	}
	return &order, &linked
}

func TestLinkAccountNews_SkipsPrivateMeeting(t *testing.T) {
	for name, current := range map[string]*model.Meeting{
		"private":    {UserID: "o1", MeetingID: "m1", AccountID: "a1", SharedToAccount: false},
		"no account": {UserID: "o1", MeetingID: "m1", SharedToAccount: true},
		"unreadable": nil,
	} {
		order, linked := withNewsLinkHooks(t)
		finishSummaryAnalysis(context.Background(), "o1", "m1", current)
		if len(*linked) != 0 || len(*order) != 1 || (*order)[0] != "terms" {
			t.Fatalf("%s: order=%v linked=%v", name, *order, *linked)
		}
	}
}

func TestLinkAccountNews_RunsBeforePrivacyEarlyReturn(t *testing.T) {
	shared := &model.Meeting{UserID: "o1", MeetingID: "m1", AccountID: "a1", SharedToAccount: true}
	for _, privacy := range []string{"1", ""} {
		t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", privacy)
		order, linked := withNewsLinkHooks(t)
		exportKB := finishSummaryAnalysis(context.Background(), "o1", "m1", shared)
		if exportKB != (privacy == "") {
			t.Fatalf("privacy=%q: exportKB=%v", privacy, exportKB)
		}
		if len(*linked) != 1 || (*linked)[0] != "o1:m1" || len(*order) != 2 || (*order)[0] != "terms" {
			t.Fatalf("privacy=%q: order=%v linked=%v", privacy, *order, *linked)
		}
	}
}
