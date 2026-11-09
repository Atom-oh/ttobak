package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const (
	peopleRunLease            = 2 * time.Minute
	maxPeopleContactsPerRun   = 2000
	maxPeopleDescendants      = 200
	peopleBackfillWindow      = 90 * 24 * time.Hour
	maxPeopleBackfillMeetings = 200
	maxPeopleRunPasses        = 2

	maxPeopleExistingEdges = 300
	peopleContactPageSize  = 100
	peopleRefPageSize      = 50
	peopleReleaseTimeout   = 5 * time.Second
)

type accountPeopleRepo interface {
	GetAccount(context.Context, string) (*model.Account, error)
	GetMeeting(context.Context, string, string) (*model.Meeting, error)
	GetUserByID(context.Context, string) (*model.User, error)
	ListAccountMembers(context.Context, string) ([]model.AccountMember, error)
	ListAccountContacts(context.Context, string, int, string) ([]model.AccountContact, string, error)
	ListChildAccountIDsPage(context.Context, string, string, int32) ([]string, *string, error)
	ListProjectRefsForAccount(context.Context, string) ([]model.ProjectRef, error)
	GetProject(context.Context, string) (*model.Project, error)
	ListMeetingRefsForAccountPage(context.Context, string, string, int32) ([]model.MeetingRef, *string, error)

	PutPeopleLink(context.Context, *model.PeopleLink, string) error
	DeletePeopleLink(context.Context, string, string, model.PeopleLinkKey, string, bool) error
	ListPeopleLinksForMeeting(context.Context, string, string, int) ([]model.PeopleLink, bool, error)
	GetMentionIgnores(context.Context, string, []string) (map[string]bool, error)
	ReplaceUnresolvedMentions(context.Context, string, string, []model.UnresolvedMention, []string) error
	ClaimPeopleRun(context.Context, string, string, string, time.Time, time.Duration) (*model.PeopleRun, bool, error)
	FinishPeopleRun(context.Context, *model.PeopleRun) (bool, error)
}

// PeopleRunOptions controls one resolver run. DryRun plans without any write,
// including the run lease.
type PeopleRunOptions struct {
	DryRun bool
}

// PeopleRunResult holds counts and flags only; it never carries mention text.
type PeopleRunResult struct {
	Meetings, Mentions, Resolved, Unresolved, Written, Removed, Skipped int
	Busy, Truncated                                                     bool
}

func (r PeopleRunResult) String() string {
	return fmt.Sprintf("meetings=%d mentions=%d resolved=%d unresolved=%d written=%d removed=%d skipped=%d busy=%t truncated=%t",
		r.Meetings, r.Mentions, r.Resolved, r.Unresolved, r.Written, r.Removed, r.Skipped, r.Busy, r.Truncated)
}

// pass replaces the plan counts with a newer pass of the same scope and adds
// its writes.
func (r *PeopleRunResult) pass(o PeopleRunResult) {
	r.Written += o.Written
	r.Removed += o.Removed
	r.Skipped += o.Skipped
	r.Meetings, r.Mentions, r.Resolved, r.Unresolved, r.Truncated = o.Meetings, o.Mentions, o.Resolved, o.Unresolved, o.Truncated
}

func (r *PeopleRunResult) add(o PeopleRunResult) {
	r.Meetings += o.Meetings
	r.Mentions += o.Mentions
	r.Resolved += o.Resolved
	r.Unresolved += o.Unresolved
	r.Written += o.Written
	r.Removed += o.Removed
	r.Skipped += o.Skipped
	r.Truncated = r.Truncated || o.Truncated
}

// AccountPeopleService runs the deterministic people resolver over account
// meetings and keeps the auto edges and unresolved mention rows in step with
// the meeting fields. Manual edges, deny rows and ignores always win.
type AccountPeopleService struct {
	repo     accountPeopleRepo
	now      func() time.Time
	newToken func() string
}

func NewAccountPeopleService(repo accountPeopleRepo) *AccountPeopleService {
	return &AccountPeopleService{repo: repo, now: time.Now, newToken: uuid.NewString}
}

func meetingPublishedTo(m *model.Meeting) bool {
	return m != nil && m.SharedToAccount && m.AccountID != "" && validLinkPlainID(m.AccountID)
}

// ResolveMeeting resolves one meeting of its current account. An unpublished
// or missing meeting is skipped without writes; a held lease returns Busy
// after marking a rerun for the holder.
func (s *AccountPeopleService) ResolveMeeting(ctx context.Context, ownerID, meetingID string, opt PeopleRunOptions) (PeopleRunResult, error) {
	if !validLinkPlainID(ownerID) || !validLinkPlainID(meetingID) {
		return PeopleRunResult{}, ErrInvalidInput
	}
	return s.resolveMeeting(ctx, ownerID, meetingID, "", nil, opt)
}

