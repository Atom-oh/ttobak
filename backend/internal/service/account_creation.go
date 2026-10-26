package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	idp "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	ct "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type accountInitialMembersRepo interface {
	CreateAccountWithInitialMembers(context.Context, *model.Account, *model.AccountMember, *model.AccountMember, string, []repository.AccountParentLink) error
}

func normalizedAccountCreation(req *model.CreateAccountRequest, requesterEmail string) (*model.CreateAccountRequest, string, error) {
	if req == nil {
		return nil, "", ErrInvalidInput
	}
	copy := *req
	copy.Name = strings.TrimSpace(req.Name)
	if copy.Name == "" || len(copy.Name) > 256 || len(copy.Industry) > 256 ||
		len(copy.Aliases) > 32 || len(copy.Domains) > 32 {
		return nil, "", ErrInvalidInput
	}
	for _, list := range [][]string{copy.Aliases, copy.Domains} {
		for _, value := range list {
			if len(value) > 256 {
				return nil, "", ErrInvalidInput
			}
		}
	}
	if copy.RequestID != "" {
		id, err := uuid.Parse(copy.RequestID)
		if err != nil || id.String() != copy.RequestID || id == uuid.Nil {
			return nil, "", ErrInvalidInput
		}
	}
	if err := validateAccountHierarchyIDs("creation", copy.ParentAccountID); err != nil {
		return nil, "", err
	}
	var err error
	if copy.OwnerEmail == "" {
		copy.OwnerEmail = requesterEmail
	}
	copy.OwnerEmail, err = normalizeAccountInviteEmail(copy.OwnerEmail)
	if err != nil {
		return nil, "", err
	}
	if copy.OwnerEmail == requesterEmail {
		copy.KeepCreatorAsMember = false
	}
	hashInput := copy
	hashInput.RequestID = ""
	bytes, err := json.Marshal(hashInput)
	if err != nil || len(bytes) > 16*1024 {
		return nil, "", ErrInvalidInput
	}
	digest := sha256.Sum256(bytes)
	return &copy, hex.EncodeToString(digest[:]), nil
}

// resolveInitialOwner checks a present identity's current email/sub rather than
// trusting a stale PROFILE email index. Missing logins use existing invite gates.
func (s *AccountService) resolveInitialOwner(ctx context.Context, requesterID, email string) (string, bool, bool, error) {
	if s.cognito == nil || s.cognitoPoolID == "" {
		return "", false, false, ErrInvitationUnavailable
	}
	budget, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	current, err := s.cognito.AdminGetUser(budget, &idp.AdminGetUserInput{
		UserPoolId: aws.String(s.cognitoPoolID), Username: aws.String(email),
	})
	var missing *ct.UserNotFoundException
	sub, pending, invited := "", false, false
	if errors.As(err, &missing) {
		sub, invited, err = s.prepareAccountInvite(budget, requesterID, email, false)
		if err != nil {
			return "", false, invited, err
		}
		if invited {
			pending = true
		} else {
			// A concurrent create won. Verify that identity before binding owner.
			current, err = s.cognito.AdminGetUser(budget, &idp.AdminGetUserInput{
				UserPoolId: aws.String(s.cognitoPoolID), Username: aws.String(email),
			})
		}
	}
	if err != nil {
		return "", false, invited, ErrInvitationUnavailable
	}
	if !invited {
		if current == nil {
			return "", false, false, ErrInvitationUnavailable
		}
		actualEmail := ""
		for _, attr := range current.UserAttributes {
			switch aws.ToString(attr.Name) {
			case "sub":
				sub = aws.ToString(attr.Value)
			case "email":
				actualEmail = strings.ToLower(strings.TrimSpace(aws.ToString(attr.Value)))
			}
		}
		if actualEmail != email {
			return "", false, false, ErrInvitationUnavailable
		}
		pending = current.UserStatus == ct.UserStatusTypeForceChangePassword
	}
	if sub == "" || len(sub) > 128 {
		return "", false, invited, ErrInvitationUnavailable
	}
	profile, err := s.repo.GetUserByEmail(budget, email)
	if err != nil || profile != nil && profile.UserID != sub {
		return "", false, invited, ErrInvitationUnavailable
	}
	return sub, pending, invited, nil
}

