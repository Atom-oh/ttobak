package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type contactTestRepo struct {
	account         bool
	member          bool
	checks          int
	revokeAfterList bool
	revokeOnWrite   bool
	contacts        []model.AccountContact
	members         []model.AccountMember
	profiles        map[string]*model.User
	writeErr        error
}

func (r *contactTestRepo) GetAccount(context.Context, string) (*model.Account, error) {
	if r.account {
		return &model.Account{}, nil
	}
	return nil, nil
}
func (r *contactTestRepo) GetMember(context.Context, string, string) (*model.AccountMember, error) {
	r.checks++
	if r.member {
		return &model.AccountMember{}, nil
	}
	return nil, nil
}
func (r *contactTestRepo) GetAccountContact(context.Context, string, string) (*model.AccountContact, error) {
	return nil, nil
}
func (r *contactTestRepo) ListAccountContacts(_ context.Context, _ string, limit int, after string) ([]model.AccountContact, string, error) {
	if r.revokeAfterList {
		r.member = false
	}
	if len(r.contacts) > limit {
		return r.contacts[:limit], r.contacts[limit-1].ContactID, nil
	}
	return r.contacts, "", nil
}
func (r *contactTestRepo) WriteAccountContact(_ context.Context, _, _ string, _ *model.AccountContact, _ string) error {
	if r.revokeOnWrite {
		return repository.ErrContactAccess
	}
	return r.writeErr
}
func (r *contactTestRepo) ListSpeakerTeamMembers(_ context.Context, _ string, limit int, _ string) ([]model.AccountMember, string, error) {
	if len(r.members) > limit {
		return r.members[:limit], r.members[limit-1].UserID, nil
	}
	return r.members, "", nil
}
func (r *contactTestRepo) GetUserByID(_ context.Context, id string) (*model.User, error) {
	return r.profiles[id], nil
}

func TestAccountContactsAuthorizationAndRecheck(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name                    string
		account, member, revoke bool
	}{{"not member", true, false, false}, {"deleted account", false, true, false}, {"revoked after query", true, true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			r := &contactTestRepo{account: tc.account, member: tc.member, revokeAfterList: tc.revoke, contacts: []model.AccountContact{{Name: "private"}}}
			s := NewAccountContactsService(r)
			got, err := s.List(ctx, "account", "user", 25, "")
			if !errors.Is(err, ErrForbidden) || got != nil {
				t.Fatalf("must deny without data: %#v %v", got, err)
			}
		})
	}
	r := &contactTestRepo{account: true, member: true, revokeOnWrite: true}
	s := NewAccountContactsService(r)
	for _, op := range []string{"create", "update", "delete"} {
		_, err := s.Mutate(ctx, "account", "user", op, "01234567-89ab-4cde-8123-0123456789ab", model.AccountContact{Name: "A", Revision: "01234567-89ab-4cde-8123-0123456789ab"})
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s revocation: %v", op, err)
		}
	}
}

