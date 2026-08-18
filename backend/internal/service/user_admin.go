package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cognitoidp "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	cognitoidptypes "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const (
	adminsGroupName = "admins"
	// dormantThreshold is the "no login in this long" cutoff for the
	// dormancy badge. A missing lastLoginAt (never recorded) is a distinct,
	// non-dormant state -- see ListUsers's three-way status derivation.
	dormantThreshold = 90 * 24 * time.Hour
	// maxCognitoListPages caps ListUsers/ListUsersInGroup pagination at
	// 20 pages * 60 users/page = ~1200 users, so a runaway pool can't turn
	// this endpoint into an unbounded loop. Hitting the cap sets
	// AdminUserListResponse.Truncated and is logged.
	maxCognitoListPages = 20
	cognitoPageSize     = 60
)

// cognitoAdminAPI is the subset of the Cognito Identity Provider SDK client
// used for admin user management, shaped to match *cognitoidp.Client's
// methods exactly so the real client satisfies it with no adapter needed.
// This is the seam that makes admin user-management (and MeetingService's
// InviteUser/SearchUsers, see meeting.go's SetCognitoAdminAPI) unit-testable
// without a live AWS account -- previously every Cognito call built its own
// client inline via config.LoadDefaultConfig and had zero test coverage.
type cognitoAdminAPI interface {
	ListUsers(ctx context.Context, params *cognitoidp.ListUsersInput, optFns ...func(*cognitoidp.Options)) (*cognitoidp.ListUsersOutput, error)
	ListUsersInGroup(ctx context.Context, params *cognitoidp.ListUsersInGroupInput, optFns ...func(*cognitoidp.Options)) (*cognitoidp.ListUsersInGroupOutput, error)
	AdminGetUser(ctx context.Context, params *cognitoidp.AdminGetUserInput, optFns ...func(*cognitoidp.Options)) (*cognitoidp.AdminGetUserOutput, error)
	AdminCreateUser(ctx context.Context, params *cognitoidp.AdminCreateUserInput, optFns ...func(*cognitoidp.Options)) (*cognitoidp.AdminCreateUserOutput, error)
	AdminAddUserToGroup(ctx context.Context, params *cognitoidp.AdminAddUserToGroupInput, optFns ...func(*cognitoidp.Options)) (*cognitoidp.AdminAddUserToGroupOutput, error)
	AdminDeleteUser(ctx context.Context, params *cognitoidp.AdminDeleteUserInput, optFns ...func(*cognitoidp.Options)) (*cognitoidp.AdminDeleteUserOutput, error)
	AdminDisableUser(ctx context.Context, params *cognitoidp.AdminDisableUserInput, optFns ...func(*cognitoidp.Options)) (*cognitoidp.AdminDisableUserOutput, error)
	AdminEnableUser(ctx context.Context, params *cognitoidp.AdminEnableUserInput, optFns ...func(*cognitoidp.Options)) (*cognitoidp.AdminEnableUserOutput, error)
	AdminResetUserPassword(ctx context.Context, params *cognitoidp.AdminResetUserPasswordInput, optFns ...func(*cognitoidp.Options)) (*cognitoidp.AdminResetUserPasswordOutput, error)
	AdminUserGlobalSignOut(ctx context.Context, params *cognitoidp.AdminUserGlobalSignOutInput, optFns ...func(*cognitoidp.Options)) (*cognitoidp.AdminUserGlobalSignOutOutput, error)
}

// CognitoAdminAPI is the exported alias of cognitoAdminAPI for cross-package test mocks.
type CognitoAdminAPI = cognitoAdminAPI

// userAdminRepo defines the repository methods UserAdminService needs.
type userAdminRepo interface {
	BatchGetUserLastLogins(ctx context.Context, userIDs []string) (map[string]time.Time, error)
	DetachDeletedUserProfile(ctx context.Context, userID string) error
}

// UserAdminRepo is the exported alias of userAdminRepo for cross-package test mocks.
type UserAdminRepo = userAdminRepo

