package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

var cutoff = time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)

type fakeRepo struct {
	accounts  []model.Account
	refs      map[string][]model.MeetingRef
	meetings  map[string]bool
	links     map[string][]model.PeopleLink // by meeting link ID
	mentions  []model.UnresolvedMention
	pruned    []string
	deletedMs []string
}

func (f *fakeRepo) ScanAccountHierarchyPage(context.Context, string) (*repository.AccountHierarchyPage, error) {
	return &repository.AccountHierarchyPage{Accounts: f.accounts}, nil
}
func (f *fakeRepo) GetAccount(_ context.Context, id string) (*model.Account, error) {
	for _, a := range f.accounts {
		if a.AccountID == id {
			return &a, nil
		}
	}
	return nil, nil
}
func (f *fakeRepo) ListMeetingRefsForAccountPage(_ context.Context, id, _ string, _ int32) ([]model.MeetingRef, *string, error) {
	return f.refs[id], nil, nil
}
func (f *fakeRepo) GetMeeting(_ context.Context, owner, id string) (*model.Meeting, error) {
	if f.meetings[owner+":"+id] {
		return &model.Meeting{UserID: owner, MeetingID: id}, nil
	}
	return nil, nil
}
func (f *fakeRepo) ListPeopleLinksByTarget(_ context.Context, _, kind, _ string, _ int) ([]model.PeopleLink, string, error) {
	var out []model.PeopleLink
	for _, ls := range f.links {
		for _, l := range ls {
			if l.ToKind == kind {
				out = append(out, l)
			}
		}
	}
	return out, "", nil
}
func (f *fakeRepo) ListPeopleLinksForMeeting(_ context.Context, _, meetingID string, _ int) ([]model.PeopleLink, bool, error) {
	return f.links[meetingID], false, nil
}
func (f *fakeRepo) ListUnresolvedMentions(context.Context, string, string, int) ([]model.UnresolvedMention, string, error) {
	return f.mentions, "", nil
}
func (f *fakeRepo) PrunePeopleLink(_ context.Context, _ string, k model.PeopleLinkKey) error {
	f.pruned = append(f.pruned, k.MeetingID+"/"+k.ToID)
	return nil
}
func (f *fakeRepo) PruneUnresolvedMention(_ context.Context, _, _, meetingID, _ string) error {
	f.deletedMs = append(f.deletedMs, meetingID)
	return nil
}

type fakeResolver struct {
	calls    []string
	accounts []string
	dry      []bool
	fail     map[string]error
}

func (r *fakeResolver) ResolveMeetingForAccount(_ context.Context, account, owner, id string, opt service.PeopleRunOptions) (service.PeopleRunResult, error) {
	r.calls = append(r.calls, owner+":"+id)
	r.accounts = append(r.accounts, account)
	r.dry = append(r.dry, opt.DryRun)
	if err := r.fail[id]; err != nil {
		return service.PeopleRunResult{}, err
	}
	res := service.PeopleRunResult{Meetings: 1, Resolved: 2, Unresolved: 1}
	if !opt.DryRun {
		res.Written = 2
	}
	return res, nil
}

func newFake() *fakeRepo {
	return &fakeRepo{
		accounts: []model.Account{{AccountID: "acct-secret"}},
		refs: map[string][]model.MeetingRef{"acct-secret": {
			{AccountID: "acct-secret", OwnerUserID: "owner-secret", MeetingID: "new-meeting", Date: cutoff.Add(time.Hour)},
			{AccountID: "acct-secret", OwnerUserID: "owner-secret", MeetingID: "at-cutoff", Date: cutoff},
			{AccountID: "acct-secret", OwnerUserID: "owner-secret", MeetingID: "old-meeting", Date: cutoff.Add(-time.Hour)},
		}},
		meetings: map[string]bool{"owner-secret:old-meeting": true},
		links:    map[string][]model.PeopleLink{},
	}
}

func TestBackfill_DryRunWritesNothing(t *testing.T) {
	f, r := newFake(), &fakeResolver{}
	f.links["owner-secret:gone"] = []model.PeopleLink{{MeetingID: "owner-secret:gone", ToKind: "contact", ToID: "c1", Origin: model.LinkOriginManual}}
	res, err := backfill(context.Background(), f, r, backfillOptions{AccountID: "acct-secret", DryRun: true, Limit: 200, Prune: true})
	if err != nil || res.Meetings != 3 || res.Written != 0 || res.Pruned != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	for _, dry := range r.dry {
		if !dry {
			t.Fatal("resolver ran without dry run")
		}
	}
	if len(f.pruned)+len(f.deletedMs) != 0 {
		t.Fatalf("dry run pruned %v %v", f.pruned, f.deletedMs)
	}
}

