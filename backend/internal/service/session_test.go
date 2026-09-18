package service

import (
	"context"
	"github.com/ttobak/backend/internal/model"
	"testing"
	"time"
)

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

func TestCoreBootstrapSignalsRemainingEligibleGrants(t *testing.T) {
	r := newMockMeetingRepo()
	r.pendingShares = []*model.PendingShare{{Kind: model.PendingShareKindAccount, Email: "user@example.com", InvitedCognitoSub: "recipient", TTL: time.Now().Add(time.Hour).Unix()}}
	s := newMeetingServiceWithRepo(r)
	for _, verified := range []bool{false, true} {
		result, err := s.BootstrapSession(context.Background(), "recipient", "user@example.com", "", verified, "")
		if err != nil || result.PendingGrants != 1 || result.RetryPending != verified || result.ProjectInvitationsEnabled {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
}
