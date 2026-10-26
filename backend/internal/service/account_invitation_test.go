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
	cognitoidp "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	cognitotypes "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/ttobak/backend/internal/model"
)

func invitedIdentity(sub string) *cognitotypes.UserType {
	return &cognitotypes.UserType{
		Username: aws.String(sub),
		Attributes: []cognitotypes.AttributeType{
			{Name: aws.String("sub"), Value: aws.String(sub)},
		},
	}
}

func TestAddMember_OrdinaryMemberInvitesNewEmail(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	account, err := svc.CreateAccount(context.Background(), "owner", "owner@example.com", &model.CreateAccountRequest{Name: "Customer"})
	if err != nil {
		t.Fatal(err)
	}
	repo.members[memberKey(account.AccountID, "member")] = &model.AccountMember{
		AccountID: account.AccountID, UserID: "member", Email: "member@example.com", Role: model.RoleSSA,
	}
	client := &fakeCognitoAdminAPI{
		adminGetUserFn: func(context.Context, *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
			return nil, &cognitotypes.UserNotFoundException{}
		},
		adminCreateUserFn: func(_ context.Context, input *cognitoidp.AdminCreateUserInput) (*cognitoidp.AdminCreateUserOutput, error) {
			if aws.ToString(input.Username) != "new@example.com" || input.MessageAction != "" ||
				len(input.DesiredDeliveryMediums) != 1 || input.DesiredDeliveryMediums[0] != cognitotypes.DeliveryMediumTypeEmail {
				t.Fatalf("unexpected invitation input: %+v", input)
			}
			return &cognitoidp.AdminCreateUserOutput{User: invitedIdentity("invitee-sub")}, nil
		},
		adminAddUserToGroupFn: func(context.Context, *cognitoidp.AdminAddUserToGroupInput) (*cognitoidp.AdminAddUserToGroupOutput, error) {
			t.Fatal("member invitations must not grant group membership")
			return nil, nil
		},
	}
	svc.SetCognitoAdminAPI(client, "pool")
	svc.SetInvitationCounter(NewWindowCounter(&fakeRateStore{}))
	dto, err := svc.AddMember(context.Background(), "member", account.AccountID, &model.AddMemberRequest{Email: " New@Example.com ", Role: model.RoleTAM})
	if err != nil {
		t.Fatalf("ordinary member invitation failed: %v", err)
	}
	data, _ := json.Marshal(dto)
	var receipt map[string]any
	_ = json.Unmarshal(data, &receipt)
	if receipt["pending"] != true || receipt["invited"] != true || dto.Email != "new@example.com" {
		t.Fatalf("expected normalized pending invitation receipt, got %s", data)
	}
	if len(client.createUserCalls) != 1 || len(repo.pendingShares) != 1 ||
		repo.pendingShares[0].InvitedCognitoSub != "invitee-sub" || repo.pendingShares[0].InvitedByUserID != "member" {
		t.Fatalf("identity-bound grant missing: creates=%d pending=%+v", len(client.createUserCalls), repo.pendingShares)
	}
}

func TestAddMember_InvalidEmailDoesNotInvite(t *testing.T) {
	for _, email := range []string{"", "not-an-email", "Name <new@example.com>", strings.Repeat("a", 255) + "@example.com"} {
		t.Run(email, func(t *testing.T) {
			repo := newMockAccountRepo()
			svc := newAccountServiceWithRepo(repo)
			account, _ := svc.CreateAccount(context.Background(), "owner", "owner@example.com", &model.CreateAccountRequest{Name: "Customer"})
			client := &fakeCognitoAdminAPI{
				adminGetUserFn: func(context.Context, *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
					t.Fatal("invalid email reached Cognito")
					return nil, nil
				},
			}
			svc.SetCognitoAdminAPI(client, "pool")
			_, err := svc.AddMember(context.Background(), "owner", account.AccountID, &model.AddMemberRequest{Email: email, Role: model.RoleSSA})
			if err == nil {
				t.Fatal("invalid email was accepted")
			}
			if len(client.createUserCalls) != 0 || len(repo.pendingShares) != 0 {
				t.Fatal("invalid email caused side effects")
			}
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
			repo := newMockAccountRepo()
			svc := newAccountServiceWithRepo(repo)
			account, _ := svc.CreateAccount(context.Background(), "owner", "owner@example.com", &model.CreateAccountRequest{Name: "Customer"})
			client := &fakeCognitoAdminAPI{
				adminGetUserFn: func(context.Context, *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
					t.Fatal("unauthorized request reached Cognito")
					return nil, nil
				},
			}
			svc.SetCognitoAdminAPI(client, "pool")
			_, err := svc.AddMember(context.Background(), test.requester, account.AccountID, &model.AddMemberRequest{Email: "new@example.com", Role: test.role})
			if !errors.Is(err, test.want) || len(client.createUserCalls) != 0 {
				t.Fatalf("error=%v creates=%d", err, len(client.createUserCalls))
			}
		})
	}
}

