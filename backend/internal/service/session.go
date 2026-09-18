package service

import (
	"context"
	"log"
	"time"

	"github.com/ttobak/backend/internal/model"
)

type SessionBootstrapResponse struct {
	JoinedAccountIDs []string `json:"joinedAccountIds,omitempty"`
	EmailVerified    bool     `json:"emailVerified"`
	PendingGrants    int      `json:"pendingGrants"`
	RetryPending     bool     `json:"retryPending,omitempty"`
}

type projectBootstrapStore interface {
	ListPendingProjectSharesForUser(context.Context, string) ([]model.PendingShare, error)
	MaterializePendingProjectGrant(context.Context, *model.PendingShare, string, string) (bool, error)
	DeletePendingProjectShareIfMatch(context.Context, *model.PendingShare) error
}

// BootstrapSession initializes the authenticated profile. Invitation side effects
// remain best-effort: one failed grant cannot block the user's unrelated data.
func (s *MeetingService) BootstrapSession(ctx context.Context, userID, email, name string, verified bool) (*SessionBootstrapResponse, error) {
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
	result := &SessionBootstrapResponse{EmailVerified: verified, JoinedAccountIDs: joined}
	pending, err := s.repo.ListPendingShares(ctx, email)
	if err != nil {
		log.Printf("session pending-grant read failed: %v", err)
		result.RetryPending = true
	}
	for _, p := range pending {
		if p.InvitedCognitoSub == userID && p.TTL > time.Now().Unix() {
			result.PendingGrants++
		}
	}
	if store, ok := s.repo.(projectBootstrapStore); ok {
		s.bootstrapProjectInvitations(ctx, store, userID, email, verified, result)
	}
	return result, nil
}

func (s *MeetingService) bootstrapProjectInvitations(ctx context.Context, store projectBootstrapStore, userID, email string, verified bool, result *SessionBootstrapResponse) {
	projects, err := store.ListPendingProjectSharesForUser(ctx, email)
	if err != nil {
		log.Printf("session project-invitation read failed: %v", err)
		result.RetryPending = true
		return
	}
	for i := range projects {
		p := &projects[i]
		if p.Kind != model.PendingShareKindProject {
			continue
		}
		if p.TTL <= time.Now().Unix() {
			if err := store.DeletePendingProjectShareIfMatch(ctx, p); err != nil {
				log.Printf("session expired project-invitation cleanup failed: %v", err)
				result.RetryPending = true
			}
			continue
		}
		if p.InvitedCognitoSub != userID {
			continue
		}
		if !verified {
			result.PendingGrants++
			continue
		}
		resolved, err := store.MaterializePendingProjectGrant(ctx, p, userID, email)
		if err != nil {
			log.Printf("session project invitation could not finish: %v", err)
			result.RetryPending = true
			result.PendingGrants++
			continue
		}
		if !resolved {
			result.PendingGrants++
		}
	}
}
