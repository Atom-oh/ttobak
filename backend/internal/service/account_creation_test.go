package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	idp "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func TestCreateAccount_DesignatedOwnerCreatorCheckbox(t *testing.T) {
	for _, keep := range []bool{false, true} {
		t.Run(map[bool]string{false: "invite-only", true: "creator-AM"}[keep], func(t *testing.T) {
			f, _ := newCreationFixture(t)
			raw, _ := json.Marshal(map[string]any{
				"name": "Prepared customer", "ownerEmail": "new@example.com",
				"keepCreatorAsMember": keep,
				"requestId":           "683c4994-ea73-40b9-85d7-14ca659fbc21",
			})
			var req model.CreateAccountRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Fatal(err)
			}
			account, err := f.svc.CreateAccountWithOwner(context.Background(), "creator", "creator@example.com", &req)
			if err != nil {
				t.Fatal(err)
			}
			if account.OwnerUserID != "invitee" {
				t.Fatalf("owner=%q, want invited colleague; creator must not become Owner", account.OwnerUserID)
			}
			owner, _ := f.repo.GetMember(context.Background(), account.AccountID, "invitee")
			if owner == nil || owner.Role != model.RoleOwner {
				t.Fatalf("missing canonical Owner: %+v", owner)
			}
			creator, _ := f.repo.GetMember(context.Background(), account.AccountID, "creator")
			if keep && (creator == nil || creator.Role != model.RoleAM) {
				t.Fatalf("checked must retain creator as AM: %+v", creator)
			}
			if !keep && creator != nil {
				t.Fatalf("unchecked must grant no creator membership: %+v", creator)
			}
			if len(f.repo.pendingShares) != 0 {
				t.Fatal("initial Owner must not be queued as an expiring pending member")
			}
		})
	}
}

type creationTestRepo struct {
	*mockAccountRepo
	before         func(*model.Account, string, []repository.AccountParentLink) error
	after          error
	writes         int
	accountReads   int
	accountReadErr error
	memberReadErr  error
}

func (r *creationTestRepo) GetAccount(ctx context.Context, accountID string) (*model.Account, error) {
	r.accountReads++
	if r.accountReads > 1 && r.accountReadErr != nil {
		return nil, r.accountReadErr
	}
	return r.mockAccountRepo.GetAccount(ctx, accountID)
}

func (r *creationTestRepo) GetMember(ctx context.Context, accountID, userID string) (*model.AccountMember, error) {
	if r.memberReadErr != nil {
		return nil, r.memberReadErr
	}
	return r.mockAccountRepo.GetMember(ctx, accountID, userID)
}

func (r *creationTestRepo) CreateAccountWithInitialMembers(ctx context.Context, a *model.Account, owner, creator *model.AccountMember, requester string, ancestors []repository.AccountParentLink) error {
	if r.before != nil {
		if err := r.before(a, requester, ancestors); err != nil {
			return err
		}
	}
	if r.accounts[a.AccountID] != nil {
		return repository.ErrConditionFailed
	}
	r.writes++
	if err := r.CreateAccount(ctx, a, owner); err != nil {
		return err
	}
	if creator != nil {
		copy := *creator
		r.members[memberKey(a.AccountID, creator.UserID)] = &copy
	}
	return r.after
}

func newCreationFixture(t *testing.T) (*inviteFixture, *creationTestRepo) {
	f := newInviteFixture(t)
	r := &creationTestRepo{mockAccountRepo: f.repo}
	f.svc.repo = r
	return f, r
}

func creationRequest() *model.CreateAccountRequest {
	return &model.CreateAccountRequest{Name: "Customer", OwnerEmail: "new@example.com", RequestID: "683c4994-ea73-40b9-85d7-14ca659fbc21"}
}

func TestCreateAccount_PostInvitationReadFailuresPreservePartialDelivery(t *testing.T) {
	for _, phase := range []string{"transaction-confirmation", "receipt-membership", "self-receipt"} {
		t.Run(phase, func(t *testing.T) {
			f, r := newCreationFixture(t)
			readErr := errors.New("confirmation unavailable")
			req := creationRequest()
			switch phase {
			case "transaction-confirmation":
				r.after = errors.New("transaction response lost")
				r.accountReadErr = readErr
			case "receipt-membership":
				r.memberReadErr = readErr
			case "self-receipt":
				req.OwnerEmail = "creator@example.com"
				r.memberReadErr = readErr
			}
			_, err := f.svc.CreateAccountWithOwner(context.Background(), "creator", "creator@example.com", req)
			if !errors.Is(err, readErr) {
				t.Fatalf("lost confirmation cause: %v", err)
			}
			invited := phase != "self-receipt"
			if errors.Is(err, ErrInvitationGrantFailed) != invited ||
				(len(f.client.createUserCalls) == 1) != invited {
				t.Fatalf("partial-delivery warning disagrees with mail: err=%v mail=%d", err, len(f.client.createUserCalls))
			}
		})
	}
}

