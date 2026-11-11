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
	ClearAccountInsights(ctx context.Context, ownerID, meetingID, accountID, generation, revision string) error
}

// meetingMoveRun caches what one moveMeetings call reads once: the target's
// member list, loaded on the first published meeting.
type meetingMoveRun struct {
	members []model.AccountMember
	loaded  bool
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
// its move, so it is finished again without a second move and counted as
// Resumed. The cursor is
// the reference page position to continue from; a repeat run moves nothing.
func (s *AccountMergeService) moveMeetings(fence *mergeFence, job *model.AccountMergeJob) (mergeMoveResult, error) {
	result := mergeMoveResult{Cursor: job.Cursor}
	ctx := fence.Context()
	if err := fence.Err(); err != nil {
		return result, err
	}
	var out mergeMoveResult
	cursor, processed := job.Cursor, 0
	run := &meetingMoveRun{}
	for {
		refs, next, err := s.mover.ListMeetingRefsForAccountPage(ctx, job.SourceAccountID, cursor, int32(min(mergeMeetingPageSize, mergeMeetingsPerRun-processed)))
		if err != nil {
			return result, fence.translate(err)
		}
		for i := range refs {
			if err := fence.Err(); err != nil {
				return result, err
			}
			outcome, err := s.moveMeeting(ctx, job, run, &refs[i])
			if err != nil {
				return result, fence.translate(err)
			}
			switch outcome {
			case meetingMoved:
				out.Moved++
			case meetingResumed:
				out.Resumed++
			default:
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
	log.Printf("account merge meetings: source=%s moved=%d resumed=%d skipped=%d", job.SourceAccountID, out.Moved, out.Resumed, out.Skipped)
	return out, nil
}

type meetingMoveOutcome int

const (
	meetingSkipped meetingMoveOutcome = iota
	meetingMoved
	meetingResumed
)

// moveMeeting handles one source reference.
func (s *AccountMergeService) moveMeeting(ctx context.Context, job *model.AccountMergeJob, run *meetingMoveRun, ref *model.MeetingRef) (meetingMoveOutcome, error) {
	if ref.MeetingID == "" {
		return meetingSkipped, nil
	}
	meeting, err := s.mover.GetMeetingByID(ctx, ref.MeetingID)
	if err != nil || meeting == nil {
		return meetingSkipped, err
	}
	outcome := meetingResumed
	switch meeting.AccountID {
	case job.SourceAccountID:
		if err := s.mover.MoveMeetingForAccountMerge(ctx, job, meeting.UserID, meeting.MeetingID, ref); err != nil {
			return meetingSkipped, err
		}
		outcome = meetingMoved
	case job.TargetAccountID:
		// Interrupted after the move: finish it below.
	default:
		return meetingSkipped, nil
	}
	if err := s.finishMeetingMove(ctx, job, run, meeting.MeetingID); err != nil {
		return meetingSkipped, err
	}
	// The source's dictionary layer stays on the retired source (an unmoved
	// category, ADR-022) and is not copied to the target; the moved meeting's
	// own automatic-term evidence leaves it like on any relink (ADR-008 FU4).
	// Best effort: a failure is logged and never fails the merge step.
	_ = sweepUnlinkedAccountTerms(ctx, s.mover, "account-merge", meeting.UserID, meeting.MeetingID)
	return outcome, s.mover.DeleteMeetingRefForMerge(ctx, job, ref)
}

// finishMeetingMove brings a meeting now on the target to the state a
// target publication would have: source-granted account shares retagged,
// every other target member granted read and the target's insight rows
// written (or cleared when the meeting has none). Grants and insights use a
// fresh canonical read taken after the move and require the meeting to be
// on the target and published: the share and legacy insight writers do not
// revalidate publication when the summary privacy writer is off, so a
// meeting unpublished concurrently, or link-only, gains no share and no
// insight. Every write is idempotent.
func (s *AccountMergeService) finishMeetingMove(ctx context.Context, job *model.AccountMergeJob, run *meetingMoveRun, meetingID string) error {
	meeting, err := s.mover.GetMeetingByID(ctx, meetingID)
	if err != nil || meeting == nil || meeting.AccountID != job.TargetAccountID {
		return err
	}
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
	if !run.loaded {
		if run.members, err = s.mover.ListAccountMembers(ctx, job.TargetAccountID); err != nil {
			return err
		}
		run.loaded = true
	}
	for _, member := range run.members {
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
	// As on a target publication, an empty result must not leave earlier
	// target rows for this meeting visible.
	return s.mover.ClearAccountInsights(ctx, meeting.UserID, meeting.MeetingID, job.TargetAccountID,
		meeting.SummaryGeneration(), meeting.InsightsRevision)
}
