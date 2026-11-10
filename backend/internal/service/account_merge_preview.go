package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// ErrAccountMergeIneligible means the requested merge is structurally
// impossible. The wrapped message names a static reason only.
var ErrAccountMergeIneligible = errors.New("account merge ineligible")

const (
	// mergePreviewCountCap bounds contact and event counting; a larger
	// category is reported as -1.
	mergePreviewCountCap = 10000
	// mergePreviewMeetingCap bounds the meeting refs revalidated one by one.
	mergePreviewMeetingCap = 2000
	// mergeSubtreeMaxAccounts bounds the breadth-first walk of A's subtree.
	mergeSubtreeMaxAccounts = 2000
	mergePreviewPageSize    = 100
)

// accountMergeRepo lists the repository reads merge eligibility and preview
// need; later tasks add the writers.
type accountMergeRepo interface {
	GetAccount(ctx context.Context, accountID string) (*model.Account, error)
	GetMember(ctx context.Context, accountID, userID string) (*model.AccountMember, error)
	ListAccountMembers(ctx context.Context, accountID string) ([]model.AccountMember, error)
	ListChildAccountIDsPage(ctx context.Context, parentID, cursor string, limit int32) ([]string, *string, error)
	ListMeetingRefsForAccountPage(ctx context.Context, accountID, cursor string, limit int32) ([]model.MeetingRef, *string, error)
	GetMeetingPublication(ctx context.Context, ownerID, meetingID string) (*model.Meeting, error)
	ListProjectRefsForAccount(ctx context.Context, accountID string) ([]model.ProjectRef, error)
	ListResearchRefsForAccount(ctx context.Context, accountID string) ([]model.ResearchRef, error)
	ListAccountContacts(ctx context.Context, account string, limit int, after string) ([]model.AccountContact, string, error)
	ListAccountEvents(ctx context.Context, account, fromKey, toKey string, descending bool, limit int, afterSK string) ([]model.AccountEvent, string, error)
	AccountPartitionHasPrefix(ctx context.Context, accountID, prefix string) (bool, error)
}

// AccountMergeService authorizes and previews merging one account into another.
type AccountMergeService struct {
	repo     accountMergeRepo
	accounts *AccountService
}

func NewAccountMergeService(repo accountMergeRepo, accounts *AccountService) *AccountMergeService {
	return &AccountMergeService{repo: repo, accounts: accounts}
}

type mergeSubjects struct {
	From, Into    *model.Account
	IntoAncestors []repository.AccountParentLink
	SubtreeHeight int
	// DirectChildren counts the source's verified child accounts.
	DirectChildren int
}

func mergeIneligible(reason string) error {
	return fmt.Errorf("%w: %s", ErrAccountMergeIneligible, reason)
}

// authorize requires the requester to own `from` (MEMBER role and META) and be
// a direct member of `into`, then rejects tombstones, concurrent merges, a
// target inside the source subtree and hierarchies that would exceed the
// ancestor limit once the source's children move under the target.
func (s *AccountMergeService) authorize(ctx context.Context, requesterID, fromID, intoID string) (*mergeSubjects, error) {
	if requesterID == "" || intoID == "" {
		return nil, ErrInvalidInput
	}
	if err := validateAccountHierarchyIDs(fromID, intoID); err != nil {
		return nil, err
	}
	if fromID == intoID {
		return nil, ErrInvalidInput
	}
	from, err := s.repo.GetAccount(ctx, fromID)
	if err != nil {
		return nil, err
	}
	if from == nil {
		return nil, ErrNotFound
	}
	owner, err := s.repo.GetMember(ctx, fromID, requesterID)
	if err != nil {
		return nil, err
	}
	if owner == nil || owner.Role != model.RoleOwner || from.OwnerUserID != requesterID {
		return nil, ErrForbidden
	}
	into, err := s.repo.GetAccount(ctx, intoID)
	if err != nil {
		return nil, err
	}
	if into == nil {
		return nil, ErrNotFound
	}
	member, err := s.repo.GetMember(ctx, intoID, requesterID)
	if err != nil {
		return nil, err
	}
	if member == nil || member.UserID != requesterID {
		return nil, ErrForbidden
	}
	if from.MergingInto != "" || from.MergedInto != "" || into.MergingInto != "" || into.MergedInto != "" {
		return nil, mergeIneligible("an account is already merging or merged")
	}
	if from.IncomingMerges > 0 {
		return nil, mergeIneligible("the source account is the target of another merge")
	}
	ancestors, err := s.intoAncestors(ctx, fromID, into)
	if err != nil {
		return nil, err
	}
	height, direct, err := s.subtreeHeight(ctx, fromID)
	if err != nil {
		return nil, err
	}
	if len(ancestors)+height > repository.AccountHierarchyMaxAncestors {
		return nil, mergeIneligible("the merged hierarchy would be too deep")
	}
	return &mergeSubjects{From: from, Into: into, IntoAncestors: ancestors, SubtreeHeight: height, DirectChildren: direct}, nil
}

