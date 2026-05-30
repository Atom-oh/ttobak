package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// Account-specific sentinel errors. (ErrForbidden, ErrNotFound, ErrUserNotFound
// are already declared in service/meeting.go in this same package — reuse them.)
var (
	ErrInvalidInput = errors.New("invalid input")
	ErrMemberExists = errors.New("member already exists")
)

// accountRepo is the persistence seam for AccountService (mirrors meetingRepo).
type accountRepo interface {
	CreateAccount(ctx context.Context, account *model.Account, ownerMember *model.AccountMember) error
	GetAccount(ctx context.Context, accountID string) (*model.Account, error)
	GetMember(ctx context.Context, accountID, userID string) (*model.AccountMember, error)
	PutMember(ctx context.Context, member *model.AccountMember) error
	ListAccountMembers(ctx context.Context, accountID string) ([]model.AccountMember, error)
	ListAccountsForUser(ctx context.Context, userID string) ([]model.AccountMember, error)
	GetUserByEmail(ctx context.Context, email string) (*model.User, error)
}

// AccountRepo is the exported alias for cross-package (handler) tests.
type AccountRepo = accountRepo

type AccountService struct {
	repo accountRepo
}

func NewAccountService(repo *repository.DynamoDBRepository) *AccountService {
	return &AccountService{repo: repo}
}

// newAccountServiceWithRepo is for same-package (service) tests.
func newAccountServiceWithRepo(repo accountRepo) *AccountService {
	return &AccountService{repo: repo}
}

// NewAccountServiceForTest is for cross-package (handler) tests.
func NewAccountServiceForTest(repo AccountRepo) *AccountService {
	return &AccountService{repo: repo}
}

func isAssignableRole(role string) bool {
	switch role {
	case model.RoleAM, model.RoleTAM, model.RoleSSA:
		return true
	default:
		return false
	}
}

func toAccountResponse(a *model.Account, members []model.AccountMember) *model.AccountResponse {
	dtos := make([]model.AccountMemberDTO, 0, len(members))
	for _, m := range members {
		dtos = append(dtos, model.AccountMemberDTO{UserID: m.UserID, Email: m.Email, Role: m.Role})
	}
	return &model.AccountResponse{
		AccountID:   a.AccountID,
		Name:        a.Name,
		Aliases:     a.Aliases,
		Domains:     a.Domains,
		Industry:    a.Industry,
		OwnerUserID: a.OwnerUserID,
		Members:     dtos,
		CreatedAt:   a.CreatedAt,
	}
}

func (s *AccountService) CreateAccount(ctx context.Context, ownerUserID, ownerEmail string, req *model.CreateAccountRequest) (*model.Account, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, ErrInvalidInput
	}
	now := time.Now().UTC()
	accountID := uuid.NewString()
	account := &model.Account{
		PK:          model.PrefixAccount + accountID,
		SK:          model.SKAccountMeta,
		AccountID:   accountID,
		Name:        strings.TrimSpace(req.Name),
		Aliases:     req.Aliases,
		Domains:     req.Domains,
		Industry:    req.Industry,
		OwnerUserID: ownerUserID,
		CreatedAt:   now,
		UpdatedAt:   now,
		EntityType:  model.EntityTypeAccount,
	}
	owner := &model.AccountMember{
		PK:         model.PrefixAccount + accountID,
		SK:         model.PrefixMember + ownerUserID,
		AccountID:  accountID,
		UserID:     ownerUserID,
		Email:      ownerEmail,
		Role:       model.RoleOwner,
		AddedAt:    now,
		GSI1PK:     model.PrefixUser + ownerUserID,
		GSI1SK:     model.PrefixAccount + accountID,
		EntityType: model.EntityTypeAccountMember,
	}
	if err := s.repo.CreateAccount(ctx, account, owner); err != nil {
		return nil, err
	}
	return account, nil
}

func (s *AccountService) GetAccount(ctx context.Context, userID, accountID string) (*model.AccountResponse, error) {
	member, err := s.repo.GetMember(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	if member == nil {
		// Distinguish "no such account" (NotFound) from "exists but not a member" (Forbidden).
		account, err := s.repo.GetAccount(ctx, accountID)
		if err != nil {
			return nil, err
		}
		if account == nil {
			return nil, ErrNotFound
		}
		return nil, ErrForbidden
	}
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrNotFound
	}
	members, err := s.repo.ListAccountMembers(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return toAccountResponse(account, members), nil
}

func (s *AccountService) ListAccounts(ctx context.Context, userID string) ([]model.AccountSummary, error) {
	memberships, err := s.repo.ListAccountsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]model.AccountSummary, 0, len(memberships))
	for _, m := range memberships {
		account, err := s.repo.GetAccount(ctx, m.AccountID)
		if err != nil {
			return nil, err
		}
		if account == nil {
			continue // membership dangling after account deletion
		}
		out = append(out, model.AccountSummary{AccountID: account.AccountID, Name: account.Name, Role: m.Role})
	}
	return out, nil
}
