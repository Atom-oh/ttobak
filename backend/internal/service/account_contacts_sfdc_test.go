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
	unreadable  map[string]bool
	gets        int
}

func (r *sfdcSyncFake) GetAccountContact(ctx context.Context, account, id string) (*model.AccountContact, error) {
	r.gets++
	if r.unreadable[id] {
		return nil, fmt.Errorf("%w: test", repository.ErrContactUnreadable)
	}
	return r.contactCRUDRepo.GetAccountContact(ctx, account, id)
}

// ListAccountContacts must not be used by the sync.
func (r *sfdcSyncFake) ListAccountContacts(context.Context, string, int, string) ([]model.AccountContact, string, error) {
	return nil, "", errors.New("sync must not list the whole account")
}

func newSfdcSyncFake() *sfdcSyncFake {
	return &sfdcSyncFake{
		contactCRUDRepo: contactCRUDRepo{contactTestRepo: contactTestRepo{account: true, member: true}, rows: map[string]model.AccountContact{}},
		accountName:     "Acme Korea", index: map[string]string{}, suppressed: map[string]bool{}, unreadable: map[string]bool{},
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

// sfAcct, sfC1 and sfC2 are shared with the generic-path guard tests.
const (
	sfAcct2 = "001000000000002AAA"
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
	s.start = func(int) int { return 0 }
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
		if _, err = s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SfdcAccountIDs: []string{sfAcct}, Present: map[string][]string{sfAcct: {sfC2}}}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("present without a complete snapshot: %v", err)
		}
	})

	t.Run("complete_snapshot_marks_missing_and_unmarks_returning", func(t *testing.T) {
		r := newSfdcSyncFake()
		gone := r.put(model.AccountContact{Name: "Gone", SfdcContactID: sfC1, SfdcAccountID: sfAcct})
		back := r.put(model.AccountContact{Name: "Back", SfdcContactID: sfC2, SfdcAccountID: sfAcct, SfdcLeftAt: "2026-10-01T00:00:00Z"})
		left := r.put(model.AccountContact{Name: "Left", SfdcContactID: sfC3, SfdcAccountID: sfAcct, SfdcLeftAt: "2026-10-01T00:00:00Z"})
		s, _ := newSfdcSync(r)
		out, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: map[string][]string{sfAcct: {sfC2}}})
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
		out, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: map[string][]string{sfAcct: {}}})
		if err != nil || out.Marked != 0 || len(r.writes) != 1 {
			t.Fatalf("got %+v %v writes=%v", out, err, r.writes)
		}
	})

	t.Run("departure_ignores_links_of_other_sfdc_accounts", func(t *testing.T) {
		r := newSfdcSyncFake()
		other := r.put(model.AccountContact{Name: "Other", SfdcContactID: sfC1, SfdcAccountID: sfAcct2})
		s, _ := newSfdcSync(r)
		out, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: map[string][]string{sfAcct: {}}})
		if err != nil || out.Marked != 0 || r.rows[other.ContactID].SfdcLeftAt != "" || len(r.writes) != 0 {
			t.Fatalf("got %+v %v", out, err)
		}
	})

	t.Run("departure_uses_union_of_present_lists", func(t *testing.T) {
		r := newSfdcSyncFake()
		moved := r.put(model.AccountContact{Name: "Moved", SfdcContactID: sfC1, SfdcAccountID: sfAcct})
		gone := r.put(model.AccountContact{Name: "Gone", SfdcContactID: sfC2, SfdcAccountID: sfAcct})
		back := r.put(model.AccountContact{Name: "Back", SfdcContactID: sfC3, SfdcAccountID: sfAcct, SfdcLeftAt: "2026-10-01T00:00:00Z"})
		s, _ := newSfdcSync(r)
		out, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct, sfAcct2},
			Present: map[string][]string{sfAcct: {}, sfAcct2: {sfC1, sfC3}}})
		if err != nil || out.Marked != 1 || out.Unmarked != 1 {
			t.Fatalf("got %+v %v", out, err)
		}
		if r.rows[moved.ContactID].SfdcLeftAt != "" || r.rows[gone.ContactID].SfdcLeftAt == "" || r.rows[back.ContactID].SfdcLeftAt != "" {
			t.Fatalf("marks %+v %+v %+v", r.rows[moved.ContactID], r.rows[gone.ContactID], r.rows[back.ContactID])
		}
		if r.rows[moved.ContactID].SfdcAccountID != sfAcct || r.rows[back.ContactID].SfdcAccountID != sfAcct {
			t.Fatalf("link account must not change: %+v %+v", r.rows[moved.ContactID], r.rows[back.ContactID])
		}
	})

	t.Run("complete_snapshot_requires_present_and_items_in_it", func(t *testing.T) {
		r := newSfdcSyncFake()
		s, _ := newSfdcSync(r)
		for _, req := range []model.SfdcContactSyncRequest{
			{SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct, sfAcct2}, Present: map[string][]string{sfAcct: {}, sfAcct2: {sfC1}},
				Items: []model.SfdcContactSyncItem{{SfdcContactID: sfC1, SfdcAccountID: sfAcct, Name: "Kim", Action: "create"}}},
			{SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}},
			{SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: map[string][]string{sfAcct: {sfC2}},
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
			tooMany[i].SfdcContactID = fmt.Sprintf("003%012dAAA", i)
		}
		ids := func(n, from int) []string {
			out := make([]string, n)
			for i := range out {
				out[i] = fmt.Sprintf("003%012dAAA", from+i)
			}
			return out
		}
		five := []string{"001000000000011AAA", "001000000000012AAA", "001000000000013AAA", "001000000000014AAA", "001000000000015AAA"}
		tooManyTotal := map[string][]string{}
		for i, a := range five {
			tooManyTotal[a] = ids(4001, i*5000)
		}
		accounts := make([]string, 21)
		for i := range accounts {
			accounts[i] = fmt.Sprintf("001%012dAAA", i)
		}
		for name, req := range map[string]model.SfdcContactSyncRequest{
			"no accounts":        {Items: []model.SfdcContactSyncItem{ok}},
			"21 accounts":        {SfdcAccountIDs: accounts},
			"bad account id":     {SfdcAccountIDs: []string{"001#"}},
			"201 items":          {SfdcAccountIDs: []string{sfAcct}, Items: tooMany},
			"foreign account":    {SfdcAccountIDs: []string{sfAcct}, Items: with(func(i *model.SfdcContactSyncItem) { i.SfdcAccountID = sfAcct2 })},
			"bad action":         {SfdcAccountIDs: []string{sfAcct}, Items: with(func(i *model.SfdcContactSyncItem) { i.Action = "delete" })},
			"link without rev":   {SfdcAccountIDs: []string{sfAcct}, Items: with(func(i *model.SfdcContactSyncItem) { i.Action, i.ContactID = "link", uuid.NewString() })},
			"create with id":     {SfdcAccountIDs: []string{sfAcct}, Items: with(func(i *model.SfdcContactSyncItem) { i.ContactID = uuid.NewString() })},
			"duplicate item":     {SfdcAccountIDs: []string{sfAcct}, Items: append(with(func(*model.SfdcContactSyncItem) {}), ok)},
			"bad present id":     {SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: map[string][]string{sfAcct: {"x"}}},
			"over 5000 per acct": {SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: map[string][]string{sfAcct: ids(5001, 0)}},
			"over 20000 total":   {SnapshotComplete: true, SfdcAccountIDs: five, Present: tooManyTotal},
			"missing account":    {SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct, sfAcct2}, Present: map[string][]string{sfAcct: {}}},
			"foreign present":    {SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: map[string][]string{sfAcct2: {}}},
			"null present list":  {SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: map[string][]string{sfAcct: nil}},
			"duplicate accounts": {SfdcAccountIDs: []string{sfAcct, sfAcct}},
			"15/18 dup accounts": {SfdcAccountIDs: []string{sfAcct, sfAcct[:15]}},
			"15/18 dup items":    {SfdcAccountIDs: []string{sfAcct}, Items: append(with(func(i *model.SfdcContactSyncItem) { i.SfdcContactID = sfC1[:15] }), ok)},
			"15/18 dup present":  {SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: map[string][]string{sfAcct: {}, sfAcct[:15]: {}}},
		} {
			if _, err := s.Sync(ctx, "acc", "u1", req); !errors.Is(err, ErrInvalidInput) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if len(r.writes) != 0 {
			t.Fatalf("invalid requests wrote %v", r.writes)
		}
	})

	t.Run("sync_canonicalizes_15_char_ids", func(t *testing.T) {
		const (
			acct15, acct18 = "001Xy0000000001", "001Xy0000000001IAA"
			new15, new18   = "003Ab0000012345", "003Ab0000012345IAA"
			link15, link18 = "003Cd0000012345", "003Cd0000012345IAA"
			gone18         = "003Ef0000012345IAA"
		)
		r := newSfdcSyncFake()
		plain := r.put(model.AccountContact{Name: "Lee"})
		gone := r.put(model.AccountContact{Name: "Gone", Source: model.ContactSourceSfdc, SfdcContactID: gone18, SfdcAccountID: acct18})
		s, _ := newSfdcSync(r)
		out, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SnapshotComplete: true, SfdcAccountIDs: []string{acct15},
			Present: map[string][]string{acct15: {new15, link15}},
			Items: []model.SfdcContactSyncItem{
				{SfdcContactID: new15, SfdcAccountID: acct15, Name: "Kim", Action: "create"},
				{SfdcContactID: link15, SfdcAccountID: acct18, Name: "Lee", Action: "link", ContactID: plain.ContactID, Revision: plain.Revision},
			}})
		if err != nil || len(out.Results) != 2 || out.Marked != 1 {
			t.Fatalf("got %+v %v", out, err)
		}
		created, filled := out.Results[0], out.Results[1]
		if created.Result != model.SfdcSyncCreated || created.SfdcContactID != new18 || filled.Result != model.SfdcSyncFilled || filled.SfdcContactID != link18 {
			t.Fatalf("results %+v", out.Results)
		}
		if c := r.rows[created.ContactID]; c.SfdcContactID != new18 || c.SfdcAccountID != acct18 {
			t.Fatalf("created %+v", c)
		}
		if c := r.rows[plain.ContactID]; c.SfdcContactID != link18 || c.SfdcAccountID != acct18 {
			t.Fatalf("linked %+v", c)
		}
		// The 15-character account scope covers the canonical stored link.
		if r.rows[gone.ContactID].SfdcLeftAt == "" {
			t.Fatalf("departure scope must use canonical IDs: %+v", r.rows[gone.ContactID])
		}
		// A later 18-character run sees the same records: nothing changes.
		out, err = s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SfdcAccountIDs: []string{acct18},
			Items: []model.SfdcContactSyncItem{{SfdcContactID: link18, SfdcAccountID: acct18, Name: "Lee", Action: "link", ContactID: plain.ContactID, Revision: r.rows[plain.ContactID].Revision}}})
		if err != nil || out.Results[0].Result != model.SfdcSyncUnchanged {
			t.Fatalf("rerun %+v %v", out, err)
		}
	})

	t.Run("sync_rejects_bad_checksum", func(t *testing.T) {
		const bad = "003Ab0000012345AAA" // the valid suffix is IAA
		r := newSfdcSyncFake()
		s, _ := newSfdcSync(r)
		for name, req := range map[string]model.SfdcContactSyncRequest{
			"account":     {SfdcAccountIDs: []string{"001Xy0000000001AAA"}},
			"present key": {SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: map[string][]string{"001000000000001AAB": {}}},
			"present id":  {SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: map[string][]string{sfAcct: {bad}}},
		} {
			if _, err := s.Sync(ctx, "acc", "u1", req); !errors.Is(err, ErrInvalidInput) {
				t.Errorf("%s: %v", name, err)
			}
		}
		out, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SfdcAccountIDs: []string{sfAcct}, Items: []model.SfdcContactSyncItem{
			{SfdcContactID: bad, SfdcAccountID: sfAcct, Name: "Kim", Action: "create"},
			{SfdcContactID: sfC1, SfdcAccountID: "001000000000001AAB", Name: "Lee", Action: "create"},
			{SfdcContactID: "003/../x", SfdcAccountID: sfAcct, Name: "Park", Action: "create"},
		}})
		if err != nil || len(out.Results) != 3 {
			t.Fatalf("got %+v %v", out, err)
		}
		for i, want := range []string{bad, sfC1, "003/../x"} {
			if res := out.Results[i]; res.Result != model.SfdcSyncInvalid || res.SfdcContactID != want || res.ContactID != "" {
				t.Fatalf("item %d: %+v", i, res)
			}
		}
		if len(r.writes) != 0 {
			t.Fatalf("bad IDs wrote %v", r.writes)
		}
	})

	t.Run("link_to_unreadable_contact_is_invalid", func(t *testing.T) {
		r := newSfdcSyncFake()
		c := r.put(model.AccountContact{Name: "Kim"})
		r.unreadable[c.ContactID] = true
		s, _ := newSfdcSync(r)
		out, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SfdcAccountIDs: []string{sfAcct}, Items: []model.SfdcContactSyncItem{
			{SfdcContactID: sfC1, SfdcAccountID: sfAcct, Name: "Kim", Action: "link", ContactID: c.ContactID, Revision: c.Revision},
			{SfdcContactID: sfC2, SfdcAccountID: sfAcct, Name: "Lee", Action: "create"}}})
		if err != nil || out.Results[0].Result != model.SfdcSyncInvalid || out.Results[1].Result != model.SfdcSyncCreated {
			t.Fatalf("got %+v %v", out, err)
		}
	})

	t.Run("departure_unreadable_contact_is_skipped_and_counted", func(t *testing.T) {
		r := newSfdcSyncFake()
		bad := r.put(model.AccountContact{Name: "Bad", SfdcContactID: sfC1, SfdcAccountID: sfAcct})
		gone := r.put(model.AccountContact{Name: "Gone", SfdcContactID: sfC2, SfdcAccountID: sfAcct})
		r.unreadable[bad.ContactID] = true
		s, _ := newSfdcSync(r)
		out, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: map[string][]string{sfAcct: {}}})
		if err != nil || out.Skipped != 1 || out.Marked != 1 || r.rows[gone.ContactID].SfdcLeftAt == "" {
			t.Fatalf("got %+v %v", out, err)
		}
	})

	manyLinks := func(r *sfdcSyncFake, n int) {
		for i := 0; i < n; i++ {
			r.put(model.AccountContact{Name: fmt.Sprint("C", i), SfdcContactID: fmt.Sprintf("003%012dAAA", i), SfdcAccountID: sfAcct})
		}
	}
	countLeft := func(r *sfdcSyncFake) int {
		n := 0
		for _, c := range r.rows {
			if c.SfdcLeftAt != "" {
				n++
			}
		}
		return n
	}

	t.Run("departure_write_cap_converges_across_runs", func(t *testing.T) {
		r := newSfdcSyncFake()
		manyLinks(r, 450)
		s, _ := newSfdcSync(r)
		s.start = func(n int) int { return n / 3 }
		req := model.SfdcContactSyncRequest{SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: map[string][]string{sfAcct: {}}}
		for run, want := range []int{200, 200, 50, 0} {
			out, err := s.Sync(ctx, "acc", "u1", req)
			if err != nil || out.Marked != want {
				t.Fatalf("run %d: %+v %v", run, out, err)
			}
		}
		if countLeft(r) != 450 {
			t.Fatalf("marked %d of 450", countLeft(r))
		}
	})

	t.Run("departure_deadline_stops_early_without_error", func(t *testing.T) {
		r := newSfdcSyncFake()
		manyLinks(r, 100)
		s, _ := newSfdcSync(r)
		t0, calls := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC), 0
		s.now = func() time.Time { calls++; return t0.Add(time.Duration(calls) * time.Second) }
		req := model.SfdcContactSyncRequest{SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: map[string][]string{sfAcct: {}}}
		out, err := s.Sync(ctx, "acc", "u1", req)
		if err != nil || out.Marked == 0 || out.Marked >= 100 {
			t.Fatalf("deadline run: %+v %v", out, err)
		}
		first := out.Marked
		s.now = func() time.Time { return t0 }
		if out, err = s.Sync(ctx, "acc", "u1", req); err != nil || out.Marked != 100-first || countLeft(r) != 100 {
			t.Fatalf("next run: %+v %v", out, err)
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if out, err = s.Sync(cancelled, "acc", "u1", model.SfdcContactSyncRequest{SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: map[string][]string{sfAcct: {"003000000000000AAA"}}}); err != nil || out.Unmarked != 0 {
			t.Fatalf("cancelled: %+v %v", out, err)
		}
	})

	t.Run("departure_reads_only_linked_contacts", func(t *testing.T) {
		r := newSfdcSyncFake()
		for i := 0; i < 50; i++ {
			r.put(model.AccountContact{Name: fmt.Sprint("Plain", i)})
		}
		r.put(model.AccountContact{Name: "Gone", SfdcContactID: sfC1, SfdcAccountID: sfAcct})
		r.put(model.AccountContact{Name: "Other", SfdcContactID: sfC2, SfdcAccountID: sfAcct2})
		s, _ := newSfdcSync(r)
		out, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: map[string][]string{sfAcct: {}}})
		if err != nil || out.Marked != 1 || r.gets != 1 {
			t.Fatalf("got %+v %v gets=%d", out, err, r.gets)
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
		out, err := s.Sync(ctx, "acc", "u1", model.SfdcContactSyncRequest{SnapshotComplete: true, SfdcAccountIDs: []string{sfAcct}, Present: map[string][]string{sfAcct: {sfC1, sfC2}},
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
