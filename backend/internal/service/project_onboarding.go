package service

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	ct "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

var ErrInvitationRequired = errors.New("administrator invitation required")
var ErrUserDisabled = errors.New("user disabled")
var ErrEmailVerificationRequired = errors.New("email verification required")

type projectInvitationStore interface {
	PutPendingShare(context.Context, *model.PendingShare) error
	GetPendingShare(context.Context, string, string) (*model.PendingShare, error)
	ListPendingProjectShares(context.Context, string, string) ([]model.PendingShare, string, error)
	MaterializePendingProjectGrant(context.Context, *model.PendingShare, string, string) (bool, error)
	RevokePendingProjectShare(context.Context, *model.PendingShare, string) error
}

type invitationRecipient struct {
	Sub      string
	Status   ct.UserStatusType
	Verified bool
}

func normalizeInvitationEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value || len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return "", ErrInvalidInput
	}
	return value, nil
}

// The lookup never sends mail or grants access. New identity creation is still
// the separately authorized admin-only InviteUser action.
func lookupInvitationRecipient(ctx context.Context, client cognitoAdminAPI, poolID, email string) (*invitationRecipient, error) {
	if client == nil || poolID == "" {
		return nil, fmt.Errorf("invitation lookup unavailable")
	}
	u, err := client.AdminGetUser(ctx, &cognitoidentityprovider.AdminGetUserInput{UserPoolId: aws.String(poolID), Username: aws.String(email)})
	if err != nil {
		var missing *ct.UserNotFoundException
		if errors.As(err, &missing) {
			return nil, ErrInvitationRequired
		}
		return nil, fmt.Errorf("lookup invitation recipient: %w", err)
	}
	if !u.Enabled {
		return nil, ErrUserDisabled
	}
	recipient := &invitationRecipient{Status: u.UserStatus}
	currentEmail := ""
	for _, a := range u.UserAttributes {
		switch aws.ToString(a.Name) {
		case "sub":
			recipient.Sub = aws.ToString(a.Value)
		case "email":
			currentEmail = aws.ToString(a.Value)
		case "email_verified":
			recipient.Verified = aws.ToString(a.Value) == "true"
		}
	}
	if recipient.Sub == "" || !strings.EqualFold(currentEmail, email) {
		return nil, ErrInvitationRequired
	}
	return recipient, nil
}

func (s *ProjectService) SetCognitoAdminAPI(client cognitoAdminAPI, poolID string) {
	s.cognito = client
	s.cognitoPoolID = poolID
}

func (s *ProjectService) addOnboardingMember(ctx context.Context, ownerID, projectID, email string) (*model.ProjectMemberDTO, error) {
	store, ok := s.repo.(projectInvitationStore)
	if !ok {
		return nil, fmt.Errorf("project invitation store unavailable")
	}
	recipient, err := lookupInvitationRecipient(ctx, s.cognito, s.cognitoPoolID, email)
	if err != nil {
		return nil, err
	}
	if recipient.Sub == ownerID {
		return nil, ErrSelfShare
	}
	member, err := s.repo.GetProjectMember(ctx, projectID, recipient.Sub)
	if err != nil {
		return nil, err
	}
	if member != nil {
		return &model.ProjectMemberDTO{UserID: member.UserID, Email: member.Email, EmailVerified: recipient.Verified}, nil
	}
	profile, err := s.repo.GetUserByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	// An email index left behind by a deleted/recreated identity cannot grant its
	// old user ID access. The pending grant binds only the current Cognito sub.
	registered := profile != nil && profile.UserID == recipient.Sub
	pending := &model.PendingShare{Kind: model.PendingShareKindProject, ProjectID: projectID, Email: email, InvitedByUserID: ownerID, InvitedCognitoSub: recipient.Sub}
	if err := store.PutPendingShare(ctx, pending); err != nil {
		return nil, err
	}
	if registered && recipient.Verified && recipient.Status == ct.UserStatusTypeConfirmed {
		_, err := store.MaterializePendingProjectGrant(ctx, pending, recipient.Sub, email)
		if err != nil {
			return nil, err
		}
		member, err = s.repo.GetProjectMember(ctx, projectID, recipient.Sub)
		if err != nil {
			return nil, err
		}
		if member != nil {
			return &model.ProjectMemberDTO{UserID: member.UserID, Email: member.Email, EmailVerified: true}, nil
		}
		remaining, err := store.GetPendingShare(ctx, email, model.PrefixPendingProject+projectID)
		if err != nil {
			return nil, err
		}
		if remaining == nil {
			return nil, repository.ErrConditionFailed
		}
	}
	return &model.ProjectMemberDTO{Email: email, Pending: true, EmailVerified: recipient.Verified}, nil
}

func (s *ProjectService) ListPendingMembers(ctx context.Context, userID, projectID, cursor string) (*model.PendingProjectMemberPage, error) {
	project, err := s.repo.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, ErrNotFound
	}
	if project.OwnerUserID != userID {
		return nil, ErrForbidden
	}
	store, ok := s.repo.(projectInvitationStore)
	if !ok {
		return nil, fmt.Errorf("project invitation store unavailable")
	}
	rows, next, err := store.ListPendingProjectShares(ctx, projectID, cursor)
	if err != nil {
		return nil, err
	}
	out := &model.PendingProjectMemberPage{Members: []model.PendingProjectMemberDTO{}, NextCursor: next}
	for _, p := range rows {
		out.Members = append(out.Members, model.PendingProjectMemberDTO{Email: p.Email, ExpiresAt: p.TTL})
	}
	return out, nil
}

func (s *ProjectService) RevokePendingMember(ctx context.Context, userID, projectID, email string) error {
	project, err := s.repo.GetProject(ctx, projectID)
	if err != nil {
		return err
	}
	if project == nil {
		return ErrNotFound
	}
	if project.OwnerUserID != userID {
		return ErrForbidden
	}
	email, err = normalizeInvitationEmail(email)
	if err != nil {
		return err
	}
	store, ok := s.repo.(projectInvitationStore)
	if !ok {
		return fmt.Errorf("project invitation store unavailable")
	}
	p, err := store.GetPendingShare(ctx, email, model.PrefixPendingProject+projectID)
	if err != nil {
		return err
	}
	sub := ""
	if p != nil {
		sub = p.InvitedCognitoSub
		if p.Kind != model.PendingShareKindProject || p.ProjectID != projectID {
			return ErrInvalidInput
		}
		if err := store.RevokePendingProjectShare(ctx, p, userID); err != nil {
			if !errors.Is(err, repository.ErrConditionFailed) {
				return err
			}
			member, readErr := s.repo.GetProjectMember(ctx, projectID, sub)
			if readErr != nil {
				return readErr
			}
			if member != nil {
				return ErrPendingAlreadyClaimed
			}
			return err
		}
		return nil
	}
	recipient, err := lookupInvitationRecipient(ctx, s.cognito, s.cognitoPoolID, email)
	if errors.Is(err, ErrInvitationRequired) {
		return nil
	}
	if err != nil {
		return err
	}
	member, err := s.repo.GetProjectMember(ctx, projectID, recipient.Sub)
	if err != nil {
		return err
	}
	if member != nil {
		return ErrPendingAlreadyClaimed
	}
	return nil
}
