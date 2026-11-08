package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type eventTestRepo struct {
	account, member bool
	events          map[string]model.AccountEvent
	sks             map[string]string
	contacts        map[string]bool
	writes          []string
	lastList        struct {
		from, to, after string
		desc            bool
	}
	writeErr     error
	revokeOnList bool
}

func newEventRepo() *eventTestRepo {
	return &eventTestRepo{account: true, member: true, events: map[string]model.AccountEvent{}, sks: map[string]string{}, contacts: map[string]bool{}}
}
func (r *eventTestRepo) GetAccount(context.Context, string) (*model.Account, error) {
	if r.account {
		return &model.Account{}, nil
	}
	return nil, nil
}
func (r *eventTestRepo) GetMember(context.Context, string, string) (*model.AccountMember, error) {
	if r.member {
		return &model.AccountMember{}, nil
	}
	return nil, nil
}
func (r *eventTestRepo) GetAccountEvent(_ context.Context, _, id string) (*model.AccountEvent, string, error) {
	e, ok := r.events[id]
	if !ok {
		return nil, "", nil
	}
	return &e, r.sks[id], nil
}
func (r *eventTestRepo) ListAccountEvents(_ context.Context, _, from, to string, desc bool, _ int, after string) ([]model.AccountEvent, string, error) {
	r.lastList.from, r.lastList.to, r.lastList.desc, r.lastList.after = from, to, desc, after
	if r.revokeOnList {
		r.member = false
	}
	return nil, "EVENT#2026-12-01T00:00:00Z#x", nil
}
func (r *eventTestRepo) WriteAccountEvent(_ context.Context, _, _ string, e *model.AccountEvent, op, newSK, previousSK, previousRevision string) error {
	r.writes = append(r.writes, op+"|"+newSK+"|"+previousSK+"|"+previousRevision)
	if r.writeErr != nil {
		return r.writeErr
	}
	if op == "delete" {
		delete(r.events, e.EventID)
		return nil
	}
	r.events[e.EventID], r.sks[e.EventID] = *e, newSK
	return nil
}
func (r *eventTestRepo) ExistingAccountContactIDs(_ context.Context, _ string, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = r.contacts[id]
	}
	return out, nil
}

const knownContact = "11111111-2222-4333-8444-555555555555"

func eventService(r *eventTestRepo) *AccountEventsService {
	s := NewAccountEventsService(r)
	s.now = func() time.Time { return time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC) }
	return s
}

