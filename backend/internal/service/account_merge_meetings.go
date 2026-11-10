package service

import (
	"context"
	"errors"
	"log"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const (
	mergeMeetingPageSize = 25
	// mergeMeetingsPerRun is how many source meeting references one
	// moveMeetings call processes; each costs a meeting move, its share
	// retags, a grant per target member lacking a share and an insight
	// fanout.
	mergeMeetingsPerRun = 50
)

// accountMergeMeetingRepo lists the reads and writers the meetings step uses.
type accountMergeMeetingRepo interface {
	ListMeetingRefsForAccountPage(ctx context.Context, accountID, cursor string, limit int32) ([]model.MeetingRef, *string, error)
	GetMeetingByID(ctx context.Context, meetingID string) (*model.Meeting, error)
	MoveMeetingForAccountMerge(ctx context.Context, job *model.AccountMergeJob, ownerID, meetingID string, ref *model.MeetingRef) error
	DeleteMeetingRefForMerge(ctx context.Context, job *model.AccountMergeJob, ref *model.MeetingRef) error
	ListSharesForMeeting(ctx context.Context, meetingID string) ([]model.Share, error)
	RetagAccountShareForMerge(ctx context.Context, job *model.AccountMergeJob, meetingID, sharedToID string) error
	CreateShareIfMember(ctx context.Context, meetingID, ownerID, ownerEmail, accountID, sharedToID, email, permission string) (*model.Share, error)
	PutAccountInsights(ctx context.Context, insights []model.AccountInsight) error
}

// moveMeetings re-points up to mergeMeetingsPerRun meetings referenced by
// the source's MEETINGREF# rows to the target, owners unchanged, then
// finishes each one: account-origin shares granted by the source are
// retagged to the target, target members without a share are granted one
// when the meeting is published, the target's insight rows are written, and
// only then is the source reference deleted. A meeting the source reference
// no longer describes (gone, or on another account) is Skipped and its stale
// reference left in place (readers revalidate). A meeting already on the
// target with its source reference still present is a run interrupted after
// its move, so it is finished again without a second move. The cursor is
// the reference page position to continue from; a repeat run moves nothing.
func (s *AccountMergeService) moveMeetings(fence *mergeFence, job *model.AccountMergeJob) (mergeMoveResult, error) {
	result := mergeMoveResult{Cursor: job.Cursor}
	ctx := fence.Context()
	if err := fence.Err(); err != nil {
		return result, err
	}
	var out mergeMoveResult
	cursor, processed := job.Cursor, 0
	for {
		refs, next, err := s.mover.ListMeetingRefsForAccountPage(ctx, job.SourceAccountID, cursor, int32(min(mergeMeetingPageSize, mergeMeetingsPerRun-processed)))
		if err != nil {
			return result, fence.translate(err)
		}
		for i := range refs {
			if err := fence.Err(); err != nil {
				return result, err
			}
			moved, err := s.moveMeeting(ctx, job, &refs[i])
			if err != nil {
				return result, fence.translate(err)
			}
			if moved {
				out.Moved++
			} else {
				out.Skipped++
			}
		}
		processed += len(refs)
		if next == nil || *next == "" {
			out.Done = true
			break
		}
		cursor = *next
		if processed >= mergeMeetingsPerRun {
			break
		}
	}
	out.Cursor = cursor
	log.Printf("account merge meetings: source=%s moved=%d skipped=%d", job.SourceAccountID, out.Moved, out.Skipped)
	return out, nil
}

// moveMeeting handles one source reference and reports whether it moved the
// meeting in this call.
func (s *AccountMergeService) moveMeeting(ctx context.Context, job *model.AccountMergeJob, ref *model.MeetingRef) (bool, error) {
	if ref.MeetingID == "" {
		return false, nil
	}
	meeting, err := s.mover.GetMeetingByID(ctx, ref.MeetingID)
	if err != nil {
		return false, err
	}
	if meeting == nil {
		return false, nil
	}
	moved := false
	switch meeting.AccountID {
	case job.SourceAccountID:
		if err := s.mover.MoveMeetingForAccountMerge(ctx, job, meeting.UserID, meeting.MeetingID, ref); err != nil {
			return false, err
		}
		meeting.AccountID, moved = job.TargetAccountID, true
	case job.TargetAccountID:
		// Interrupted after the move: finish it below.
	default:
		return false, nil
	}
	if err := s.finishMeetingMove(ctx, job, meeting); err != nil {
		return false, err
	}
	return moved, s.mover.DeleteMeetingRefForMerge(ctx, job, ref)
}

// finishMeetingMove brings a meeting now on the target to the state a
// target publication would have: source-granted account shares retagged,
// every other target member granted read when the meeting is published, and
// the target's insight rows written. Every write is idempotent. A link-only
// meeting gains no share and no insight.
func (s *AccountMergeService) finishMeetingMove(ctx context.Context, job *model.AccountMergeJob, meeting *model.Meeting) error {
	shares, err := s.mover.ListSharesForMeeting(ctx, meeting.MeetingID)
	if err != nil {
		return err
	}
	shared := map[string]bool{meeting.UserID: true}
	ownerEmail := ""
	for _, share := range shares {
		if share.SharedToID == "" {
			continue
		}
		shared[share.SharedToID] = true
		if ownerEmail == "" {
			ownerEmail = share.OwnerEmail
		}
		if share.Origin == model.ShareOriginAccount && share.AccountID == job.SourceAccountID {
			if err := s.mover.RetagAccountShareForMerge(ctx, job, meeting.MeetingID, share.SharedToID); err != nil {
				return err
			}
		}
	}
	if !meeting.SharedToAccount {
		return nil
	}
	members, err := s.mover.ListAccountMembers(ctx, job.TargetAccountID)
	if err != nil {
		return err
	}
	for _, member := range members {
		if member.UserID == "" || shared[member.UserID] {
			continue
		}
		if ownerEmail == "" {
			ownerEmail = creatorEmail(ctx, s.mover, meeting.UserID, "", "")
		}
		if _, err := s.mover.CreateShareIfMember(ctx, meeting.MeetingID, meeting.UserID, ownerEmail, job.TargetAccountID,
			member.UserID, member.Email, model.PermissionRead); err != nil && !errors.Is(err, repository.ErrMemberRemoved) {
			return err
		}
	}
	items, err := BuildAccountInsights(job.TargetAccountID, meeting)
	if err != nil {
		// Unparseable stored insights have nothing to fan out, as on a
		// target publication.
		return nil
	}
	if len(items) > 0 {
		return s.mover.PutAccountInsights(ctx, items)
	}
	return nil
}