// Sentinel errors for admin user-management guards. All three are surfaced
// to the client as 400 BAD_REQUEST (this codebase has no CONFLICT code and
// the frontend only ever reads the error message, not a code -- see
// handler/user_admin.go).
var (
	ErrCannotModifySelf       = errors.New("본인 계정은 이 작업을 수행할 수 없습니다")
	ErrLastAdmin              = errors.New("마지막 관리자 계정은 제거할 수 없습니다. 다른 관리자를 먼저 추가하세요")
	ErrInvalidStatusForAction = errors.New("현재 계정 상태에서는 이 작업을 수행할 수 없습니다")
)

// UserAdminService implements the admin-only user-management operations
// (list, delete, enable/disable, resend invite, force password reset)
// backing the Settings page's "사용자 관리" panel.
type UserAdminService struct {
	cognito cognitoAdminAPI
	poolID  string
	repo    userAdminRepo
}

// NewUserAdminService creates a UserAdminService using a real Cognito client
// and DynamoDB repository, constructed once at Lambda cold start.
func NewUserAdminService(cognito *cognitoidp.Client, poolID string, repo *repository.DynamoDBRepository) *UserAdminService {
	return &UserAdminService{cognito: cognito, poolID: poolID, repo: repo}
}

// NewUserAdminServiceForTest creates a UserAdminService with injected mocks (exported for cross-package tests).
func NewUserAdminServiceForTest(cognito CognitoAdminAPI, poolID string, repo UserAdminRepo) *UserAdminService {
	return &UserAdminService{cognito: cognito, poolID: poolID, repo: repo}
}

// listAdminUserIDs returns the Cognito Username (=sub, see the package doc
// note in handler/user_admin.go on why Username==sub for this pool) of every
// member of the "admins" group, paginating up to maxCognitoListPages.
func (s *UserAdminService) listAdminUserIDs(ctx context.Context) ([]string, error) {
	var ids []string
	var nextToken *string
	for page := 0; page < maxCognitoListPages; page++ {
		out, err := s.cognito.ListUsersInGroup(ctx, &cognitoidp.ListUsersInGroupInput{
			UserPoolId: aws.String(s.poolID),
			GroupName:  aws.String(adminsGroupName),
			Limit:      aws.Int32(cognitoPageSize),
			NextToken:  nextToken,
		})
		if err != nil {
			return nil, err
		}
		for _, u := range out.Users {
			ids = append(ids, aws.ToString(u.Username))
		}
		if out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
	}
	return ids, nil
}

// guardNotSelfAndNotLastAdmin rejects an admin-removal action (delete,
// disable) targeting the acting admin's own account, or targeting the sole
// remaining member of the admins group. The last-admin check has a
// microseconds-wide TOCTOU window between this read and the caller's
// subsequent write; see warnIfNoAdminsLeft for the post-hoc detection that
// covers the rare case where two concurrent removals both pass this check.
func (s *UserAdminService) guardNotSelfAndNotLastAdmin(ctx context.Context, actorUserID, targetUserID string) error {
	if actorUserID == targetUserID {
		return ErrCannotModifySelf
	}
	adminIDs, err := s.listAdminUserIDs(ctx)
	if err != nil {
		return fmt.Errorf("failed to check admins group: %w", err)
	}
	targetIsAdmin := false
	for _, id := range adminIDs {
		if id == targetUserID {
			targetIsAdmin = true
			break
		}
	}
	if targetIsAdmin && len(adminIDs) <= 1 {
		return ErrLastAdmin
	}
	return nil
}

// warnIfNoAdminsLeft re-checks the admins group after a delete/disable
// completes and returns a non-empty Korean warning if it finds the group
// empty -- the TOCTOU backstop for guardNotSelfAndNotLastAdmin. A pool with
// zero admins is not permanently stuck: `aws cognito-idp
// admin-add-user-to-group` from an operator shell recovers it.
func (s *UserAdminService) warnIfNoAdminsLeft(ctx context.Context) string {
	ids, err := s.listAdminUserIDs(ctx)
	if err != nil {
		log.Printf("warnIfNoAdminsLeft: failed to check admins group: %v", err)
		return ""
	}
	if len(ids) == 0 {
		log.Printf("ERROR: admins group is now empty")
		return "관리자 계정이 남아있지 않습니다. AWS CLI로 admins 그룹에 관리자를 다시 추가해야 합니다."
	}
	return ""
}