func TestAccountEventCreateNormalizesAndBindsAuthor(t *testing.T) {
	r := newEventRepo()
	r.contacts[knownContact] = true
	s := eventService(r)
	e, err := s.Mutate(context.Background(), "acct", "user", "create", "", AccountEventInput{
		Title: "  아키텍처 리뷰 ", StartAt: "2026-11-10T14:00:00+09:00", EndAt: "2026-11-10T15:30:00+09:00",
		Attendees: []model.EventAttendee{{ContactID: knownContact, Name: "홍길동"}, {Name: "외부 참석자", Email: "a@example.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if e.Title != "아키텍처 리뷰" || e.StartAt != "2026-11-10T05:00:00Z" || e.EndAt != "2026-11-10T06:30:00Z" || e.CreatedBy != "user" || len(e.Attendees) != 2 {
		t.Fatalf("normalized: %#v", e)
	}
	if r.writes[0] != "create|"+repository.EventSortKey("2026-11-10T05:00:00Z", e.EventID)+"||" {
		t.Fatalf("write: %v", r.writes)
	}
}

func TestAccountEventValidation(t *testing.T) {
	ok := AccountEventInput{Title: "t", StartAt: "2026-11-10T14:00:00Z"}
	long := make([]model.EventAttendee, maxEventAttendees+1)
	for i := range long {
		long[i].Name = "n"
	}
	for name, in := range map[string]AccountEventInput{
		"no title":        {StartAt: ok.StartAt},
		"bad start":       {Title: "t", StartAt: "next tuesday"},
		"end before":      {Title: "t", StartAt: ok.StartAt, EndAt: "2026-11-10T13:00:00Z"},
		"span too long":   {Title: "t", StartAt: ok.StartAt, EndAt: "2026-12-30T13:00:00Z"},
		"unknown contact": {Title: "t", StartAt: ok.StartAt, Attendees: []model.EventAttendee{{ContactID: knownContact, Name: "x"}}},
		"bad contact id":  {Title: "t", StartAt: ok.StartAt, Attendees: []model.EventAttendee{{ContactID: "x", Name: "x"}}},
		"nameless":        {Title: "t", StartAt: ok.StartAt, Attendees: []model.EventAttendee{{Email: "a@b.c"}}},
		"bad email":       {Title: "t", StartAt: ok.StartAt, Attendees: []model.EventAttendee{{Name: "a", Email: "nope"}}},
		"too many":        {Title: "t", StartAt: ok.StartAt, Attendees: long},
		"revision create": {Title: "t", StartAt: ok.StartAt, Revision: knownContact},
	} {
		r := newEventRepo()
		if _, err := eventService(r).Mutate(context.Background(), "acct", "user", "create", "", in); !errors.Is(err, ErrInvalidInput) || len(r.writes) != 0 {
			t.Fatalf("%s: %v writes=%d", name, err, len(r.writes))
		}
	}
}

func TestAccountEventsRequireDirectMembership(t *testing.T) {
	r := newEventRepo()
	r.member = false
	s := eventService(r)
	if _, err := s.Mutate(context.Background(), "acct", "user", "create", "", AccountEventInput{Title: "t", StartAt: "2026-11-10T14:00:00Z"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.List(context.Background(), "acct", "user", EventListOptions{Limit: 10}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("list: %v", err)
	}
	r2 := newEventRepo()
	r2.revokeOnList = true
	if _, err := eventService(r2).List(context.Background(), "acct", "user", EventListOptions{Limit: 10}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revocation during a list must hide the page: %v", err)
	}
}

func TestAccountEventUpdateAndDeleteUseObservedRevisionAndKey(t *testing.T) {
	r := newEventRepo()
	s := eventService(r)
	e, _ := s.Mutate(context.Background(), "acct", "author", "create", "", AccountEventInput{Title: "t", StartAt: "2026-11-10T14:00:00Z"})
	oldSK := r.sks[e.EventID]
	if _, err := s.Mutate(context.Background(), "acct", "user", "update", e.EventID, AccountEventInput{Revision: knownContact, Title: "t", StartAt: "2026-11-11T14:00:00Z"}); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	updated, err := s.Mutate(context.Background(), "acct", "user", "update", e.EventID, AccountEventInput{Revision: e.Revision, Title: "moved", StartAt: "2026-11-11T14:00:00Z"})
	if err != nil || updated.CreatedBy != "author" || updated.Revision == e.Revision {
		t.Fatalf("update: %#v %v", updated, err)
	}
	if got := r.writes[len(r.writes)-1]; got != "update|"+repository.EventSortKey("2026-11-11T14:00:00Z", e.EventID)+"|"+oldSK+"|"+e.Revision {
		t.Fatalf("update write: %s", got)
	}
	if _, err := s.Mutate(context.Background(), "acct", "user", "delete", e.EventID, AccountEventInput{Revision: updated.Revision}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Mutate(context.Background(), "acct", "user", "delete", e.EventID, AccountEventInput{Revision: updated.Revision}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	r.writeErr = repository.ErrEventAccess
	if _, err := s.Mutate(context.Background(), "acct", "user", "create", "", AccountEventInput{Title: "t", StartAt: "2026-11-10T14:00:00Z"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("transactional revocation: %v", err)
	}
}

func TestAccountEventListRangesAndCursorBinding(t *testing.T) {
	r := newEventRepo()
	s := eventService(r)
	page, err := s.List(context.Background(), "acct", "user", EventListOptions{Limit: 10})
	if err != nil || r.lastList.from != "2026-11-08T09:00:00Z" || r.lastList.to != eventRangeEnd || r.lastList.desc {
		t.Fatalf("upcoming default: %+v %v", r.lastList, err)
	}
	s.now = func() time.Time { return time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC) }
	if _, err := s.List(context.Background(), "acct", "user", EventListOptions{Limit: 10, Cursor: page.NextCursor}); err != nil || r.lastList.from != "2026-11-08T09:00:00Z" || r.lastList.after == "" {
		t.Fatalf("cursor must keep the resolved range: %+v %v", r.lastList, err)
	}
	if _, err := s.List(context.Background(), "other", "user", EventListOptions{Limit: 10, Cursor: page.NextCursor}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cursor reused across accounts: %v", err)
	}
	if _, err := s.List(context.Background(), "acct", "user", EventListOptions{Limit: 10, Cursor: page.NextCursor, Descending: true}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cursor reused with another order: %v", err)
	}
	if _, err := s.List(context.Background(), "acct", "user", EventListOptions{Limit: 10, Descending: true, To: "2026-11-08T09:00:00Z"}); err != nil || r.lastList.from != eventRangeStart || !r.lastList.desc {
		t.Fatalf("past events: %+v %v", r.lastList, err)
	}
	for _, o := range []EventListOptions{{Limit: 0}, {Limit: 26}, {Limit: 5, From: "x"}, {Limit: 5, From: "2026-11-09T00:00:00Z", To: "2026-11-08T00:00:00Z"}} {
		if _, err := s.List(context.Background(), "acct", "user", o); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%+v accepted", o)
		}
	}
}

func TestAccountEventContactReferencesAreCheckedOnlyAfterAuthorization(t *testing.T) {
	r := newEventRepo()
	r.member = false
	probes := 0
	s := NewAccountEventsService(&countingContacts{eventTestRepo: r, probes: &probes})
	_, err := s.Mutate(context.Background(), "acct", "outsider", "create", "", AccountEventInput{Title: "t", StartAt: "2026-11-10T14:00:00Z", Attendees: []model.EventAttendee{{ContactID: knownContact, Name: "x"}}})
	if !errors.Is(err, ErrForbidden) || probes != 0 {
		t.Fatalf("a non-member probed contact existence: %v probes=%d", err, probes)
	}
}

type countingContacts struct {
	*eventTestRepo
	probes *int
}

func (c *countingContacts) ExistingAccountContactIDs(ctx context.Context, a string, ids []string) (map[string]bool, error) {
	*c.probes++
	return c.eventTestRepo.ExistingAccountContactIDs(ctx, a, ids)
}

func TestAccountEventsFitOneBridgeRead(t *testing.T) {
	attendees := make([]model.EventAttendee, 20)
	for i := range attendees {
		attendees[i].Name = strings.Repeat("n", 200)
	}
	in := AccountEventInput{Title: strings.Repeat("t", 250), StartAt: "2026-11-10T14:00:00Z", Notes: strings.Repeat("x", 8000), Attendees: attendees}
	r := newEventRepo()
	if _, err := eventService(r).Mutate(context.Background(), "acct", "user", "create", "", in); !errors.Is(err, ErrInvalidInput) || len(r.writes) != 0 {
		t.Fatalf("an event too large for a bridge read was accepted: %v", err)
	}
	if maxEventJSONBytes > 14000-1000 {
		t.Fatal("the event bound must leave room for the page envelope under the 14,000-byte bridge cap")
	}
}
