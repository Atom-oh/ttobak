package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// eventFake is the event half of fakeMoverRepo: account -> sort key -> event,
// with unreadable rows kept beside them.
type eventFake struct {
	events          map[string]map[string]model.AccountEvent
	badEvents       map[string]map[string]bool
	eventWrites     []string
	existenceChecks int
	// beforeEventMove may fail an event move before it is applied.
	beforeEventMove func(sk string) error
}

func mergeTestEventSK(i int, id string) string {
	return fmt.Sprintf("EVENT#2026-11-%02dT00:00:00Z#%s", 1+i%28, id)
}

func (f *eventFake) putEvent(account, sk string, e model.AccountEvent) {
	if f.events == nil {
		f.events = map[string]map[string]model.AccountEvent{}
	}
	if f.events[account] == nil {
		f.events[account] = map[string]model.AccountEvent{}
	}
	if e.Revision == "" {
		e.Revision = "seed"
	}
	f.events[account][sk] = e
}

func (f *eventFake) putBadEvent(account, sk string) {
	if f.badEvents == nil {
		f.badEvents = map[string]map[string]bool{}
	}
	if f.badEvents[account] == nil {
		f.badEvents[account] = map[string]bool{}
	}
	f.badEvents[account][sk] = true
}

func (f *eventFake) ListAccountEventsForMerge(_ context.Context, account string, limit int, after string) ([]repository.MergeEventRow, []string, string, error) {
	var sks []string
	for sk := range f.events[account] {
		sks = append(sks, sk)
	}
	for sk := range f.badEvents[account] {
		sks = append(sks, sk)
	}
	sort.Strings(sks)
	var rows []repository.MergeEventRow
	var bad []string
	n := 0
	for i, sk := range sks {
		if sk <= after {
			continue
		}
		if n == limit {
			return rows, bad, sks[i-1], nil
		}
		n++
		if f.badEvents[account][sk] {
			bad = append(bad, sk)
		} else {
			rows = append(rows, repository.MergeEventRow{SK: sk, Event: f.events[account][sk]})
		}
	}
	return rows, bad, "", nil
}

func (f *fakeMoverRepo) ExistingAccountContactIDs(_ context.Context, account string, ids []string) (map[string]bool, error) {
	f.existenceChecks++
	if len(ids) > mergeContactCheckBatch {
		return nil, errors.New("too many contact references")
	}
	found := map[string]bool{}
	for _, id := range ids {
		if _, ok := f.contacts[account][id]; ok {
			found[id] = true
		}
	}
	return found, nil
}

// MoveEventForMerge applies the mover transaction: create-only target row,
// source delete at the read revision, attendees remapped like the repository.
func (f *eventFake) MoveEventForMerge(_ context.Context, job *model.AccountMergeJob, sk string, from model.AccountEvent, contactMap map[string]string) error {
	if f.beforeEventMove != nil {
		if err := f.beforeEventMove(sk); err != nil {
			return err
		}
	}
	if _, exists := f.events[job.TargetAccountID][sk]; exists {
		return repository.ErrConditionFailed
	}
	if cur, ok := f.events[job.SourceAccountID][sk]; !ok || cur.Revision != from.Revision {
		return repository.ErrConditionFailed
	}
	delete(f.events[job.SourceAccountID], sk)
	moved := from
	moved.Revision = "moved"
	moved.Attendees = make([]model.EventAttendee, len(from.Attendees))
	for i, a := range from.Attendees {
		if a.ContactID != "" {
			a.ContactID = contactMap[a.ContactID]
		}
		moved.Attendees[i] = a
	}
	f.putEvent(job.TargetAccountID, sk, moved)
	f.eventWrites = append(f.eventWrites, sk)
	return nil
}

func eventsFixture() (*AccountMergeService, *fakeMoverRepo, *model.AccountMergeJob) {
	repo := newFakeMoverRepo()
	s, job := moverFixture(repo)
	job.Step = "events"
	return s, repo, job
}

func TestMergeStepsRunEventsAfterContacts(t *testing.T) {
	at := map[string]int{}
	for i, step := range model.MergeSteps {
		at[step] = i
	}
	if c, ok := at["contacts"]; !ok || at["events"] <= c {
		t.Fatalf("events must follow contacts: %v", model.MergeSteps)
	}
}

