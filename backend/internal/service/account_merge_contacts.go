package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"unicode/utf8"

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
	// mergeContactsPerRun is how many source contacts one moveContacts call
	// processes. Building the target index decrypts every target row, so the
	// contacts step ignores the worker's per-call budget and amortizes one
	// index over this many source contacts.
	mergeContactsPerRun = 500
	// mergeSuppressionsPerRun is how many source SFDC suppression rows one
	// moveContacts call moves before it touches contacts.
	mergeSuppressionsPerRun = 200
)

// MergeFailureCode returns the job error code for a mover error, or "" when
// the error is not a terminal merge failure.
func MergeFailureCode(err error) string {
	if errors.Is(err, ErrMergeContactIndexTooLarge) {
		return MergeErrorContactIndexTooLarge
	}
	return ""
}

// contactMatchKey returns the dedupe key of a contact: its trimmed,
// lower-cased email, or "" when it has none. Phones never match: people who
// share a company line are different contacts.
func contactMatchKey(c model.AccountContact) string {
	if email := strings.ToLower(strings.TrimSpace(c.Email)); email != "" {
		return "e:" + email
	}
	return ""
}

// contactSfdcKey returns the SFDC dedupe key of a linked contact, or "".
// Two contacts linked to one SFDC contact are the same person whatever
// their emails, so this key is matched before the email key. The key uses
// the canonical 18-character ID; a malformed ID gives no key.
func contactSfdcKey(c model.AccountContact) string {
	if id, ok := model.CanonicalSfdcID(c.SfdcContactID); ok {
		return "s:" + id
	}
	return ""
}

// contactMergeFields lists every contact field with its note label.
func contactMergeFields(c *model.AccountContact) []struct {
	label string
	value *string
} {
	return []struct {
		label string
		value *string
	}{
		{"name", &c.Name}, {"company", &c.Company}, {"title", &c.Title}, {"department", &c.Department},
		{"email", &c.Email}, {"address", &c.Address}, {"workPhone", &c.WorkPhone}, {"fax", &c.Fax},
		{"phone", &c.Phone}, {"cardRegisteredAt", &c.CardRegisteredAt}, {"cardBook", &c.CardBook},
		{"group", &c.Group}, {"notes", &c.Notes},
	}
}

// mergeContactFields folds from into into: every contact field keeps into's
// value when it is non-empty after trimming and takes from's otherwise. A
// non-empty from value that differs from into's is kept by appending one
// "[merged from <source>] <field>: <value>" line per field to into's notes.
// Identity and revision are into's. ok is false when the merged contact
// would exceed the contact size limits; the caller then keeps from separate.
func mergeContactFields(into, from model.AccountContact, source string) (model.AccountContact, bool) {
	out := into
	var lines []string
	fromFields := contactMergeFields(&from)
	for i, f := range contactMergeFields(&out) {
		a, b := strings.TrimSpace(*fromFields[i].value), strings.TrimSpace(*f.value)
		switch {
		case b == "":
			*f.value = *fromFields[i].value
		case a == "" || a == b || f.label == "email" && strings.EqualFold(a, b):
		default:
			lines = append(lines, "[merged from "+source+"] "+f.label+": "+a)
		}
	}
	// The SFDC link moves only onto an unlinked contact; a linked target
	// keeps its own (the repository suppresses the duplicate's ID).
	if out.SfdcContactID == "" && from.SfdcContactID != "" {
		out.Source, out.SfdcContactID, out.SfdcAccountID, out.SfdcLeftAt = from.Source, from.SfdcContactID, from.SfdcAccountID, from.SfdcLeftAt
	}
	if len(lines) > 0 {
		notes := strings.Join(lines, "\n")
		if strings.TrimSpace(out.Notes) != "" {
			notes = out.Notes + "\n" + notes
		}
		out.Notes = notes
	}
	return out, contactWithinLimits(out)
}

func contactWithinLimits(c model.AccountContact) bool {
	if len(c.Notes) > contactNotesMaxBytes || !utf8.ValidString(c.Notes) {
		return false
	}
	c.PK, c.SK = "", ""
	encoded, err := json.Marshal(c)
	return err == nil && len(encoded) <= contactEncodedMaxBytes
}

// contactIndexEntry is one target contact. stale means this run wrote it,
// so its revision is unknown and it is re-read before another merge.
type contactIndexEntry struct {
	contact model.AccountContact
	stale   bool
}

// contactIndex maps email keys to target contacts; the first contact to
// claim a key keeps it, so matching is deterministic in target key order.
type contactIndex map[string]*contactIndexEntry

