package service

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// SFDC contact sync bounds (ADR-021, SFDC contacts).
const (
	sfdcSyncMaxAccounts          = 20
	sfdcSyncMaxItems             = 200
	sfdcSyncMaxPresentPerAccount = 5000
	sfdcSyncMaxPresent           = 20000
	// sfdcDepartureMaxWrites bounds departure marks set or cleared per
	// request; marking is idempotent, so the rest follow on later runs.
	sfdcDepartureMaxWrites = 200
	// sfdcDepartureBudget stops the departure pass this long after the
	// request started, well inside the API Gateway timeout.
	sfdcDepartureBudget = 15 * time.Second
)

type sfdcSyncRepo interface {
	accountContactsRepo
	ListSfdcContactLinks(context.Context, string) ([]repository.SfdcContactLink, error)
}

// AccountContactSyncService applies one SFDC contact batch through the
// contact service, so every write keeps encryption, revision CAS, the access
// conditions and the SFDC index and suppression rows.
type AccountContactSyncService struct {
	repo     sfdcSyncRepo
	contacts *AccountContactsService
	now      func() time.Time
	// start picks the first link the departure pass visits; a pass stopped
	// by its budget therefore covers a different part of the links next run.
	start func(n int) int
}

func NewAccountContactSyncService(repo sfdcSyncRepo, contacts *AccountContactsService) *AccountContactSyncService {
	return &AccountContactSyncService{repo: repo, contacts: contacts, now: time.Now, start: rand.IntN}
}

// sfdcSyncItem is one request item after validation: its SFDC IDs are in
// canonical 18-character form unless invalid is set.
type sfdcSyncItem struct {
	model.SfdcContactSyncItem
	invalid bool
}

// validateSfdcSync checks the whole request before any read and rewrites
// every SFDC ID to its canonical 18-character form, so comparisons, keys and
// the departure scope treat the 15- and 18-character spellings of one record
// as one ID. A malformed request-level ID (sfdcAccountIds, present keys and
// values), caps and contradictions reject the request. An item whose own SFDC
// IDs do not canonicalize, or whose name or title is over the contact limits,
// only gets the invalid result.
func validateSfdcSync(req model.SfdcContactSyncRequest) (accounts map[string]bool, present map[string]map[string]bool, items []sfdcSyncItem, err error) {
	if len(req.SfdcAccountIDs) < 1 || len(req.SfdcAccountIDs) > sfdcSyncMaxAccounts || len(req.Items) > sfdcSyncMaxItems {
		return nil, nil, nil, ErrInvalidInput
	}
	if req.SnapshotComplete != (req.Present != nil) {
		return nil, nil, nil, ErrInvalidInput
	}
	accounts = map[string]bool{}
	for _, raw := range req.SfdcAccountIDs {
		id, ok := model.CanonicalSfdcID(raw)
		if !ok || accounts[id] {
			return nil, nil, nil, ErrInvalidInput
		}
		accounts[id] = true
	}
	// A complete snapshot lists every SFDC account, even one with no contacts.
	if req.SnapshotComplete && len(req.Present) != len(accounts) {
		return nil, nil, nil, ErrInvalidInput
	}
	present, total := map[string]map[string]bool{}, 0
	for raw, ids := range req.Present {
		account, ok := model.CanonicalSfdcID(raw)
		if !ok || !accounts[account] || present[account] != nil || ids == nil || len(ids) > sfdcSyncMaxPresentPerAccount {
			return nil, nil, nil, ErrInvalidInput
		}
		if total += len(ids); total > sfdcSyncMaxPresent {
			return nil, nil, nil, ErrInvalidInput
		}
		set := make(map[string]bool, len(ids))
		for _, rawID := range ids {
			id, ok := model.CanonicalSfdcID(rawID)
			if !ok {
				return nil, nil, nil, ErrInvalidInput
			}
			set[id] = true
		}
		present[account] = set
	}
	seen := map[string]bool{}
	items = make([]sfdcSyncItem, 0, len(req.Items))
	for _, it := range req.Items {
		switch it.Action {
		case model.SfdcSyncActionCreate:
			if it.ContactID != "" || it.Revision != "" {
				return nil, nil, nil, ErrInvalidInput
			}
		case model.SfdcSyncActionLink:
			if !validContactID(it.ContactID) || !validContactID(it.Revision) {
				return nil, nil, nil, ErrInvalidInput
			}
		default:
			return nil, nil, nil, ErrInvalidInput
		}
		contactID, okC := model.CanonicalSfdcID(it.SfdcContactID)
		accountID, okA := model.CanonicalSfdcID(it.SfdcAccountID)
		if !okC || !okA {
			items = append(items, sfdcSyncItem{SfdcContactSyncItem: it, invalid: true})
			continue
		}
		if seen[contactID] || !accounts[accountID] {
			return nil, nil, nil, ErrInvalidInput
		}
		seen[contactID] = true
		if req.SnapshotComplete && !present[accountID][contactID] {
			return nil, nil, nil, ErrInvalidInput
		}
		it.SfdcContactID, it.SfdcAccountID = contactID, accountID
		items = append(items, sfdcSyncItem{SfdcContactSyncItem: it})
	}
	return accounts, present, items, nil
}