func TestAccountContactsPaginationAndValidation(t *testing.T) {
	ctx := context.Background()
	r := &contactTestRepo{account: true, member: true, contacts: []model.AccountContact{{ContactID: "01234567-89ab-4cde-8123-0123456789ab"}, {ContactID: "11234567-89ab-4cde-8123-0123456789ab"}}}
	s := NewAccountContactsService(r)
	page, err := s.List(ctx, "account", "user", 1, "")
	if err != nil || len(page.Contacts) != 1 || page.NextCursor == "" {
		t.Fatalf("first page: %#v %v", page, err)
	}
	if _, err = s.List(ctx, "other", "user", 1, page.NextCursor); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-account cursor: %v", err)
	}
	if _, err = s.List(ctx, "account", "user", 1, "invalid"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid cursor: %v", err)
	}
	for _, limit := range []int{0, 26, -1} {
		if _, err = s.List(ctx, "account", "user", limit, ""); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
	for _, input := range []model.AccountContact{{Name: " "}, {Name: strings.Repeat("a", 257)}, {Name: "a", Notes: strings.Repeat("a", 8193)}} {
		if _, err = s.Mutate(ctx, "account", "user", "create", "", input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid fields: %v", err)
		}
	}
	if _, err = s.Mutate(ctx, "account", "user", "create", "", model.AccountContact{Name: "a", Notes: strings.Repeat("\x00", 2000)}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("escaped JSON must fit response budget: %v", err)
	}
	if _, err = s.Mutate(ctx, "account#bad", "user", "create", "", model.AccountContact{Name: "a"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unsafe account id: %v", err)
	}
	if _, err = s.Mutate(ctx, "account", "user", "delete", "../../x", model.AccountContact{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unsafe contact id: %v", err)
	}
	r.writeErr = repository.ErrConditionFailed
	if _, err = s.Mutate(ctx, "account", "user", "update", "01234567-89ab-4cde-8123-0123456789ab", model.AccountContact{Name: "A", Revision: "01234567-89ab-4cde-8123-0123456789ab"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing contact: %v", err)
	}
}

func TestSpeakerTeamProfileFallback(t *testing.T) {
	r := &contactTestRepo{account: true, member: true, members: []model.AccountMember{{UserID: "a", Email: "a@example.test", Role: "owner"}, {UserID: "b", Email: "b@example.test", Role: "AM"}}, profiles: map[string]*model.User{"a": {Name: "Alice"}}}
	team, err := NewAccountContactsService(r).SpeakerTeamPage(context.Background(), "account", "user", 25, "")
	if err != nil || len(team.Members) != 2 || team.Members[0].Name != "Alice" || team.Members[1].Name != "b@example.test" || r.checks != 2 {
		t.Fatalf("team: %#v %v checks %d", team, err, r.checks)
	}
}

func TestSpeakerTeamPagesRecheckAccessAndBindCursor(t *testing.T) {
	r := &contactTestRepo{account: true, member: true, members: []model.AccountMember{{UserID: "a"}, {UserID: "b"}}}
	s := NewAccountContactsService(r)
	first, err := s.SpeakerTeamPage(context.Background(), "account", "user", 1, "")
	if err != nil || len(first.Members) != 1 || first.NextCursor == "" {
		t.Fatalf("page: %#v %v", first, err)
	}
	if _, err = s.SpeakerTeamPage(context.Background(), "other", "user", 1, first.NextCursor); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("foreign cursor: %v", err)
	}
	r.member = false
	page, err := s.SpeakerTeamPage(context.Background(), "account", "user", 1, first.NextCursor)
	if !errors.Is(err, ErrForbidden) || page != nil {
		t.Fatalf("revoked: %#v %v", page, err)
	}
}

func TestContactEveryPageReauthorizes(t *testing.T) {
	r := &contactTestRepo{account: true, member: true, contacts: []model.AccountContact{{ContactID: "01234567-89ab-4cde-8123-0123456789ab"}, {ContactID: "11234567-89ab-4cde-8123-0123456789ab"}}}
	s := NewAccountContactsService(r)
	page, err := s.List(context.Background(), "account", "user", 1, "")
	if err != nil || page.NextCursor == "" {
		t.Fatal(err)
	}
	r.member = false
	got, err := s.List(context.Background(), "account", "user", 1, page.NextCursor)
	if !errors.Is(err, ErrForbidden) || got != nil {
		t.Fatalf("revoked page: %#v %v", got, err)
	}
}

type contactCRUDRepo struct {
	contactTestRepo
	rows map[string]model.AccountContact
}

func (r *contactCRUDRepo) GetAccountContact(_ context.Context, _, id string) (*model.AccountContact, error) {
	c, ok := r.rows[id]
	if !ok {
		return nil, nil
	}
	return &c, nil
}

func (r *contactCRUDRepo) WriteAccountContact(_ context.Context, _, _ string, c *model.AccountContact, op string) error {
	if op == "create" {
		if _, ok := r.rows[c.ContactID]; ok {
			return repository.ErrConditionFailed
		}
		r.rows[c.ContactID] = *c
		return nil
	}
	current, ok := r.rows[c.ContactID]
	if !ok || current.Revision != c.Revision {
		return repository.ErrConditionFailed
	}
	if op == "delete" {
		delete(r.rows, c.ContactID)
	} else {
		c.Revision = uuid.NewString()
		r.rows[c.ContactID] = *c
	}
	return nil
}
func (r *contactCRUDRepo) ListAccountContacts(_ context.Context, _ string, _ int, _ string) ([]model.AccountContact, string, error) {
	result := []model.AccountContact{}
	for _, c := range r.rows {
		result = append(result, c)
	}
	return result, "", nil
}
func TestAccountContactsCRUDPreservesRememberFields(t *testing.T) {
	r := &contactCRUDRepo{contactTestRepo: contactTestRepo{account: true, member: true}, rows: map[string]model.AccountContact{}}
	s := NewAccountContactsService(r)
	ctx := context.Background()
	input := model.AccountContact{Name: "이름", Company: "회사", Title: "직함", Department: "부서", Email: "a@example.test", Address: "주소", WorkPhone: "work", Fax: "fax", Phone: "mobile", CardRegisteredAt: "날짜", CardBook: "명함첩", Group: "그룹", Notes: "메모"}
	created, err := s.Mutate(ctx, "account", "user", "create", "", input)
	if err != nil || !validContactID(created.ContactID) {
		t.Fatalf("create: %#v %v", created, err)
	}
	page, err := s.List(ctx, "account", "user", 25, "")
	if err != nil || len(page.Contacts) != 1 || page.Contacts[0] != *created {
		t.Fatalf("round trip: %#v %v", page, err)
	}
	replacement := input
	replacement.Name = "다른 이름"
	replacement.Revision = created.Revision
	updated, err := s.Mutate(ctx, "account", "user", "update", created.ContactID, replacement)
	if err != nil || updated.Name != replacement.Name || r.rows[created.ContactID] != *updated {
		t.Fatalf("update: %#v %v", updated, err)
	}
	if _, err = s.Mutate(ctx, "account", "user", "update", created.ContactID, replacement); !errors.Is(err, ErrContactConflict) {
		t.Fatalf("stale update must not overwrite: %v", err)
	}
	if _, err = s.Mutate(ctx, "account", "user", "delete", created.ContactID, model.AccountContact{Revision: created.Revision}); !errors.Is(err, ErrContactConflict) {
		t.Fatalf("stale delete must not remove newer changes: %v", err)
	}
	if _, err = s.Mutate(ctx, "account", "user", "delete", created.ContactID, model.AccountContact{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("delete requires a revision: %v", err)
	}
	if _, err = s.Mutate(ctx, "account", "user", "delete", created.ContactID, model.AccountContact{Revision: updated.Revision}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Mutate(ctx, "account", "user", "delete", created.ContactID, model.AccountContact{Revision: updated.Revision}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing after delete: %v", err)
	}
}
