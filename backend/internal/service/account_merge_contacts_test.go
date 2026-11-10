package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

var _ accountMergeMoverRepo = (*repository.DynamoDBRepository)(nil)

// contactFake is the contact half of fakeMoverRepo: account -> id -> contact,
// with unreadable rows and MERGE#contact# map rows kept beside them.
type contactFake struct {
	contacts   map[string]map[string]model.AccountContact
	unreadable map[string]map[string]bool
	maps       map[string]string
	writes     []string
	revs       int
	// sourceName is the source account's name.
	sourceName string
	// beforeWrite may fail a contact write before it is applied.
	beforeWrite func(op, id string) error
}

func (f *contactFake) putContact(account string, c model.AccountContact) {
	if f.contacts == nil {
		f.contacts = map[string]map[string]model.AccountContact{}
	}
	if f.contacts[account] == nil {
		f.contacts[account] = map[string]model.AccountContact{}
	}
	if c.Revision == "" {
		c.Revision = "seed"
	}
	f.contacts[account][c.ContactID] = c
}

func (f *contactFake) putUnreadable(account, id string) {
	if f.unreadable == nil {
		f.unreadable = map[string]map[string]bool{}
	}
	if f.unreadable[account] == nil {
		f.unreadable[account] = map[string]bool{}
	}
	f.unreadable[account][id] = true
}