// sfdcItemResult maps a per-item write error to its result. Errors that are
// not about this item (access, merge, encryption, unexpected) fail the request.
func sfdcItemResult(err error) (string, error) {
	switch {
	case errors.Is(err, repository.ErrSfdcAlreadyLinked):
		return model.SfdcSyncAlreadyLinked, nil
	case errors.Is(err, repository.ErrSfdcSuppressed):
		return model.SfdcSyncSuppressed, nil
	case errors.Is(err, ErrContactConflict), errors.Is(err, ErrNotFound), isTransactionConflict(err): // transient; next run retries
		return model.SfdcSyncConflict, nil
	case errors.Is(err, ErrInvalidInput), errors.Is(err, repository.ErrInvalidSfdcLink), errors.Is(err, repository.ErrContactUnreadable):
		return model.SfdcSyncInvalid, nil
	}
	return "", err
}

// Sync applies creates and links, then, for a complete snapshot only, marks
// links of the listed SFDC accounts that are absent from Present as departed
// and clears the mark of those that returned. One people resolve request is
// published at the end when anything changed.
func (s *AccountContactSyncService) Sync(ctx context.Context, account, user string, req model.SfdcContactSyncRequest) (*model.SfdcContactSyncResponse, error) {
	deadline := s.now().Add(sfdcDepartureBudget)
	accounts, present, items, err := validateSfdcSync(req)
	if err != nil {
		return nil, err
	}
	acct, err := s.contacts.authorizedAccount(ctx, account, user)
	if err != nil {
		return nil, err
	}
	out := &model.SfdcContactSyncResponse{Results: make([]model.SfdcContactSyncResult, 0, len(items))}
	changed := false
	for _, it := range items {
		res, err := s.applyItem(ctx, account, user, acct.Name, it)
		if err != nil {
			return nil, err
		}
		if res.Result == model.SfdcSyncCreated || res.Result == model.SfdcSyncFilled {
			changed = true
		}
		out.Results = append(out.Results, res)
	}
	if req.SnapshotComplete {
		if out.Marked, out.Unmarked, out.Skipped, err = s.markDepartures(ctx, account, user, deadline, accounts, present); err != nil {
			return nil, err
		}
		changed = changed || out.Marked > 0 || out.Unmarked > 0
	}
	if changed {
		publishPeopleResolve(ctx, s.contacts.peopleResolve, model.PeopleResolveRequested{AccountID: account, Reason: model.PeopleResolveReasonContacts})
	}
	return out, nil
}