// appendWarning joins two optional warning strings with a space, skipping empties.
func appendWarning(existing, addition string) string {
	if addition == "" {
		return existing
	}
	if existing == "" {
		return addition
	}
	return existing + " " + addition
}

// ListUsers returns every Cognito user in the pool, joined with their last
// recorded login timestamp (from DynamoDB, written by the PostAuthentication
// trigger) and admins-group membership.
func (s *UserAdminService) ListUsers(ctx context.Context) (*model.AdminUserListResponse, error) {
	adminIDs, err := s.listAdminUserIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list admins group: %w", err)
	}
	adminSet := make(map[string]bool, len(adminIDs))
	for _, id := range adminIDs {
		adminSet[id] = true
	}

	var (
		users     []cognitoidptypes.UserType
		truncated bool
		nextToken *string
	)
	for page := 0; page < maxCognitoListPages; page++ {
		out, err := s.cognito.ListUsers(ctx, &cognitoidp.ListUsersInput{
			UserPoolId:      aws.String(s.poolID),
			Limit:           aws.Int32(cognitoPageSize),
			PaginationToken: nextToken,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to list users: %w", err)
		}
		users = append(users, out.Users...)
		if out.PaginationToken == nil {
			break
		}
		nextToken = out.PaginationToken
		if page == maxCognitoListPages-1 {
			truncated = true
			log.Printf("ListUsers: hit the %d-page safety cap with more results remaining", maxCognitoListPages)
		}
	}

	userIDs := make([]string, 0, len(users))
	for _, u := range users {
		userIDs = append(userIDs, aws.ToString(u.Username))
	}

	lastLogins, joinErr := s.repo.BatchGetUserLastLogins(ctx, userIDs)
	lastLoginUnavailable := false
	if joinErr != nil {
		// Degrade, don't fail: enable/disable/delete are the load-bearing
		// operations of this endpoint -- the last-login column is a
		// nice-to-have that can render as "unknown" instead.
		log.Printf("ListUsers: failed to join last-login timestamps: %v", joinErr)
		lastLoginUnavailable = true
		lastLogins = map[string]time.Time{}
	}

	summaries := make([]model.AdminUserSummary, 0, len(users))
	for _, u := range users {
		userID := aws.ToString(u.Username)
		var email, name string
		for _, attr := range u.Attributes {
			switch aws.ToString(attr.Name) {
			case "email":
				email = aws.ToString(attr.Value)
			case "name":
				name = aws.ToString(attr.Value)
			}
		}

		var lastLoginAt *time.Time
		if t, ok := lastLogins[userID]; ok {
			lastLoginAt = &t
		}

		summaries = append(summaries, model.AdminUserSummary{
			UserID:      userID,
			Email:       email,
			Name:        name,
			Status:      string(u.UserStatus),
			Enabled:     u.Enabled,
			IsAdmin:     adminSet[userID],
			CreatedAt:   aws.ToTime(u.UserCreateDate),
			LastLoginAt: lastLoginAt,
			// Missing lastLoginAt must never count as dormant -- on day one
			// after this feature ships, every existing user has no record
			// yet, and "missing implies dormant" would paint the whole panel
			// red. Only an actual timestamp older than the threshold does.
			Dormant: lastLoginAt != nil && time.Since(*lastLoginAt) > dormantThreshold,
		})
	}

	return &model.AdminUserListResponse{
		Users:                summaries,
		LastLoginUnavailable: lastLoginUnavailable,
		Truncated:            truncated,
	}, nil
}