func TestCreateAccount_ParentAuthorizationPrecedesMail(t *testing.T) {
	f, _ := newCreationFixture(t)
	f.repo.accounts["parent"] = &model.Account{AccountID: "parent"}
	req := creationRequest()
	req.ParentAccountID = "parent"
	_, err := f.svc.CreateAccountWithOwner(context.Background(), "creator", "creator@example.com", req)
	f.requireNoInvite(t, err, ErrForbidden)
}

func TestCreateAccount_ParentTransactionUsesCreator(t *testing.T) {
	f, r := newCreationFixture(t)
	f.repo.accounts["parent"] = &model.Account{AccountID: "parent"}
	f.repo.members[memberKey("parent", "creator")] = &model.AccountMember{Role: model.RoleAM}
	r.before = func(a *model.Account, requester string, ancestors []repository.AccountParentLink) error {
		if requester != "creator" || len(ancestors) != 1 || ancestors[0].AccountID != "parent" {
			t.Fatalf("parent authorization used wrong identity/path: %s %+v", requester, ancestors)
		}
		delete(f.repo.members, memberKey("parent", "creator"))
		return repository.ErrConditionFailed
	}
	req := creationRequest()
	req.ParentAccountID = "parent"
	_, err := f.svc.CreateAccountWithOwner(context.Background(), "creator", "creator@example.com", req)
	if !errors.Is(err, ErrForbidden) || r.writes != 0 {
		t.Fatalf("lost parent access: %v, writes=%d", err, r.writes)
	}
}

func TestCreateAccount_RetryReturnsReceiptWithoutRegrant(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		t.Run(map[bool]string{false: "replay", true: "ambiguous-commit"}[ambiguous], func(t *testing.T) {
			f, r := newCreationFixture(t)
			if ambiguous {
				r.after = errors.New("response lost after commit")
			}
			req := creationRequest()
			req.KeepCreatorAsMember = true
			first, err := f.svc.CreateAccountWithOwner(context.Background(), "creator", "creator@example.com", req)
			if err != nil {
				t.Fatal(err)
			}
			delete(r.members, memberKey(first.AccountID, "creator"))
			r.accounts[first.AccountID].Name = "Later private name"
			retry, err := f.svc.CreateAccountWithOwner(context.Background(), "creator", "creator@example.com", req)
			if err != nil {
				t.Fatal(err)
			}
			if retry.CreatorMember || !retry.OwnerInvitationPending || retry.Name != "Customer" || r.writes != 1 || len(f.client.createUserCalls) != 1 {
				t.Fatalf("retry leaked/restored/recreated: %+v writes=%d mails=%d", retry, r.writes, len(f.client.createUserCalls))
			}
			req.Name = "Different customer"
			if _, err := f.svc.CreateAccountWithOwner(context.Background(), "creator", "creator@example.com", req); !errors.Is(err, ErrAccountHierarchyConflict) {
				t.Fatalf("changed retry input must conflict: %v", err)
			}
		})
	}
}

func TestCreateAccount_StaleProfileCannotReceiveOwner(t *testing.T) {
	f, r := newCreationFixture(t)
	f.client.adminGetUserFn = lookupInvitedIdentity("current-sub", types.UserStatusTypeConfirmed)
	f.repo.users["new@example.com"] = &model.User{UserID: "old-sub", Email: "new@example.com"}
	_, err := f.svc.CreateAccountWithOwner(context.Background(), "creator", "creator@example.com", creationRequest())
	if !errors.Is(err, ErrInvitationUnavailable) || r.writes != 0 {
		t.Fatalf("stale identity accepted: %v", err)
	}
}