func initialAccountMember(accountID, userID, email, role string, at time.Time) *model.AccountMember {
	return &model.AccountMember{
		PK: model.PrefixAccount + accountID, SK: model.PrefixMember + userID,
		AccountID: accountID, UserID: userID, Email: email, Role: role, AddedAt: at,
		GSI1PK: model.PrefixUser + userID, GSI1SK: model.PrefixAccount + accountID,
		EntityType: model.EntityTypeAccountMember,
	}
}

// creationReceipt exposes original normalized input only. Retry capability must
// not become a read grant after the creator opts out or loses membership.
func (s *AccountService) creationReceipt(ctx context.Context, requesterID string, req *model.CreateAccountRequest, account *model.Account, pending, invited bool) (*model.AccountCreationResponse, error) {
	member, err := s.repo.GetMember(ctx, account.AccountID, requesterID)
	if err != nil {
		if invited {
			return nil, errors.Join(ErrInvitationGrantFailed, err)
		}
		return nil, err
	}
	members := []model.AccountMemberDTO{{UserID: account.OwnerUserID, Email: account.InitialOwnerEmail, Role: model.RoleOwner}}
	if member != nil && requesterID != account.OwnerUserID {
		members = append(members, model.AccountMemberDTO{UserID: requesterID, Email: member.Email, Role: member.Role})
	}
	return &model.AccountCreationResponse{
		AccountResponse: model.AccountResponse{
			AccountID: account.AccountID, ParentAccountID: req.ParentAccountID, Name: req.Name,
			Aliases: req.Aliases, Domains: req.Domains, Industry: req.Industry,
			OwnerUserID: account.OwnerUserID, Members: members, CreatedAt: account.CreatedAt,
			OwnerInvitationPending: pending,
		},
		CreatorMember: member != nil, OwnerInvitationRequested: invited,
	}, nil
}

