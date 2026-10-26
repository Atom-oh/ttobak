package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	idp "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	ct "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/ttobak/backend/internal/model"
)

func invitedIdentity(sub string) *ct.UserType {
	return &ct.UserType{Username: aws.String(sub), Attributes: []ct.AttributeType{
		{Name: aws.String("sub"), Value: aws.String(sub)},
	}}
}

// Cases override the default missing identity and successful delivery.
type inviteFixture struct {
	svc     *AccountService
	repo    *mockAccountRepo
	client  *fakeCognitoAdminAPI
	counter *fakeRateStore
	id      string
}

func newInviteFixture(t *testing.T) *inviteFixture {
	t.Helper()
	f := &inviteFixture{repo: newMockAccountRepo(), counter: &fakeRateStore{}}
	f.svc = newAccountServiceWithRepo(f.repo)
	account, err := f.svc.CreateAccount(context.Background(), "owner", "owner@example.com", &model.CreateAccountRequest{Name: "Customer"})
	if err != nil {
		t.Fatal(err)
	}
	f.id = account.AccountID
	f.client = &fakeCognitoAdminAPI{
		adminGetUserFn: func(context.Context, *idp.AdminGetUserInput) (*idp.AdminGetUserOutput, error) {
			return nil, &ct.UserNotFoundException{}
		},
		adminCreateUserFn: func(context.Context, *idp.AdminCreateUserInput) (*idp.AdminCreateUserOutput, error) {
			return &idp.AdminCreateUserOutput{User: invitedIdentity("invitee")}, nil
		},
		adminAddUserToGroupFn: func(context.Context, *idp.AdminAddUserToGroupInput) (*idp.AdminAddUserToGroupOutput, error) {
			t.Fatal("member invitations must not grant group membership")
			return nil, nil
		},
	}
	f.svc.SetCognitoAdminAPI(f.client, "pool")
	f.svc.SetInvitationCounter(NewWindowCounter(f.counter))
	return f
}

func (f *inviteFixture) add(requester, email, role string, resend bool) (*model.AccountMemberDTO, error) {
	return f.svc.AddMember(context.Background(), requester, f.id, &model.AddMemberRequest{
		Email: email, Role: role, ResendInvitation: resend,
	})
}

func (f *inviteFixture) requireNoInvite(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || len(f.client.createUserCalls) != 0 || len(f.repo.pendingShares) != 0 {
		t.Fatalf("unexpected invite side effects: %v", err)
	}
}

func (f *inviteFixture) rejectLookup(t *testing.T) {
	f.client.adminGetUserFn = func(context.Context, *idp.AdminGetUserInput) (*idp.AdminGetUserOutput, error) {
		t.Fatal("rejected request reached Cognito")
		return nil, nil
	}
}

func lookupInvitedIdentity(sub string, status ct.UserStatusType) func(context.Context, *idp.AdminGetUserInput) (*idp.AdminGetUserOutput, error) {
	return func(context.Context, *idp.AdminGetUserInput) (*idp.AdminGetUserOutput, error) {
		return &idp.AdminGetUserOutput{
			Username: aws.String("generated-username"), UserStatus: status,
			UserAttributes: append(invitedIdentity(sub).Attributes,
				ct.AttributeType{Name: aws.String("email"), Value: aws.String("new@example.com")}),
		}, nil
	}
}

func TestAddMember_OrdinaryMemberInvitesNewEmail(t *testing.T) {
	f := newInviteFixture(t)
	f.repo.members[memberKey(f.id, "member")] = &model.AccountMember{
		AccountID: f.id, UserID: "member", Email: "member@example.com", Role: model.RoleSSA,
	}
	f.client.adminCreateUserFn = func(_ context.Context, in *idp.AdminCreateUserInput) (*idp.AdminCreateUserOutput, error) {
		if aws.ToString(in.Username) != "new@example.com" || in.MessageAction != "" ||
			len(in.DesiredDeliveryMediums) != 1 || in.DesiredDeliveryMediums[0] != ct.DeliveryMediumTypeEmail {
			t.Fatalf("unexpected invitation: %+v", in)
		}
		return &idp.AdminCreateUserOutput{User: invitedIdentity("invitee-sub")}, nil
	}
	dto, err := f.add("member", " New@Example.com ", model.RoleTAM, false)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(dto)
	var receipt map[string]any
	_ = json.Unmarshal(data, &receipt)
	if receipt["pending"] != true || receipt["invited"] != true || dto.Email != "new@example.com" {
		t.Fatalf("invalid receipt: %s", data)
	}
	if len(f.client.createUserCalls) != 1 || len(f.repo.pendingShares) != 1 ||
		f.repo.pendingShares[0].InvitedCognitoSub != "invitee-sub" || f.repo.pendingShares[0].InvitedByUserID != "member" {
		t.Fatalf("identity-bound grant missing: %+v", f.repo.pendingShares)
	}
}

func TestAddMember_InvalidEmailDoesNotInvite(t *testing.T) {
	for _, email := range []string{"", "not-an-email", "Name <new@example.com>", strings.Repeat("a", 255) + "@example.com"} {
		t.Run(email, func(t *testing.T) {
			f := newInviteFixture(t)
			f.rejectLookup(t)
			_, err := f.add("owner", email, model.RoleSSA, false)
			f.requireNoInvite(t, err, ErrInvalidInviteEmail)
		})
	}
}

