package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func (f *fakePeopleRepo) GetMeetingFocusState(ctx context.Context, owner, id string) (*model.Meeting, error) {
	return f.GetMeetingPublication(ctx, owner, id)
}
func (f *fakePeopleRepo) ListPeopleLinksToTarget(_ context.Context, _, kind, id string, maxRows int) ([]model.PeopleLink, bool, error) {
	var rows []model.PeopleLink
	for _, l := range f.targetLinks {
		if l.ToKind == kind && l.ToID == id {
			rows = append(rows, l)
		}
	}
	if len(rows) > maxRows {
		return rows[:maxRows], true, nil
	}
	return rows, false, nil
}
func (f *fakePeopleRepo) ListProjectMeetingRefsForProject(context.Context, string) ([]model.ProjectMeetingRef, error) {
	return f.projectMeetingRefs, nil
}

func focusMeetingFixture(id string, day int, tags ...string) *model.Meeting {
	m := peopleTestMeeting(id)
	m.Title = "Meeting " + id
	m.Date = time.Date(2026, 10, day, 9, 0, 0, 0, time.UTC)
	m.Tags = tags
	return m
}

func newFocusRepo() *fakePeopleRepo {
	f := newPeopleReadRepo()
	f.contacts = []model.AccountContact{
		{ContactID: peopleTestContact, Name: "Kim Minsu", Title: "Manager", Department: "Infra"},
		peopleContact(peopleTestContact2, "Lee Jiwon", "lee@acme.example"),
	}
	f.meetings["owner:m1"] = focusMeetingFixture("m1", 3, "eks", "k8s", "terraform")
	f.meetings["owner:m3"] = focusMeetingFixture("m3", 20, "Oracle", "eks")
	f.targetLinks = []model.PeopleLink{
		peopleEdge("owner:m1", "contact", peopleTestContact, "participant", model.PeopleSourceParticipants),
		peopleEdge("owner:m1", "contact", peopleTestContact, "assignee", model.PeopleSourceActionItems),
		peopleEdge("owner:m2", "contact", peopleTestContact, "participant", model.PeopleSourceParticipants), // unpublished
		peopleEdge("owner:m3", "contact", peopleTestContact, "speaker", model.PeopleSourceSpeakerMap),
	}
	f.meetingLinks = map[string][]model.PeopleLink{
		"owner:m3": {
			peopleEdge("owner:m3", "contact", peopleTestContact, "speaker", model.PeopleSourceSpeakerMap),
			peopleEdge("owner:m3", "contact", peopleTestContact2, "participant", model.PeopleSourceParticipants),
			peopleEdge("owner:m3", "member", "owner", "speaker", model.PeopleSourceSpeakerMap),
			peopleEdge("owner:m3", "account", "acc", "mentions", model.PeopleSourceInsights),
		},
	}
	return f
}

func TestFocus_ContactOrdersMeetingsAndMapsTechs(t *testing.T) {
	f := newFocusRepo()
	g, err := NewAccountPeopleService(f).Focus(context.Background(), "viewer", "acc", "contact:"+peopleTestContact, "")
	if err != nil {
		t.Fatal(err)
	}
	if g.Focus.Label != "Kim Minsu" || g.Focus.Detail != "Manager · Infra" || g.Focus.Meetings != 2 {
		t.Fatalf("focus = %+v", g.Focus)
	}
	if len(g.Meetings) != 2 || g.Meetings[0].ID != "owner:m3" || g.Meetings[1].ID != "owner:m1" {
		t.Fatalf("meetings = %+v", g.Meetings)
	}
	if !slices.Equal(g.Meetings[1].Roles, []string{"participant", "assignee"}) || !slices.Equal(g.Meetings[0].Roles, []string{"speaker"}) {
		t.Fatalf("roles = %v / %v", g.Meetings[0].Roles, g.Meetings[1].Roles)
	}
	if !slices.Equal(g.Meetings[0].Techs, []string{"eks", "oracle"}) || g.Meetings[0].Date != "2026-10-20T09:00:00Z" {
		t.Fatalf("meeting 0 = %+v", g.Meetings[0])
	}
	if len(g.Techs) != 2 || g.Techs[0] != (model.FocusTech{ID: "eks", Label: "EKS", Group: techGroupContainers, Meetings: 2}) ||
		g.Techs[1].ID != "oracle" || g.Techs[1].Meetings != 1 {
		t.Fatalf("techs = %+v", g.Techs)
	}
	if len(g.Other) != 1 || g.Other[0] != (model.FocusTag{Tag: "terraform", Meetings: 1}) {
		t.Fatalf("other = %+v", g.Other)
	}
	people := g.Meetings[0].People
	if len(people) != 3 || people[0].Label != "Kim Minsu" || people[1].Label != "Lee Jiwon" || people[2].Kind != "member" {
		t.Fatalf("people = %+v", people)
	}
	if g.NextCursor != "" || g.Truncated {
		t.Fatalf("paging = %q %v", g.NextCursor, g.Truncated)
	}
}