func TestMoveEventRemapsAttendees(t *testing.T) {
	s, repo, job := eventsFixture()
	// Contacts step outcome: c1 merged into b9, c2 moved under its own ID,
	// c3 left unreadable on the source (no map row).
	repo.putContact("tgt", model.AccountContact{ContactID: "b9"})
	repo.putContact("tgt", model.AccountContact{ContactID: "c2"})
	repo.putContact("tgt", model.AccountContact{ContactID: "b5"})
	repo.maps = map[string]string{"c1": "b9", "c2": "c2", "c4": "gone"}
	sk := mergeTestEventSK(0, "e1")
	repo.putEvent("src", sk, model.AccountEvent{EventID: "e1", Title: "Kickoff", Attendees: []model.EventAttendee{
		{ContactID: "c1", Name: "Alice", Email: "a@x.com"},
		{ContactID: "c2", Name: "Bob"},
		{ContactID: "c3", Name: "Cy", Email: "cy@x.com"},
		{ContactID: "c4", Name: "Dee"},
		{ContactID: "b5", Name: "Eve"},
		{ContactID: "x#y", Name: "Fay"},
		{Name: "Gus", Email: "gus@x.com"},
	}})
	res, err := s.moveEvents(newMergeFence(context.Background()), job)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Done || res.Moved != 1 || res.Unreadable != 0 {
		t.Fatalf("result %+v", res)
	}
	if _, left := repo.events["src"][sk]; left {
		t.Error("source event must be gone")
	}
	got := repo.events["tgt"][sk]
	want := []model.EventAttendee{
		{ContactID: "b9", Name: "Alice", Email: "a@x.com"},
		{ContactID: "c2", Name: "Bob"},
		{Name: "Cy", Email: "cy@x.com"}, // no map row and no target contact
		{Name: "Dee"},                   // mapped to a target contact that no longer exists
		{ContactID: "b5", Name: "Eve"},  // already a target contact
		{Name: "Fay"},
		{Name: "Gus", Email: "gus@x.com"},
	}
	if got.Title != "Kickoff" || len(got.Attendees) != len(want) {
		t.Fatalf("target event %+v", got)
	}
	for i := range want {
		if got.Attendees[i] != want[i] {
			t.Errorf("attendee %d = %+v, want %+v", i, got.Attendees[i], want[i])
		}
	}
}

func TestMoveEvents_Idempotent(t *testing.T) {
	s, repo, job := eventsFixture()
	repo.putContact("tgt", model.AccountContact{ContactID: "b9"})
	repo.maps = map[string]string{"c1": "b9"}
	for i, id := range []string{"e1", "e2"} {
		repo.putEvent("src", mergeTestEventSK(i, id), model.AccountEvent{EventID: id, Attendees: []model.EventAttendee{{ContactID: "c1", Name: "Alice"}}})
	}
	if res, err := s.moveEvents(newMergeFence(context.Background()), job); err != nil || res.Moved != 2 || !res.Done {
		t.Fatalf("first run %+v %v", res, err)
	}
	writes := len(repo.eventWrites)
	// A crash before the cursor update re-runs from the same cursor.
	res, err := s.moveEvents(newMergeFence(context.Background()), job)
	if err != nil || res.Moved != 0 || !res.Done || len(repo.eventWrites) != writes {
		t.Fatalf("repeat run %+v %v writes=%d", res, err, len(repo.eventWrites)-writes)
	}
	for i, id := range []string{"e1", "e2"} {
		if e := repo.events["tgt"][mergeTestEventSK(i, id)]; e.Attendees[0].ContactID != "b9" {
			t.Errorf("%s attendees %+v", id, e.Attendees)
		}
	}
}

func TestMoveEventsWithoutEventsReadsNothingFromTarget(t *testing.T) {
	s, repo, job := eventsFixture()
	repo.putContact("tgt", model.AccountContact{ContactID: "b9"})
	res, err := s.moveEvents(newMergeFence(context.Background()), job)
	if err != nil || !res.Done || res.Moved != 0 {
		t.Fatalf("result %+v %v", res, err)
	}
	if repo.mapReads != 0 || repo.existenceChecks != 0 {
		t.Errorf("empty source read map=%d existence=%d", repo.mapReads, repo.existenceChecks)
	}
	repo.putEvent("src", mergeTestEventSK(0, "e1"), model.AccountEvent{EventID: "e1", Attendees: []model.EventAttendee{{Name: "Alice"}}})
	if res, err := s.moveEvents(newMergeFence(context.Background()), job); err != nil || res.Moved != 1 || repo.mapReads != 0 || repo.existenceChecks != 0 {
		t.Errorf("events without contact references: %+v %v map=%d existence=%d", res, err, repo.mapReads, repo.existenceChecks)
	}
}

