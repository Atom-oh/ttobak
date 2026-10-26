package service

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cognitoidp "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	cognitotypes "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
)

var (
	ErrInvalidInviteEmail    = errors.New("invalid invitation email")
	ErrInvitationUnavailable = errors.New("invitation service unavailable")
	ErrInvitationRejected    = errors.New("invitation rejected")
	ErrInvitationRateLimited = errors.New("invitation limit exceeded")
	ErrInvitationGrantFailed = errors.New("invitation requested but membership queue failed")
	ErrInvitationNotPending  = errors.New("user has already completed invitation")
)

const accountInvitationsPerHour = 20

func normalizeAccountInviteEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	// Cognito's Username is limited to 128 characters; cap bytes as well.
	if email == "" || len(email) > 128 || strings.ContainsAny(email, " \t\r\n") {
		return "", ErrInvalidInviteEmail
	}
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email {
		return "", ErrInvalidInviteEmail
	}
	return email, nil
}

func (s *AccountService) SetInvitationCounter(counter *WindowCounter) {
	s.invitationCounter = counter
}

func (s *AccountService) takeInvitationAttempt(ctx context.Context, requesterID string) error {
	budget, cancel := context.WithTimeout(ctx, rateCounterTimeout)
	defer cancel()
	usage, err := s.invitationCounter.Take(budget, requesterID, "account-invite", time.Hour, 1)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvitationUnavailable, err)
	}
	if usage.Count > accountInvitationsPerHour {
		return ErrInvitationRateLimited
	}
	return nil
}

// prepareAccountInvite reuses a present identity without sending mail. New users
// receive Cognito's temporary-password email; no group or owner grant is made.
func (s *AccountService) prepareAccountInvite(ctx context.Context, requesterID, email string, resend bool) (sub string, invited bool, err error) {
	if s.cognito == nil || s.cognitoPoolID == "" {
		return "", false, ErrInvitationUnavailable
	}
	budget, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	exists, sub, err := emailHasPendingInvite(budget, s.cognito, s.cognitoPoolID, email)
	if err != nil {
		return "", false, fmt.Errorf("%w: %v", ErrInvitationUnavailable, err)
	}
	if exists {
		if sub == "" {
			return "", false, ErrInvitationUnavailable
		}
		if resend {
			return s.resendAccountInvite(budget, requesterID, email, sub)
		}
		return sub, false, nil
	}
	if err := s.checkInvitationDomain(budget, email); err != nil {
		return "", false, err
	}
	if err := s.takeInvitationAttempt(budget, requesterID); err != nil {
		return "", false, err
	}
	out, err := s.cognito.AdminCreateUser(budget, &cognitoidp.AdminCreateUserInput{
		UserPoolId: aws.String(s.cognitoPoolID),
		Username:   aws.String(email),
		UserAttributes: []cognitotypes.AttributeType{
			{Name: aws.String("email"), Value: aws.String(email)},
			{Name: aws.String("email_verified"), Value: aws.String("true")},
		},
		DesiredDeliveryMediums: []cognitotypes.DeliveryMediumType{cognitotypes.DeliveryMediumTypeEmail},
		ForceAliasCreation:     false,
	}, func(options *cognitoidp.Options) {
		// Creation can deliver mail; an ambiguous response must not replay it.
		options.RetryMaxAttempts = 1
	})
	if err != nil {
		var exists *cognitotypes.UsernameExistsException
		if errors.As(err, &exists) {
			_, sub, err = emailHasPendingInvite(budget, s.cognito, s.cognitoPoolID, email)
			if err != nil || sub == "" {
				return "", false, ErrInvitationUnavailable
			}
			return sub, false, nil
		}
		var rejected *cognitotypes.UserLambdaValidationException
		var invalid *cognitotypes.InvalidParameterException
		if errors.As(err, &rejected) || errors.As(err, &invalid) {
			return "", false, ErrInvitationRejected
		}
		return "", false, fmt.Errorf("%w: %v", ErrInvitationUnavailable, err)
	}
	if out != nil && out.User != nil {
		for _, attribute := range out.User.Attributes {
			if aws.ToString(attribute.Name) == "sub" {
				sub = aws.ToString(attribute.Value)
				break
			}
		}
	}
	if sub == "" {
		_, sub, err = emailHasPendingInvite(budget, s.cognito, s.cognitoPoolID, email)
		if err != nil || sub == "" {
			return "", true, ErrInvitationGrantFailed
		}
	}
	return sub, true, nil
}

func (s *AccountService) checkInvitationDomain(ctx context.Context, email string) error {
	domains, err := s.repo.GetAllowedDomains(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvitationUnavailable, err)
	}
	_, domain, found := strings.Cut(email, "@")
	if found {
		for _, allowed := range domains {
			if strings.EqualFold(domain, allowed) {
				return nil
			}
		}
	}
	return ErrInvitationRejected
}

func (s *AccountService) resendAccountInvite(ctx context.Context, requesterID, email, sub string) (string, bool, error) {
	// RESEND's pre-sign-up trigger behavior is unverified, so enforce the current
	// domain allowlist ourselves rather than depending on a trigger invocation.
	if err := s.checkInvitationDomain(ctx, email); err != nil {
		return "", false, err
	}
	current, err := s.cognito.AdminGetUser(ctx, &cognitoidp.AdminGetUserInput{
		UserPoolId: aws.String(s.cognitoPoolID), Username: aws.String(email),
	})
	if err != nil || current == nil {
		return "", false, ErrInvitationUnavailable
	}
	if current.UserStatus != cognitotypes.UserStatusTypeForceChangePassword {
		return "", false, ErrInvitationNotPending
	}
	currentSub, currentEmail := "", ""
	for _, attribute := range current.UserAttributes {
		switch aws.ToString(attribute.Name) {
		case "sub":
			currentSub = aws.ToString(attribute.Value)
		case "email":
			currentEmail = strings.ToLower(aws.ToString(attribute.Value))
		}
	}
	currentUsername := aws.ToString(current.Username)
	if currentUsername == "" || currentSub != sub || currentEmail != email {
		return "", false, ErrInvitationUnavailable
	}
	if err := s.takeInvitationAttempt(ctx, requesterID); err != nil {
		return "", false, err
	}
	out, err := s.cognito.AdminCreateUser(ctx, &cognitoidp.AdminCreateUserInput{
		UserPoolId: aws.String(s.cognitoPoolID), Username: aws.String(currentUsername),
		MessageAction:          cognitotypes.MessageActionTypeResend,
		DesiredDeliveryMediums: []cognitotypes.DeliveryMediumType{cognitotypes.DeliveryMediumTypeEmail},
	}, func(options *cognitoidp.Options) { options.RetryMaxAttempts = 1 })
	if err != nil {
		return "", false, fmt.Errorf("%w: %v", ErrInvitationUnavailable, err)
	}
	// Cognito has no conditional RESEND operation. Bind the queued grant to the
	// identity returned by the delivery request, even if its email changed after
	// the preflight lookup. Delivery may already have happened on rejection.
	deliveredSub, deliveredEmail := "", ""
	if out != nil && out.User != nil {
		for _, attribute := range out.User.Attributes {
			switch aws.ToString(attribute.Name) {
			case "sub":
				deliveredSub = aws.ToString(attribute.Value)
			case "email":
				deliveredEmail = strings.ToLower(aws.ToString(attribute.Value))
			}
		}
	}
	if deliveredSub != sub || deliveredEmail != email {
		return "", true, ErrInvitationGrantFailed
	}
	return sub, true, nil
}