func TestAddMember_ConcurrentIdentityCreationReusesWinner(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	account, _ := svc.CreateAccount(context.Background(), "owner", "owner@example.com", &model.CreateAccountRequest{Name: "Customer"})
	reads := 0
	client := &fakeCognitoAdminAPI{
		adminGetUserFn: func(context.Context, *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
			reads++
			if reads == 1 {
				return nil, &cognitotypes.UserNotFoundException{}
			}
			return &cognitoidp.AdminGetUserOutput{UserAttributes: invitedIdentity("winner-sub").Attributes}, nil
		},
		adminCreateUserFn: func(context.Context, *cognitoidp.AdminCreateUserInput) (*cognitoidp.AdminCreateUserOutput, error) {
			return nil, &cognitotypes.UsernameExistsException{}
		},
	}
	svc.SetCognitoAdminAPI(client, "pool")
	svc.SetInvitationCounter(NewWindowCounter(&fakeRateStore{}))
	dto, err := svc.AddMember(context.Background(), "owner", account.AccountID, &model.AddMemberRequest{Email: "new@example.com", Role: model.RoleSSA})
	if err != nil || !dto.Pending || dto.Invited || repo.pendingShares[0].InvitedCognitoSub != "winner-sub" {
		t.Fatalf("receipt=%+v error=%v pending=%+v", dto, err, repo.pendingShares)
	}
	if len(client.createUserCalls) != 1 || client.createUserCalls[0].MessageAction != "" {
		t.Fatal("concurrent creation must not send another email")
	}
}

type failingAccountQueue struct{ *mockAccountRepo }

func (r *failingAccountQueue) PutPendingShare(context.Context, *model.PendingShare) error {
	return errors.New("queue unavailable")
}

func TestAddMember_InvitationQueueFailureIsPartialSuccess(t *testing.T) {
	repo := &failingAccountQueue{newMockAccountRepo()}
	svc := newAccountServiceWithRepo(repo)
	account, _ := svc.CreateAccount(context.Background(), "owner", "owner@example.com", &model.CreateAccountRequest{Name: "Customer"})
	svc.SetCognitoAdminAPI(&fakeCognitoAdminAPI{
		adminGetUserFn: func(context.Context, *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
			return nil, &cognitotypes.UserNotFoundException{}
		},
		adminCreateUserFn: func(context.Context, *cognitoidp.AdminCreateUserInput) (*cognitoidp.AdminCreateUserOutput, error) {
			return &cognitoidp.AdminCreateUserOutput{User: invitedIdentity("invitee")}, nil
		},
	}, "pool")
	svc.SetInvitationCounter(NewWindowCounter(&fakeRateStore{}))
	_, err := svc.AddMember(context.Background(), "owner", account.AccountID, &model.AddMemberRequest{Email: "new@example.com", Role: model.RoleSSA})
	if !errors.Is(err, ErrInvitationGrantFailed) {
		t.Fatalf("sent invitation was not reported as partial success: %v", err)
	}
}

func TestAddMember_InvitationCounterStopsBeforeMail(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprintf("unavailable=%v", unavailable), func(t *testing.T) {
			repo := newMockAccountRepo()
			svc := newAccountServiceWithRepo(repo)
			account, _ := svc.CreateAccount(context.Background(), "owner", "owner@example.com", &model.CreateAccountRequest{Name: "Customer"})
			client := &fakeCognitoAdminAPI{
				adminGetUserFn: func(context.Context, *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
					return nil, &cognitotypes.UserNotFoundException{}
				},
			}
			svc.SetCognitoAdminAPI(client, "pool")
			store := &fakeRateStore{}
			counter := NewWindowCounter(store)
			if unavailable {
				store.err = errors.New("storage unavailable")
			} else {
				_, _, key, _ := rateWindow(counter.now(), time.Hour)
				store.counts = map[string]int64{"owner|RATE#account-invite#" + key: 20}
			}
			svc.SetInvitationCounter(counter)
			_, err := svc.AddMember(context.Background(), "owner", account.AccountID, &model.AddMemberRequest{Email: "new@example.com", Role: model.RoleSSA})
			want := ErrInvitationRateLimited
			if unavailable {
				want = ErrInvitationUnavailable
			}
			if !errors.Is(err, want) || len(client.createUserCalls) != 0 {
				t.Fatalf("error=%v creates=%d", err, len(client.createUserCalls))
			}
		})
	}
}

