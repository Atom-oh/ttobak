package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	idp "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	ct "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type transferRepo struct {
	*mockAccountRepo
	calls int
	err   error
}

func (r *transferRepo) TransferAccountOwner(_ context.Context, id, from, to string, _ time.Time) error {
	r.calls++
	if r.err != nil {
		return r.err
	}
	r.accounts[id].OwnerUserID = to
	r.accounts[id].InitialOwnerEmail = ""
	r.members[memberKey(id, to)].Role = model.RoleOwner
	r.members[memberKey(id, from)].Role = model.RoleSA
	return nil
}

func confirmedUser(status ct.UserStatusType, enabled bool) func(context.Context, *idp.AdminGetUserInput) (*idp.AdminGetUserOutput, error) {
	return cognitoTarget(status, enabled, "true")
}

// cognitoTarget returns the "target" user; verified "" omits email_verified.
func cognitoTarget(status ct.UserStatusType, enabled bool, verified string) func(context.Context, *idp.AdminGetUserInput) (*idp.AdminGetUserOutput, error) {
	return func(_ context.Context, in *idp.AdminGetUserInput) (*idp.AdminGetUserOutput, error) {
		if aws.ToString(in.UserPoolId) != "pool" || aws.ToString(in.Username) != "target" {
			return nil, fmt.Errorf("unexpected lookup %v", in)
		}
		attrs := []ct.AttributeType{{Name: aws.String("sub"), Value: aws.String("target")}}
		if verified != "" {
			attrs = append(attrs, ct.AttributeType{Name: aws.String("email_verified"), Value: aws.String(verified)})
		}
		return &idp.AdminGetUserOutput{Enabled: enabled, UserStatus: status, UserAttributes: attrs}, nil
	}
}

func newTransferFixture() (*AccountService, *transferRepo, *fakeCognitoAdminAPI) {
	r := &transferRepo{mockAccountRepo: newMockAccountRepo()}
	r.accounts["acc"] = &model.Account{AccountID: "acc", OwnerUserID: "owner", InitialOwnerEmail: "owner@example.com"}
	r.members[memberKey("acc", "owner")] = &model.AccountMember{AccountID: "acc", UserID: "owner", Email: "owner@example.com", Role: model.RoleOwner}
	r.members[memberKey("acc", "target")] = &model.AccountMember{AccountID: "acc", UserID: "target", Email: "t@example.com", Role: model.RoleSA}
	r.members[memberKey("acc", "am")] = &model.AccountMember{AccountID: "acc", UserID: "am", Role: model.RoleAM}
	c := &fakeCognitoAdminAPI{adminGetUserFn: confirmedUser(ct.UserStatusTypeConfirmed, true)}
	s := newAccountServiceWithRepo(r)
	s.SetCognitoAdminAPI(c, "pool")
	return s, r, c
}