func TestFocus_GuardedMeetingDropsTagsAndSummaryRoles(t *testing.T) {
	withPeopleSummaryGuard(t)
	f := newFocusRepo()
	g, err := NewAccountPeopleService(f).Focus(context.Background(), "viewer", "acc", "contact:"+peopleTestContact, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Meetings) != 2 || !slices.Equal(g.Meetings[1].Roles, []string{"participant"}) {
		t.Fatalf("meetings = %+v", g.Meetings)
	}
	if len(g.Techs) != 0 || len(g.Other) != 0 || len(g.Meetings[0].Techs) != 0 {
		t.Fatalf("guarded tags leaked: %+v %+v", g.Techs, g.Other)
	}
	// A meeting reached only through a summary-derived row disappears.
	f.targetLinks = []model.PeopleLink{peopleEdge("owner:m1", "contact", peopleTestContact, "assignee", model.PeopleSourceActionItems)}
	g, err = NewAccountPeopleService(f).Focus(context.Background(), "viewer", "acc", "contact:"+peopleTestContact, "")
	if err != nil || len(g.Meetings) != 0 || g.Focus.Meetings != 0 {
		t.Fatalf("guarded-only = %+v, %v", g, err)
	}
}

func TestFocus_ProjectUsesRefsAndProjectMembership(t *testing.T) {
	f := newFocusRepo()
	f.projects["p1"] = &model.Project{ProjectID: "p1", Name: "EKS move", Stage: "poc", AccountIDs: []string{"acc"}}
	f.meetings["owner:m1"].ProjectIDs = []string{"p1"}
	f.projectMeetingRefs = []model.ProjectMeetingRef{
		{ProjectID: "p1", MeetingID: "m1", OwnerUserID: "owner"},
		{ProjectID: "p1", MeetingID: "m3", OwnerUserID: "owner"}, // no longer lists p1
		{ProjectID: "p2", MeetingID: "m1", OwnerUserID: "owner"},
	}
	g, err := NewAccountPeopleService(f).Focus(context.Background(), "viewer", "acc", "project:p1", "")
	if err != nil {
		t.Fatal(err)
	}
	if g.Focus.Label != "EKS move" || g.Focus.Detail != "poc" || len(g.Meetings) != 1 || g.Meetings[0].ID != "owner:m1" ||
		len(g.Meetings[0].Roles) != 0 {
		t.Fatalf("graph = %+v", g)
	}
}

func TestFocus_RejectsUnknownOrMalformedFocus(t *testing.T) {
	f := newFocusRepo()
	f.projects["p9"] = &model.Project{ProjectID: "p9", Name: "Other", AccountIDs: []string{"other"}}
	svc := NewAccountPeopleService(f)
	for _, focus := range []string{"contact:99999999-9999-4999-8999-999999999999", "project:p9", "member:stranger"} {
		if _, err := svc.Focus(context.Background(), "viewer", "acc", focus, ""); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s: err = %v", focus, err)
		}
	}
	for _, focus := range []string{"", "contact", "account:acc", "contact:a#b"} {
		if _, err := svc.Focus(context.Background(), "viewer", "acc", focus, ""); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%q: err = %v", focus, err)
		}
	}
	if _, err := svc.Focus(context.Background(), "stranger", "acc", "contact:"+peopleTestContact, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-member err = %v", err)
	}
}

