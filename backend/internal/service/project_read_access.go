package service

import (
	"context"
	"errors"

	"github.com/ttobak/backend/internal/model"
)

// Mutation eligibility continues to use requireProjectAccess. A grant returned
// here identifies readers whose only project access comes from an ancestor.
func (s *ProjectService) requireProjectReadAccess(ctx context.Context, user, id string) (*model.Project, *accountReadGrant, error) {
	project, err := s.requireProjectAccess(ctx, user, id)
	if !errors.Is(err, ErrForbidden) {
		return project, nil, err
	}
	project, err = s.repo.GetProject(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if project == nil {
		return nil, nil, ErrNotFound
	}
	for _, account := range project.AccountIDs {
		grant, err := resolveAccountReadGrant(ctx, s.repo, account, user)
		if err != nil {
			return nil, nil, err
		}
		if grant == nil {
			continue
		}
		valid, err := revalidateAccountReadGrant(ctx, s.repo, grant, user)
		if err != nil {
			return nil, nil, err
		}
		if valid {
			return project, grant, nil
		}
	}
	return nil, nil, ErrForbidden
}

func (s *ProjectService) independentlyReadableMeeting(ctx context.Context, user string, meeting *model.Meeting) (*model.Meeting, error) {
	if meeting.UserID == user {
		return meeting, nil
	}
	if shares, ok := s.repo.(interface {
		GetShare(context.Context, string, string) (*model.Share, error)
	}); ok {
		share, err := shares.GetShare(ctx, user, meeting.MeetingID)
		if err != nil {
			return nil, err
		}
		if share != nil && share.Origin != model.ShareOriginAccount && share.OwnerID == meeting.UserID {
			return meeting, nil
		}
	}
	if !meeting.SharedToAccount || meeting.AccountID == "" {
		return nil, nil
	}
	grant, err := resolveAccountReadGrant(ctx, s.repo, meeting.AccountID, user)
	if err != nil || grant == nil {
		return nil, err
	}
	valid, err := revalidateAccountReadGrant(ctx, s.repo, grant, user)
	if err != nil || !valid {
		return nil, err
	}
	return projectAccountReadMeeting(meeting, grant), nil
}

func (s *ProjectService) independentlyReadableResearch(ctx context.Context, user string, research model.Research) (bool, error) {
	if research.UserID == user {
		return true, nil
	}
	if shares, ok := s.repo.(interface {
		GetShare(context.Context, string, string) (*model.Share, error)
	}); ok {
		share, err := shares.GetShare(ctx, user, research.ResearchID)
		if err != nil {
			return false, err
		}
		if share != nil && share.EntityType == "RESEARCH_SHARE" && share.OwnerID == research.UserID {
			return true, nil
		}
	}
	for _, account := range research.AccountIDs {
		grant, err := resolveAccountReadGrant(ctx, s.repo, account, user)
		if err != nil {
			return false, err
		}
		if grant != nil {
			return revalidateAccountReadGrant(ctx, s.repo, grant, user)
		}
	}
	return false, nil
}