func TestTransferOwner(t *testing.T) {
	for _, tc := range []struct {
		name, requester, account, target string
		setup                            func(*AccountService, *transferRepo, *fakeCognitoAdminAPI)
		want                             error
		calls                            int
	}{
		{name: "meta owner differs", requester: "owner", account: "acc", target: "target", want: ErrForbidden,
			setup: func(_ *AccountService, r *transferRepo, _ *fakeCognitoAdminAPI) {
				r.accounts["acc"].OwnerUserID = "other"
			}},
		{name: "requester is AM", requester: "am", account: "acc", target: "target", want: ErrForbidden},
		{name: "target is requester", requester: "owner", account: "acc", target: "owner", want: ErrInvalidInput},
		{name: "target not a member", requester: "owner", account: "acc", target: "stranger", want: ErrNotFound},
		{name: "missing account", requester: "owner", account: "missing", target: "target", want: ErrNotFound},
		{name: "empty target", requester: "owner", account: "acc", target: "", want: ErrInvalidInput},
		{name: "invalid account id", requester: "owner", account: "a/b", target: "target", want: ErrInvalidInput},
		{name: "invalid target id", requester: "owner", account: "acc", target: "t#1", want: ErrInvalidInput},
		{name: "cognito disabled", requester: "owner", account: "acc", target: "target", want: ErrInvalidInput,
			setup: func(_ *AccountService, _ *transferRepo, c *fakeCognitoAdminAPI) {
				c.adminGetUserFn = confirmedUser(ct.UserStatusTypeConfirmed, false)
			}},
		{name: "cognito not confirmed", requester: "owner", account: "acc", target: "target", want: ErrInvalidInput,
			setup: func(_ *AccountService, _ *transferRepo, c *fakeCognitoAdminAPI) {
				c.adminGetUserFn = confirmedUser(ct.UserStatusTypeForceChangePassword, true)
			}},
		{name: "email not verified", requester: "owner", account: "acc", target: "target", want: ErrInvalidInput,
			setup: func(_ *AccountService, _ *transferRepo, c *fakeCognitoAdminAPI) {
				c.adminGetUserFn = cognitoTarget(ct.UserStatusTypeConfirmed, true, "false")
			}},
		{name: "email_verified absent", requester: "owner", account: "acc", target: "target", want: ErrInvalidInput,
			setup: func(_ *AccountService, _ *transferRepo, c *fakeCognitoAdminAPI) {
				c.adminGetUserFn = cognitoTarget(ct.UserStatusTypeConfirmed, true, "")
			}},
		{name: "cognito sub mismatch", requester: "owner", account: "acc", target: "target", want: ErrInvalidInput,
			setup: func(_ *AccountService, _ *transferRepo, c *fakeCognitoAdminAPI) {
				c.adminGetUserFn = func(context.Context, *idp.AdminGetUserInput) (*idp.AdminGetUserOutput, error) {
					return &idp.AdminGetUserOutput{Enabled: true, UserStatus: ct.UserStatusTypeConfirmed, UserAttributes: []ct.AttributeType{
						{Name: aws.String("sub"), Value: aws.String("someone-else")},
					}}, nil
				}
			}},
		{name: "cognito sub missing", requester: "owner", account: "acc", target: "target", want: ErrInvalidInput,
			setup: func(_ *AccountService, _ *transferRepo, c *fakeCognitoAdminAPI) {
				c.adminGetUserFn = func(context.Context, *idp.AdminGetUserInput) (*idp.AdminGetUserOutput, error) {
					return &idp.AdminGetUserOutput{Enabled: true, UserStatus: ct.UserStatusTypeConfirmed}, nil
				}
			}},
		{name: "meta owner but member role not owner", requester: "owner", account: "acc", target: "target", want: ErrForbidden,
			setup: func(_ *AccountService, r *transferRepo, c *fakeCognitoAdminAPI) {
				r.members[memberKey("acc", "owner")].Role = model.RoleSA
				c.adminGetUserFn = func(context.Context, *idp.AdminGetUserInput) (*idp.AdminGetUserOutput, error) {
					return nil, errors.New("cognito must not be called")
				}
			}},
		{name: "cognito user not found", requester: "owner", account: "acc", target: "target", want: ErrInvalidInput,
			setup: func(_ *AccountService, _ *transferRepo, c *fakeCognitoAdminAPI) {
				c.adminGetUserFn = func(context.Context, *idp.AdminGetUserInput) (*idp.AdminGetUserOutput, error) {
					return nil, &ct.UserNotFoundException{}
				}
			}},
		{name: "cognito lookup error", requester: "owner", account: "acc", target: "target", want: ErrOwnerTransferUnavailable,
			setup: func(_ *AccountService, _ *transferRepo, c *fakeCognitoAdminAPI) {
				c.adminGetUserFn = func(context.Context, *idp.AdminGetUserInput) (*idp.AdminGetUserOutput, error) {
					return nil, errors.New("throttled")
				}
			}},
		{name: "cognito unwired", requester: "owner", account: "acc", target: "target", want: ErrOwnerTransferUnavailable,
			setup: func(s *AccountService, _ *transferRepo, _ *fakeCognitoAdminAPI) { s.SetCognitoAdminAPI(nil, "") }},
		{name: "repo condition failed", requester: "owner", account: "acc", target: "target", want: ErrOwnerTransferConflict, calls: 1,
			setup: func(_ *AccountService, r *transferRepo, _ *fakeCognitoAdminAPI) {
				r.err = fmt.Errorf("transaction: %w", repository.ErrConditionFailed)
			}},
		{name: "success", requester: "owner", account: "acc", target: "target", calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r, c := newTransferFixture()
			if tc.setup != nil {
				tc.setup(s, r, c)
			}
			got, err := s.TransferOwner(context.Background(), tc.requester, tc.account, &model.TransferOwnerRequest{TargetUserID: tc.target})
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if r.calls != tc.calls {
				t.Fatalf("repo calls = %d, want %d", r.calls, tc.calls)
			}
			if tc.want == nil {
				want := model.TransferOwnerResponse{AccountID: "acc", OwnerUserID: "target", PreviousOwnerUserID: "owner", PreviousOwnerRole: model.RoleSA}
				if got == nil || *got != want {
					t.Fatalf("response = %+v, want %+v", got, want)
				}
			} else if got != nil {
				t.Fatalf("unexpected response %+v", got)
			}
		})
	}
	if _, err := newTransferFixtureService().TransferOwner(context.Background(), "owner", "acc", nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil request = %v", err)
	}
}