// resolveMeeting runs up to maxPeopleRunPasses passes. accountID, when set,
// requires the meeting to be published to that account. dir, when set, is a
// directory shared by an account run.
func (s *AccountPeopleService) resolveMeeting(ctx context.Context, ownerID, meetingID, accountID string, dir *PeopleDirectory, opt PeopleRunOptions) (PeopleRunResult, error) {
	var total PeopleRunResult
	linkID := ownerID + ":" + meetingID
	for pass := 1; pass <= maxPeopleRunPasses; pass++ {
		meeting, err := s.repo.GetMeeting(ctx, ownerID, meetingID)
		if err != nil {
			return total, err
		}
		if !meetingPublishedTo(meeting) || (accountID != "" && meeting.AccountID != accountID) ||
			meeting.UserID != ownerID || meeting.MeetingID != meetingID {
			total.Skipped++
			return total, nil
		}
		run := &model.PeopleRun{AccountID: meeting.AccountID, Scope: linkID}
		if !opt.DryRun {
			claimed, ok, err := s.repo.ClaimPeopleRun(ctx, meeting.AccountID, linkID, s.newToken(), s.now(), peopleRunLease)
			if err != nil {
				return total, err
			}
			if !ok {
				total.Busy = true
				return total, nil
			}
			run = claimed
		}
		passDir := dir
		if passDir == nil {
			if passDir, err = s.loadDirectory(ctx, meeting.AccountID); err != nil {
				s.release(ctx, run, nil, opt)
				return total, err
			}
		}
		res, mentionSKs, err := s.applyMeeting(ctx, meeting, linkID, passDir, run, opt)
		total.pass(res)
		if err != nil {
			s.release(ctx, run, mentionSKs, opt)
			return total, err
		}
		if opt.DryRun {
			return total, nil
		}
		run.Mentions = mentionSKs
		run.RunAt = s.now().UTC().Format(time.RFC3339)
		run.Resolved, run.Unresolved, run.Truncated = res.Resolved, res.Unresolved, res.Truncated
		rerun, err := s.repo.FinishPeopleRun(ctx, run)
		if errors.Is(err, repository.ErrConditionFailed) {
			// Another run took over after the lease expired; it reconciles.
			total.Busy = true
			return total, nil
		}
		if err != nil {
			return total, err
		}
		if !rerun {
			break
		}
	}
	return total, nil
}

// release returns the lease after a failed pass. The recorded mention keys are
// the union of the prior and planned rows, a superset of what may exist, so
// the next run still deletes every stale row.
func (s *AccountPeopleService) release(ctx context.Context, run *model.PeopleRun, planned []string, opt PeopleRunOptions) {
	if opt.DryRun || run == nil || run.LeaseToken == "" {
		return
	}
	keys := append([]string(nil), run.Mentions...)
	for _, sk := range planned {
		if !slices.Contains(keys, sk) {
			keys = append(keys, sk)
		}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), peopleReleaseTimeout)
	defer cancel()
	release := *run
	release.Mentions = keys
	_, _ = s.repo.FinishPeopleRun(ctx, &release)
}

