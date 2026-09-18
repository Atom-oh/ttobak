package service

import (
	"context"
	"errors"
	"github.com/ttobak/backend/internal/model"
	"testing"
	"time"
)

type sessionProjectRepo struct {
	listErr  error
	grantErr error
	*mockMeetingRepo
	grants   int
	profiles int
}

func (r *sessionProjectRepo) GetOrCreateUser(_ context.Context, id, email, name string) (*model.User, bool, error) {
	r.profiles++
	return &model.User{UserID: id, Email: email}, true, nil
}
func (r *sessionProjectRepo) MaterializePendingProjectGrant(_ context.Context, p *model.PendingShare, id, email string) (bool, error) {
	r.grants++
	if r.grantErr != nil {
		return false, r.grantErr
	}
	r.deletePendingShareLocked(email, p.SK)
	return true, nil
}
func (r *sessionProjectRepo) ListPendingShares(ctx context.Context, email string) ([]model.PendingShare, error) {
	all, err := r.mockMeetingRepo.ListPendingShares(ctx, email)
	result := []model.PendingShare{}
	for _, p := range all {
		if p.Kind != model.PendingShareKindProject {
			result = append(result, p)
		}
	}
	return result, err
}
func (r *sessionProjectRepo) ListPendingProjectSharesForUser(ctx context.Context, email, cursor string) ([]model.PendingShare, string, error) {
	if r.listErr != nil {
		return nil, "", r.listErr
	}
	rows, err := r.mockMeetingRepo.ListPendingShares(ctx, email)
	return rows, "", err
}
func (r *sessionProjectRepo) DeletePendingProjectShareIfMatch(ctx context.Context, p *model.PendingShare) error {
	return r.mockMeetingRepo.DeletePendingShareIfVersionMatches(ctx, p.Email, p)
}
func TestBootstrapSessionAppliesProjectInviteOnlyToVerifiedBoundIdentity(t *testing.T) {
	for _, test := range []struct {
		name, sub string
		verified  bool
		want      int
	}{{"verified", "recipient", true, 1}, {"unverified", "recipient", false, 0}, {"recreated", "old-sub", true, 0}} {
		t.Run(test.name, func(t *testing.T) {
			r := &sessionProjectRepo{mockMeetingRepo: newMockMeetingRepo()}
			r.pendingShares = []*model.PendingShare{{Kind: model.PendingShareKindProject, ProjectID: "project", SK: model.PrefixPendingProject + "project", Email: "user@example.com", InvitedCognitoSub: test.sub, TTL: time.Now().Add(time.Hour).Unix()}}
			s := newMeetingServiceWithRepo(r)
			for i := 0; i < 2; i++ {
				result, err := s.BootstrapSession(context.Background(), "recipient", "user@example.com", "", test.verified, "")
				if err != nil || result.EmailVerified != test.verified {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			}
			if r.grants != test.want || r.profiles != 2 {
				t.Fatalf("grants=%d profiles=%d", r.grants, r.profiles)
			}
		})
	}
}

func TestBootstrapPreservesFreshAccountDiscoveryWithoutGrantingFromHints(t *testing.T) {
	repo, svc := publishedTeamMeeting(t)
	repo.pendingShares = append(repo.pendingShares, &model.PendingShare{Email: "new@example.com", Kind: model.PendingShareKindAccount, AccountID: "acc-a", Role: model.RoleSA, InvitedByUserID: "owner", InvitedCognitoSub: "new-member", TTL: time.Now().Add(time.Hour).Unix(), SK: model.PrefixPendingAccount + "acc-a"})
	result, err := svc.BootstrapSession(context.Background(), "new-member", "new@example.com", "", true, "")
	if err != nil || len(result.JoinedAccountIDs) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	repo.membershipIndexLag = true
	page, err := svc.ListMeetings(context.Background(), "new-member", "all", "", "", 20, result.JoinedAccountIDs...)
	if err != nil || len(page.Meetings) != 1 {
		t.Fatalf("new membership lost during bootstrap: page=%+v err=%v", page, err)
	}
	outsider, err := svc.ListMeetings(context.Background(), "outsider", "all", "", "", 20, result.JoinedAccountIDs...)
	if err != nil || len(outsider.Meetings) != 0 {
		t.Fatalf("hint granted access: page=%+v err=%v", outsider, err)
	}
}

func TestBootstrapInvitationFailureIsVisibleWithoutBlockingExistingProfile(t *testing.T) {
	for _, failedRead := range []bool{false, true} {
		r := &sessionProjectRepo{mockMeetingRepo: newMockMeetingRepo()}
		if failedRead {
			r.listErr = errors.New("read unavailable")
		} else {
			r.grantErr = errors.New("write unavailable")
		}
		r.pendingShares = []*model.PendingShare{{Kind: model.PendingShareKindProject, ProjectID: "project", SK: model.PrefixPendingProject + "project", Email: "user@example.com", InvitedCognitoSub: "recipient", TTL: time.Now().Add(time.Hour).Unix()}}
		s := newMeetingServiceWithRepo(r)
		response, err := s.BootstrapSession(context.Background(), "recipient", "user@example.com", "", true, "")
		if err != nil || !response.RetryPending || r.profiles != 1 {
			t.Fatalf("response=%+v err=%v", response, err)
		}
	}
}

type pagedProjectSessionRepo struct {
	*sessionProjectRepo
	next, seen string
	deadline   time.Time
	listRows   []model.PendingShare
}

func (r *pagedProjectSessionRepo) ListPendingProjectSharesForUser(ctx context.Context, email, cursor string) ([]model.PendingShare, string, error) {
	r.seen = cursor
	r.deadline, _ = ctx.Deadline()
	return r.listRows, r.next, nil
}
func TestProjectBootstrapProcessesOneBoundedPageAndReturnsContinuation(t *testing.T) {
	r := &pagedProjectSessionRepo{sessionProjectRepo: &sessionProjectRepo{mockMeetingRepo: newMockMeetingRepo()}, next: "next-page"}
	for i := 0; i < 25; i++ {
		r.listRows = append(r.listRows, model.PendingShare{Kind: model.PendingShareKindProject, ProjectID: "00000000-0000-4000-8000-000000000001", InvitedCognitoSub: "recipient", Email: "user@example.com", TTL: time.Now().Add(time.Hour).Unix()})
	}
	s := newMeetingServiceWithRepo(r)
	result, err := s.BootstrapSession(context.Background(), "recipient", "user@example.com", "", true, "current-page")
	if err != nil || result.ProjectCursor != "next-page" || r.seen != "current-page" || r.grants != 25 || len(result.JoinedProjectIDs) != 25 || r.deadline.IsZero() || time.Until(r.deadline) > 5*time.Second {
		t.Fatalf("result=%+v err=%v grants=%d deadline=%v", result, err, r.grants, r.deadline)
	}
	r.grantErr = errors.New("temporarily unavailable")
	result, err = s.BootstrapSession(context.Background(), "recipient", "user@example.com", "", true, "current-page")
	if err != nil || !result.RetryPending || result.ProjectCursor != "current-page" {
		t.Fatalf("failed page was skipped: result=%+v err=%v", result, err)
	}
}