// intoAncestors walks the target's parent chain with fresh reads. Reaching the
// source means the target is its descendant.
func (s *AccountMergeService) intoAncestors(ctx context.Context, fromID string, into *model.Account) ([]repository.AccountParentLink, error) {
	seen := map[string]bool{into.AccountID: true}
	var ancestors []repository.AccountParentLink
	for id := into.ParentAccountID; id != ""; {
		if id == fromID {
			return nil, mergeIneligible("the target is a descendant of the source")
		}
		if seen[id] || len(ancestors) >= repository.AccountHierarchyMaxAncestors {
			return nil, mergeIneligible("the target hierarchy is too deep or cyclic")
		}
		seen[id] = true
		account, err := s.repo.GetAccount(ctx, id)
		if err != nil {
			return nil, err
		}
		if account == nil {
			return nil, mergeIneligible("the target hierarchy is incomplete")
		}
		ancestors = append(ancestors, repository.AccountParentLink{AccountID: id, ParentAccountID: account.ParentAccountID})
		id = account.ParentAccountID
	}
	return ancestors, nil
}

// subtreeHeight walks the source's descendants breadth-first. Child references
// are discovery candidates, so each child's META parent is rechecked. It
// returns the longest downward edge count (a leaf is 0) and the verified
// direct-child count, and gives up past mergeSubtreeMaxAccounts accounts.
func (s *AccountMergeService) subtreeHeight(ctx context.Context, rootID string) (int, int, error) {
	visited := map[string]bool{rootID: true}
	level := []string{rootID}
	height, direct := 0, 0
	for len(level) > 0 {
		var next []string
		for _, parent := range level {
			cursor := ""
			for {
				ids, after, err := s.repo.ListChildAccountIDsPage(ctx, parent, cursor, mergePreviewPageSize)
				if err != nil {
					return 0, 0, err
				}
				for _, id := range ids {
					child, err := s.repo.GetAccount(ctx, id)
					if err != nil {
						return 0, 0, err
					}
					if child == nil || child.ParentAccountID != parent {
						continue
					}
					if visited[id] {
						return 0, 0, mergeIneligible("the source hierarchy is cyclic")
					}
					if len(visited) >= mergeSubtreeMaxAccounts {
						return 0, 0, mergeIneligible("the source hierarchy is too large")
					}
					visited[id] = true
					next = append(next, id)
					if parent == rootID {
						direct++
					}
				}
				if after == nil {
					break
				}
				cursor = *after
			}
		}
		if len(next) > 0 {
			height++
		}
		level = next
	}
	return height, direct, nil
}