func (ix contactIndex) add(e *contactIndexEntry) {
	for _, k := range []string{contactSfdcKey(e.contact), contactMatchKey(e.contact)} {
		if k != "" {
			if _, taken := ix[k]; !taken {
				ix[k] = e
			}
		}
	}
}

func (ix contactIndex) match(c model.AccountContact) *contactIndexEntry {
	for _, k := range []string{contactSfdcKey(c), contactMatchKey(c)} {
		if e := ix[k]; k != "" && e != nil {
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

// moveContacts moves up to mergeContactsPerRun source contacts into the
// target. The target's SFDC-link and email index is built once per call. A
// source contact linked to the same SFDC contact as a target contact, or
// else whose email matches one, is folded into it (see mergeContactFields); one whose merged form would exceed the size limits is
// kept as a separate target contact (KeptSeparate); any other moves under its
// own ID. Every relocation records a MERGE#contact# map row for the events
// step. A source row that cannot be opened is left on the source and counted
// as Unreadable; a row that already has a map row is skipped. The cursor is
// the source contact ID to continue after; a repeat run moves nothing
// because moved rows are gone. SFDC index rows move with their contacts and
// suppression rows move before any contact (repository MoveContactForMerge,
// MergeContactIntoExisting and MoveSfdcSuppressionsForMerge); the target's
// existing link or suppression wins over the source's.
func (s *AccountMergeService) moveContacts(fence *mergeFence, job *model.AccountMergeJob) (mergeMoveResult, error) {
	result := mergeMoveResult{Cursor: job.Cursor}
	ctx := fence.Context()
	if err := fence.Err(); err != nil {
		return result, err
	}
	// SFDC suppression rows move first, in bounded batches that keep the
	// cursor, so the target index is built only once they are all moved.
	if _, done, err := s.mover.MoveSfdcSuppressionsForMerge(ctx, job, mergeSuppressionsPerRun); err != nil {
		return result, fence.translate(err)
	} else if !done {
		return result, nil
	}
	index, err := s.buildContactIndex(fence, job)
	if err != nil {
		return result, err
	}
	source := job.SourceAccountID
	if acct, err := s.mover.GetAccount(ctx, job.SourceAccountID); err != nil {
		return result, fence.translate(err)
	} else if acct != nil && strings.TrimSpace(acct.Name) != "" {
		source = strings.TrimSpace(acct.Name)
	}
	var out mergeMoveResult
	merged := 0
	cursor, processed := job.Cursor, 0
	for {
		page, unreadable, next, err := s.mover.ListAccountContactsForMerge(ctx, job.SourceAccountID, min(mergeContactPageSize, mergeContactsPerRun-processed), cursor)
		if err != nil {
			return result, fence.translate(err)
		}
		ids := make([]string, len(page))
		for i, c := range page {
			ids[i] = c.ContactID
		}
		mapped, err := s.mover.GetMergeContactMap(ctx, job.SourceAccountID, job.TargetAccountID, ids)
		if err != nil {
			return result, fence.translate(err)
		}
		for _, c := range page {
			if err := fence.Err(); err != nil {
				return result, err
			}
			if _, done := mapped[c.ContactID]; done {
				out.Skipped++
				continue
			}
			target := index.match(c)
			var into model.AccountContact
			fits := false
			if target != nil {
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
				into, fits = mergeContactFields(target.contact, c, source)
			}
			if !fits {
				if err := s.mover.MoveContactForMerge(ctx, job, c); err != nil {
					return result, fence.translate(err)
				}
				index.add(&contactIndexEntry{contact: c, stale: true})
				if target != nil {
					out.KeptSeparate++
				}
				out.Moved++
				continue
			}
			if err := s.mover.MergeContactIntoExisting(ctx, job, c, into); err != nil {
				return result, fence.translate(err)
			}
			target.contact, target.stale = into, true
			// Register keys the fold filled (email, SFDC link); taken keys stay.
			index.add(target)
			merged++
			out.Moved++
		}
		out.Unreadable += len(unreadable)
		processed += len(page) + len(unreadable)
		if next == "" {
			out.Done = true
			break
		}
		cursor = next
		if processed >= mergeContactsPerRun {
			break
		}
	}
	out.Cursor = cursor
	if out.Unreadable > 0 || out.Skipped > 0 || merged > 0 || out.KeptSeparate > 0 {
		log.Printf("account merge contacts: source=%s moved=%d merged=%d keptSeparate=%d skipped=%d unreadable=%d",
			job.SourceAccountID, out.Moved, merged, out.KeptSeparate, out.Skipped, out.Unreadable)
	}
	return out, nil
}
