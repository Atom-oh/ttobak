package service

import (
	"context"
	"log"
	"time"
)

type SessionBootstrapResponse struct {
	ProjectCursor             string   `json:"projectCursor,omitempty"`
	ProjectInvitationsEnabled bool     `json:"projectInvitationsEnabled"`
	JoinedAccountIDs          []string `json:"joinedAccountIds,omitempty"`
	EmailVerified             bool     `json:"emailVerified"`
	PendingGrants             int      `json:"pendingGrants"`
	RetryPending              bool     `json:"retryPending,omitempty"`
}

// Core initialization retains existing account/meeting behavior. Project-page
// consumption is supplied by the guard/bootstrap companion before activation.
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
	result := &SessionBootstrapResponse{EmailVerified: verified, JoinedAccountIDs: joined}
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
	return result, nil
}