func TestFocus_PagesWithBoundCursor(t *testing.T) {
	f := newFocusRepo()
	f.targetLinks = nil
	for i := 1; i <= 14; i++ {
		id := fmt.Sprintf("p%02d", i)
		f.meetings["owner:"+id] = focusMeetingFixture(id, i, "bedrock")
		f.targetLinks = append(f.targetLinks, peopleEdge("owner:"+id, "contact", peopleTestContact, "participant", model.PeopleSourceParticipants))
	}
	svc := NewAccountPeopleService(f)
	focus := "contact:" + peopleTestContact
	g, err := svc.Focus(context.Background(), "viewer", "acc", focus, "")
	if err != nil || len(g.Meetings) != 12 || g.Meetings[0].ID != "owner:p14" || g.NextCursor == "" || g.Techs[0].Meetings != 14 {
		t.Fatalf("page 1 = %+v, %v", g, err)
	}
	g2, err := svc.Focus(context.Background(), "viewer", "acc", focus, g.NextCursor)
	if err != nil || len(g2.Meetings) != 2 || g2.Meetings[1].ID != "owner:p01" || g2.NextCursor != "" {
		t.Fatalf("page 2 = %+v, %v", g2, err)
	}
	if _, err := svc.Focus(context.Background(), "viewer", "acc", "contact:"+peopleTestContact2, g.NextCursor); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cursor for another focus: %v", err)
	}
	if _, err := svc.Focus(context.Background(), "viewer", "acc", focus, "!!"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("garbage cursor: %v", err)
	}
	// Meetings unpublished between pages: a valid cursor past the end is an empty final page.
	f.targetLinks = f.targetLinks[:3]
	g3, err := svc.Focus(context.Background(), "viewer", "acc", focus, g.NextCursor)
	if err != nil || len(g3.Meetings) != 0 || g3.NextCursor != "" || g3.Focus.Meetings != 3 {
		t.Fatalf("stale cursor = %+v, %v", g3, err)
	}
}

func TestFocus_BoundsMeetingReads(t *testing.T) {
	f := newFocusRepo()
	f.targetLinks = nil
	for i := 0; i < maxFocusMeetings+5; i++ {
		id := fmt.Sprintf("b%03d", i)
		f.meetings["owner:"+id] = focusMeetingFixture(id, 1+i%28)
		f.targetLinks = append(f.targetLinks, peopleEdge("owner:"+id, "contact", peopleTestContact, "participant", model.PeopleSourceParticipants))
	}
	g, err := NewAccountPeopleService(f).Focus(context.Background(), "viewer", "acc", "contact:"+peopleTestContact, "")
	if err != nil || !g.Truncated || g.Focus.Meetings != maxFocusMeetings {
		t.Fatalf("graph = %+v, %v", g.Focus, err)
	}
}

func TestFocus_ProjectKeepsNewestMeetingsPastTheBound(t *testing.T) {
	f := newFocusRepo()
	f.projects["p1"] = &model.Project{ProjectID: "p1", Name: "Big", AccountIDs: []string{"acc"}}
	start := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	// The repository returns project meeting refs newest first.
	for i := maxFocusMeetings + 10; i >= 1; i-- {
		id := fmt.Sprintf("q%03d", i)
		m := peopleTestMeeting(id)
		m.Date, m.ProjectIDs = start.AddDate(0, 0, i), []string{"p1"}
		f.meetings["owner:"+id] = m
		f.projectMeetingRefs = append(f.projectMeetingRefs, model.ProjectMeetingRef{ProjectID: "p1", MeetingID: id, OwnerUserID: "owner", Date: m.Date})
	}
	g, err := NewAccountPeopleService(f).Focus(context.Background(), "viewer", "acc", "project:p1", "")
	if err != nil || !g.Truncated || g.Meetings[0].ID != fmt.Sprintf("owner:q%03d", maxFocusMeetings+10) {
		t.Fatalf("first = %+v truncated=%v err=%v", g.Meetings[0], g.Truncated, err)
	}
}

func TestFocus_PeopleRowBoundSetsTruncated(t *testing.T) {
	f := &cutMeetingLinksRepo{newFocusRepo()}
	g, err := NewAccountPeopleService(f).Focus(context.Background(), "viewer", "acc", "contact:"+peopleTestContact, "")
	if err != nil || !g.Truncated {
		t.Fatalf("truncated = %v, %v", g.Truncated, err)
	}
}

type cutMeetingLinksRepo struct{ *fakePeopleRepo }

func (r *cutMeetingLinksRepo) ListPeopleLinksForMeeting(ctx context.Context, account, meeting string, n int) ([]model.PeopleLink, bool, error) {
	links, _, err := r.fakePeopleRepo.ListPeopleLinksForMeeting(ctx, account, meeting, n)
	return links, true, err
}