// applyItem writes one validated item; its SFDC IDs are canonical, and the
// result echoes the canonical contact ID.
func (s *AccountContactSyncService) applyItem(ctx context.Context, account, user, company string, item sfdcSyncItem) (model.SfdcContactSyncResult, error) {
	it := item.SfdcContactSyncItem
	res := model.SfdcContactSyncResult{SfdcContactID: it.SfdcContactID}
	if item.invalid || validateContact(model.AccountContact{Name: it.Name, Title: it.Title}) != nil {
		res.Result = model.SfdcSyncInvalid
		return res, nil
	}
	if it.Action == model.SfdcSyncActionCreate {
		c := model.AccountContact{Name: it.Name, Title: it.Title, Company: company, Source: model.ContactSourceSfdc,
			SfdcContactID: it.SfdcContactID, SfdcAccountID: it.SfdcAccountID}
		created, err := s.contacts.MutateWith(ctx, account, user, "create", "", c, ContactMutateOptions{DeferPeopleResolve: true, sfdc: &sfdcWrite{}})
		if err != nil {
			res.Result, err = sfdcItemResult(err)
			return res, err
		}
		res.Result, res.ContactID = model.SfdcSyncCreated, created.ContactID
		return res, nil
	}
	res.ContactID = it.ContactID
	current, err := s.repo.GetAccountContact(ctx, account, it.ContactID)
	if errors.Is(err, repository.ErrContactUnreadable) {
		res.Result = model.SfdcSyncInvalid
		return res, nil
	}
	if err != nil {
		return res, err
	}
	if current == nil || current.Revision != it.Revision || current.SfdcContactID != "" && !sameSfdcID(current.SfdcContactID, it.SfdcContactID) {
		res.Result = model.SfdcSyncConflict
		return res, nil
	}
	next, changed := *current, false
	// Fill-empty-only: the name and any non-empty field are never changed.
	if next.Title == "" && it.Title != "" {
		next.Title, changed = it.Title, true
	}
	if next.Company == "" && company != "" {
		next.Company, changed = company, true
	}
	if next.SfdcContactID == "" {
		next.Source, next.SfdcContactID, changed = model.ContactSourceSfdc, it.SfdcContactID, true
	}
	if next.SfdcAccountID == "" {
		next.SfdcAccountID, changed = it.SfdcAccountID, true
	}
	if !changed {
		res.Result = model.SfdcSyncUnchanged
		return res, nil
	}
	base := *current
	if _, err = s.contacts.MutateWith(ctx, account, user, "update", it.ContactID, next, ContactMutateOptions{DeferPeopleResolve: true, sfdc: &sfdcWrite{base: &base}}); err != nil {
		res.Result, err = sfdcItemResult(err)
		return res, err
	}
	res.Result = model.SfdcSyncFilled
	return res, nil
}

// markDepartures only sets or clears SfdcLeftAt, through the same sealed
// update with the observed revision. It walks the in-scope links, opening
// only linked contacts, from a varying start, and stops at the write budget,
// the deadline or a cancelled context; contacts already correct cost one read
// and no write, so later runs continue the pass. A contact that changed
// concurrently is skipped, not retried; one that cannot be opened is counted
// in skipped.
func (s *AccountContactSyncService) markDepartures(ctx context.Context, account, user string, deadline time.Time, accounts map[string]bool, present map[string]map[string]bool) (marked, unmarked, skipped int, err error) {
	links, err := s.repo.ListSfdcContactLinks(ctx, account)
	if err != nil {
		return 0, 0, 0, err
	}
	scoped := links[:0:0]
	for _, l := range links {
		if accounts[l.SfdcAccountID] {
			scoped = append(scoped, l)
		}
	}
	if len(scoped) == 0 {
		return 0, 0, 0, nil
	}
	now := s.now().UTC().Format(time.RFC3339)
	first := s.start(len(scoped))
	for i := range scoped {
		if marked+unmarked >= sfdcDepartureMaxWrites || ctx.Err() != nil || !s.now().Before(deadline) {
			break
		}
		l := scoped[(first+i)%len(scoped)]
		c, err := s.repo.GetAccountContact(ctx, account, l.ContactID)
		if errors.Is(err, repository.ErrContactUnreadable) {
			skipped++
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			return 0, 0, 0, err
		}
		if c == nil || !sameSfdcID(c.SfdcContactID, l.SfdcContactID) || !sameSfdcID(c.SfdcAccountID, l.SfdcAccountID) {
			continue
		}
		isPresent := present[l.SfdcAccountID][l.SfdcContactID]
		target := c.SfdcLeftAt
		if !isPresent && c.SfdcLeftAt == "" {
			target = now
		} else if isPresent && c.SfdcLeftAt != "" {
			target = ""
		}
		if target == c.SfdcLeftAt {
			continue
		}
		base, next := *c, *c
		next.SfdcLeftAt = target
		_, werr := s.contacts.MutateWith(ctx, account, user, "update", c.ContactID, next, ContactMutateOptions{DeferPeopleResolve: true, sfdc: &sfdcWrite{base: &base}})
		if werr != nil {
			res, werr := sfdcItemResult(werr)
			if werr != nil {
				return 0, 0, 0, werr
			}
			if res == model.SfdcSyncInvalid {
				skipped++
			}
			continue
		}
		if target == "" {
			unmarked++
		} else {
			marked++
		}
	}
	return marked, unmarked, skipped, nil
}
