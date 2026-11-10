package service

import (
	"errors"
	"sort"
	"strings"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const (
	// mergeLinksPerRun is how many children, projects or research reports
	// one mover call moves.
	mergeLinksPerRun = 25
	// mergeMaxTerms caps the target's merged aliases and domains; the
	// target's own terms always stay, source terms fill the remainder.
	mergeMaxTerms = 32
)

// moveChildren reparents up to mergeLinksPerRun of the source's direct
// children under the target, rechecking the target's ancestor chain read at
// the start of the call. A child that no longer sits under the source, or
// that is itself merging or merged, is Skipped; any other refusal is returned
// so the retry re-reads the chain. The cursor is the child-reference page
// position.
func (s *AccountMergeService) moveChildren(fence *mergeFence, job *model.AccountMergeJob) (mergeMoveResult, error) {
	result := mergeMoveResult{Cursor: job.Cursor}
	ctx := fence.Context()
	if err := fence.Err(); err != nil {
		return result, err
	}
	ids, next, err := s.repo.ListChildAccountIDsPage(ctx, job.SourceAccountID, job.Cursor, mergeLinksPerRun)
	if err != nil {
		return result, fence.translate(err)
	}
	var out mergeMoveResult
	if len(ids) > 0 {
		into, err := s.repo.GetAccount(ctx, job.TargetAccountID)
		if err != nil {
			return result, fence.translate(err)
		}
		if into == nil {
			return result, repository.ErrConditionFailed
		}
		above, err := s.intoAncestors(ctx, job.SourceAccountID, into)
		if err != nil {
			return result, fence.translate(err)
		}
		chain := append([]repository.AccountParentLink{{AccountID: into.AccountID, ParentAccountID: into.ParentAccountID}}, above...)
		for _, id := range ids {
			if err := fence.Err(); err != nil {
				return result, err
			}
			err := s.worker.ReparentAccountForMerge(ctx, job, id, chain)
			if err == nil {
				out.Moved++
				continue
			}
			if !errors.Is(err, repository.ErrConditionFailed) {
				return result, fence.translate(err)
			}
			child, readErr := s.repo.GetAccount(ctx, id)
			if readErr != nil {
				return result, fence.translate(readErr)
			}
			if child == nil || child.ParentAccountID != job.SourceAccountID || child.MergingInto != "" || child.MergedInto != "" {
				out.Skipped++
				continue
			}
			return result, err
		}
	}
	if next == nil || *next == "" {
		out.Done = true
	} else {
		out.Cursor = *next
	}
	return out, nil
}

// moveLinkedIDs moves up to mergeLinksPerRun of ids (sorted) after the job
// cursor with move. A link that is already gone is a no-op in the writer, so
// a repeat run moves nothing new. The cursor is the last ID handled.
func moveLinkedIDs(fence *mergeFence, job *model.AccountMergeJob, ids []string, move func(id string) error) (mergeMoveResult, error) {
	result := mergeMoveResult{Cursor: job.Cursor}
	sort.Strings(ids)
	out := mergeMoveResult{Cursor: job.Cursor, Done: true}
	for _, id := range ids {
		if id == "" || id <= job.Cursor {
			continue
		}
		if out.Moved == mergeLinksPerRun {
			out.Done = false
			break
		}
		if err := fence.Err(); err != nil {
			return result, err
		}
		if err := move(id); err != nil {
			return result, fence.translate(err)
		}
		out.Moved++
		out.Cursor = id
	}
	if out.Done {
		out.Cursor = ""
	}
	return out, nil
}

// moveProjects moves the source's project links (canonical accountIds set
// and PROJECTREF# row) to the target.
func (s *AccountMergeService) moveProjects(fence *mergeFence, job *model.AccountMergeJob) (mergeMoveResult, error) {
	if err := fence.Err(); err != nil {
		return mergeMoveResult{Cursor: job.Cursor}, err
	}
	refs, err := s.worker.ListProjectRefsForAccount(fence.Context(), job.SourceAccountID)
	if err != nil {
		return mergeMoveResult{Cursor: job.Cursor}, fence.translate(err)
	}
	ids := make([]string, len(refs))
	for i, ref := range refs {
		ids[i] = ref.ProjectID
	}
	return moveLinkedIDs(fence, job, ids, func(id string) error {
		return s.worker.MoveProjectAccountLinkForMerge(fence.Context(), job, id)
	})
}

// moveResearch moves the source's research links (canonical accountIds set
// and RESEARCHREF# row) to the target.
func (s *AccountMergeService) moveResearch(fence *mergeFence, job *model.AccountMergeJob) (mergeMoveResult, error) {
	if err := fence.Err(); err != nil {
		return mergeMoveResult{Cursor: job.Cursor}, err
	}
	refs, err := s.worker.ListResearchRefsForAccount(fence.Context(), job.SourceAccountID)
	if err != nil {
		return mergeMoveResult{Cursor: job.Cursor}, fence.translate(err)
	}
	ids := make([]string, len(refs))
	for i, ref := range refs {
		ids[i] = ref.ResearchID
	}
	return moveLinkedIDs(fence, job, ids, func(id string) error {
		return s.worker.MoveResearchAccountLinkForMerge(fence.Context(), job, id)
	})
}

// mergedTerms returns the target's terms unchanged followed by the source's
// additions not already present (trimmed, case-insensitive, also skipping
// any seed such as the target's name) until mergeMaxTerms is reached. It
// returns the full list to store and how many terms were added.
func mergedTerms(target []string, seed []string, additions []string) ([]string, int) {
	seen := map[string]bool{}
	for _, term := range append(append([]string{}, seed...), target...) {
		seen[strings.ToLower(strings.TrimSpace(term))] = true
	}
	out := append([]string{}, target...)
	added := 0
	for _, term := range additions {
		term = strings.TrimSpace(term)
		key := strings.ToLower(term)
		if term == "" || seen[key] {
			continue
		}
		if len(out) >= mergeMaxTerms {
			break
		}
		seen[key] = true
		out = append(out, term)
		added++
	}
	return out, added
}

// moveMetadata adds the source's name and aliases to the target's aliases
// and the source's domains to its domains, capped at mergeMaxTerms with the
// target's own terms first. It writes the full lists against the target's
// observed updatedAt; a concurrent edit is retried with a fresh read.
func (s *AccountMergeService) moveMetadata(fence *mergeFence, job *model.AccountMergeJob) (mergeMoveResult, error) {
	result := mergeMoveResult{Cursor: job.Cursor}
	ctx := fence.Context()
	if err := fence.Err(); err != nil {
		return result, err
	}
	from, err := s.repo.GetAccount(ctx, job.SourceAccountID)
	if err != nil {
		return result, fence.translate(err)
	}
	into, err := s.repo.GetAccount(ctx, job.TargetAccountID)
	if err != nil {
		return result, fence.translate(err)
	}
	if from == nil || into == nil {
		return result, repository.ErrConditionFailed
	}
	aliases, addedAliases := mergedTerms(into.Aliases, []string{into.Name}, append([]string{from.Name}, from.Aliases...))
	domains, addedDomains := mergedTerms(into.Domains, nil, from.Domains)
	if addedAliases+addedDomains == 0 {
		return mergeMoveResult{Skipped: 1, Done: true}, nil
	}
	if err := s.worker.MergeAccountMetadata(ctx, job, into, aliases, domains); err != nil {
		return result, fence.translate(err)
	}
	return mergeMoveResult{Moved: addedAliases + addedDomains, Done: true}, nil
}