// applyMeeting plans one claimed meeting and, unless DryRun, applies the plan.
// It returns the sort keys of the planned unresolved mention rows.
func (s *AccountPeopleService) applyMeeting(ctx context.Context, meeting *model.Meeting, linkID string, dir *PeopleDirectory, run *model.PeopleRun, opt PeopleRunOptions) (PeopleRunResult, []string, error) {
	accountID := meeting.AccountID
	res := PeopleRunResult{Meetings: 1, Truncated: dir.ContactsTruncated}
	collected, truncated := CollectMeetingMentions(meeting)
	res.Truncated = res.Truncated || truncated

	hashes := make([]string, 0, len(collected))
	for _, m := range collected {
		if n := NormalizePersonName(m.Text); n != "" {
			hashes = append(hashes, model.MentionHash(n))
		}
	}
	ignored, err := s.repo.GetMentionIgnores(ctx, accountID, hashes)
	if err != nil {
		return res, nil, err
	}
	mentions := make([]PeopleMention, 0, len(collected))
	for _, m := range collected {
		if n := NormalizePersonName(m.Text); n != "" && ignored[model.MentionHash(n)] {
			continue
		}
		mentions = append(mentions, m)
	}
	res.Mentions = len(mentions)

	existing, more, err := s.repo.ListPeopleLinksForMeeting(ctx, accountID, linkID, maxPeopleExistingEdges)
	if err != nil {
		return res, nil, err
	}
	res.Truncated = res.Truncated || more
	type roleHash struct{ role, hash string }
	manual := map[roleHash]bool{}
	existingKeys := map[model.PeopleLinkKey]model.PeopleLink{}
	for _, l := range existing {
		existingKeys[l.Key()] = l
		if l.Origin == model.LinkOriginManual {
			if n := NormalizePersonName(l.Mention); n != "" {
				manual[roleHash{l.Role, model.MentionHash(n)}] = true
			}
		}
	}

	edges, planned, truncated := PlanMeetingPeople(accountID, linkID, dir, mentions, s.now())
	res.Truncated = res.Truncated || truncated
	unresolved := planned[:0]
	for _, m := range planned {
		// A user already bound this text for this role.
		if !manual[roleHash{m.Role, m.Hash}] {
			unresolved = append(unresolved, m)
		}
	}
	res.Resolved, res.Unresolved = len(edges), len(unresolved)
	mentionSKs := make([]string, 0, len(unresolved))
	for _, m := range unresolved {
		mentionSKs = append(mentionSKs, model.MentionSortKey(m.Hash, linkID, m.Role))
	}
	if opt.DryRun {
		return res, mentionSKs, nil
	}

	plannedKeys := map[model.PeopleLinkKey]bool{}
	for i := range edges {
		k := edges[i].Key()
		plannedKeys[k] = true
		if _, ok := existingKeys[k]; ok {
			continue
		}
		err := s.repo.PutPeopleLink(ctx, &edges[i], "")
		switch {
		case err == nil:
			res.Written++
		case errors.Is(err, repository.ErrAccountLinkExists), errors.Is(err, repository.ErrAccountLinkDenied),
			errors.Is(err, repository.ErrAccountLinkTarget):
			res.Skipped++
		default:
			return res, mentionSKs, err
		}
	}
	for k, l := range existingKeys {
		if l.Origin != model.LinkOriginAuto || plannedKeys[k] {
			continue
		}
		err := s.repo.DeletePeopleLink(ctx, accountID, "", k, model.LinkOriginAuto, false)
		switch {
		case err == nil:
			res.Removed++
		case errors.Is(err, repository.ErrConditionFailed):
			// The row became manual or was removed meanwhile.
			res.Skipped++
		default:
			return res, mentionSKs, err
		}
	}
	if err := s.repo.ReplaceUnresolvedMentions(ctx, accountID, linkID, unresolved, run.Mentions); err != nil {
		return res, mentionSKs, err
	}
	return res, mentionSKs, nil
}

// ResolveAccount resolves the account's meetings dated at or after since,
// newest first, at most maxMeetings, with one shared directory per pass. It
// holds the account lease so concurrent requests mark a rerun. When ctx ends
// it stops early with Truncated and a nil error.
func (s *AccountPeopleService) ResolveAccount(ctx context.Context, accountID string, since time.Time, maxMeetings int, opt PeopleRunOptions) (PeopleRunResult, error) {
	if !validLinkPlainID(accountID) || maxMeetings <= 0 {
		return PeopleRunResult{}, ErrInvalidInput
	}
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return PeopleRunResult{}, err
	}
	if !canonicalReadAccount(account, accountID) {
		return PeopleRunResult{}, ErrNotFound
	}
	var total PeopleRunResult
	for pass := 1; pass <= maxPeopleRunPasses; pass++ {
		run := &model.PeopleRun{AccountID: accountID, Scope: model.PeopleRunScopeAccount}
		if !opt.DryRun {
			claimed, ok, err := s.repo.ClaimPeopleRun(ctx, accountID, model.PeopleRunScopeAccount, s.newToken(), s.now(), peopleRunLease)
			if err != nil {
				return total, err
			}
			if !ok {
				total.Busy = true
				return total, nil
			}
			run = claimed
		}
		res, err := s.resolveAccountPass(ctx, accountID, since, maxMeetings, opt)
		total.pass(res)
		if err != nil {
			s.release(ctx, run, nil, opt)
			return total, err
		}
		if opt.DryRun {
			return total, nil
		}
		run.Mentions = nil
		run.RunAt = s.now().UTC().Format(time.RFC3339)
		run.Resolved, run.Unresolved, run.Truncated = res.Resolved, res.Unresolved, res.Truncated
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), peopleReleaseTimeout)
		rerun, err := s.repo.FinishPeopleRun(finishCtx, run)
		cancel()
		if errors.Is(err, repository.ErrConditionFailed) {
			total.Busy = true
			return total, nil
		}
		if err != nil {
			return total, err
		}
		if !rerun || ctx.Err() != nil {
			break
		}
	}
	return total, nil
}

