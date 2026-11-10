package service

import (
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// ErrMergeContactIndexTooLarge means the target's contacts plus the source's
// remaining contacts exceed the in-memory dedupe index. The job fails with
// MergeErrorContactIndexTooLarge before the step writes anything.
var ErrMergeContactIndexTooLarge = errors.New("account merge contact index too large")

const (
	MergeErrorContactIndexTooLarge = "CONTACT_INDEX_TOO_LARGE"

	// mergeContactIndexCap bounds the dedupe index. It counts every target
	// row plus every remaining source row, the most the index can hold by
	// the end of the step, so a step that starts cannot outgrow it.
	mergeContactIndexCap = 10000
	mergeContactPageSize = 100
	// mergeMinPhoneDigits keeps short extensions from matching unrelated people.
	mergeMinPhoneDigits = 7
)

// MergeFailureCode returns the job error code for a mover error, or "" when
// the error is not a terminal merge failure.
func MergeFailureCode(err error) string {
	if errors.Is(err, ErrMergeContactIndexTooLarge) {
		return MergeErrorContactIndexTooLarge
	}
	return ""
}

// contactMatchKeys returns the dedupe keys of a contact: its normalized email
// and the digits of its mobile and work phones. Empty values and phone keys
// shorter than mergeMinPhoneDigits digits are skipped.
func contactMatchKeys(c model.AccountContact) []string {
	var keys []string
	if email := strings.ToLower(strings.TrimSpace(c.Email)); email != "" {
		keys = append(keys, "e:"+email)
	}
	for _, phone := range []string{c.Phone, c.WorkPhone} {
		var digits strings.Builder
		for _, r := range phone {
			if r >= '0' && r <= '9' {
				digits.WriteRune(r)
			}
		}
		if digits.Len() >= mergeMinPhoneDigits {
			keys = append(keys, "p:"+digits.String())
		}
	}
	return keys
}

// mergeContactFields folds from into into: every contact field keeps into's
// value when it is non-empty after trimming and takes from's otherwise.
// Identity and revision are into's.
func mergeContactFields(into, from model.AccountContact) model.AccountContact {
	pick := func(b, a string) string {
		if strings.TrimSpace(b) != "" {
			return b
		}
		return a
	}
	out := into
	out.Name = pick(into.Name, from.Name)
	out.Company = pick(into.Company, from.Company)
	out.Title = pick(into.Title, from.Title)
	out.Department = pick(into.Department, from.Department)
	out.Email = pick(into.Email, from.Email)
	out.Address = pick(into.Address, from.Address)
	out.WorkPhone = pick(into.WorkPhone, from.WorkPhone)
	out.Fax = pick(into.Fax, from.Fax)
	out.Phone = pick(into.Phone, from.Phone)
	out.CardRegisteredAt = pick(into.CardRegisteredAt, from.CardRegisteredAt)
	out.CardBook = pick(into.CardBook, from.CardBook)
	out.Group = pick(into.Group, from.Group)
	out.Notes = pick(into.Notes, from.Notes)
	return out
}

// contactIndexEntry is one target contact. stale means this run wrote it,
// so its revision is unknown and it is re-read before another merge.
type contactIndexEntry struct {
	contact model.AccountContact
	stale   bool
}

// contactIndex maps dedupe keys to target contacts; the first contact to
// claim a key keeps it, so matching is deterministic in target key order.
type contactIndex map[string]*contactIndexEntry

func (ix contactIndex) add(e *contactIndexEntry) {
	for _, k := range contactMatchKeys(e.contact) {
		if _, taken := ix[k]; !taken {
			ix[k] = e
		}
	}
}

// match returns the target for c, trying email before phones.
func (ix contactIndex) match(c model.AccountContact) *contactIndexEntry {
	for _, k := range contactMatchKeys(c) {
		if e := ix[k]; e != nil {
			return e
		}
	}
	return nil
}

// buildContactIndex reads every target contact once. It fails before any
// write when the target rows plus the source's remaining rows exceed
// mergeContactIndexCap. Unreadable target rows cannot be matched and are
// only counted.
func (s *AccountMergeService) buildContactIndex(fence *mergeFence, job *model.AccountMergeJob) (contactIndex, error) {
	ctx := fence.Context()
	sourceKeys, over, err := s.mover.ListAccountPartitionKeys(ctx, job.SourceAccountID, mergeContactPrefix, mergeContactIndexCap)
	if err != nil {
		return nil, fence.translate(err)
	}
	total := len(sourceKeys)
	if over || total > mergeContactIndexCap {
		return nil, ErrMergeContactIndexTooLarge
	}
	ix := contactIndex{}
	unreadable := 0
	for after := ""; ; {
		page, bad, next, err := s.mover.ListAccountContactsForMerge(ctx, job.TargetAccountID, mergeContactPageSize, after)
		if err != nil {
			return nil, fence.translate(err)
		}
		total += len(page) + len(bad)
		if total > mergeContactIndexCap {
			return nil, ErrMergeContactIndexTooLarge
		}
		unreadable += len(bad)
		for _, c := range page {
			ix.add(&contactIndexEntry{contact: c})
		}
		if next == "" {
			break
		}
		after = next
	}
	if unreadable > 0 {
		log.Printf("account merge contacts: target=%s unreadable=%d", job.TargetAccountID, unreadable)
	}
	return ix, nil
}

// moveContacts moves up to budget source contacts into the target, folding
// each into an existing target contact that shares a dedupe key and
// otherwise moving it under its own ID. Every relocation records a
// MERGE#contact# map row for the events step. A source row that cannot be
// opened is left on the source and counted as Unreadable; a row that already
// has a map row is skipped. The cursor is the source contact ID to continue
// after; a repeat run moves nothing because moved rows are gone.
func (s *AccountMergeService) moveContacts(fence *mergeFence, job *model.AccountMergeJob, budget int) (mergeMoveResult, error) {
	result := mergeMoveResult{Cursor: job.Cursor}
	ctx := fence.Context()
	if err := fence.Err(); err != nil {
		return result, err
	}
	if budget < 1 {
		return result, fmt.Errorf("%w: merge budget", ErrInvalidInput)
	}
	index, err := s.buildContactIndex(fence, job)
	if err != nil {
		return result, err
	}
	page, unreadable, next, err := s.mover.ListAccountContactsForMerge(ctx, job.SourceAccountID, min(budget, mergeContactPageSize), job.Cursor)
	if err != nil {
		return result, fence.translate(err)
	}
	ids := make([]string, len(page))
	for i, c := range page {
		ids[i] = c.ContactID
	}
	mapped, err := s.mover.GetMergeContactMap(ctx, job.SourceAccountID, ids)
	if err != nil {
		return result, fence.translate(err)
	}
	moved, merged, skipped := 0, 0, 0
	for _, c := range page {
		if err := fence.Err(); err != nil {
			return result, err
		}
		if _, done := mapped[c.ContactID]; done {
			skipped++
			continue
		}
		target := index.match(c)
		if target == nil {
			if err := s.mover.MoveContactForMerge(ctx, job, c); err != nil {
				return result, fence.translate(err)
			}
			index.add(&contactIndexEntry{contact: c, stale: true})
			moved++
			continue
		}
		if target.stale {
			fresh, err := s.mover.GetAccountContact(ctx, job.TargetAccountID, target.contact.ContactID)
			if err != nil {
				return result, fence.translate(err)
			}
			if fresh == nil {
				return result, fmt.Errorf("merge target contact vanished: %w", repository.ErrConditionFailed)
			}
			target.contact, target.stale = *fresh, false
		}
		into := mergeContactFields(target.contact, c)
		if err := s.mover.MergeContactIntoExisting(ctx, job, c, into); err != nil {
			return result, fence.translate(err)
		}
		target.contact, target.stale = into, true
		index.add(target)
		merged++
	}
	cursor := next
	if cursor == "" {
		cursor = job.Cursor
	}
	if len(unreadable) > 0 || skipped > 0 || merged > 0 {
		log.Printf("account merge contacts: source=%s moved=%d merged=%d skipped=%d unreadable=%d",
			job.SourceAccountID, moved, merged, skipped, len(unreadable))
	}
	return mergeMoveResult{Moved: moved + merged, Skipped: skipped, Unreadable: len(unreadable), Cursor: cursor, Done: next == ""}, nil
}
