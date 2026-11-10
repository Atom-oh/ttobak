package service

import (
	"context"
	"log"
	"strings"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const (
	mergeEventPageSize = 100
	// mergeEventsPerRun is how many source events one moveEvents call
	// processes; each costs one decrypt and one encrypt.
	mergeEventsPerRun = 500
	// mergeContactCheckBatch is the most IDs ExistingAccountContactIDs takes.
	mergeContactCheckBatch = 100
)

// resolveEventContacts maps every attendee contact ID on rows to the target
// contact it now refers to: the MERGE#contact# mapping when one exists, the
// same ID otherwise, kept only when that contact exists in the target. IDs
// absent from the result are cleared by the mover. Nothing is read when no
// attendee references a contact.
func (s *AccountMergeService) resolveEventContacts(ctx context.Context, job *model.AccountMergeJob, rows []repository.MergeEventRow) (map[string]string, error) {
	seen := map[string]bool{}
	var ids []string
	for _, row := range rows {
		for _, a := range row.Event.Attendees {
			if id := a.ContactID; id != "" && !strings.Contains(id, "#") && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	resolved := map[string]string{}
	if len(ids) == 0 {
		return resolved, nil
	}
	mapped, err := s.mover.GetMergeContactMap(ctx, job.SourceAccountID, job.TargetAccountID, ids)
	if err != nil {
		return nil, err
	}
	candidate := make(map[string]string, len(ids))
	var targets []string
	for _, id := range ids {
		c := id
		if t, ok := mapped[id]; ok {
			c = t
		}
		candidate[id] = c
		targets = append(targets, c)
	}
	exists := map[string]bool{}
	for start := 0; start < len(targets); start += mergeContactCheckBatch {
		found, err := s.mover.ExistingAccountContactIDs(ctx, job.TargetAccountID, targets[start:min(start+mergeContactCheckBatch, len(targets))])
		if err != nil {
			return nil, err
		}
		for id := range found {
			exists[id] = true
		}
	}
	for id, c := range candidate {
		if exists[c] {
			resolved[id] = c
		}
	}
	return resolved, nil
}

// moveEvents moves up to mergeEventsPerRun source events into the target
// under their own sort keys. Attendee contact IDs follow the contacts step's
// MERGE#contact# rows, so this step runs only after contacts is done
// (model.MergeSteps). An attendee whose contact is neither mapped nor present
// in the target keeps its name and email without a contact ID. A source row
// that cannot be opened is left on the source and counted as Unreadable. The
// cursor is the source sort key to continue after; a repeat run moves
// nothing because moved rows are gone. A source without events costs one
// page read and no target reads.
func (s *AccountMergeService) moveEvents(fence *mergeFence, job *model.AccountMergeJob) (mergeMoveResult, error) {
	result := mergeMoveResult{Cursor: job.Cursor}
	ctx := fence.Context()
	if err := fence.Err(); err != nil {
		return result, err
	}
	var out mergeMoveResult
	cursor, processed := job.Cursor, 0
	for {
		rows, unreadable, next, err := s.mover.ListAccountEventsForMerge(ctx, job.SourceAccountID, min(mergeEventPageSize, mergeEventsPerRun-processed), cursor)
		if err != nil {
			return result, fence.translate(err)
		}
		contactMap, err := s.resolveEventContacts(ctx, job, rows)
		if err != nil {
			return result, fence.translate(err)
		}
		for _, row := range rows {
			if err := fence.Err(); err != nil {
				return result, err
			}
			if err := s.mover.MoveEventForMerge(ctx, job, row.SK, row.Event, contactMap); err != nil {
				return result, fence.translate(err)
			}
			out.Moved++
		}
		out.Unreadable += len(unreadable)
		processed += len(rows) + len(unreadable)
		if next == "" {
			out.Done = true
			break
		}
		cursor = next
		if processed >= mergeEventsPerRun {
			break
		}
	}
	out.Cursor = cursor
	if out.Unreadable > 0 {
		log.Printf("account merge events: source=%s moved=%d unreadable=%d", job.SourceAccountID, out.Moved, out.Unreadable)
	}
	return out, nil
}