func TestMoveEventsUnreadableLeftOnSource(t *testing.T) {
	s, repo, job := eventsFixture()
	repo.putEvent("src", mergeTestEventSK(0, "e1"), model.AccountEvent{EventID: "e1"})
	repo.putBadEvent("src", mergeTestEventSK(1, "e2"))
	res, err := s.moveEvents(newMergeFence(context.Background()), job)
	if err != nil || !res.Done || res.Moved != 1 || res.Unreadable != 1 {
		t.Fatalf("result %+v %v", res, err)
	}
	if !repo.badEvents["src"][mergeTestEventSK(1, "e2")] {
		t.Error("unreadable event must stay on the source")
	}
	if _, moved := repo.events["tgt"][mergeTestEventSK(1, "e2")]; moved {
		t.Error("unreadable event must not reach the target")
	}
}

func TestMoveEventsBudgetAndCursor(t *testing.T) {
	s, repo, job := eventsFixture()
	var sks []string
	for i := 0; i < mergeEventsPerRun+1; i++ {
		sk := fmt.Sprintf("EVENT#2026-11-01T00:00:00Z#e%04d", i)
		sks = append(sks, sk)
		repo.putEvent("src", sk, model.AccountEvent{EventID: fmt.Sprintf("e%04d", i), Attendees: []model.EventAttendee{{ContactID: fmt.Sprintf("c%04d", i)}}})
	}
	res, err := s.moveEvents(newMergeFence(context.Background()), job)
	if err != nil || res.Done || res.Moved != mergeEventsPerRun || res.Cursor != sks[mergeEventsPerRun-1] {
		t.Fatalf("first call %+v %v", res, err)
	}
	job.Cursor = res.Cursor
	res, err = s.moveEvents(newMergeFence(context.Background()), job)
	if err != nil || !res.Done || res.Moved != 1 {
		t.Fatalf("second call %+v %v", res, err)
	}
	if len(repo.events["src"]) != 0 || len(repo.events["tgt"]) != mergeEventsPerRun+1 {
		t.Errorf("source %d target %d", len(repo.events["src"]), len(repo.events["tgt"]))
	}
}

func TestMoveEventsStopsOnFailure(t *testing.T) {
	s, repo, job := eventsFixture()
	job.Cursor = "EVENT#2026"
	repo.putEvent("src", mergeTestEventSK(0, "e1"), model.AccountEvent{EventID: "e1"})
	repo.putEvent("src", mergeTestEventSK(1, "e2"), model.AccountEvent{EventID: "e2"})
	repo.beforeEventMove = func(sk string) error {
		if sk == mergeTestEventSK(1, "e2") {
			return repository.ErrConditionFailed
		}
		return nil
	}
	res, err := s.moveEvents(newMergeFence(context.Background()), job)
	if !errors.Is(err, repository.ErrConditionFailed) || res.Cursor != job.Cursor || res.Done {
		t.Fatalf("conflict: %+v %v", res, err)
	}
	if _, left := repo.events["src"][mergeTestEventSK(1, "e2")]; !left {
		t.Error("failed event must stay on the source")
	}
	repo.beforeEventMove = func(string) error { return repository.ErrMergeFenceLost }
	fence := newMergeFence(context.Background())
	res, err = s.moveEvents(fence, job)
	if !errors.Is(err, ErrMergeFenceLost) || res.Cursor != job.Cursor || !errors.Is(fence.Err(), ErrMergeFenceLost) {
		t.Fatalf("fence lost: %+v %v fence=%v", res, err, fence.Err())
	}
	repo.beforeEventMove = nil
	if res, err = s.moveEvents(newMergeFence(context.Background()), job); err != nil || !res.Done || res.Moved != 1 {
		t.Fatalf("retry %+v %v", res, err)
	}
}
