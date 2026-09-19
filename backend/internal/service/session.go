package service

import (
	"context"
	"errors"
	"github.com/ttobak/backend/internal/repository"
	"log"
	"os"
	"time"

	"github.com/ttobak/backend/internal/model"
)

type SessionBootstrapResponse struct {
	ProjectCursor             string   `json:"projectCursor,omitempty"`
	ProjectInvitationsEnabled bool     `json:"projectInvitationsEnabled"`
	JoinedAccountIDs          []string `json:"joinedAccountIds,omitempty"`
	EmailVerified             bool     `json:"emailVerified"`
	PendingGrants             int      `json:"pendingGrants"`
	RetryPending              bool     `json:"retryPending,omitempty"`
}

type projectBootstrapStore interface {
	ProjectInvitationsAllowed(context.Context) (bool, error)
	ListPendingProjectSharesForUser(context.Context, string, string) ([]model.PendingShare, string, error)
	MaterializePendingProjectGrant(context.Context, *model.PendingShare, string, string) (bool, error)
	DeletePendingProjectShareIfMatch(context.Context, *model.PendingShare) error
}

// BootstrapSession initializes the authenticated profile. Invitation side effects
// remain best-effort: one failed grant cannot block the user's unrelated data.
func (s *MeetingService) BootstrapSession(ctx context.Context, userID, email, name string, verified bool, projectCursor string) (*SessionBootstrapResponse, error) {
	if userID == "" || email == "" {
		return nil, ErrInvalidInput
	}
	if _, _, err := s.repo.GetOrCreateUser(ctx, userID, email, name); err != nil {
		return nil, err
	}
	joined := s.MaterializePendingShares(ctx, userID, email, verified)
	if len(joined) > 100 {
		joined = joined[:100]
	}
	result := &SessionBootstrapResponse{EmailVerified: verified, JoinedAccountIDs: joined, ProjectCursor: projectCursor, ProjectInvitationsEnabled: os.Getenv("PROJECT_INVITATIONS_ENABLED") == "true"}
	pending, err := s.repo.ListPendingShares(ctx, email)
	if err != nil {
		log.Printf("session pending-grant read failed: %v", err)
		result.RetryPending = true
	}
	for _, p := range pending {
		if p.InvitedCognitoSub == userID && p.TTL > time.Now().Unix() {
			result.PendingGrants++
			if verified {
				result.RetryPending = true
			}
		}
	}
	if !result.ProjectInvitationsEnabled {
		result.ProjectCursor = ""
		return result, nil
	}
	if store, ok := s.repo.(projectBootstrapStore); ok {
		budget, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		allowed, err := store.ProjectInvitationsAllowed(budget)
		if err != nil {
			log.Printf("project invitation control unavailable: %v", err)
			result.ProjectInvitationsEnabled = false
			result.RetryPending = true
			return result, nil
		}
		result.ProjectInvitationsEnabled = result.ProjectInvitationsEnabled && allowed
		if !allowed {
			result.ProjectCursor = ""
			return result, nil
		}
		if err := s.bootstrapProjectInvitations(budget, store, userID, email, verified, projectCursor, result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *MeetingService) bootstrapProjectInvitations(ctx context.Context, store projectBootstrapStore, userID, email string, verified bool, cursor string, result *SessionBootstrapResponse) error {
	budget := ctx
	projects, next, err := store.ListPendingProjectSharesForUser(budget, email, cursor)
	if errors.Is(err, repository.ErrInvalidCursor) {
		return err
	}
	if err != nil {
		log.Printf("session project-invitation read failed: %v", err)
		result.RetryPending = true
		return nil
	}
	awaitingVerification := false
	for i := range projects {
		if budget.Err() != nil {
			result.RetryPending = true
			return nil
		}
		p := &projects[i]
		if p.Kind != model.PendingShareKindProject {
			continue
		}
		if p.TTL <= time.Now().Unix() {
			if err := store.DeletePendingProjectShareIfMatch(budget, p); err != nil {
				log.Printf("session expired invitation cleanup failed: %v", err)
				result.RetryPending = true
				return nil
			}
			continue
		}
		if p.InvitedCognitoSub != userID {
			continue
		}
		if !verified {
			result.PendingGrants++
			awaitingVerification = true
			continue
		}
		resolved, err := store.MaterializePendingProjectGrant(budget, p, userID, email)
		if err != nil || !resolved {
			if errors.Is(err, repository.ErrProjectInvitationsPaused) {
				result.ProjectInvitationsEnabled = false
				result.ProjectCursor = ""
				return nil
			}
			if err != nil {
				log.Printf("session project invitation could not finish: %v", err)
			}
			result.RetryPending = true
			result.PendingGrants++
			return nil
		}
	}
	if !awaitingVerification {
		result.ProjectCursor = next
	}
	return nil
}