func TestAddMember_ExplicitResendUsesPendingIdentity(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	account, _ := svc.CreateAccount(context.Background(), "owner", "owner@example.com", &model.CreateAccountRequest{Name: "Customer"})
	client := &fakeCognitoAdminAPI{
		adminGetUserFn: func(context.Context, *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
			return &cognitoidp.AdminGetUserOutput{
				Username: aws.String("immutable-username"),
				UserStatus: cognitotypes.UserStatusTypeForceChangePassword,
				UserAttributes: append(invitedIdentity("invitee").Attributes,
					cognitotypes.AttributeType{Name: aws.String("email"), Value: aws.String("new@example.com")}),
			}, nil
		},
		adminCreateUserFn: func(_ context.Context, in *cognitoidp.AdminCreateUserInput) (*cognitoidp.AdminCreateUserOutput, error) {
			if in.MessageAction != cognitotypes.MessageActionTypeResend {
				t.Fatal("explicit resend must use Cognito RESEND")
			}
			if aws.ToString(in.Username) != "immutable-username" {
				t.Fatal("resend must use the current immutable username")
			}
			user := invitedIdentity("invitee")
			user.Attributes = append(user.Attributes, cognitotypes.AttributeType{Name: aws.String("email"), Value: aws.String("new@example.com")})
			return &cognitoidp.AdminCreateUserOutput{User: user}, nil
		},
	}
	svc.SetCognitoAdminAPI(client, "pool")
	svc.SetInvitationCounter(NewWindowCounter(&fakeRateStore{}))
	var request model.AddMemberRequest
	_ = json.Unmarshal([]byte(`{"email":"new@example.com","role":"SSA","resendInvitation":true}`), &request)
	dto, err := svc.AddMember(context.Background(), "owner", account.AccountID, &request)
	if err != nil || !dto.Pending || !dto.Invited || len(client.createUserCalls) != 1 {
		t.Fatalf("resend receipt=%+v error=%v calls=%d", dto, err, len(client.createUserCalls))
	}
}

func TestAddMember_ResendIdentityChangedDuringDelivery(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	account, _ := svc.CreateAccount(context.Background(), "owner", "owner@example.com", &model.CreateAccountRequest{Name: "Customer"})
	client := &fakeCognitoAdminAPI{
		adminGetUserFn: func(context.Context, *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
			return &cognitoidp.AdminGetUserOutput{
				Username: aws.String("immutable-username"), UserStatus: cognitotypes.UserStatusTypeForceChangePassword,
				UserAttributes: append(invitedIdentity("original-sub").Attributes,
					cognitotypes.AttributeType{Name: aws.String("email"), Value: aws.String("new@example.com")}),
			}, nil
		},
		adminCreateUserFn: func(context.Context, *cognitoidp.AdminCreateUserInput) (*cognitoidp.AdminCreateUserOutput, error) {
			return &cognitoidp.AdminCreateUserOutput{User: invitedIdentity("replacement-sub")}, nil
		},
	}
	svc.SetCognitoAdminAPI(client, "pool")
	svc.SetInvitationCounter(NewWindowCounter(&fakeRateStore{}))
	_, err := svc.AddMember(context.Background(), "owner", account.AccountID, &model.AddMemberRequest{Email: "new@example.com", Role: model.RoleSSA, ResendInvitation: true})
	if !errors.Is(err, ErrInvitationGrantFailed) || len(repo.pendingShares) != 0 {
		t.Fatalf("changed identity queued: error=%v grants=%+v", err, repo.pendingShares)
	}
}

func TestAddMember_DomainAllowlistRunsBeforeMail(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	account, _ := svc.CreateAccount(context.Background(), "owner", "owner@example.com", &model.CreateAccountRequest{Name: "Customer"})
	client := &fakeCognitoAdminAPI{
		adminGetUserFn: func(context.Context, *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
			return nil, &cognitotypes.UserNotFoundException{}
		},
	}
	svc.SetCognitoAdminAPI(client, "pool")
	store := &fakeRateStore{}
	svc.SetInvitationCounter(NewWindowCounter(store))
	_, err := svc.AddMember(context.Background(), "owner", account.AccountID, &model.AddMemberRequest{Email: "new@unapproved.invalid", Role: model.RoleSSA})
	if !errors.Is(err, ErrInvitationRejected) || len(client.createUserCalls) != 0 || store.calls != 0 {
		t.Fatalf("unapproved domain caused mail: error=%v mail=%d counter=%d", err, len(client.createUserCalls), store.calls)
	}
}

func TestAddMember_ConfirmedIdentityCannotBeReset(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	account, _ := svc.CreateAccount(context.Background(), "owner", "owner@example.com", &model.CreateAccountRequest{Name: "Customer"})
	client := &fakeCognitoAdminAPI{
		adminGetUserFn: func(context.Context, *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
			return &cognitoidp.AdminGetUserOutput{UserStatus: cognitotypes.UserStatusTypeConfirmed, UserAttributes: invitedIdentity("invitee").Attributes}, nil
		},
	}
	svc.SetCognitoAdminAPI(client, "pool")
	svc.SetInvitationCounter(NewWindowCounter(&fakeRateStore{}))
	_, err := svc.AddMember(context.Background(), "owner", account.AccountID, &model.AddMemberRequest{Email: "new@example.com", Role: model.RoleSSA, ResendInvitation: true})
	if !errors.Is(err, ErrInvitationNotPending) || len(client.createUserCalls) != 0 || len(repo.pendingShares) != 0 {
		t.Fatalf("confirmed identity affected: error=%v mail=%d grants=%d", err, len(client.createUserCalls), len(repo.pendingShares))
	}
}
