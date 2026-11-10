package service

import (
	"context"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cognitoidp "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	cognitotypes "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

var (
	// ErrOwnerTransferConflict means the owner, target role or membership changed
	// while the transfer was being applied; nothing was written.
	ErrOwnerTransferConflict = errors.New("account owner transfer conflict")
	// ErrOwnerTransferUnavailable means the target identity could not be verified.
	ErrOwnerTransferUnavailable = errors.New("account owner transfer unavailable")
)

// Optional capability, type-asserted like accountHierarchyRepo.
type accountOwnerTransferRepo interface {
	TransferAccountOwner(ctx context.Context, accountID, currentOwnerID, targetUserID string, at time.Time) error
}

// requireConfirmedUser fails closed: the repository cannot tell a pending or
// disabled invitee from a confirmed user, so Cognito is the identity authority.
func (s *AccountService) requireConfirmedUser(ctx context.Context, userID string) error {
	if s.cognito == nil || s.cognitoPoolID == "" {
		return ErrOwnerTransferUnavailable
	}
	budget, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := s.cognito.AdminGetUser(budget, &cognitoidp.AdminGetUserInput{
		UserPoolId: aws.String(s.cognitoPoolID), Username: aws.String(userID),
	})
	if err != nil {
		var notFound *cognitotypes.UserNotFoundException
		if errors.As(err, &notFound) {
			return ErrInvalidInput
		}
		return ErrOwnerTransferUnavailable
	}
	if out == nil || !out.Enabled || out.UserStatus != cognitotypes.UserStatusTypeConfirmed {
		return ErrInvalidInput
	}
	for _, attribute := range out.UserAttributes {
		if aws.ToString(attribute.Name) == "sub" && aws.ToString(attribute.Value) == userID {
			return nil
		}
	}
	return ErrInvalidInput
}

// TransferOwner moves ownership to an existing, confirmed direct member. The
// previous Owner becomes SA. It is the only path that grants Owner.
func (s *AccountService) TransferOwner(ctx context.Context, requesterID, accountID string, req *model.TransferOwnerRequest) (*model.TransferOwnerResponse, error) {
	if req == nil || requesterID == "" || req.TargetUserID == "" {
		return nil, ErrInvalidInput
	}
	if err := validateAccountHierarchyIDs(accountID, req.TargetUserID); err != nil {
		return nil, err
	}
	if req.TargetUserID == requesterID {
		return nil, ErrInvalidInput
	}
	repo, ok := s.repo.(accountOwnerTransferRepo)
	if !ok {
		return nil, ErrOwnerTransferUnavailable
	}
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrNotFound
	}
	requester, err := s.repo.GetMember(ctx, accountID, requesterID)
	if err != nil {
		return nil, err
	}
	if requester == nil || requester.Role != model.RoleOwner || account.OwnerUserID != requesterID {
		return nil, ErrForbidden
	}
	target, err := s.repo.GetMember(ctx, accountID, req.TargetUserID)
	if err != nil {
		return nil, err
	}
	if target == nil || target.UserID != req.TargetUserID {
		return nil, ErrNotFound
	}
	if target.Role == model.RoleOwner {
		return nil, ErrInvalidInput
	}
	if err := s.requireConfirmedUser(ctx, req.TargetUserID); err != nil {
		return nil, err
	}
	if err := repo.TransferAccountOwner(ctx, accountID, requesterID, req.TargetUserID, time.Now()); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return nil, ErrOwnerTransferConflict
		}
		return nil, err
	}
	return &model.TransferOwnerResponse{
		AccountID: accountID, OwnerUserID: req.TargetUserID,
		PreviousOwnerUserID: requesterID, PreviousOwnerRole: model.RoleSA,
	}, nil
}