func (s *AccountService) CreateAccountWithOwner(ctx context.Context, requesterID, requesterEmail string, raw *model.CreateAccountRequest) (*model.AccountCreationResponse, error) {
	requesterEmail, err := normalizeAccountInviteEmail(requesterEmail)
	if err != nil || requesterID == "" {
		return nil, ErrInvalidInput
	}
	req, fingerprint, err := normalizedAccountCreation(raw, requesterEmail)
	if err != nil {
		return nil, err
	}
	id := req.RequestID
	if id == "" {
		id = uuid.NewString()
	}
	existing, err := s.repo.GetAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.CreatedByUserID != requesterID || existing.CreationRequestHash != fingerprint {
			return nil, ErrAccountHierarchyConflict
		}
		// No Cognito calls or mail during ordinary receipt replay.
		return s.creationReceipt(ctx, requesterID, req, existing, existing.InitialOwnerInvitationPending, false)
	}
	repo, ok := s.repo.(accountInitialMembersRepo)
	if !ok {
		return nil, ErrInvitationUnavailable
	}
	ancestors, err := s.observeAccountAncestors(ctx, requesterID, id, req.ParentAccountID)
	if err != nil {
		return nil, err
	}
	ownerID, pending, invited := requesterID, false, false
	if req.OwnerEmail != requesterEmail {
		ownerID, pending, invited, err = s.resolveInitialOwner(ctx, requesterID, req.OwnerEmail)
		if err != nil {
			if invited {
				return nil, errors.Join(ErrInvitationGrantFailed, err)
			}
			return nil, err
		}
		if ownerID == requesterID {
			return nil, ErrInvitationUnavailable
		}
	}
	at := time.Now().UTC()
	account := &model.Account{
		PK: model.PrefixAccount + id, SK: model.SKAccountMeta, AccountID: id,
		ParentAccountID: req.ParentAccountID, Name: req.Name, Aliases: req.Aliases, Domains: req.Domains,
		Industry: req.Industry, OwnerUserID: ownerID, CreatedByUserID: requesterID,
		CreationRequestHash: fingerprint, InitialOwnerEmail: req.OwnerEmail,
		InitialOwnerInvitationPending: pending,
		CreatedAt:                     at, UpdatedAt: at, EntityType: model.EntityTypeAccount,
	}
	owner := initialAccountMember(id, ownerID, req.OwnerEmail, model.RoleOwner, at)
	var creator *model.AccountMember
	if ownerID != requesterID && req.KeepCreatorAsMember {
		creator = initialAccountMember(id, requesterID, requesterEmail, model.RoleAM, at)
	}
	for attempt := 0; attempt < accountHierarchyAttempts; attempt++ {
		err = repo.CreateAccountWithInitialMembers(ctx, account, owner, creator, requesterID, ancestors)
		if err == nil {
			return s.creationReceipt(ctx, requesterID, req, account, pending, invited)
		}
		// Read after ANY failed response: the transaction may have committed.
		observed, readErr := s.repo.GetAccount(ctx, id)
		if readErr != nil {
			if invited {
				return nil, errors.Join(ErrInvitationGrantFailed, readErr)
			}
			return nil, readErr
		}
		if observed != nil {
			if observed.CreatedByUserID != requesterID || observed.CreationRequestHash != fingerprint {
				return nil, ErrAccountHierarchyConflict
			}
			return s.creationReceipt(ctx, requesterID, req, observed, pending, invited)
		}
		if !errors.Is(err, repository.ErrConditionFailed) {
			break
		}
		var observeErr error
		ancestors, observeErr = s.observeAccountAncestors(ctx, requesterID, id, req.ParentAccountID)
		if observeErr != nil {
			if invited {
				return nil, errors.Join(ErrInvitationGrantFailed, observeErr)
			}
			return nil, observeErr
		}
	}
	if invited {
		return nil, ErrInvitationGrantFailed
	}
	if errors.Is(err, repository.ErrConditionFailed) {
		return nil, ErrAccountHierarchyConflict
	}
	return nil, err
}

// ResendOwnerInvitation is a delivery capability, not membership or read access.
// The original creator's capability expires synchronously after thirty days.
func (s *AccountService) ResendOwnerInvitation(ctx context.Context, requesterID, accountID string) (*model.AccountMemberDTO, error) {
	if err := validateAccountHierarchyIDs(accountID, ""); err != nil {
		return nil, err
	}
	member, err := s.repo.GetMember(ctx, accountID, requesterID)
	if err != nil {
		return nil, err
	}
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if member == nil {
		age := time.Duration(-1)
		if account != nil {
			age = time.Since(account.CreatedAt)
		}
		if account == nil || account.CreatedByUserID != requesterID || requesterID == "" || age < 0 || age >= 30*24*time.Hour {
			return nil, ErrForbidden
		}
	}
	if account == nil {
		return nil, ErrNotFound
	}
	owner, err := s.repo.GetMember(ctx, accountID, account.OwnerUserID)
	if err != nil {
		return nil, err
	}
	if owner == nil || owner.Role != model.RoleOwner || owner.UserID != account.OwnerUserID ||
		owner.Email == "" || account.InitialOwnerEmail == "" || owner.Email != account.InitialOwnerEmail {
		return nil, ErrInvitationUnavailable
	}
	email, err := normalizeAccountInviteEmail(owner.Email)
	if err != nil || s.cognito == nil || s.cognitoPoolID == "" {
		return nil, ErrInvitationUnavailable
	}
	budget, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, invited, err := s.resendAccountInvite(budget, requesterID, email, owner.UserID)
	if err != nil {
		return nil, err
	}
	return &model.AccountMemberDTO{UserID: owner.UserID, Email: email, Role: model.RoleOwner, Invited: invited}, nil
}
