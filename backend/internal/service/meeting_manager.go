package service

import (
	"context"

	"github.com/ttobak/backend/internal/model"
)

// managerRepo is what manager resolution needs: owner lookup plus the same
// share/membership/hierarchy reads as resolveSharedAccess.
type managerRepo interface {
	shareAccessRepo
	GetMeeting(ctx context.Context, userID, meetingID string) (*model.Meeting, error)
}

// resolveMeetingManager returns the canonical (owner-partition) meeting when
// userID may manage it like its creator: the creator, or a live direct or
// inherited member of the account the meeting is published to (ADR-016
// 2026-11-06 amendment). Delete is never covered. Callers write through
// meeting.UserID and keep userID as the actor. Another kind of access
// (e.g. a direct email share) yields ErrForbidden; no access yields
// ErrNotFound so existence is not revealed.
func resolveMeetingManager(ctx context.Context, repo managerRepo, userID, meetingID string) (*model.Meeting, error) {
	owned, err := repo.GetMeeting(ctx, userID, meetingID)
	if err != nil {
		return nil, err
	}
	if owned != nil {
		return owned, nil
	}
	team, err := teamManagedMeeting(ctx, repo, userID, meetingID)
	if err != nil || team != nil {
		return team, err
	}
	shared, _, err := resolveSharedAccess(ctx, repo, userID, meetingID)
	if err != nil {
		return nil, err
	}
	if shared != nil {
		return nil, ErrForbidden
	}
	return nil, ErrNotFound
}

// teamManagedMeeting returns the canonical meeting when userID is a live
// member (direct or, with descendant reads enabled, inherited) of the
// account it is currently published to; otherwise nil.
func teamManagedMeeting(ctx context.Context, repo shareAccessRepo, userID, meetingID string) (*model.Meeting, error) {
	meeting, err := repo.GetMeetingByID(ctx, meetingID)
	if err != nil || meeting == nil || !meeting.SharedToAccount || meeting.AccountID == "" || meeting.UserID == userID {
		return nil, err
	}
	grant, err := resolveAccountReadGrant(ctx, repo, meeting.AccountID, userID)
	if err != nil || grant == nil {
		return nil, err
	}
	valid, err := revalidateAccountReadGrant(ctx, repo, grant, userID)
	if err != nil || !valid {
		return nil, err
	}
	return meeting, nil
}

// canManageMeeting reports manager capability for an already-authorized
// reader (used for detail flags).
func canManageMeeting(ctx context.Context, repo shareAccessRepo, userID string, meeting *model.Meeting, permission string) (bool, error) {
	if permission == "owner" {
		return true, nil
	}
	if meeting == nil || !meeting.SharedToAccount {
		return false, nil
	}
	team, err := teamManagedMeeting(ctx, repo, userID, meeting.MeetingID)
	return team != nil, err
}

// creatorEmail returns the creator's profile email for share rows; best effort
// (empty when the profile cannot be read).
func creatorEmail(ctx context.Context, repo interface{}, ownerID, actorID, actorEmail string) string {
	if ownerID == actorID {
		return actorEmail
	}
	if users, ok := repo.(interface {
		GetUserByID(context.Context, string) (*model.User, error)
	}); ok {
		if user, err := users.GetUserByID(ctx, ownerID); err == nil && user != nil {
			return user.Email
		}
	}
	return ""
}

// requireSameManagedAccount limits a non-creator manager to republishing or
// unpublishing within the account that granted management; moving the
// meeting to another account stays creator-only.
func requireSameManagedAccount(actorID string, meeting *model.Meeting, accountID string) error {
	if actorID != meeting.UserID && accountID != meeting.AccountID {
		return ErrForbidden
	}
	return nil
}
