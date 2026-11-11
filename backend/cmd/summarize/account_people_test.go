package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func withPeopleHooks(t *testing.T) *[]string {
	t.Helper()
	restoreTerms, restoreLinks, restorePeople := enqueueTerms, linkAccountNews, resolveMeetingPeople
	t.Cleanup(func() {
		enqueueTerms, linkAccountNews, resolveMeetingPeople = restoreTerms, restoreLinks, restorePeople
	})
	var order []string
	enqueueTerms = func(context.Context, string, string, string) { order = append(order, "terms") }
	linkAccountNews = func(context.Context, *model.Meeting) { order = append(order, "links") }
	resolveMeetingPeople = func(_ context.Context, m *model.Meeting) { order = append(order, "people:"+m.UserID+":"+m.MeetingID) }
	return &order
}

func TestResolveMeetingPeople_RunsAfterNewsLinks(t *testing.T) {
	order := withPeopleHooks(t)
	finishSummaryAnalysis(context.Background(), "o1", "m1", &model.Meeting{UserID: "o1", MeetingID: "m1", AccountID: "a1", SharedToAccount: true})
	if len(*order) != 3 || (*order)[1] != "links" || (*order)[2] != "people:o1:m1" {
		t.Fatalf("order %v", *order)
	}
}

func TestResolveMeetingPeople_SkipsPrivateMeeting(t *testing.T) {
	for name, current := range map[string]*model.Meeting{
		"private":    {UserID: "o1", MeetingID: "m1", AccountID: "a1"},
		"no account": {UserID: "o1", MeetingID: "m1", SharedToAccount: true},
		"unreadable": nil,
	} {
		order := withPeopleHooks(t)
		finishSummaryAnalysis(context.Background(), "o1", "m1", current)
		if len(*order) != 1 || (*order)[0] != "terms" {
			t.Fatalf("%s: order %v", name, *order)
		}
	}
}

func TestResolveMeetingPeople_RunsBeforePrivacyEarlyReturn(t *testing.T) {
	shared := &model.Meeting{UserID: "o1", MeetingID: "m1", AccountID: "a1", SharedToAccount: true}
	for _, privacy := range []string{"1", ""} {
		t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", privacy)
		order := withPeopleHooks(t)
		if exportKB := finishSummaryAnalysis(context.Background(), "o1", "m1", shared); exportKB != (privacy == "") {
			t.Fatalf("privacy=%q exportKB=%v", privacy, exportKB)
		}
		if len(*order) != 3 || (*order)[2] != "people:o1:m1" {
			t.Fatalf("privacy=%q order %v", privacy, *order)
		}
	}
}

func TestDispatch_PeopleResolveRequested(t *testing.T) {
	restore := processPeopleResolveRequested
	t.Cleanup(func() { processPeopleResolveRequested = restore })
	sentinel := errors.New("people worker reached")
	var got model.PeopleResolveRequested
	processPeopleResolveRequested = func(_ context.Context, e model.PeopleResolveRequested) error {
		got = e
		return sentinel
	}
	raw := json.RawMessage(`{"source":"ttobak.analysis","detail-type":"PeopleResolveRequested","detail":{"accountId":"a1","ownerId":"o1","meetingId":"m1","reason":"speakers"}}`)
	if err := Handler(context.Background(), raw); !errors.Is(err, sentinel) {
		t.Fatalf("not routed: %v", err)
	}
	if got != (model.PeopleResolveRequested{AccountID: "a1", OwnerID: "o1", MeetingID: "m1", Reason: "speakers"}) {
		t.Fatalf("decoded %+v", got)
	}
}

func TestDispatch_AccountMergeRequested(t *testing.T) {
	restore := processAccountMergeRequested
	t.Cleanup(func() { processAccountMergeRequested = restore })
	sentinel := errors.New("merge worker reached")
	var got model.AccountMergeRequested
	processAccountMergeRequested = func(_ context.Context, e model.AccountMergeRequested) error {
		got = e
		return sentinel
	}
	raw := json.RawMessage(`{"source":"ttobak.analysis","detail-type":"AccountMergeRequested","detail":{"sourceAccountId":"a1"}}`)
	if err := Handler(context.Background(), raw); !errors.Is(err, sentinel) {
		t.Fatalf("not routed: %v", err)
	}
	if got != (model.AccountMergeRequested{SourceAccountID: "a1"}) {
		t.Fatalf("decoded %+v", got)
	}
}

func TestDispatch_CustomerBriefRequested(t *testing.T) {
	restore := processCustomerBriefRequested
	t.Cleanup(func() { processCustomerBriefRequested = restore })
	sentinel := errors.New("customer brief worker reached")
	var got model.CustomerBriefRequested
	processCustomerBriefRequested = func(_ context.Context, e model.CustomerBriefRequested) error {
		got = e
		return sentinel
	}
	raw := json.RawMessage(`{"source":"ttobak.analysis","detail-type":"CustomerBriefRequested","detail":{"accountId":"a1","userId":"u1","runId":"r1"}}`)
	if err := Handler(context.Background(), raw); !errors.Is(err, sentinel) {
		t.Fatalf("not routed: %v", err)
	}
	if got != (model.CustomerBriefRequested{AccountID: "a1", UserID: "u1", RunID: "r1"}) {
		t.Fatalf("decoded %+v", got)
	}
}