// Preview inventories what a merge would move. It performs no writes.
func (s *AccountMergeService) Preview(ctx context.Context, requesterID, fromID, intoID string) (*model.AccountMergePreview, error) {
	subjects, err := s.authorize(ctx, requesterID, fromID, intoID)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	fromMembers, err := s.repo.ListAccountMembers(ctx, fromID)
	if err != nil {
		return nil, err
	}
	fromUsers := map[string]bool{}
	for _, m := range fromMembers {
		if m.UserID != "" {
			fromUsers[m.UserID] = true
		}
	}
	counts["members"] = len(fromUsers)
	intoMembers, err := s.repo.ListAccountMembers(ctx, intoID)
	if err != nil {
		return nil, err
	}
	newcomers := map[string]bool{}
	for _, m := range intoMembers {
		if m.UserID != "" && !fromUsers[m.UserID] {
			newcomers[m.UserID] = true
		}
	}
	counts["children"] = subjects.DirectChildren
	if counts["meetings"], err = s.countBoundMeetings(ctx, fromID); err != nil {
		return nil, err
	}
	projects, err := s.repo.ListProjectRefsForAccount(ctx, fromID)
	if err != nil {
		return nil, err
	}
	counts["projects"] = len(projects)
	research, err := s.repo.ListResearchRefsForAccount(ctx, fromID)
	if err != nil {
		return nil, err
	}
	counts["research"] = len(research)
	if counts["contacts"], err = countCapped(func(after string) (int, string, error) {
		page, next, err := s.repo.ListAccountContacts(ctx, fromID, mergePreviewPageSize, after)
		return len(page), next, err
	}); err != nil {
		return nil, err
	}
	if counts["events"], err = countCapped(func(after string) (int, string, error) {
		page, next, err := s.repo.ListAccountEvents(ctx, fromID, flowEventKeyLow, flowEventKeyHigh, false, mergePreviewPageSize, after)
		return len(page), next, err
	}); err != nil {
		return nil, err
	}
	unmoved, err := s.unmovedCategories(ctx, fromID)
	if err != nil {
		return nil, err
	}
	requires := false
	for _, u := range unmoved {
		requires = requires || u.Present
	}
	return &model.AccountMergePreview{
		FromAccountID: fromID, IntoAccountID: intoID, Counts: counts, Unmoved: unmoved,
		CLIOnly:               append([]string(nil), model.MergeCLIOnlyCategories...),
		NewAudience:           model.MergeAudience{IntoMembersNotInFrom: len(newcomers), IntoHasAncestors: len(subjects.IntoAncestors) > 0},
		RequiresAcceptUnmoved: requires,
	}, nil
}

// countCapped pages a listing and reports -1 once it exceeds the preview cap.
func countCapped(page func(after string) (int, string, error)) (int, error) {
	total, after := 0, ""
	for {
		n, next, err := page(after)
		if err != nil {
			return 0, err
		}
		if total += n; total > mergePreviewCountCap {
			return -1, nil
		}
		if next == "" {
			return total, nil
		}
		after = next
	}
}

// countBoundMeetings counts meeting refs whose meeting is still published to
// the account, revalidating each; stale refs are not counted. Beyond the ref
// cap it reports -1.
func (s *AccountMergeService) countBoundMeetings(ctx context.Context, accountID string) (int, error) {
	bound, scanned, cursor := 0, 0, ""
	for {
		refs, next, err := s.repo.ListMeetingRefsForAccountPage(ctx, accountID, cursor, mergePreviewPageSize)
		if err != nil {
			return 0, err
		}
		for _, ref := range refs {
			if ref.OwnerUserID == "" || ref.MeetingID == "" {
				continue
			}
			meeting, err := s.repo.GetMeetingPublication(ctx, ref.OwnerUserID, ref.MeetingID)
			if err != nil {
				return 0, err
			}
			if meetingPublishedToAccount(meeting, accountID, ref.OwnerUserID, ref.MeetingID) {
				bound++
			}
		}
		scanned += len(refs)
		if next == nil {
			return bound, nil
		}
		if scanned >= mergePreviewMeetingCap {
			return -1, nil
		}
		cursor = *next
	}
}

// unmovedCategories probes each category v1 leaves on the source. Billing
// data lives in a separate store the API cannot read, so it is always reported
// present.
func (s *AccountMergeService) unmovedCategories(ctx context.Context, accountID string) ([]model.MergeUnmovedCategory, error) {
	probes := map[string][]string{
		"newsSources":    {model.PrefixAccountNewsSource},
		"kb":             {model.PrefixAccountKBRef},
		"documents":      {model.PrefixDoc},
		"calendarShares": {model.PrefixCalendarShare},
		"dictionary":     {model.PrefixDictTerm, model.PrefixVocab},
	}
	out := make([]model.MergeUnmovedCategory, 0, len(model.MergeUnmovedCategories))
	for _, category := range model.MergeUnmovedCategories {
		present := category == "billing"
		for _, prefix := range probes[category] {
			found, err := s.repo.AccountPartitionHasPrefix(ctx, accountID, prefix)
			if err != nil {
				return nil, err
			}
			present = present || found
		}
		out = append(out, model.MergeUnmovedCategory{Category: category, Present: present})
	}
	return out, nil
}