func (f *contactFake) ids(account string) []string {
	var ids []string
	for id := range f.contacts[account] {
		ids = append(ids, id)
	}
	for id := range f.unreadable[account] {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (f *contactFake) ListAccountPartitionKeys(_ context.Context, account, prefix string, limit int) ([]string, bool, error) {
	if prefix != mergeContactPrefix {
		return nil, false, fmt.Errorf("unexpected prefix %s", prefix)
	}
	ids := f.ids(account)
	if len(ids) > limit {
		return ids[:limit], true, nil
	}
	return ids, false, nil
}

func (f *contactFake) ListAccountContactsForMerge(_ context.Context, account string, limit int, after string) ([]model.AccountContact, []string, string, error) {
	var page []model.AccountContact
	var bad []string
	ids := f.ids(account)
	n := 0
	for i, id := range ids {
		if id <= after {
			continue
		}
		if n == limit {
			return page, bad, ids[i-1], nil
		}
		n++
		if f.unreadable[account][id] {
			bad = append(bad, id)
		} else {
			page = append(page, f.contacts[account][id])
		}
	}
	return page, bad, "", nil
}

func (f *contactFake) GetAccount(_ context.Context, id string) (*model.Account, error) {
	return &model.Account{AccountID: id, Name: f.sourceName}, nil
}

func (f *contactFake) GetAccountContact(_ context.Context, account, id string) (*model.AccountContact, error) {
	c, ok := f.contacts[account][id]
	if !ok {
		return nil, nil
	}
	return &c, nil
}

func (f *contactFake) GetMergeContactMap(_ context.Context, _ string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if t, ok := f.maps[id]; ok {
			out[id] = t
		}
	}
	return out, nil
}

// relocate applies the transaction both writers share: source delete at the
// read revision, create-only map row.
func (f *contactFake) relocate(job *model.AccountMergeJob, from model.AccountContact, target string) error {
	if cur, ok := f.contacts[job.SourceAccountID][from.ContactID]; !ok || cur.Revision != from.Revision {
		return repository.ErrConditionFailed
	}
	if _, ok := f.maps[from.ContactID]; ok {
		return repository.ErrConditionFailed
	}
	if f.maps == nil {
		f.maps = map[string]string{}
	}
	delete(f.contacts[job.SourceAccountID], from.ContactID)
	f.maps[from.ContactID] = target
	return nil
}

func (f *contactFake) nextRev() string {
	f.revs++
	return fmt.Sprintf("rev-%d", f.revs)
}

func (f *contactFake) MoveContactForMerge(_ context.Context, job *model.AccountMergeJob, from model.AccountContact) error {
	if f.beforeWrite != nil {
		if err := f.beforeWrite("move", from.ContactID); err != nil {
			return err
		}
	}
	if _, exists := f.contacts[job.TargetAccountID][from.ContactID]; exists {
		return repository.ErrConditionFailed
	}
	if err := f.relocate(job, from, from.ContactID); err != nil {
		return err
	}
	moved := from
	moved.Revision = f.nextRev()
	f.putContact(job.TargetAccountID, moved)
	f.writes = append(f.writes, "move:"+from.ContactID)
	return nil
}

func (f *contactFake) MergeContactIntoExisting(_ context.Context, job *model.AccountMergeJob, from, into model.AccountContact) error {
	if f.beforeWrite != nil {
		if err := f.beforeWrite("merge", from.ContactID); err != nil {
			return err
		}
	}
	if cur, ok := f.contacts[job.TargetAccountID][into.ContactID]; !ok || cur.Revision != into.Revision {
		return repository.ErrConditionFailed
	}
	if err := f.relocate(job, from, into.ContactID); err != nil {
		return err
	}
	into.Revision = f.nextRev()
	f.putContact(job.TargetAccountID, into)
	f.writes = append(f.writes, "merge:"+from.ContactID+">"+into.ContactID)
	return nil
}

func contactsFixture() (*AccountMergeService, *fakeMoverRepo, *model.AccountMergeJob) {
	repo := newFakeMoverRepo()
	s, job := moverFixture(repo)
	job.Step = "contacts"
	return s, repo, job
}

func TestContactMatchKeys(t *testing.T) {
	if a, b := contactMatchKey(model.AccountContact{Email: " A@X.com "}), contactMatchKey(model.AccountContact{Email: "a@x.com"}); a != "e:a@x.com" || a != b {
		t.Errorf("email keys %q vs %q must be equal", a, b)
	}
	for _, c := range []model.AccountContact{{}, {Email: "  "}, {Phone: "010-1234-5678"}, {WorkPhone: "02-555-0100"}, {Name: "Alice"}} {
		if k := contactMatchKey(c); k != "" {
			t.Errorf("%+v: want no key, got %q", c, k)
		}
	}
}

func TestMergeContactFieldsBWins(t *testing.T) {
	into := model.AccountContact{ContactID: "b9", Revision: "rb", Name: "Alice Kim", Company: "  ", Title: "VP", Email: "alice@x.com", Notes: "B note"}
	from := model.AccountContact{ContactID: "c1", Revision: "ra", Name: "Alice", Company: "Acme", Title: "CTO", Email: " ALICE@x.com",
		Address: "Seoul", WorkPhone: "02-1", Fax: "02-2", Phone: "010-3", CardRegisteredAt: "2026-01-01", CardBook: "book",
		Group: "g", Department: "R&D", Notes: "VP"}
	got, ok := mergeContactFields(into, from, "Acme Korea")
	if !ok {
		t.Fatal("merge within limits must fit")
	}
	want := model.AccountContact{ContactID: "b9", Revision: "rb", Name: "Alice Kim", Company: "Acme", Title: "VP", Email: "alice@x.com",
		Address: "Seoul", WorkPhone: "02-1", Fax: "02-2", Phone: "010-3", CardRegisteredAt: "2026-01-01", CardBook: "book",
		Group: "g", Department: "R&D",
		Notes: "B note\n[merged from Acme Korea] name: Alice\n[merged from Acme Korea] title: CTO\n[merged from Acme Korea] notes: VP"}
	if got != want {
		t.Errorf("merged\n got %+v\nwant %+v", got, want)
	}
	same, ok := mergeContactFields(want, model.AccountContact{Name: "Alice Kim", Email: "ALICE@X.COM"}, "Acme Korea")
	if !ok || same.Notes != want.Notes {
		t.Errorf("equal values must add no notes: %q", same.Notes)
	}
	long := into
	long.Notes = strings.Repeat("n", contactNotesMaxBytes-10)
	if _, ok := mergeContactFields(long, from, "Acme Korea"); ok {
		t.Error("notes beyond the limit must not fit")
	}
}

// Review Focus 5: case/whitespace email variants merge; conflicting values
// survive in notes.
func TestMoveContactsDedupe(t *testing.T) {
	s, repo, job := contactsFixture()
	repo.sourceName = "Acme Korea"
	repo.putContact("tgt", model.AccountContact{ContactID: "b9", Name: "Alice Kim", Title: "VP", Email: "a@x.com"})
	repo.putContact("src", model.AccountContact{ContactID: "c1", Name: "Alice", Email: " A@X.com ", Title: "CTO"})
	repo.putContact("src", model.AccountContact{ContactID: "c3", Name: "Cy", Email: "cy@x.com"})
	repo.putContact("src", model.AccountContact{ContactID: "c4", Name: "Cy", Email: "CY@x.com", Phone: "010-9999-0000"})
	res, err := s.moveContacts(newMergeFence(context.Background()), job)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Done || res.Moved != 3 || res.Skipped != 0 || res.Unreadable != 0 || res.KeptSeparate != 0 {
		t.Fatalf("result %+v", res)
	}
	if want := map[string]string{"c1": "b9", "c3": "c3", "c4": "c3"}; !reflect.DeepEqual(repo.maps, want) {
		t.Errorf("map rows %v, want %v", repo.maps, want)
	}
	if len(repo.contacts["src"]) != 0 || len(repo.contacts["tgt"]) != 2 {
		t.Errorf("src %d tgt %d contacts", len(repo.contacts["src"]), len(repo.contacts["tgt"]))
	}
	b9 := repo.contacts["tgt"]["b9"]
	if b9.Name != "Alice Kim" || b9.Title != "VP" ||
		b9.Notes != "[merged from Acme Korea] name: Alice\n[merged from Acme Korea] title: CTO" {
		t.Errorf("b9 %+v", b9)
	}
	if c3 := repo.contacts["tgt"]["c3"]; c3.Name != "Cy" || c3.Phone != "010-9999-0000" || c3.Notes != "" {
		t.Errorf("c3 must absorb c4: %+v", c3)
	}
}

func TestMoveContactsSharedPhoneNotMerged(t *testing.T) {
	s, repo, job := contactsFixture()
	repo.putContact("tgt", model.AccountContact{ContactID: "b1", Name: "Bo", WorkPhone: "02-555-0100", Phone: "010-1234-5678"})
	repo.putContact("src", model.AccountContact{ContactID: "c1", Name: "Cy", WorkPhone: "025550100", Phone: "01012345678"})
	res, err := s.moveContacts(newMergeFence(context.Background()), job)
	if err != nil || res.Moved != 1 || res.KeptSeparate != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if repo.maps["c1"] != "c1" || repo.contacts["tgt"]["b1"].Name != "Bo" || repo.contacts["tgt"]["c1"].Name != "Cy" {
		t.Errorf("people sharing a phone must stay separate: %v", repo.maps)
	}
}

func TestMoveContactsNotesOverflowKeepsSeparate(t *testing.T) {
	s, repo, job := contactsFixture()
	full := strings.Repeat("n", contactNotesMaxBytes-5)
	repo.putContact("tgt", model.AccountContact{ContactID: "b9", Name: "Alice Kim", Email: "a@x.com", Notes: full})
	repo.putContact("src", model.AccountContact{ContactID: "c1", Name: "Alice", Email: "a@x.com", Title: "CTO"})
	res, err := s.moveContacts(newMergeFence(context.Background()), job)
	if err != nil || res.Moved != 1 || res.KeptSeparate != 1 || !res.Done {
		t.Fatalf("%+v %v", res, err)
	}
	if repo.maps["c1"] != "c1" || repo.contacts["tgt"]["b9"].Notes != full || repo.contacts["tgt"]["c1"].Title != "CTO" {
		t.Errorf("overflow must keep the source contact separate and leave b9 unchanged: %v", repo.maps)
	}
}

func TestMoveContacts_Idempotent(t *testing.T) {
	s, repo, job := contactsFixture()
	repo.putContact("tgt", model.AccountContact{ContactID: "b9", Email: "a@x.com"})
	repo.putContact("src", model.AccountContact{ContactID: "c1", Email: "a@x.com"})
	repo.putContact("src", model.AccountContact{ContactID: "c2", Email: "new@x.com"})
	if _, err := s.moveContacts(newMergeFence(context.Background()), job); err != nil {
		t.Fatal(err)
	}
	writes := len(repo.writes)
	res, err := s.moveContacts(newMergeFence(context.Background()), job)
	if err != nil || res.Moved != 0 || !res.Done || len(repo.writes) != writes {
		t.Fatalf("rerun: %+v %v writes %d -> %d", res, err, writes, len(repo.writes))
	}

	// A source row that already has a map row is skipped, never rewritten.
	repo.putContact("src", model.AccountContact{ContactID: "c1", Email: "a@x.com"})
	res, err = s.moveContacts(newMergeFence(context.Background()), job)
	if err != nil || res.Moved != 0 || res.Skipped != 1 || len(repo.writes) != writes {
		t.Fatalf("mapped row: %+v %v", res, err)
	}
	if repo.maps["c1"] != "b9" {
		t.Errorf("mapping changed: %v", repo.maps)
	}
}

func TestMoveContactsIndexCap(t *testing.T) {
	for name, tc := range map[string]struct{ target, source int }{
		"target over cap":             {target: mergeContactIndexCap + 1, source: 1},
		"target plus source over cap": {target: mergeContactIndexCap - 5, source: 6},
	} {
		t.Run(name, func(t *testing.T) {
			s, repo, job := contactsFixture()
			for i := 0; i < tc.target; i++ {
				repo.putContact("tgt", model.AccountContact{ContactID: fmt.Sprintf("b%05d", i), Email: fmt.Sprintf("b%d@x.com", i)})
			}
			for i := 0; i < tc.source; i++ {
				repo.putContact("src", model.AccountContact{ContactID: fmt.Sprintf("c%d", i)})
			}
			res, err := s.moveContacts(newMergeFence(context.Background()), job)
			if !errors.Is(err, ErrMergeContactIndexTooLarge) || MergeFailureCode(err) != "CONTACT_INDEX_TOO_LARGE" {
				t.Fatalf("got %v", err)
			}
			if len(repo.writes) != 0 || res.Cursor != job.Cursor || res.Done {
				t.Errorf("writes %v result %+v", repo.writes, res)
			}
		})
	}
	s, repo, job := contactsFixture()
	for i := 0; i < mergeContactIndexCap-1; i++ {
		repo.putContact("tgt", model.AccountContact{ContactID: fmt.Sprintf("b%05d", i)})
	}
	repo.putContact("src", model.AccountContact{ContactID: "c1"})
	if res, err := s.moveContacts(newMergeFence(context.Background()), job); err != nil || res.Moved != 1 {
		t.Fatalf("exactly at cap: %+v %v", res, err)
	}
}

func TestMoveContactsUnreadableLeftOnSource(t *testing.T) {
	s, repo, job := contactsFixture()
	repo.putContact("src", model.AccountContact{ContactID: "c1", Email: "a@x.com"})
	repo.putUnreadable("src", "c2")
	repo.putContact("src", model.AccountContact{ContactID: "c3", Email: "c@x.com"})
	repo.putUnreadable("tgt", "b1")
	res, err := s.moveContacts(newMergeFence(context.Background()), job)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Done || res.Moved != 2 || res.Unreadable != 1 {
		t.Fatalf("result %+v", res)
	}
	if !repo.unreadable["src"]["c2"] || repo.maps["c2"] != "" {
		t.Error("unreadable source row must stay in place without a mapping")
	}
}

func TestMoveContactsPerRunAndCursor(t *testing.T) {
	s, repo, job := contactsFixture()
	for i := 0; i < mergeContactsPerRun+3; i++ {
		repo.putContact("src", model.AccountContact{ContactID: fmt.Sprintf("c%04d", i)})
	}
	res, err := s.moveContacts(newMergeFence(context.Background()), job)
	if err != nil || res.Done || res.Moved != mergeContactsPerRun || res.Cursor != fmt.Sprintf("c%04d", mergeContactsPerRun-1) {
		t.Fatalf("first call %+v %v", res, err)
	}
	job.Cursor = res.Cursor
	res, err = s.moveContacts(newMergeFence(context.Background()), job)
	if err != nil || !res.Done || res.Moved != 3 {
		t.Fatalf("second call %+v %v", res, err)
	}
}

func TestMoveContactsStopsOnFailure(t *testing.T) {
	s, repo, job := contactsFixture()
	job.Cursor = "c0"
	repo.putContact("tgt", model.AccountContact{ContactID: "b9", Email: "a@x.com"})
	repo.putContact("src", model.AccountContact{ContactID: "c1", Email: "new@x.com"})
	repo.putContact("src", model.AccountContact{ContactID: "c2", Email: "a@x.com"})
	repo.beforeWrite = func(op, _ string) error {
		if op == "merge" {
			return repository.ErrConditionFailed // a concurrent edit of b9
		}
		return nil
	}
	res, err := s.moveContacts(newMergeFence(context.Background()), job)
	if !errors.Is(err, repository.ErrConditionFailed) || res.Cursor != "c0" || res.Done {
		t.Fatalf("conflict: %+v %v", res, err)
	}
	if _, ok := repo.contacts["src"]["c2"]; !ok {
		t.Fatal("source row must stay until its target write commits")
	}
	repo.beforeWrite = nil
	if res, err = s.moveContacts(newMergeFence(context.Background()), job); err != nil || !res.Done || res.Moved != 1 || repo.maps["c2"] != "b9" {
		t.Fatalf("retry: %+v %v %v", res, err, repo.maps)
	}

	s, repo, job = contactsFixture()
	repo.putContact("src", model.AccountContact{ContactID: "c1"})
	repo.putContact("src", model.AccountContact{ContactID: "c2"})
	repo.beforeWrite = func(string, string) error { return repository.ErrMergeFenceLost }
	fence := newMergeFence(context.Background())
	if res, err := s.moveContacts(fence, job); !errors.Is(err, ErrMergeFenceLost) || res.Cursor != "" || len(repo.writes) != 0 {
		t.Fatalf("fence lost: %+v %v", res, err)
	}
	if !errors.Is(fence.Err(), ErrMergeFenceLost) {
		t.Error("a lost fence must cancel the mover context")
	}
}