func TestBackfill_ApplyRequiresBefore(t *testing.T) {
	_, err := backfill(context.Background(), newFake(), &fakeResolver{}, backfillOptions{AccountID: "acct-secret", Limit: 200})
	if !errors.Is(err, errBeforeRequired) {
		t.Fatalf("err=%v", err)
	}
}

func TestBackfill_AccountXorAll(t *testing.T) {
	for _, o := range []backfillOptions{
		{DryRun: true, Limit: 200},
		{AccountID: "acct-secret", All: true, DryRun: true, Limit: 200},
	} {
		if _, err := backfill(context.Background(), newFake(), &fakeResolver{}, o); !errors.Is(err, errAccountXorAll) {
			t.Fatalf("%+v: %v", o, err)
		}
	}
	res, err := backfill(context.Background(), newFake(), &fakeResolver{}, backfillOptions{All: true, DryRun: true, Limit: 200})
	if err != nil || res.Accounts != 1 {
		t.Fatalf("all: %+v %v", res, err)
	}
}

func TestBackfill_BeforeCutoffExcludesNewer(t *testing.T) {
	f, r := newFake(), &fakeResolver{}
	res, err := backfill(context.Background(), f, r, backfillOptions{AccountID: "acct-secret", Before: cutoff, Limit: 200})
	if err != nil || strings.Join(r.calls, ",") != "owner-secret:old-meeting" || res.Written != 2 {
		t.Fatalf("res=%+v err=%v calls=%v", res, err, r.calls)
	}
	r = &fakeResolver{}
	if _, err := backfill(context.Background(), f, r, backfillOptions{AccountID: "acct-secret", DryRun: true, Limit: 1}); err != nil || len(r.calls) != 1 {
		t.Fatalf("limit: %v %v", r.calls, err)
	}
}

func TestBackfill_PruneOnlyMissingMeetings(t *testing.T) {
	f := newFake()
	f.links["owner-secret:old-meeting"] = []model.PeopleLink{{MeetingID: "owner-secret:old-meeting", ToKind: "member", ToID: "kept", Origin: model.LinkOriginManual}}
	f.links["owner-secret:gone"] = []model.PeopleLink{
		{MeetingID: "owner-secret:gone", ToKind: "contact", ToID: "c1", Origin: model.LinkOriginManual},
		{MeetingID: "owner-secret:gone", ToKind: "member", ToID: "u1", Origin: model.LinkOriginAuto},
	}
	f.mentions = []model.UnresolvedMention{{MeetingID: "owner-secret:gone", Hash: strings.Repeat("a", 32), Role: "speaker"},
		{MeetingID: "owner-secret:old-meeting", Hash: strings.Repeat("b", 32), Role: "speaker"}}
	res, err := backfill(context.Background(), f, &fakeResolver{}, backfillOptions{AccountID: "acct-secret", Before: cutoff, Limit: 200, Prune: true})
	if err != nil || res.Pruned != 3 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if strings.Join(f.pruned, ",") != "owner-secret:gone/c1,owner-secret:gone/u1" || strings.Join(f.deletedMs, ",") != "owner-secret:gone" {
		t.Fatalf("pruned %v mentions %v", f.pruned, f.deletedMs)
	}
}

func TestBackfill_OutputHasNoIDs(t *testing.T) {
	f := newFake()
	res, err := backfill(context.Background(), f, &fakeResolver{}, backfillOptions{AccountID: "acct-secret", Before: cutoff, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	writeResult(&out, "apply", res)
	for _, id := range []string{"acct-secret", "owner-secret", "old-meeting"} {
		if strings.Contains(out.String(), id) {
			t.Fatalf("output leaks %s: %s", id, out.String())
		}
	}
	for _, key := range []string{`"accounts":1`, `"meetings":1`, `"written":2`, `"unresolved":1`, `"pruned":0`, `"busy":0`} {
		if !strings.Contains(out.String(), key) {
			t.Fatalf("output %s lacks %s", out.String(), key)
		}
	}
}

func TestBackfill_ResolvesOnlyForSelectedAccount(t *testing.T) {
	f, r := newFake(), &fakeResolver{}
	if _, err := backfill(context.Background(), f, r, backfillOptions{AccountID: "acct-secret", DryRun: true, Limit: 200}); err != nil {
		t.Fatal(err)
	}
	for _, a := range r.accounts {
		if a != "acct-secret" {
			t.Fatalf("resolved for %q", a)
		}
	}
}

func TestBackfill_ContinuesPastMeetingErrors(t *testing.T) {
	f := newFake()
	r := &fakeResolver{fail: map[string]error{"new-meeting": errors.New("throttled")}}
	res, err := backfill(context.Background(), f, r, backfillOptions{AccountID: "acct-secret", DryRun: true, Limit: 200})
	if err != nil || res.Errors != 1 || res.Meetings != 2 || len(r.calls) != 3 {
		t.Fatalf("res=%+v err=%v calls=%v", res, err, r.calls)
	}
}