func TestCreateAccount_SelfOwnerAndInvalidInputDoNotSendMail(t *testing.T) {
	f, _ := newCreationFixture(t)
	req := creationRequest()
	req.OwnerEmail = "CREATOR@example.com"
	req.KeepCreatorAsMember = true
	a, err := f.svc.CreateAccountWithOwner(context.Background(), "creator", "creator@example.com", req)
	if err != nil || a.OwnerUserID != "creator" || len(a.Members) != 1 || !a.CreatorMember {
		t.Fatalf("self owner: %+v %v", a, err)
	}
	for _, invalid := range []model.CreateAccountRequest{
		{Name: "Customer", OwnerEmail: "bad", RequestID: "not-a-uuid"},
		{Name: "Customer", ParentAccountID: "../parent", OwnerEmail: "new@example.com"},
	} {
		if _, err := f.svc.CreateAccountWithOwner(context.Background(), "creator", "creator@example.com", &invalid); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	if len(f.client.createUserCalls) != 0 {
		t.Fatal("self/invalid input sent mail")
	}
}

func TestCreateAccount_InvitationDependenciesFailClosed(t *testing.T) {
	for _, kind := range []string{"domain", "quota", "counter-unavailable", "foreign-retry"} {
		t.Run(kind, func(t *testing.T) {
			f, r := newCreationFixture(t)
			req := creationRequest()
			want := ErrInvitationRejected
			switch kind {
			case "domain":
				req.OwnerEmail = "new@unapproved.example"
			case "quota":
				_, _, key, _ := rateWindow(f.svc.invitationCounter.now(), time.Hour)
				f.counter.counts = map[string]int64{"creator|RATE#account-invite#" + key: 20}
				want = ErrInvitationRateLimited
			case "counter-unavailable":
				f.counter.err = errors.New("storage unavailable")
				want = ErrInvitationUnavailable
			case "foreign-retry":
				r.accounts[req.RequestID] = &model.Account{AccountID: req.RequestID, CreatedByUserID: "another"}
				want = ErrAccountHierarchyConflict
			}
			_, err := f.svc.CreateAccountWithOwner(context.Background(), "creator", "creator@example.com", req)
			f.requireNoInvite(t, err, want)
			if r.writes != 0 {
				t.Fatal("failed gate still created Account")
			}
		})
	}
}

func TestCreateAccount_DetailRetainsInitialOwnerInvitationHint(t *testing.T) {
	f, _ := newCreationFixture(t)
	req := creationRequest()
	req.KeepCreatorAsMember = true
	a, err := f.svc.CreateAccountWithOwner(context.Background(), "creator", "creator@example.com", req)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := f.svc.GetAccount(context.Background(), "creator", a.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	bytes, _ := json.Marshal(detail)
	var response map[string]any
	if err := json.Unmarshal(bytes, &response); err != nil {
		t.Fatal(err)
	}
	if response["ownerInvitationPending"] != true {
		t.Fatalf("member cannot recover initial Owner invite: %s", bytes)
	}
}

func TestResendOwnerInvitation_OnlyCanonicalOwnerAndAuthorizedCaller(t *testing.T) {
	for _, kind := range []string{"creator", "member", "foreign", "expired", "confirmed", "recreated"} {
		t.Run(kind, func(t *testing.T) {
			f, _ := newCreationFixture(t)
			a, err := f.svc.CreateAccountWithOwner(context.Background(), "creator", "creator@example.com", creationRequest())
			if err != nil {
				t.Fatal(err)
			}
			resender, ok := any(f.svc).(interface {
				ResendOwnerInvitation(context.Context, string, string) (*model.AccountMemberDTO, error)
			})
			if !ok {
				t.Fatal("owner invitation recovery is missing")
			}
			f.client.createUserCalls = nil
			caller, status, sub := "creator", types.UserStatusTypeForceChangePassword, "invitee"
			want := error(nil)
			switch kind {
			case "member":
				caller = "member"
				f.repo.members[memberKey(a.AccountID, caller)] = &model.AccountMember{UserID: caller, Role: model.RoleSSA}
			case "foreign":
				caller = "stranger"
				want = ErrForbidden
			case "expired":
				f.repo.accounts[a.AccountID].CreatedAt = time.Now().Add(-31 * 24 * time.Hour)
				want = ErrForbidden
			case "confirmed":
				status = types.UserStatusTypeConfirmed
				want = ErrInvitationNotPending
			case "recreated":
				sub = "replacement-sub"
				want = ErrInvitationUnavailable
			}
			f.client.adminGetUserFn = lookupInvitedIdentity(sub, status)
			f.client.adminCreateUserFn = func(context.Context, *idp.AdminCreateUserInput) (*idp.AdminCreateUserOutput, error) {
				return &idp.AdminCreateUserOutput{User: &types.UserType{Attributes: []types.AttributeType{
					{Name: aws.String("sub"), Value: aws.String("invitee")},
					{Name: aws.String("email"), Value: aws.String("new@example.com")},
				}}}, nil
			}
			member, err := resender.ResendOwnerInvitation(context.Background(), caller, a.AccountID)
			if want != nil {
				if !errors.Is(err, want) || len(f.client.createUserCalls) != 0 {
					t.Fatalf("unsafe resend %v calls=%d", err, len(f.client.createUserCalls))
				}
			} else if err != nil || member.UserID != "invitee" || member.Role != model.RoleOwner || !member.Invited || len(f.client.createUserCalls) != 1 {
				t.Fatalf("canonical resend: %+v %v", member, err)
			}
		})
	}
}