func TestAddMember_DeniedRequestsDoNotInvite(t *testing.T) {
	for _, test := range []struct {
		name, requester, role string
		want                  error
	}{
		{"non-member", "outsider", model.RoleSSA, ErrForbidden},
		{"owner-role", "owner", model.RoleOwner, ErrInvalidInput},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newInviteFixture(t)
			f.rejectLookup(t)
			_, err := f.add(test.requester, "new@example.com", test.role, false)
			f.requireNoInvite(t, err, test.want)
		})
	}
}

func TestAddMember_ConcurrentIdentityCreationReusesWinner(t *testing.T) {
	f := newInviteFixture(t)
	reads := 0
	f.client.adminGetUserFn = func(context.Context, *idp.AdminGetUserInput) (*idp.AdminGetUserOutput, error) {
		reads++
		if reads == 1 {
			return nil, &ct.UserNotFoundException{}
		}
		return &idp.AdminGetUserOutput{UserAttributes: invitedIdentity("winner-sub").Attributes}, nil
	}
	f.client.adminCreateUserFn = func(context.Context, *idp.AdminCreateUserInput) (*idp.AdminCreateUserOutput, error) {
		return nil, &ct.UsernameExistsException{}
	}
	dto, err := f.add("owner", "new@example.com", model.RoleSSA, false)
	if err != nil || !dto.Pending || dto.Invited || f.repo.pendingShares[0].InvitedCognitoSub != "winner-sub" {
		t.Fatalf("receipt=%+v error=%v", dto, err)
	}
	if len(f.client.createUserCalls) != 1 || f.client.createUserCalls[0].MessageAction != "" {
		t.Fatal("duplicate email")
	}
}

type failingAccountQueue struct{ *mockAccountRepo }

func (r *failingAccountQueue) PutPendingShare(context.Context, *model.PendingShare) error {
	return errors.New("queue unavailable")
}

func TestAddMember_InvitationQueueFailureIsPartialSuccess(t *testing.T) {
	f := newInviteFixture(t)
	f.svc.repo = &failingAccountQueue{f.repo}
	_, err := f.add("owner", "new@example.com", model.RoleSSA, false)
	if !errors.Is(err, ErrInvitationGrantFailed) {
		t.Fatalf("missing partial success: %v", err)
	}
}

func TestAddMember_InvitationCounterStopsBeforeMail(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprintf("unavailable=%v", unavailable), func(t *testing.T) {
			f := newInviteFixture(t)
			want := ErrInvitationRateLimited
			if unavailable {
				f.counter.err = errors.New("storage unavailable")
				want = ErrInvitationUnavailable
			} else {
				_, _, key, _ := rateWindow(f.svc.invitationCounter.now(), time.Hour)
				f.counter.counts = map[string]int64{"owner|RATE#account-invite#" + key: 20}
			}
			_, err := f.add("owner", "new@example.com", model.RoleSSA, false)
			f.requireNoInvite(t, err, want)
		})
	}
}

func TestAddMember_ExplicitResendUsesCurrentEmailForEmailOnlyPool(t *testing.T) {
	f := newInviteFixture(t)
	f.client.adminGetUserFn = lookupInvitedIdentity("invitee", ct.UserStatusTypeForceChangePassword)
	f.client.adminCreateUserFn = func(_ context.Context, in *idp.AdminCreateUserInput) (*idp.AdminCreateUserOutput, error) {
		if in.MessageAction != ct.MessageActionTypeResend {
			t.Fatal("missing RESEND")
		}
		if aws.ToString(in.Username) != "new@example.com" {
			return nil, &ct.InvalidParameterException{Message: aws.String("Username should be an email.")}
		}
		user := invitedIdentity("invitee")
		user.Attributes = append(user.Attributes, ct.AttributeType{Name: aws.String("email"), Value: aws.String("new@example.com")})
		return &idp.AdminCreateUserOutput{User: user}, nil
	}
	var request model.AddMemberRequest
	_ = json.Unmarshal([]byte(`{"email":"new@example.com","role":"SSA","resendInvitation":true}`), &request)
	dto, err := f.svc.AddMember(context.Background(), "owner", f.id, &request)
	if err != nil || !dto.Pending || !dto.Invited || len(f.client.createUserCalls) != 1 {
		t.Fatalf("receipt=%+v error=%v", dto, err)
	}
}

func TestAddMember_ResendIdentityChangedDuringDelivery(t *testing.T) {
	f := newInviteFixture(t)
	f.client.adminGetUserFn = lookupInvitedIdentity("original-sub", ct.UserStatusTypeForceChangePassword)
	f.client.adminCreateUserFn = func(context.Context, *idp.AdminCreateUserInput) (*idp.AdminCreateUserOutput, error) {
		return &idp.AdminCreateUserOutput{User: invitedIdentity("replacement-sub")}, nil
	}
	_, err := f.add("owner", "new@example.com", model.RoleSSA, true)
	if !errors.Is(err, ErrInvitationGrantFailed) || len(f.repo.pendingShares) != 0 {
		t.Fatalf("changed identity queued: %v", err)
	}
}

func TestAddMember_DomainAllowlistRunsBeforeMail(t *testing.T) {
	f := newInviteFixture(t)
	_, err := f.add("owner", "new@unapproved.invalid", model.RoleSSA, false)
	f.requireNoInvite(t, err, ErrInvitationRejected)
	if f.counter.calls != 0 {
		t.Fatal("domain check consumed the counter")
	}
}

func TestAddMember_ConfirmedIdentityCannotBeReset(t *testing.T) {
	f := newInviteFixture(t)
	f.client.adminGetUserFn = lookupInvitedIdentity("invitee", ct.UserStatusTypeConfirmed)
	_, err := f.add("owner", "new@example.com", model.RoleSSA, true)
	f.requireNoInvite(t, err, ErrInvitationNotPending)
}