// DeleteUser removes a user's Cognito account only; their DynamoDB data
// (meetings, documents, etc.) is preserved. Guarded against self-delete and
// removing the last admin.
func (s *UserAdminService) DeleteUser(ctx context.Context, actorUserID, targetUserID string) (*model.AdminUserActionResponse, error) {
	if err := s.guardNotSelfAndNotLastAdmin(ctx, actorUserID, targetUserID); err != nil {
		return nil, err
	}

	resp := &model.AdminUserActionResponse{UserID: targetUserID}

	// Global sign-out BEFORE delete: AdminUserGlobalSignOut invalidates the
	// user's refresh tokens, so they can no longer silently renew a session
	// -- but it does NOT invalidate an access/ID token already issued; this
	// API verifies JWTs locally (JWKS) without re-checking Cognito per
	// request, so an already-issued token stays valid until its own natural
	// expiry (up to ~1h) regardless. This call narrows that window, it does
	// not close it. Also: once the user is deleted, AdminUserGlobalSignOut
	// can no longer target them, so this must run first. A failure here is
	// non-fatal -- the delete below is the operation that actually matters.
	if _, err := s.cognito.AdminUserGlobalSignOut(ctx, &cognitoidp.AdminUserGlobalSignOutInput{
		UserPoolId: aws.String(s.poolID),
		Username:   aws.String(targetUserID),
	}); err != nil {
		log.Printf("DeleteUser: global sign-out failed for %s: %v", targetUserID, err)
		resp.Warning = appendWarning(resp.Warning, "기존 세션이 즉시 만료되지 않았을 수 있습니다.")
	}

	if _, err := s.cognito.AdminDeleteUser(ctx, &cognitoidp.AdminDeleteUserInput{
		UserPoolId: aws.String(s.poolID),
		Username:   aws.String(targetUserID),
	}); err != nil {
		return nil, fmt.Errorf("failed to delete user: %w", err)
	}

	// Preserve DynamoDB data but detach the email-search index so re-inviting
	// the same address later can't resolve back to this now-dead userID. A
	// failure here is surfaced as a Warning, not just logged -- silently
	// swallowing it would let exactly the GetUserByEmail non-determinism
	// this call exists to prevent recur on the next re-invite, with no
	// signal to the admin that cleanup didn't happen.
	if err := s.repo.DetachDeletedUserProfile(ctx, targetUserID); err != nil {
		log.Printf("DeleteUser: failed to detach profile for %s: %v", targetUserID, err)
		resp.Warning = appendWarning(resp.Warning, "계정은 삭제됐지만 이전 데이터 정리에 실패했습니다. 같은 이메일 재초대 시 문제가 발생할 수 있습니다.")
	}

	resp.Warning = appendWarning(resp.Warning, s.warnIfNoAdminsLeft(ctx))
	return resp, nil
}

// DisableUser blocks a user from signing in and immediately invalidates any
// session they currently hold. Guarded against self-disable and disabling
// the last admin.
func (s *UserAdminService) DisableUser(ctx context.Context, actorUserID, targetUserID string) (*model.AdminUserActionResponse, error) {
	if err := s.guardNotSelfAndNotLastAdmin(ctx, actorUserID, targetUserID); err != nil {
		return nil, err
	}

	if _, err := s.cognito.AdminDisableUser(ctx, &cognitoidp.AdminDisableUserInput{
		UserPoolId: aws.String(s.poolID),
		Username:   aws.String(targetUserID),
	}); err != nil {
		return nil, fmt.Errorf("failed to disable user: %w", err)
	}

	resp := &model.AdminUserActionResponse{UserID: targetUserID}

	// AdminDisableUser only blocks new sign-ins; an already-issued access/ID
	// token stays valid until it expires regardless (the API verifies JWTs
	// locally, it doesn't re-check Cognito per request). AdminUserGlobalSignOut
	// only revokes refresh tokens -- it does NOT invalidate that already-issued
	// token, so it narrows the up-to-an-hour continued-access window (the user
	// can no longer silently renew), it does not close it. Non-fatal on
	// failure -- the disable already succeeded.
	if _, err := s.cognito.AdminUserGlobalSignOut(ctx, &cognitoidp.AdminUserGlobalSignOutInput{
		UserPoolId: aws.String(s.poolID),
		Username:   aws.String(targetUserID),
	}); err != nil {
		log.Printf("DisableUser: global sign-out failed for %s: %v", targetUserID, err)
		resp.Warning = appendWarning(resp.Warning, "계정은 비활성화됐지만 기존 세션이 즉시 만료되지 않았을 수 있습니다.")
	}

	resp.Warning = appendWarning(resp.Warning, s.warnIfNoAdminsLeft(ctx))
	return resp, nil
}

