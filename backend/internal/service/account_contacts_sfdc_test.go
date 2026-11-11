package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// sfdcSyncRepo emulates the repository's link reservation: one index row per
// SFDC ID, suppression rows that refuse a link, and revision CAS.
type sfdcSyncFake struct {
	contactCRUDRepo
	accountName string
	index       map[string]string // sfdcContactId -> contactId
	suppressed  map[string]bool
	writes      []string
	writeErr    func(c *model.AccountContact, op string) error
}

func newSfdcSyncFake() *sfdcSyncFake {
	return &sfdcSyncFake{
		contactCRUDRepo: contactCRUDRepo{contactTestRepo: contactTestRepo{account: true, member: true}, rows: map[string]model.AccountContact{}},
		accountName:     "Acme Korea", index: map[string]string{}, suppressed: map[string]bool{},
	}
}

func (r *sfdcSyncFake) GetAccount(ctx context.Context, id string) (*model.Account, error) {
	a, err := r.contactTestRepo.GetAccount(ctx, id)
	if a != nil {
		a.Name = r.accountName
	}
	return a, err
}

func (r *sfdcSyncFake) WriteAccountContact(ctx context.Context, account, user string, c *model.AccountContact, op string) error {
	r.writes = append(r.writes, op+":"+c.ContactID)
	if r.writeErr != nil {
		if err := r.writeErr(c, op); err != nil {
			return err
		}
	}
	if op != "delete" && c.SfdcContactID != "" {
		if r.suppressed[c.SfdcContactID] {
			return fmt.Errorf("%w: cancelled", repository.ErrSfdcSuppressed)
		}
		if holder, ok := r.index[c.SfdcContactID]; ok && holder != c.ContactID {
			return fmt.Errorf("%w: cancelled", repository.ErrSfdcAlreadyLinked)
		}
	}
	if err := r.contactCRUDRepo.WriteAccountContact(ctx, account, user, c, op); err != nil {
		return err
	}
	if op != "delete" && c.SfdcContactID != "" {
		r.index[c.SfdcContactID] = c.ContactID
	}
	return nil
}

func (r *sfdcSyncFake) ListSfdcContactLinks(context.Context, string) ([]repository.SfdcContactLink, error) {
	out := []repository.SfdcContactLink{}
	for sid, cid := range r.index {
		out = append(out, repository.SfdcContactLink{SfdcContactID: sid, SfdcAccountID: r.rows[cid].SfdcAccountID, ContactID: cid})
	}
	return out, nil
}

func (r *sfdcSyncFake) put(c model.AccountContact) model.AccountContact {
	c.ContactID, c.Revision = uuid.NewString(), uuid.NewString()
	r.rows[c.ContactID] = c
	if c.SfdcContactID != "" {
		r.index[c.SfdcContactID] = c.ContactID
	}
	return c
}

const (
	sfAcct  = "001000000000001AAA"
	sfAcct2 = "001000000000002AAA"
	sfC1    = "003000000000001AAA"
	sfC2    = "003000000000002AAA"
	sfC3    = "003000000000003AAA"
)

func newSfdcSync(r *sfdcSyncFake) (*AccountContactSyncService, *[]model.PeopleResolveRequested) {
	contacts := NewAccountContactsService(r)
	var published []model.PeopleResolveRequested
	contacts.SetPeopleResolvePublisher(func(_ context.Context, e model.PeopleResolveRequested) error {
		published = append(published, e)
		return nil
	})
	s := NewAccountContactSyncService(r, contacts)
	s.now = func() time.Time { return time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC) }
	return s, &published
}

