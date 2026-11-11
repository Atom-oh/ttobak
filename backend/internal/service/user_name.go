package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	cognitoidp "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	cognitoidptypes "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/ttobak/backend/internal/model"
)

// maxUserNameRunes bounds a display name in Unicode code points. Cognito's
// own limit for `name` is 2048 characters; this is the product limit.
const maxUserNameRunes = 64

var (
	// ErrInvalidUserName rejects a blank, oversized or control-character name.
	ErrInvalidUserName = errors.New("이름은 1~64자로 입력하고 줄바꿈이나 제어 문자는 사용할 수 없습니다")
	// ErrUserNameProfileSync means Cognito accepted the new name but the
	// DynamoDB PROFILE copy was not updated. Retrying the same request is safe.
	ErrUserNameProfileSync = errors.New("user name saved in Cognito but profile sync failed")
)

// NormalizeUserName trims surrounding whitespace and validates a display name.
// Control and bidirectional-override characters are rejected so a name cannot
// break single-line layouts or visually reorder neighbouring text.
func NormalizeUserName(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", ErrInvalidUserName
	}
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > maxUserNameRunes {
		return "", ErrInvalidUserName
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return "", ErrInvalidUserName
		}
	}
	return name, nil
}

// UpdateUserName changes a user's display name in both places it lives: the
// Cognito `name` attribute (the source of truth, surfaced in ID tokens and the
// admin user list) and the USER#/PROFILE copy read by account people lists.
// Cognito is written first; a later profile failure returns
// ErrUserNameProfileSync, and an identical retry converges both stores.
//
// Authorization is the caller's job: the self route passes the verified JWT
// subject, the admin route sits behind middleware.RequireAdmin. Username==sub
// for this pool (see listAdminUserIDs).
func (s *UserAdminService) UpdateUserName(ctx context.Context, targetUserID, rawName string) (*model.UserNameResponse, error) {
	if strings.TrimSpace(targetUserID) == "" {
		return nil, ErrUserNotFound
	}
	name, err := NormalizeUserName(rawName)
	if err != nil {
		return nil, err
	}

	if _, err := s.cognito.AdminUpdateUserAttributes(ctx, &cognitoidp.AdminUpdateUserAttributesInput{
		UserPoolId: aws.String(s.poolID),
		Username:   aws.String(targetUserID),
		UserAttributes: []cognitoidptypes.AttributeType{
			{Name: aws.String("name"), Value: aws.String(name)},
		},
	}); err != nil {
		var notFound *cognitoidptypes.UserNotFoundException
		if errors.As(err, &notFound) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("failed to update user name: %w", err)
	}

	if err := s.repo.UpdateUserProfileName(ctx, targetUserID, name); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUserNameProfileSync, err)
	}
	return &model.UserNameResponse{UserID: targetUserID, Name: name}, nil
}