// EnableUser re-allows sign-in for a previously disabled account. No guard
// is needed -- re-enabling can never remove an admin.
func (s *UserAdminService) EnableUser(ctx context.Context, targetUserID string) (*model.AdminUserActionResponse, error) {
	if _, err := s.cognito.AdminEnableUser(ctx, &cognitoidp.AdminEnableUserInput{
		UserPoolId: aws.String(s.poolID),
		Username:   aws.String(targetUserID),
	}); err != nil {
		return nil, fmt.Errorf("failed to enable user: %w", err)
	}
	return &model.AdminUserActionResponse{UserID: targetUserID}, nil
}

// ResendInvite re-sends the initial invite email (a fresh temporary
// password) to a user who never completed their first login. Only valid for
// users still in FORCE_CHANGE_PASSWORD; other statuses return
// ErrInvalidStatusForAction. Always looks up the user's current status
// first rather than trusting a possibly-stale caller-supplied one.
func (s *UserAdminService) ResendInvite(ctx context.Context, targetUserID string) error {
	current, err := s.cognito.AdminGetUser(ctx, &cognitoidp.AdminGetUserInput{
		UserPoolId: aws.String(s.poolID),
		Username:   aws.String(targetUserID),
	})
	if err != nil {
		var notFound *cognitoidptypes.UserNotFoundException
		if errors.As(err, &notFound) {
			return ErrUserNotFound
		}
		return fmt.Errorf("failed to look up user: %w", err)
	}
	if current.UserStatus != cognitoidptypes.UserStatusTypeForceChangePassword {
		return ErrInvalidStatusForAction
	}

	// MessageAction=RESEND requires the same immutable Username used at
	// creation time -- since this pool aliases email as a sign-in attribute,
	// that immutable value is the sub, not the email address.
	if _, err := s.cognito.AdminCreateUser(ctx, &cognitoidp.AdminCreateUserInput{
		UserPoolId:             aws.String(s.poolID),
		Username:               aws.String(targetUserID),
		MessageAction:          cognitoidptypes.MessageActionTypeResend,
		DesiredDeliveryMediums: []cognitoidptypes.DeliveryMediumType{cognitoidptypes.DeliveryMediumTypeEmail},
	}); err != nil {
		return fmt.Errorf("failed to resend invite: %w", err)
	}
	return nil
}

// ForceResetPassword invalidates a confirmed user's password and has
// Cognito email them a reset code, usable via the login screen's "비밀번호
// 찾기" flow (frontend/src/components/auth/ForgotPasswordForm.tsx). Only
// valid for users in CONFIRMED status; other statuses return
// ErrInvalidStatusForAction -- in particular, this must never be called for
// a user still in FORCE_CHANGE_PASSWORD, since there is no code-entry screen
// for that state and it would lock them out instead of helping them in.
func (s *UserAdminService) ForceResetPassword(ctx context.Context, targetUserID string) error {
	current, err := s.cognito.AdminGetUser(ctx, &cognitoidp.AdminGetUserInput{
		UserPoolId: aws.String(s.poolID),
		Username:   aws.String(targetUserID),
	})
	if err != nil {
		var notFound *cognitoidptypes.UserNotFoundException
		if errors.As(err, &notFound) {
			return ErrUserNotFound
		}
		return fmt.Errorf("failed to look up user: %w", err)
	}
	if current.UserStatus != cognitoidptypes.UserStatusTypeConfirmed {
		return ErrInvalidStatusForAction
	}

	if _, err := s.cognito.AdminResetUserPassword(ctx, &cognitoidp.AdminResetUserPasswordInput{
		UserPoolId: aws.String(s.poolID),
		Username:   aws.String(targetUserID),
	}); err != nil {
		return fmt.Errorf("failed to reset password: %w", err)
	}
	return nil
}
