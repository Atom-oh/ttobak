package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

var ErrAccountHierarchyConflict = errors.New("account hierarchy changed concurrently")

const accountHierarchyAttempts = 3

// Optional capability: existing document/account mocks need not implement
// hierarchy writes unless they exercise creation with a parent or reparenting.
type accountHierarchyRepo interface {
	CreateAccountWithParent(context.Context, *model.Account, *model.AccountMember, []repository.AccountParentLink) error
	UpdateAccountParent(ctx context.Context, accountID, requesterID, expectedParentID, parentID string, ancestors []repository.AccountParentLink) error
}

// Only an exactly empty parent means root/detach. IDs are never normalized.
func validateAccountHierarchyIDs(accountID, parentID string) error {
	if accountID == "" {
		return ErrInvalidInput
	}
	for _, id := range []string{accountID, parentID} {
		if len(id) > 128 {
			return ErrInvalidInput
		}
		for i := 0; i < len(id); i++ {
			c := id[i]
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
				c >= '0' && c <= '9' || c == '_' || c == '-' {
				continue
			}
			return ErrInvalidInput
		}
	}
	return nil
}

// observeAccountAncestors always reads fresh, strongly consistent META/member
// records. Only the immediate parent's membership is required. Ancestor names
// and other metadata are never exposed to the requester.
func (s *AccountService) observeAccountAncestors(ctx context.Context, userID, childID, parentID string) ([]repository.AccountParentLink, error) {
	seen := map[string]bool{childID: true}
	var ancestors []repository.AccountParentLink
	for id := parentID; id != ""; {
		if seen[id] || len(ancestors) >= repository.AccountHierarchyMaxAncestors {
			return nil, fmt.Errorf("%w: account hierarchy cycle or depth limit", ErrInvalidInput)
		}
		seen[id] = true
		account, err := s.repo.GetAccount(ctx, id)
		if err != nil {
			return nil, err
		}
		if account == nil {
			return nil, ErrNotFound
		}
		if id == parentID {
			member, err := s.repo.GetMember(ctx, parentID, userID)
			if err != nil {
				return nil, err
			}
			if member == nil {
				return nil, ErrForbidden
			}
		}
		ancestors = append(ancestors, repository.AccountParentLink{AccountID: id, ParentAccountID: account.ParentAccountID})
		id = account.ParentAccountID
	}
	return ancestors, nil
}

func (s *AccountService) createAccountWithParent(ctx context.Context, account *model.Account, owner *model.AccountMember) error {
	if err := validateAccountHierarchyIDs(account.AccountID, account.ParentAccountID); err != nil {
		return err
	}
	repo, ok := s.repo.(accountHierarchyRepo)
	if !ok {
		return fmt.Errorf("account hierarchy persistence unavailable")
	}
	for range accountHierarchyAttempts {
		ancestors, err := s.observeAccountAncestors(ctx, owner.UserID, account.AccountID, account.ParentAccountID)
		if err != nil {
			return err
		}
		err = repo.CreateAccountWithParent(ctx, account, owner, ancestors)
		if !errors.Is(err, repository.ErrConditionFailed) {
			return err
		}
	}
	return ErrAccountHierarchyConflict
}

// UpdateAccountParent only organizes accounts; it neither inherits nor changes
// grants. Detaching requires no access to the old parent.
func (s *AccountService) UpdateAccountParent(ctx context.Context, requesterID, accountID string, req *model.UpdateAccountParentRequest) (*model.UpdateAccountParentResponse, error) {
	if req == nil || req.ParentAccountID == nil {
		return nil, ErrInvalidInput
	}
	if err := validateAccountHierarchyIDs(accountID, *req.ParentAccountID); err != nil {
		return nil, err
	}
	repo, ok := s.repo.(accountHierarchyRepo)
	if !ok {
		return nil, fmt.Errorf("account hierarchy persistence unavailable")
	}
	parentID := *req.ParentAccountID
	for range accountHierarchyAttempts {
		account, err := s.repo.GetAccount(ctx, accountID)
		if err != nil {
			return nil, err
		}
		if account == nil {
			return nil, ErrNotFound
		}
		member, err := s.repo.GetMember(ctx, accountID, requesterID)
		if err != nil {
			return nil, err
		}
		if member == nil || member.Role != model.RoleOwner || account.OwnerUserID != requesterID {
			return nil, ErrForbidden
		}
		ancestors, err := s.observeAccountAncestors(ctx, requesterID, accountID, parentID)
		if err != nil {
			return nil, err
		}
		err = repo.UpdateAccountParent(ctx, accountID, requesterID, account.ParentAccountID, parentID, ancestors)
		if err == nil {
			return &model.UpdateAccountParentResponse{AccountID: accountID, ParentAccountID: parentID}, nil
		}
		if !errors.Is(err, repository.ErrConditionFailed) {
			return nil, err
		}
	}
	return nil, ErrAccountHierarchyConflict
}