func (s *AccountPeopleService) resolveAccountPass(ctx context.Context, accountID string, since time.Time, maxMeetings int, opt PeopleRunOptions) (PeopleRunResult, error) {
	var res PeopleRunResult
	dir, err := s.loadDirectory(ctx, accountID)
	if err != nil {
		return res, err
	}
	res.Truncated = dir.ContactsTruncated
	done := 0
	cursor := ""
	for {
		refs, next, err := s.repo.ListMeetingRefsForAccountPage(ctx, accountID, cursor, peopleRefPageSize)
		if err != nil {
			if ctx.Err() != nil {
				res.Truncated = true
				return res, nil
			}
			return res, err
		}
		for _, ref := range refs {
			if ref.Date.Before(since) {
				return res, nil
			}
			if done >= maxMeetings {
				res.Truncated = true
				return res, nil
			}
			if ctx.Err() != nil {
				res.Truncated = true
				return res, nil
			}
			if !validLinkPlainID(ref.OwnerUserID) || !validLinkPlainID(ref.MeetingID) || ref.AccountID != accountID {
				continue
			}
			done++
			one, err := s.resolveMeeting(ctx, ref.OwnerUserID, ref.MeetingID, accountID, dir, opt)
			if one.Busy {
				one.Busy = false
				one.Skipped++
			}
			res.add(one)
			if err != nil {
				if ctx.Err() != nil {
					res.Truncated = true
					return res, nil
				}
				return res, err
			}
		}
		if next == nil || *next == "" {
			return res, nil
		}
		cursor = *next
	}
}

// loadDirectory reads the account's candidate people and entities. Members
// are labelled by profile name (falling back to email); contacts are capped at
// maxPeopleContactsPerRun; entities are descendant accounts (breadth-first,
// revalidated against their current parent) and linked projects that still
// list the account.
func (s *AccountPeopleService) loadDirectory(ctx context.Context, accountID string) (*PeopleDirectory, error) {
	dir := &PeopleDirectory{}
	members, err := s.repo.ListAccountMembers(ctx, accountID)
	if err != nil {
		return nil, err
	}
	for i := range members {
		m := &members[i]
		if !canonicalReadMember(m, accountID, m.UserID) || !validLinkPlainID(m.UserID) {
			continue
		}
		name := m.Email
		user, err := s.repo.GetUserByID(ctx, m.UserID)
		if err != nil {
			return nil, err
		}
		if user != nil && user.UserID == m.UserID && user.Name != "" {
			name = user.Name
		}
		dir.People = append(dir.People, PersonCandidate{Kind: model.LinkKindMember, ID: m.UserID, Name: name, Email: m.Email})
	}

	after := ""
	contacts := 0
	for {
		page, next, err := s.repo.ListAccountContacts(ctx, accountID, peopleContactPageSize, after)
		if err != nil {
			return nil, err
		}
		for _, c := range page {
			if contacts >= maxPeopleContactsPerRun {
				dir.ContactsTruncated = true
				break
			}
			contacts++
			if validContactID(c.ContactID) {
				dir.People = append(dir.People, PersonCandidate{Kind: model.LinkKindContact, ID: c.ContactID, Name: c.Name, Email: c.Email})
			}
		}
		if next == "" || dir.ContactsTruncated {
			break
		}
		if contacts >= maxPeopleContactsPerRun {
			dir.ContactsTruncated = true
			break
		}
		after = next
	}

	if err := s.loadDescendants(ctx, accountID, dir); err != nil {
		return nil, err
	}

	refs, err := s.repo.ListProjectRefsForAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		if ref.AccountID != accountID || !validLinkPlainID(ref.ProjectID) {
			continue
		}
		project, err := s.repo.GetProject(ctx, ref.ProjectID)
		if err != nil {
			return nil, err
		}
		if project == nil || project.ProjectID != ref.ProjectID || !slices.Contains(project.AccountIDs, accountID) {
			continue
		}
		dir.Entities = append(dir.Entities, NamedCandidate{Kind: model.LinkKindProject, ID: project.ProjectID, Name: project.Name})
	}
	return dir, nil
}

func (s *AccountPeopleService) loadDescendants(ctx context.Context, accountID string, dir *PeopleDirectory) error {
	seen := map[string]bool{accountID: true}
	queue := []string{accountID}
	found := 0
	for len(queue) > 0 && found < maxPeopleDescendants {
		parent := queue[0]
		queue = queue[1:]
		cursor := ""
		for found < maxPeopleDescendants {
			ids, next, err := s.repo.ListChildAccountIDsPage(ctx, parent, cursor, 100)
			if err != nil {
				return err
			}
			for _, id := range ids {
				if seen[id] || found >= maxPeopleDescendants {
					continue
				}
				seen[id] = true
				child, err := s.repo.GetAccount(ctx, id)
				if err != nil {
					return err
				}
				if !canonicalReadAccount(child, id) || child.ParentAccountID != parent {
					continue
				}
				found++
				queue = append(queue, id)
				dir.Entities = append(dir.Entities, NamedCandidate{Kind: model.LinkKindAccount, ID: id, Name: child.Name, Aliases: child.Aliases})
			}
			if next == nil || *next == "" {
				break
			}
			cursor = *next
		}
	}
	return nil
}