func TestSfdcContactSync(t *testing.T) {
	ctx := context.Background()

	t.Run("create_sets_company_and_sfdc_fields", func(t *testing.T) {
		r := newSfdcSyncFake()
		s, _ := newSfdcSync(r)
		out, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SfdcAccountIDs: []string{sfAcct},
			Items: []model.SfdcContactSyncItem{{SfdcContactID: sfC1, SfdcAccountID: sfAcct, Name: "Kim", Title: "CTO", Action: "create"}}})
		if err != nil || len(out.Results) != 1 || out.Results[0].Result != model.SfdcSyncCreated || !validContactID(out.Results[0].ContactID) {
			t.Fatalf("got %+v %v", out, err)
		}
		c := r.rows[out.Results[0].ContactID]
		want := model.AccountContact{ContactID: c.ContactID, Revision: c.Revision, Name: "Kim", Title: "CTO", Company: "Acme Korea",
			Source: model.ContactSourceSfdc, SfdcContactID: sfC1, SfdcAccountID: sfAcct}
		if c != want {
			t.Fatalf("stored %+v", c)
		}
	})

	t.Run("create_reports_already_linked_and_suppressed", func(t *testing.T) {
		r := newSfdcSyncFake()
		r.put(model.AccountContact{Name: "Kim", SfdcContactID: sfC1, SfdcAccountID: sfAcct})
		r.suppressed[sfC2] = true
		s, _ := newSfdcSync(r)
		out, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SfdcAccountIDs: []string{sfAcct}, Items: []model.SfdcContactSyncItem{
			{SfdcContactID: sfC1, SfdcAccountID: sfAcct, Name: "Kim", Action: "create"},
			{SfdcContactID: sfC2, SfdcAccountID: sfAcct, Name: "Lee", Action: "create"}}})
		if err != nil || out.Results[0].Result != model.SfdcSyncAlreadyLinked || out.Results[1].Result != model.SfdcSyncSuppressed || len(r.rows) != 1 {
			t.Fatalf("got %+v %v", out, err)
		}
	})

	t.Run("link_fills_only_empty_fields_and_never_changes_name", func(t *testing.T) {
		r := newSfdcSyncFake()
		empty := r.put(model.AccountContact{Name: "김철수", Email: "kim@example.test"})
		full := r.put(model.AccountContact{Name: "Lee", Title: "VP", Company: "Card Co"})
		s, _ := newSfdcSync(r)
		out, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SfdcAccountIDs: []string{sfAcct}, Items: []model.SfdcContactSyncItem{
			{SfdcContactID: sfC1, SfdcAccountID: sfAcct, Name: "Chulsoo Kim", Title: "CTO", Action: "link", ContactID: empty.ContactID, Revision: empty.Revision},
			{SfdcContactID: sfC2, SfdcAccountID: sfAcct, Name: "Other", Title: "CEO", Action: "link", ContactID: full.ContactID, Revision: full.Revision}}})
		if err != nil || out.Results[0].Result != model.SfdcSyncFilled || out.Results[1].Result != model.SfdcSyncFilled {
			t.Fatalf("got %+v %v", out, err)
		}
		a, b := r.rows[empty.ContactID], r.rows[full.ContactID]
		if a.Name != "김철수" || a.Title != "CTO" || a.Company != "Acme Korea" || a.Email != "kim@example.test" || a.SfdcContactID != sfC1 || a.SfdcAccountID != sfAcct || a.Source != model.ContactSourceSfdc {
			t.Fatalf("filled %+v", a)
		}
		if b.Name != "Lee" || b.Title != "VP" || b.Company != "Card Co" || b.SfdcContactID != sfC2 {
			t.Fatalf("non-empty fields changed %+v", b)
		}
		// Linked and nothing to fill: no write.
		before := len(r.writes)
		b = r.rows[full.ContactID]
		out, err = s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SfdcAccountIDs: []string{sfAcct}, Items: []model.SfdcContactSyncItem{
			{SfdcContactID: sfC2, SfdcAccountID: sfAcct, Name: "Other", Title: "CEO", Action: "link", ContactID: b.ContactID, Revision: b.Revision}}})
		if err != nil || out.Results[0].Result != model.SfdcSyncUnchanged || len(r.writes) != before {
			t.Fatalf("unchanged %+v %v writes=%d", out, err, len(r.writes)-before)
		}
	})

	t.Run("link_with_stale_revision_is_conflict", func(t *testing.T) {
		r := newSfdcSyncFake()
		c := r.put(model.AccountContact{Name: "Kim"})
		s, _ := newSfdcSync(r)
		out, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SfdcAccountIDs: []string{sfAcct}, Items: []model.SfdcContactSyncItem{
			{SfdcContactID: sfC1, SfdcAccountID: sfAcct, Name: "Kim", Title: "CTO", Action: "link", ContactID: c.ContactID, Revision: uuid.NewString()},
			{SfdcContactID: sfC2, SfdcAccountID: sfAcct, Name: "Gone", Action: "link", ContactID: uuid.NewString(), Revision: uuid.NewString()}}})
		if err != nil || out.Results[0].Result != model.SfdcSyncConflict || out.Results[1].Result != model.SfdcSyncConflict || len(r.writes) != 0 || r.rows[c.ContactID].Title != "" {
			t.Fatalf("got %+v %v", out, err)
		}
	})

	t.Run("link_to_contact_with_other_sfdc_id_is_conflict", func(t *testing.T) {
		r := newSfdcSyncFake()
		c := r.put(model.AccountContact{Name: "Kim", SfdcContactID: sfC2, SfdcAccountID: sfAcct, Source: model.ContactSourceSfdc})
		s, _ := newSfdcSync(r)
		out, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SfdcAccountIDs: []string{sfAcct}, Items: []model.SfdcContactSyncItem{
			{SfdcContactID: sfC1, SfdcAccountID: sfAcct, Name: "Kim", Title: "CTO", Action: "link", ContactID: c.ContactID, Revision: c.Revision}}})
		if err != nil || out.Results[0].Result != model.SfdcSyncConflict || len(r.writes) != 0 || r.rows[c.ContactID].SfdcContactID != sfC2 {
			t.Fatalf("got %+v %v", out, err)
		}
	})

	t.Run("transaction_conflict_is_item_conflict", func(t *testing.T) {
		r := newSfdcSyncFake()
		r.writeErr = func(*model.AccountContact, string) error {
			return &types.TransactionCanceledException{CancellationReasons: []types.CancellationReason{{Code: aws.String("None")}, {Code: aws.String("TransactionConflict")}}}
		}
		s, _ := newSfdcSync(r)
		out, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SfdcAccountIDs: []string{sfAcct}, Items: []model.SfdcContactSyncItem{
			{SfdcContactID: sfC1, SfdcAccountID: sfAcct, Name: "Kim", Action: "create"}}})
		if err != nil || out.Results[0].Result != model.SfdcSyncConflict {
			t.Fatalf("got %+v %v", out, err)
		}
		r.writeErr = func(*model.AccountContact, string) error { return errors.New("boom") }
		if _, err = s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SfdcAccountIDs: []string{sfAcct}, Items: []model.SfdcContactSyncItem{
			{SfdcContactID: sfC1, SfdcAccountID: sfAcct, Name: "Kim", Action: "create"}}}); err == nil {
			t.Fatal("unexpected write errors must fail the request")
		}
	})

	t.Run("item_over_field_limit_is_invalid", func(t *testing.T) {
		r := newSfdcSyncFake()
		s, _ := newSfdcSync(r)
		long := string(make([]byte, 257))
		out, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SfdcAccountIDs: []string{sfAcct}, Items: []model.SfdcContactSyncItem{
			{SfdcContactID: sfC1, SfdcAccountID: sfAcct, Name: "Kim", Title: long, Action: "create"},
			{SfdcContactID: sfC2, SfdcAccountID: sfAcct, Name: "  ", Action: "create"},
			{SfdcContactID: sfC3, SfdcAccountID: sfAcct, Name: "Lee", Action: "create"}}})
		if err != nil || out.Results[0].Result != model.SfdcSyncInvalid || out.Results[1].Result != model.SfdcSyncInvalid || out.Results[2].Result != model.SfdcSyncCreated {
			t.Fatalf("got %+v %v", out, err)
		}
	})

	t.Run("incomplete_snapshot_marks_nobody", func(t *testing.T) {
		r := newSfdcSyncFake()
		r.put(model.AccountContact{Name: "Gone", SfdcContactID: sfC1, SfdcAccountID: sfAcct})
		r.put(model.AccountContact{Name: "Back", SfdcContactID: sfC2, SfdcAccountID: sfAcct, SfdcLeftAt: "2026-10-01T00:00:00Z"})
		s, _ := newSfdcSync(r)
		out, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SfdcAccountIDs: []string{sfAcct}, Items: []model.SfdcContactSyncItem{}})
		if err != nil || out.Marked != 0 || out.Unmarked != 0 || len(r.writes) != 0 {
			t.Fatalf("got %+v %v writes=%v", out, err, r.writes)
		}
		if _, err = s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SfdcAccountIDs: []string{sfAcct}, Present: []string{sfC2}}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("present without a complete snapshot: %v", err)
		}
	})

	t.Run("complete_snapshot_marks_missing_and_unmarks_returning", func(t *testing.T) {
		r := newSfdcSyncFake()
		gone := r.put(model.AccountContact{Name: "Gone", SfdcContactID: sfC1, SfdcAccountID: sfAcct})
		back := r.put(model.AccountContact{Name: "Back", SfdcContactID: sfC2, SfdcAccountID: sfAcct, SfdcLeftAt: "2026-10-01T00:00:00Z"})
		left := r.put(model.AccountContact{Name: "Left", SfdcContactID: sfC3, SfdcAccountID: sfAcct, SfdcLeftAt: "2026-10-01T00:00:00Z"})
		s, _ := newSfdcSync(r)
		out, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: []string{sfC2}})
		if err != nil || out.Marked != 1 || out.Unmarked != 1 || len(out.Results) != 0 {
			t.Fatalf("got %+v %v", out, err)
		}
		if r.rows[gone.ContactID].SfdcLeftAt != "2026-11-11T09:00:00Z" || r.rows[back.ContactID].SfdcLeftAt != "" || r.rows[left.ContactID].SfdcLeftAt != "2026-10-01T00:00:00Z" {
			t.Fatalf("marks %+v %+v %+v", r.rows[gone.ContactID], r.rows[back.ContactID], r.rows[left.ContactID])
		}
		if r.rows[gone.ContactID].SfdcContactID != sfC1 || r.rows[gone.ContactID].Name != "Gone" {
			t.Fatalf("marking must keep the link: %+v", r.rows[gone.ContactID])
		}
	})

	t.Run("departure_revision_conflict_skips_contact", func(t *testing.T) {
		r := newSfdcSyncFake()
		r.put(model.AccountContact{Name: "Gone", SfdcContactID: sfC1, SfdcAccountID: sfAcct})
		r.writeErr = func(*model.AccountContact, string) error { return repository.ErrConditionFailed }
		s, _ := newSfdcSync(r)
		out, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: []string{}})
		if err != nil || out.Marked != 0 || len(r.writes) != 1 {
			t.Fatalf("got %+v %v writes=%v", out, err, r.writes)
		}
	})

	t.Run("departure_ignores_links_of_other_sfdc_accounts", func(t *testing.T) {
		r := newSfdcSyncFake()
		other := r.put(model.AccountContact{Name: "Other", SfdcContactID: sfC1, SfdcAccountID: sfAcct2})
		s, _ := newSfdcSync(r)
		out, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: []string{}})
		if err != nil || out.Marked != 0 || r.rows[other.ContactID].SfdcLeftAt != "" || len(r.writes) != 0 {
			t.Fatalf("got %+v %v", out, err)
		}
	})

	t.Run("complete_snapshot_requires_present_and_items_in_it", func(t *testing.T) {
		r := newSfdcSyncFake()
		s, _ := newSfdcSync(r)
		for _, req := range []model.SfdcContactSyncRequest{
			{SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}},
			{SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: []string{sfC2},
				Items: []model.SfdcContactSyncItem{{SfdcContactID: sfC1, SfdcAccountID: sfAcct, Name: "Kim", Action: "create"}}},
		} {
			if _, err := s.Sync(ctx, "acc", "u1", req); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("%+v: %v", req, err)
			}
		}
	})

	t.Run("request_validation", func(t *testing.T) {
		r := newSfdcSyncFake()
		s, _ := newSfdcSync(r)
		ok := model.SfdcContactSyncItem{SfdcContactID: sfC1, SfdcAccountID: sfAcct, Name: "Kim", Action: "create"}
		with := func(f func(*model.SfdcContactSyncItem)) []model.SfdcContactSyncItem {
			i := ok
			f(&i)
			return []model.SfdcContactSyncItem{i}
		}
		tooMany := make([]model.SfdcContactSyncItem, 201)
		for i := range tooMany {
			tooMany[i] = ok
			tooMany[i].SfdcContactID = fmt.Sprintf("003%015d", i)
		}
		present := make([]string, 20001)
		for i := range present {
			present[i] = fmt.Sprintf("003%015d", i)
		}
		accounts := make([]string, 21)
		for i := range accounts {
			accounts[i] = fmt.Sprintf("001%015d", i)
		}
		for name, req := range map[string]model.SfdcContactSyncRequest{
			"no accounts":        {Items: []model.SfdcContactSyncItem{ok}},
			"21 accounts":        {SfdcAccountIDs: accounts},
			"bad account id":     {SfdcAccountIDs: []string{"001#"}},
			"201 items":          {SfdcAccountIDs: []string{sfAcct}, Items: tooMany},
			"bad contact id":     {SfdcAccountIDs: []string{sfAcct}, Items: with(func(i *model.SfdcContactSyncItem) { i.SfdcContactID = "003/../x" })},
			"foreign account":    {SfdcAccountIDs: []string{sfAcct}, Items: with(func(i *model.SfdcContactSyncItem) { i.SfdcAccountID = sfAcct2 })},
			"bad action":         {SfdcAccountIDs: []string{sfAcct}, Items: with(func(i *model.SfdcContactSyncItem) { i.Action = "delete" })},
			"link without rev":   {SfdcAccountIDs: []string{sfAcct}, Items: with(func(i *model.SfdcContactSyncItem) { i.Action, i.ContactID = "link", uuid.NewString() })},
			"create with id":     {SfdcAccountIDs: []string{sfAcct}, Items: with(func(i *model.SfdcContactSyncItem) { i.ContactID = uuid.NewString() })},
			"duplicate item":     {SfdcAccountIDs: []string{sfAcct}, Items: append(with(func(*model.SfdcContactSyncItem) {}), ok)},
			"bad present id":     {SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: []string{"x"}},
			"too many present":   {SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: present},
			"duplicate accounts": {SfdcAccountIDs: []string{sfAcct, sfAcct}},
		} {
			if _, err := s.Sync(ctx, "acc", "u1", req); !errors.Is(err, ErrInvalidInput) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if len(r.writes) != 0 {
			t.Fatalf("invalid requests wrote %v", r.writes)
		}
	})

	t.Run("non_member_forbidden", func(t *testing.T) {
		r := newSfdcSyncFake()
		r.member = false
		s, _ := newSfdcSync(r)
		if _, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SfdcAccountIDs: []string{sfAcct}, Items: []model.SfdcContactSyncItem{
			{SfdcContactID: sfC1, SfdcAccountID: sfAcct, Name: "Kim", Action: "create"}}}); !errors.Is(err, ErrForbidden) || len(r.writes) != 0 {
			t.Fatalf("%v %v", err, r.writes)
		}
	})

	t.Run("one_people_resolve_publish_per_batch", func(t *testing.T) {
		r := newSfdcSyncFake()
		r.put(model.AccountContact{Name: "Gone", SfdcContactID: sfC3, SfdcAccountID: sfAcct})
		s, published := newSfdcSync(r)
		out, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: []string{sfC1, sfC2},
			Items: []model.SfdcContactSyncItem{{SfdcContactID: sfC1, SfdcAccountID: sfAcct, Name: "A", Action: "create"}, {SfdcContactID: sfC2, SfdcAccountID: sfAcct, Name: "B", Action: "create"}}})
		if err != nil || out.Marked != 1 || len(*published) != 1 || (*published)[0] != (model.PeopleResolveRequested{AccountID: "acc", Reason: model.PeopleResolveReasonContacts}) {
			t.Fatalf("got %+v %v published=%+v", out, err, *published)
		}
		*published = nil
		if _, err = s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SfdcAccountIDs: []string{sfAcct}}); err != nil || len(*published) != 0 {
			t.Fatalf("no change must not publish: %v %+v", err, *published)
		}
	})
}

