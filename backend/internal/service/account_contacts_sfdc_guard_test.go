package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
)

const (
	sfAcct = "001000000000001AAA"
	sfC1   = "003000000000001AAA"
	sfC2   = "003000000000002AAA"
)

func newGuardRepo() *contactCRUDRepo {
	return &contactCRUDRepo{contactTestRepo: contactTestRepo{account: true, member: true}, rows: map[string]model.AccountContact{}}
}

func putGuardContact(r *contactCRUDRepo, c model.AccountContact) model.AccountContact {
	c.ContactID, c.Revision = uuid.NewString(), uuid.NewString()
	r.rows[c.ContactID] = c
	return c
}

func TestGenericContactPathsProtectSfdcFields(t *testing.T) {
	ctx := context.Background()
	t.Run("generic_create_strips_sfdc_fields", func(t *testing.T) {
		r := newGuardRepo()
		out, err := NewAccountContactsService(r).Mutate(ctx, "acc", "u1", "create", "", model.AccountContact{Name: "Kim", Source: "sfdc", SfdcContactID: sfC1, SfdcAccountID: sfAcct, SfdcLeftAt: "2026-01-01T00:00:00Z"})
		if err != nil {
			t.Fatal(err)
		}
		if c := r.rows[out.ContactID]; c.Source != "" || c.SfdcContactID != "" || c.SfdcAccountID != "" || c.SfdcLeftAt != "" {
			t.Fatalf("stored %+v", c)
		}
	})
	t.Run("generic_update_preserves_sfdc_fields", func(t *testing.T) {
		r := newGuardRepo()
		linked := putGuardContact(r, model.AccountContact{Name: "Kim", Source: "sfdc", SfdcContactID: sfC1, SfdcAccountID: sfAcct, SfdcLeftAt: "2026-10-01T00:00:00Z"})
		plain := putGuardContact(r, model.AccountContact{Name: "Lee"})
		s := NewAccountContactsService(r)
		// A client clearing or changing the link is ignored.
		out, err := s.Mutate(ctx, "acc", "u1", "update", linked.ContactID, model.AccountContact{Name: "Kim2", Revision: linked.Revision, SfdcContactID: sfC2})
		if err != nil {
			t.Fatal(err)
		}
		c := r.rows[linked.ContactID]
		if c.Name != "Kim2" || c.Source != "sfdc" || c.SfdcContactID != sfC1 || c.SfdcAccountID != sfAcct || c.SfdcLeftAt != "2026-10-01T00:00:00Z" || *out != c {
			t.Fatalf("stored %+v", c)
		}
		// A client setting a link on an unlinked contact is ignored.
		if _, err = s.Mutate(ctx, "acc", "u1", "update", plain.ContactID, model.AccountContact{Name: "Lee", Revision: plain.Revision, Source: "sfdc", SfdcContactID: sfC2, SfdcAccountID: sfAcct, SfdcLeftAt: "x"}); err != nil {
			t.Fatal(err)
		}
		if p := r.rows[plain.ContactID]; p.Source != "" || p.SfdcContactID != "" || p.SfdcAccountID != "" || p.SfdcLeftAt != "" {
			t.Fatalf("stored %+v", p)
		}
		// A stale revision carries nothing over and fails the conditional write.
		if _, err = s.Mutate(ctx, "acc", "u1", "update", linked.ContactID, model.AccountContact{Name: "X", Revision: linked.Revision}); !errors.Is(err, ErrContactConflict) {
			t.Fatalf("stale: %v", err)
		}
	})
	t.Run("sync_option_sets_but_never_changes_a_link", func(t *testing.T) {
		r := newGuardRepo()
		plain := putGuardContact(r, model.AccountContact{Name: "Lee"})
		s := NewAccountContactsService(r)
		set := plain
		set.Source, set.SfdcContactID, set.SfdcAccountID = model.ContactSourceSfdc, sfC1, sfAcct
		if _, err := s.MutateWith(ctx, "acc", "u1", "update", plain.ContactID, set, ContactMutateOptions{sfdc: &sfdcWrite{base: &plain}}); err != nil {
			t.Fatal(err)
		}
		linked := r.rows[plain.ContactID]
		if linked.SfdcContactID != sfC1 {
			t.Fatalf("stored %+v", linked)
		}
		// The 15-character spelling of the same ID is the same link.
		same := linked
		same.SfdcContactID = sfC1[:15]
		if _, err := s.MutateWith(ctx, "acc", "u1", "update", linked.ContactID, same, ContactMutateOptions{sfdc: &sfdcWrite{base: &linked}}); err != nil {
			t.Fatal(err)
		}
		linked = r.rows[plain.ContactID]
		changed := linked
		changed.SfdcContactID = sfC2
		if _, err := s.MutateWith(ctx, "acc", "u1", "update", linked.ContactID, changed, ContactMutateOptions{sfdc: &sfdcWrite{base: &linked}}); !errors.Is(err, ErrContactConflict) {
			t.Fatalf("changed link: %v", err)
		}
		stale := linked
		stale.Revision = uuid.NewString()
		if _, err := s.MutateWith(ctx, "acc", "u1", "update", linked.ContactID, linked, ContactMutateOptions{sfdc: &sfdcWrite{base: &stale}}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("base mismatch: %v", err)
		}
	})
}
