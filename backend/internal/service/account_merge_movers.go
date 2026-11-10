package service

import (
	"context"
	"errors"
	"sort"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// ErrMergeFenceLost means the merge job lost its fence (lease, source marker or
// requester authority). Movers stop at once and leave their cursor unchanged.
var ErrMergeFenceLost = repository.ErrMergeFenceLost

// accountMergeMoverRepo lists the writers merge movers use. They are the
// operator path conditioned on the job, not the open-fenced account writers.
type accountMergeMoverRepo interface {
	ListAccountMembers(ctx context.Context, accountID string) ([]model.AccountMember, error)
	GetMember(ctx context.Context, accountID, userID string) (*model.AccountMember, error)
	PutMergedMember(ctx context.Context, job *model.AccountMergeJob, member *model.AccountMember) error

	GetAccount(ctx context.Context, accountID string) (*model.Account, error)
	ListAccountPartitionKeys(ctx context.Context, accountID, prefix string, limit int) ([]string, bool, error)
	ListAccountContactsForMerge(ctx context.Context, accountID string, limit int, after string) ([]model.AccountContact, []string, string, error)
	GetAccountContact(ctx context.Context, accountID, contactID string) (*model.AccountContact, error)
	GetMergeContactMap(ctx context.Context, sourceAccountID string, ids []string) (map[string]string, error)
	MoveContactForMerge(ctx context.Context, job *model.AccountMergeJob, from model.AccountContact) error
	MergeContactIntoExisting(ctx context.Context, job *model.AccountMergeJob, from model.AccountContact, into model.AccountContact) error

	ListAccountEventsForMerge(ctx context.Context, accountID string, limit int, afterSK string) ([]repository.MergeEventRow, []string, string, error)
	ExistingAccountContactIDs(ctx context.Context, accountID string, ids []string) (map[string]bool, error)
	MoveEventForMerge(ctx context.Context, job *model.AccountMergeJob, sk string, from model.AccountEvent, contactMap map[string]string) error

	accountMergeMeetingRepo
}

// mergeFence carries one worker's authority over a job. The lease heartbeat
// calls Lose when it cannot renew; movers observe it between writes and pass
// Context to every repository call so in-flight work is cancelled too.
type mergeFence struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
}

func newMergeFence(parent context.Context) *mergeFence {
	ctx, cancel := context.WithCancelCause(parent)
	return &mergeFence{ctx: ctx, cancel: cancel}
}

// Context is cancelled with ErrMergeFenceLost once the fence is lost.
func (f *mergeFence) Context() context.Context { return f.ctx }

// Lose cancels the fence. It is safe to call more than once.
func (f *mergeFence) Lose() { f.cancel(ErrMergeFenceLost) }

// Err returns ErrMergeFenceLost when the fence was lost, the parent's error
// when it was cancelled, and nil otherwise.
func (f *mergeFence) Err() error {
	if f.ctx.Err() == nil {
		return nil
	}
	if errors.Is(context.Cause(f.ctx), ErrMergeFenceLost) {
		return ErrMergeFenceLost
	}
	return f.ctx.Err()
}

// translate maps a repository error to the fence outcome: a lost fence also
// cancels the context so sibling work stops.
func (f *mergeFence) translate(err error) error {
	if errors.Is(err, ErrMergeFenceLost) {
		f.Lose()
		return ErrMergeFenceLost
	}
	if fenceErr := f.Err(); fenceErr != nil {
		return fenceErr
	}
	return err
}

// mergeMoveResult is one mover run. Cursor is the value to persist; it equals
// the input cursor whenever an error is returned alongside the result.
type mergeMoveResult struct {
	Moved int
	// Skipped counts items already moved by an earlier run.
	Skipped int
	// Unreadable counts source items left in place because they could not
	// be opened; the worker reports them on the job.
	Unreadable int
	// KeptSeparate counts source contacts that matched a target contact but
	// were moved separately because the merged contact would be too large.
	KeptSeparate int
	Cursor       string
	Done         bool
}

// mergedMemberRole maps a source role onto the target: the source Owner joins
// as SA (Owner is only granted by explicit transfer); other roles carry over.
func mergedMemberRole(role string) string {
	if role == model.RoleOwner {
		return model.RoleSA
	}
	return role
}

// moveMembers copies the source's members into the target. Existing target
// members keep their role and are not counted. Pending invites live outside
// the member rows and are not touched. The cursor is the last member user ID
// copied, so a resumed run continues after it; a repeat run moves nothing.
func (s *AccountMergeService) moveMembers(fence *mergeFence, job *model.AccountMergeJob) (mergeMoveResult, error) {
	result := mergeMoveResult{Cursor: job.Cursor}
	ctx := fence.Context()
	if err := fence.Err(); err != nil {
		return result, err
	}
	members, err := s.mover.ListAccountMembers(ctx, job.SourceAccountID)
	if err != nil {
		return result, fence.translate(err)
	}
	sort.Slice(members, func(i, j int) bool { return members[i].UserID < members[j].UserID })
	cursor := job.Cursor
	moved := 0
	for _, member := range members {
		if member.UserID == "" || member.UserID <= job.Cursor {
			continue
		}
		if err := fence.Err(); err != nil {
			return result, err
		}
		existing, err := s.mover.GetMember(ctx, job.TargetAccountID, member.UserID)
		if err != nil {
			return result, fence.translate(err)
		}
		if existing == nil {
			copied := model.AccountMember{AccountID: job.TargetAccountID, UserID: member.UserID, Email: member.Email,
				Role: mergedMemberRole(member.Role), AddedAt: member.AddedAt}
			if err := s.mover.PutMergedMember(ctx, job, &copied); err != nil {
				return result, fence.translate(err)
			}
			moved++
		}
		cursor = member.UserID
	}
	return mergeMoveResult{Moved: moved, Cursor: cursor, Done: true}, nil
}