func TestGenericContactPathsProtectSfdcFields(t *testing.T) {
	ctx := context.Background()
	t.Run("generic_create_strips_sfdc_fields", func(t *testing.T) {
		r := newSfdcSyncFake()
		s := NewAccountContactsService(r)
		out, err := s.Mutate(ctx, "acc", "u1", "create", "", model.AccountContact{Name: "Kim", Source: "sfdc", SfdcContactID: sfC1, SfdcAccountID: sfAcct, SfdcLeftAt: "2026-01-01T00:00:00Z"})
		if err != nil {
			t.Fatal(err)
		}
		c := r.rows[out.ContactID]
		if c.Source != "" || c.SfdcContactID != "" || c.SfdcAccountID != "" || c.SfdcLeftAt != "" || len(r.index) != 0 {
			t.Fatalf("stored %+v index %v", c, r.index)
		}
	})
	t.Run("generic_update_preserves_sfdc_fields", func(t *testing.T) {
		r := newSfdcSyncFake()
		linked := r.put(model.AccountContact{Name: "Kim", Source: "sfdc", SfdcContactID: sfC1, SfdcAccountID: sfAcct, SfdcLeftAt: "2026-10-01T00:00:00Z"})
		plain := r.put(model.AccountContact{Name: "Lee"})
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
		if p := r.rows[plain.ContactID]; p.Source != "" || p.SfdcContactID != "" || p.SfdcAccountID != "" || p.SfdcLeftAt != "" || r.index[sfC2] != "" {
			t.Fatalf("stored %+v", p)
		}
	})
}
