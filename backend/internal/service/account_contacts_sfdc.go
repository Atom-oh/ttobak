package service

import (
	"context"
	"errors"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// SFDC contact sync bounds (ADR-021, SFDC contacts).
const (
	sfdcSyncMaxAccounts = 20
	sfdcSyncMaxItems    = 200
	sfdcSyncMaxPresent  = 20000
	// sfdcDepartureMaxWrites bounds departure marks set or cleared per
	// request; marking is idempotent, so the rest follow on later runs.
	sfdcDepartureMaxWrites = 200
	// sfdcDepartureMaxContacts bounds the contacts read to find departures.
	sfdcDepartureMaxContacts = 10000
	sfdcDeparturePageSize    = 100
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
}

func NewAccountContactSyncService(repo sfdcSyncRepo, contacts *AccountContactsService) *AccountContactSyncService {
	return &AccountContactSyncService{repo: repo, contacts: contacts, now: time.Now}
}

// validateSfdcSync checks the whole request before any read. Malformed IDs,
// caps and contradictions reject the request; a name or title over the
// contact limits only marks its item invalid.
func validateSfdcSync(req model.SfdcContactSyncRequest) (accounts, present map[string]bool, err error) {
	if len(req.SfdcAccountIDs) < 1 || len(req.SfdcAccountIDs) > sfdcSyncMaxAccounts || len(req.Items) > sfdcSyncMaxItems || len(req.Present) > sfdcSyncMaxPresent {
		return nil, nil, ErrInvalidInput
	}
	if req.SnapshotComplete != (req.Present != nil) {
		return nil, nil, ErrInvalidInput
	}
	accounts = map[string]bool{}
	for _, id := range req.SfdcAccountIDs {
		if !model.ValidSfdcID(id) || accounts[id] {
			return nil, nil, ErrInvalidInput
		}
		accounts[id] = true
	}
	present = map[string]bool{}
	for _, id := range req.Present {
		if !model.ValidSfdcID(id) {
			return nil, nil, ErrInvalidInput
		}
		present[id] = true
	}
	seen := map[string]bool{}
	for _, it := range req.Items {
		if !model.ValidSfdcID(it.SfdcContactID) || seen[it.SfdcContactID] || !accounts[it.SfdcAccountID] {
			return nil, nil, ErrInvalidInput
		}
		seen[it.SfdcContactID] = true
		if req.SnapshotComplete && !present[it.SfdcContactID] {
			return nil, nil, ErrInvalidInput
		}
		switch it.Action {
		case model.SfdcSyncActionCreate:
			if it.ContactID != "" || it.Revision != "" {
				return nil, nil, ErrInvalidInput
			}
		case model.SfdcSyncActionLink:
			if !validContactID(it.ContactID) || !validContactID(it.Revision) {
				return nil, nil, ErrInvalidInput
			}
		default:
			return nil, nil, ErrInvalidInput
		}
	}
	return accounts, present, nil
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
	case errors.Is(err, ErrInvalidInput), errors.Is(err, repository.ErrInvalidSfdcLink):
		return model.SfdcSyncInvalid, nil
	}
	return "", err
}

// Sync applies creates and links, then, for a complete snapshot only, marks
// links of the listed SFDC accounts that are absent from Present as departed
// and clears the mark of those that returned. One people resolve request is
// published at the end when anything changed.
func (s *AccountContactSyncService) Sync(ctx context.Context, account, user string, req model.SfdcContactSyncRequest) (*model.SfdcContactSyncResponse, error) {
	accounts, present, err := validateSfdcSync(req)
	if err != nil {
		return nil, err
	}
	if err = s.contacts.authorize(ctx, account, user); err != nil {
		return nil, err
	}
	acct, err := s.repo.GetAccount(ctx, account)
	if err != nil {
		return nil, err
	}
	if acct == nil {
		return nil, ErrForbidden
	}
	out := &model.SfdcContactSyncResponse{Results: make([]model.SfdcContactSyncResult, 0, len(req.Items))}
	changed := false
	for _, it := range req.Items {
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
		if out.Marked, out.Unmarked, err = s.markDepartures(ctx, account, user, accounts, present); err != nil {
			return nil, err
		}
		changed = changed || out.Marked > 0 || out.Unmarked > 0
	}
	if changed {
		publishPeopleResolve(ctx, s.contacts.peopleResolve, model.PeopleResolveRequested{AccountID: account, Reason: model.PeopleResolveReasonContacts})
	}
	return out, nil
}

func (s *AccountContactSyncService) applyItem(ctx context.Context, account, user, company string, it model.SfdcContactSyncItem) (model.SfdcContactSyncResult, error) {
	res := model.SfdcContactSyncResult{SfdcContactID: it.SfdcContactID}
	if validateContact(model.AccountContact{Name: it.Name, Title: it.Title}) != nil {
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
	if err != nil {
		return res, err
	}
	if current == nil || current.Revision != it.Revision || current.SfdcContactID != "" && current.SfdcContactID != it.SfdcContactID {
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
// update with the observed revision. A contact that changed concurrently is
// skipped, not retried.
func (s *AccountContactSyncService) markDepartures(ctx context.Context, account, user string, accounts, present map[string]bool) (marked, unmarked int, err error) {
	links, err := s.repo.ListSfdcContactLinks(ctx, account)
	if err != nil {
		return 0, 0, err
	}
	scope := map[string]string{} // contactId -> sfdcContactId
	for _, l := range links {
		if accounts[l.SfdcAccountID] {
			scope[l.ContactID] = l.SfdcContactID
		}
	}
	if len(scope) == 0 {
		return 0, 0, nil
	}
	now := s.now().UTC().Format(time.RFC3339)
	after, read := "", 0
	for read < sfdcDepartureMaxContacts && marked+unmarked < sfdcDepartureMaxWrites {
		page, next, err := s.repo.ListAccountContacts(ctx, account, sfdcDeparturePageSize, after)
		if err != nil {
			return 0, 0, err
		}
		read += len(page)
		for _, c := range page {
			sfdcID, ok := scope[c.ContactID]
			if !ok || c.SfdcContactID != sfdcID || !accounts[c.SfdcAccountID] {
				continue
			}
			target := c.SfdcLeftAt
			if !present[sfdcID] && c.SfdcLeftAt == "" {
				target = now
			} else if present[sfdcID] && c.SfdcLeftAt != "" {
				target = ""
			}
			if target == c.SfdcLeftAt {
				continue
			}
			if marked+unmarked >= sfdcDepartureMaxWrites {
				break
			}
			base, nextContact := c, c
			nextContact.SfdcLeftAt = target
			_, werr := s.contacts.MutateWith(ctx, account, user, "update", c.ContactID, nextContact, ContactMutateOptions{DeferPeopleResolve: true, sfdc: &sfdcWrite{base: &base}})
			if werr != nil {
				if _, werr = sfdcItemResult(werr); werr != nil {
					return 0, 0, werr
				}
				continue
			}
			if target == "" {
				unmarked++
			} else {
				marked++
			}
		}
		if next == "" {
			break
		}
		after = next
	}
	return marked, unmarked, nil
}