func newTransferFixtureService() *AccountService { s, _, _ := newTransferFixture(); return s }

func TestTransferOwnerRepoWithoutCapabilityIsUnavailable(t *testing.T) {
	r := newMockAccountRepo()
	s := newAccountServiceWithRepo(r)
	if _, err := s.TransferOwner(context.Background(), "owner", "acc", &model.TransferOwnerRequest{TargetUserID: "target"}); !errors.Is(err, ErrOwnerTransferUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestTransferOwnerThenGenericRoutesStillOwnerFree(t *testing.T) {
	s, _, _ := newTransferFixture()
	ctx := context.Background()
	if _, err := s.TransferOwner(ctx, "owner", "acc", &model.TransferOwnerRequest{TargetUserID: "target"}); err != nil {
		t.Fatal(err)
	}
	// The new Owner is the requester from here on; the previous Owner is SA.
	if _, err := s.UpdateMemberRole(ctx, "target", "acc", "owner", &model.UpdateMemberRequest{Role: model.RoleOwner}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("assign owner role = %v", err)
	}
	if _, err := s.UpdateMemberRole(ctx, "owner", "acc", "target", &model.UpdateMemberRequest{Role: model.RoleSA}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("change new owner role = %v", err)
	}
	if _, err := s.ResendOwnerInvitation(ctx, "target", "acc"); !errors.Is(err, ErrInvitationUnavailable) {
		t.Fatalf("resend after transfer = %v", err)
	}
}

// racingMemberRepo commits an owner transfer to the target after the service
// read the target's non-owner role but before its member write lands.
type racingMemberRepo struct {
	*transferRepo
	to string // removal races a transfer to this member; "" means the target
}

func (r *racingMemberRepo) race(accountID, userID string) {
	if err := r.TransferAccountOwner(context.Background(), accountID, "owner", userID, time.Now()); err != nil {
		panic(err)
	}
}

func (r *racingMemberRepo) raceTarget(userID string) string {
	if r.to != "" {
		return r.to
	}
	return userID
}

func (r *racingMemberRepo) UpdateMemberRole(ctx context.Context, accountID, userID, role string) error {
	r.race(accountID, userID)
	return r.transferRepo.UpdateMemberRole(ctx, accountID, userID, role)
}

func (r *racingMemberRepo) DeleteMember(ctx context.Context, accountID, ownerID, userID string) error {
	r.race(accountID, r.raceTarget(userID))
	return r.transferRepo.DeleteMember(ctx, accountID, ownerID, userID)
}

func TestMemberWritersInterleavedWithTransferKeepNewOwner(t *testing.T) {
	for _, op := range []string{"update", "remove"} {
		t.Run(op, func(t *testing.T) {
			_, base, _ := newTransferFixture()
			r := &racingMemberRepo{transferRepo: base}
			s := newAccountServiceWithRepo(r)
			var err error
			if op == "update" {
				_, err = s.UpdateMemberRole(context.Background(), "am", "acc", "target", &model.UpdateMemberRequest{Role: model.RoleAM})
			} else {
				_, err = s.RemoveMember(context.Background(), "owner", "acc", "target", false)
			}
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("err = %v, want ErrConflict", err)
			}
			if m := r.members[memberKey("acc", "target")]; m == nil || m.Role != model.RoleOwner || r.accounts["acc"].OwnerUserID != "target" {
				t.Fatalf("new Owner row overwritten: %+v", m)
			}
		})
	}
}

func TestRemoveMemberRequiresRequesterStillOwner(t *testing.T) {
	_, base, _ := newTransferFixture()
	// Ownership moves to "target" between the requester check and the delete of "am".
	r := &racingMemberRepo{transferRepo: base, to: "target"}
	s := newAccountServiceWithRepo(r)
	if _, err := s.RemoveMember(context.Background(), "owner", "acc", "am", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if r.members[memberKey("acc", "am")] == nil {
		t.Fatal("former owner removed a member after losing ownership")
	}
}

type existsOnPutRepo struct{ *mockAccountRepo }

func (r *existsOnPutRepo) PutMember(context.Context, *model.AccountMember) error {
	return repository.ErrConditionFailed
}

func TestAddMemberConcurrentCreateIsMemberExists(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "Customer"})
	seedUser(repo, "tam-1", "tam@x.com")
	svc.repo = &existsOnPutRepo{mockAccountRepo: repo}
	if _, err := svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM}); !errors.Is(err, ErrMemberExists) {
		t.Fatalf("err = %v, want ErrMemberExists", err)
	}
}
