package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// Account-specific sentinel errors. (ErrForbidden, ErrNotFound, ErrUserNotFound
// are already declared in service/meeting.go in this same package — reuse them.)
var (
	ErrInvalidInput   = errors.New("invalid input")
	ErrMemberExists   = errors.New("member already exists")
	ErrAmbiguousAlias = errors.New("alias maps to multiple accounts")
	ErrLoopGuard      = errors.New("document originated from ttobak (loop guard)")
	// ErrAmbiguousShareBlocksRemoval is returned by RemoveMember when the
	// target holds at least one Share with Origin != "account" on a meeting
	// still linked to this account, AND force was not passed. See
	// RemoveMemberResult's doc comment for why this ambiguity exists (a
	// pre-Origin-field legacy account-share is indistinguishable from a
	// direct grant) -- this precheck is what actually closes the fail-open
	// gap: previously RemoveMember always deleted membership and merely
	// reported the ambiguity afterward, so a member who held only legacy
	// shares kept transcript/Bedrock access forever with no forced
	// checkpoint for the owner to notice. Passing force=true proceeds
	// exactly as before (delete membership, leave ambiguous shares
	// untouched, report them in the response).
	ErrAmbiguousShareBlocksRemoval = errors.New("ambiguous untagged share blocks removal (pass force=true to proceed anyway)")
)

const maxInlineDocBytes = 300 * 1024 // mirror repo transcript inline threshold

// hasTtobakOriginMarker reports whether markdown carries a ttobak_id key in a
// leading YAML frontmatter block (i.e. TTOBAK-origin → must not be re-ingested).
func hasTtobakOriginMarker(markdown string) bool {
	// Strip a leading UTF-8 BOM first: otherwise a BOM+"---\nttobak_id: ..."
	// document would fail the HasPrefix("---") check and bypass the loop guard.
	s := strings.TrimPrefix(markdown, "\ufeff")
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "---") {
		return false
	}
	rest := s[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return false
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		// Compare the YAML key (text before the first ':') so that whitespace-
		// padded keys like "ttobak_id :" — valid YAML — can't bypass the guard.
		trimmed := strings.TrimSpace(line)
		if idx := strings.Index(trimmed, ":"); idx >= 0 {
			if strings.TrimSpace(trimmed[:idx]) == "ttobak_id" {
				return true
			}
		}
	}
	return false
}

// accountRepo is the persistence seam for AccountService (mirrors meetingRepo).
type accountRepo interface {
	CreateAccount(ctx context.Context, account *model.Account, ownerMember *model.AccountMember) error
	GetAccount(ctx context.Context, accountID string) (*model.Account, error)
	GetMember(ctx context.Context, accountID, userID string) (*model.AccountMember, error)
	PutMember(ctx context.Context, member *model.AccountMember) error
	DeleteMember(ctx context.Context, accountID, userID string) error
	UpdateMemberRole(ctx context.Context, accountID, userID, role string) error
	ListAccountMembers(ctx context.Context, accountID string) ([]model.AccountMember, error)
	ListAccountsForUser(ctx context.Context, userID string) ([]model.AccountMember, error)
	GetUserByEmail(ctx context.Context, email string) (*model.User, error)
	ListMeetingRefsForAccount(ctx context.Context, accountID string) ([]model.MeetingRef, error)
	GetMeetingByID(ctx context.Context, meetingID string) (*model.Meeting, error)
	GetShare(ctx context.Context, sharedToID, meetingID string) (*model.Share, error)
	DeleteShareIfAccountOrigin(ctx context.Context, accountID, sharedToID, meetingID string) error
	ListInsightsForAccount(ctx context.Context, accountID string) ([]model.AccountInsight, error)
	PutAccountDocument(ctx context.Context, doc *model.AccountDocument) error
	UpdateAccountDocumentFields(ctx context.Context, pk, docID string, fields map[string]interface{}, removeFields []string) (map[string]string, error)
	ListAccountDocuments(ctx context.Context, pk string) ([]model.AccountDocument, error)
	GetAccountDocument(ctx context.Context, pk, docID string) (*model.AccountDocument, error)
	DeleteAccountDocument(ctx context.Context, pk, docID string) error
	PutPublicShare(ctx context.Context, share *model.PublicShare) error
	GetPublicShare(ctx context.Context, token string) (*model.PublicShare, error)
	DeletePublicShare(ctx context.Context, token string) error
	SetPublicShareTokenIfAbsent(ctx context.Context, pk, docID, token string) error
	ClearPublicShareTokenIfMatches(ctx context.Context, pk, docID, expectedToken string) error
	CreateDocShare(ctx context.Context, docID, ownerID, ownerEmail, sharedToID, email string) (*model.Share, error)
	GetDocShare(ctx context.Context, sharedToID, docID string) (*model.Share, error)
	DeleteDocShare(ctx context.Context, sharedToID, docID string) error
	ListDocSharesForUser(ctx context.Context, userID string) ([]model.Share, error)
	ListDocSharesForDoc(ctx context.Context, docID string) ([]model.Share, error)
}

// AccountRepo is the exported alias for cross-package (handler) tests.
type AccountRepo = accountRepo

// s3ObjectDeleter is the S3 capability AccountService needs: cleaning up a
// slide's underlying object when its document is deleted/replaced, and
// copying a slide's object when a personal doc is shared to an account (see
// ShareUserDocumentToAccount). Matches *s3.Client's methods directly so the
// real client satisfies it without wrapping; a mock can implement just
// these two methods in tests.
type s3ObjectDeleter interface {
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	CopyObject(ctx context.Context, params *s3.CopyObjectInput, optFns ...func(*s3.Options)) (*s3.CopyObjectOutput, error)
}

type AccountService struct {
	repo       accountRepo
	s3         s3ObjectDeleter
	bucketName string
}

func NewAccountService(repo *repository.DynamoDBRepository, s3Client *s3.Client, bucketName string) *AccountService {
	return &AccountService{repo: repo, s3: s3Client, bucketName: bucketName}
}

// newAccountServiceWithRepo is for same-package (service) tests. s3 is left
// nil -- deleteDoc treats that as "no S3 cleanup available" rather than
// panicking, so tests that don't care about slide cleanup don't need to
// wire up an S3 mock.
func newAccountServiceWithRepo(repo accountRepo) *AccountService {
	return &AccountService{repo: repo}
}

// NewAccountServiceForTest is for cross-package (handler) tests.
func NewAccountServiceForTest(repo AccountRepo) *AccountService {
	return &AccountService{repo: repo}
}

func isAssignableRole(role string) bool {
	for _, r := range model.AssignableRoles {
		if role == r {
			return true
		}
	}
	return false
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

func (s *AccountService) AddMember(ctx context.Context, requesterUserID, accountID string, req *model.AddMemberRequest) (*model.AccountMemberDTO, error) {
	requester, err := s.repo.GetMember(ctx, accountID, requesterUserID)
	if err != nil {
		return nil, err
	}
	if requester == nil || requester.Role != model.RoleOwner {
		return nil, ErrForbidden
	}
	if !isAssignableRole(req.Role) {
		return nil, ErrInvalidInput
	}
	user, err := s.repo.GetUserByEmail(ctx, req.Email)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrUserNotFound
	}
	existing, err := s.repo.GetMember(ctx, accountID, user.UserID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, ErrMemberExists
	}
	member := &model.AccountMember{
		PK:         model.PrefixAccount + accountID,
		SK:         model.PrefixMember + user.UserID,
		AccountID:  accountID,
		UserID:     user.UserID,
		Email:      user.Email,
		Role:       req.Role,
		AddedAt:    time.Now().UTC(),
		GSI1PK:     model.PrefixUser + user.UserID,
		GSI1SK:     model.PrefixAccount + accountID,
		EntityType: model.EntityTypeAccountMember,
	}
	if err := s.repo.PutMember(ctx, member); err != nil {
		return nil, err
	}
	return &model.AccountMemberDTO{UserID: user.UserID, Email: user.Email, Role: req.Role}, nil
}

// RemoveMemberResult reports the outcome of RemoveMember's best-effort Share
// cleanup. FailedMeetingIDs is a genuine cleanup error (retryable, worth
// alerting on). AmbiguousUntaggedMeetingIDs is NOT a failure -- it flags
// meetings where a Share exists with Origin != "account" on a meeting still
// shared to this account, so cleanup could not safely touch it. This is a
// COARSE signal, not a precise "these are legacy" list: Origin=="" is the
// shape of BOTH a legacy account-share (written before the Origin field
// existed) AND a perfectly normal direct grant the owner made explicitly --
// they are indistinguishable at this layer (see model.Share.Origin's doc
// and CreateShare's clobber-guard). Any meeting where the removed member
// ALSO holds a direct share to a meeting still linked to this account will
// appear here too, regardless of whether a legacy account-share also
// exists; this is intentional noise traded for not silently hiding the
// legacy case, not a bug. Surfacing this list gives an operator a signal to
// check whether backfill-share-origin is needed for this account, without
// RemoveMember itself guessing wrong and revoking an owner's explicit
// direct share.
type RemoveMemberResult struct {
	FailedMeetingIDs            []string
	AmbiguousUntaggedMeetingIDs []string
}

// checkNoAmbiguousShares returns ErrAmbiguousShareBlocksRemoval if
// targetUserID holds a Share with Origin != "account" on any of refs'
// meetings that are still linked to accountID via SharedToAccount -- the
// same predicate RemoveMember's cleanup loop uses to populate
// AmbiguousUntaggedMeetingIDs, run here BEFORE membership is deleted so the
// caller can block on it instead of only learning about it after the fact.
//
// Unlike the cleanup loop (which runs after membership is already gone and
// can only report failures via FailedMeetingIDs), a GetMeetingByID/GetShare
// error HERE returns an error instead of silently continuing: this precheck
// is the security gate that closes RemoveMember's fail-open access-retention
// gap, so a transient DynamoDB blip must not let it pass silently -- the
// caller gets a retryable error and membership is left untouched (same
// fail-closed treatment RemoveMember already gives ListMeetingRefsForAccount
// failures).
func (s *AccountService) checkNoAmbiguousShares(ctx context.Context, accountID, targetUserID string, refs []model.MeetingRef) error {
	for _, ref := range refs {
		meeting, err := s.repo.GetMeetingByID(ctx, ref.MeetingID)
		if err != nil {
			return fmt.Errorf("ambiguous-share precheck: get meeting %s: %w", ref.MeetingID, err)
		}
		if meeting == nil || !meeting.SharedToAccount || meeting.AccountID != accountID {
			continue // stale ref, or Link-only (not a team-share grant) -- same skip the cleanup loop and backfill CLI apply
		}
		share, err := s.repo.GetShare(ctx, targetUserID, ref.MeetingID)
		if err != nil {
			return fmt.Errorf("ambiguous-share precheck: get share for meeting %s: %w", ref.MeetingID, err)
		}
		if share != nil && share.Origin != model.ShareOriginAccount {
			return ErrAmbiguousShareBlocksRemoval
		}
	}
	return nil
}

// RemoveMember deletes a non-owner member. Only the account owner may call
// this; the owner member itself can never be removed (an account must never
// exist without an owner -- see CreateAccount's transactional invariant).
// If the target holds any ambiguous untagged share (see RemoveMemberResult)
// on a meeting still linked to this account, membership is NOT deleted and
// ErrAmbiguousShareBlocksRemoval is returned instead -- unless force is
// true, in which case removal proceeds exactly as it always has (delete
// membership, leave ambiguous shares untouched, report them in the
// result). This precheck is what actually closes the fail-open access gap:
// without it, a member holding only legacy (pre-Origin-field) shares could
// be removed and keep transcript/Bedrock access forever with nothing
// forcing the owner to notice before that happened.
func (s *AccountService) RemoveMember(ctx context.Context, requesterUserID, accountID, targetUserID string, force bool) (*RemoveMemberResult, error) {
	requester, err := s.repo.GetMember(ctx, accountID, requesterUserID)
	if err != nil {
		return nil, err
	}
	if requester == nil || requester.Role != model.RoleOwner {
		return nil, ErrForbidden
	}
	target, err := s.repo.GetMember(ctx, accountID, targetUserID)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, ErrNotFound
	}
	if target.Role == model.RoleOwner {
		return nil, ErrInvalidInput
	}

	// List refs BEFORE deleting membership: membership is still intact here,
	// so a list failure can safely return an error (500, retryable) instead
	// of silently producing a 204 that leaves cleanup un-run with no way to
	// signal or retry it -- a retried DELETE after membership is gone would
	// just 404. The precheck below reuses this same list, so a re-list after
	// the delete (which would also defeat the "membership untouched on early
	// return" guarantee) is unnecessary.
	refs, err := s.repo.ListMeetingRefsForAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}

	if !force {
		if err := s.checkNoAmbiguousShares(ctx, accountID, targetUserID, refs); err != nil {
			return nil, err
		}
	}

	if err := s.repo.DeleteMember(ctx, accountID, targetUserID); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return nil, ErrNotFound // removed concurrently between our Get and Delete
		}
		return nil, err
	}

	result := &RemoveMemberResult{
		FailedMeetingIDs:            []string{},
		AmbiguousUntaggedMeetingIDs: []string{},
	}

	for _, ref := range refs {
		// A meeting can be re-shared from account A to account B without A's
		// MeetingRef ever being cleaned up (ADR-016's known non-transactional
		// write-order gap) -- verifying the meeting is STILL linked to THIS
		// account before touching its share prevents A's RemoveMember from
		// deleting a share that actually belongs to B's membership grant. The
		// !SharedToAccount check additionally excludes Link-only meetings
		// (AccountID set, SharedToAccount false): those were never a team
		// grant, so any share the target holds there is unrelated to this
		// account's membership and must not be swept up as "ambiguous" or
		// deleted. Mirrors the exact guard checkNoAmbiguousShares and the
		// backfill CLI both apply (!meeting.SharedToAccount || meeting.AccountID != accountID).
		meeting, err := s.repo.GetMeetingByID(ctx, ref.MeetingID)
		if err != nil {
			log.Printf("cleanup share for removed member %s (meeting %s): get meeting: %v", targetUserID, ref.MeetingID, err)
			result.FailedMeetingIDs = append(result.FailedMeetingIDs, ref.MeetingID)
			continue
		}
		if meeting == nil || !meeting.SharedToAccount || meeting.AccountID != accountID {
			continue // stale ref: meeting deleted, re-shared to a different account, or Link-only (never a team grant)
		}
		share, err := s.repo.GetShare(ctx, targetUserID, ref.MeetingID)
		if err != nil {
			log.Printf("cleanup share for removed member %s (meeting %s): get share: %v", targetUserID, ref.MeetingID, err)
			result.FailedMeetingIDs = append(result.FailedMeetingIDs, ref.MeetingID)
			continue
		}
		if share == nil {
			continue
		}
		if share.Origin != model.ShareOriginAccount {
			// Origin != "account" here (in practice always Origin=="") means
			// cleanup must not touch this share: it might be a legitimate direct
			// grant, but it is also the shape of a legacy account share.
			result.AmbiguousUntaggedMeetingIDs = append(result.AmbiguousUntaggedMeetingIDs, ref.MeetingID)
			continue
		}
		// This GetShare read is just a cheap pre-filter to skip meetings with
		// no account-origin share -- DeleteShareIfAccountOrigin re-checks
		// origin=="account" AND accountId==accountID as a transaction
		// condition at delete time, so neither an owner creating a new direct
		// share NOR this same meeting being re-shared to a DIFFERENT account
		// in the gap between the read above and this delete can have that
		// new grant silently swept up: the condition fails and the delete is
		// skipped instead. The accountID here (not accountID re-derived from
		// meeting, which the read above already re-checked non-atomically)
		// is this call's own security boundary against that exact race.
		if err := s.repo.DeleteShareIfAccountOrigin(ctx, accountID, targetUserID, ref.MeetingID); err != nil {
			if errors.Is(err, repository.ErrConditionFailed) {
				continue // origin changed (or row gone) between the read and this delete -- no-op skip, not a failure
			}
			log.Printf("cleanup share for removed member %s (meeting %s): delete share: %v", targetUserID, ref.MeetingID, err)
			result.FailedMeetingIDs = append(result.FailedMeetingIDs, ref.MeetingID)
		}
	}
	return result, nil
}

// UpdateMemberRole changes a non-owner member's role. Only the account owner
// may call this; the owner's own role can never be changed via this path.
func (s *AccountService) UpdateMemberRole(ctx context.Context, requesterUserID, accountID, targetUserID string, req *model.UpdateMemberRequest) (*model.AccountMemberDTO, error) {
	requester, err := s.repo.GetMember(ctx, accountID, requesterUserID)
	if err != nil {
		return nil, err
	}
	if requester == nil || requester.Role != model.RoleOwner {
		return nil, ErrForbidden
	}
	if !isAssignableRole(req.Role) {
		return nil, ErrInvalidInput
	}
	target, err := s.repo.GetMember(ctx, accountID, targetUserID)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, ErrNotFound
	}
	if target.Role == model.RoleOwner {
		return nil, ErrInvalidInput
	}
	if err := s.repo.UpdateMemberRole(ctx, accountID, targetUserID, req.Role); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return nil, ErrNotFound // removed concurrently between our Get and Update
		}
		return nil, err
	}
	return &model.AccountMemberDTO{UserID: target.UserID, Email: target.Email, Role: req.Role}, nil
}

// ListAccountMeetings returns the shared-meeting references for an account.
// Only members may read; non-members get ErrForbidden, missing account ErrNotFound.
func (s *AccountService) ListAccountMeetings(ctx context.Context, userID, accountID string) ([]model.MeetingRefDTO, error) {
	member, err := s.repo.GetMember(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	if member == nil {
		account, err := s.repo.GetAccount(ctx, accountID)
		if err != nil {
			return nil, err
		}
		if account == nil {
			return nil, ErrNotFound
		}
		return nil, ErrForbidden
	}
	refs, err := s.repo.ListMeetingRefsForAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]model.MeetingRefDTO, 0, len(refs))
	for _, r := range refs {
		out = append(out, model.MeetingRefDTO{MeetingID: r.MeetingID, OwnerUserID: r.OwnerUserID, Title: r.Title, Date: r.Date})
	}
	return out, nil
}

// ListAccountInsights returns insight raw material for an account, filtered by
// optional period [from,to] and optional types. Member-gated. (spec §6.3: filter
// client-side for v1.)
func (s *AccountService) ListAccountInsights(ctx context.Context, userID, accountID string, from, to time.Time, types []string) ([]model.AccountInsightDTO, error) {
	member, err := s.repo.GetMember(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	if member == nil {
		account, err := s.repo.GetAccount(ctx, accountID)
		if err != nil {
			return nil, err
		}
		if account == nil {
			return nil, ErrNotFound
		}
		return nil, ErrForbidden
	}
	insights, err := s.repo.ListInsightsForAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	typeSet := make(map[string]bool, len(types))
	for _, t := range types {
		typeSet[t] = true
	}
	out := make([]model.AccountInsightDTO, 0, len(insights))
	for _, ins := range insights {
		if !from.IsZero() && ins.OccurredAt.Before(from) {
			continue
		}
		if !to.IsZero() && ins.OccurredAt.After(to) {
			continue
		}
		if len(typeSet) > 0 && !typeSet[ins.Type] {
			continue
		}
		out = append(out, model.AccountInsightDTO{
			Type:        ins.Type,
			Text:        ins.Text,
			Evidence:    ins.Evidence,
			Implication: ins.Implication,
			NextAction:  ins.NextAction,
			SourceType:  ins.SourceType,
			SourceID:    ins.SourceID,
			OccurredAt:  ins.OccurredAt,
			TsMarker:    ins.TsMarker,
			Entities:    ins.Entities,
		})
	}
	return out, nil
}

// GetAccountBrief composes account meta + insights (grouped by type) + shared
// meetings into one payload. Access is enforced by the composed methods
// (GetAccount gates membership first).
func (s *AccountService) GetAccountBrief(ctx context.Context, userID, accountID string, from, to time.Time, types []string) (*model.AccountBrief, error) {
	account, err := s.GetAccount(ctx, userID, accountID)
	if err != nil {
		return nil, err
	}
	meetings, err := s.ListAccountMeetings(ctx, userID, accountID)
	if err != nil {
		return nil, err
	}
	insights, err := s.ListAccountInsights(ctx, userID, accountID, from, to, types)
	if err != nil {
		return nil, err
	}
	byType := make(map[string][]model.AccountInsightDTO)
	for _, ins := range insights {
		byType[ins.Type] = append(byType[ins.Type], ins)
	}
	return &model.AccountBrief{Account: account, InsightsByType: byType, Meetings: meetings}, nil
}

// ResolveAccountByAlias finds, among the user's accounts, the one whose aliases
// (or name) match the given tag. Returns ErrNotFound if none, ErrAmbiguousAlias
// if more than one (never auto-pick).
func (s *AccountService) ResolveAccountByAlias(ctx context.Context, userID, alias string) (*model.Account, error) {
	memberships, err := s.repo.ListAccountsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	target := strings.ToLower(strings.TrimSpace(alias))
	var matches []*model.Account
	for _, m := range memberships {
		account, err := s.repo.GetAccount(ctx, m.AccountID)
		if err != nil {
			return nil, err
		}
		if account == nil {
			continue
		}
		if strings.ToLower(account.Name) == target {
			matches = append(matches, account)
			continue
		}
		for _, a := range account.Aliases {
			if strings.ToLower(strings.TrimSpace(a)) == target {
				matches = append(matches, account)
				break
			}
		}
	}
	switch len(matches) {
	case 0:
		return nil, ErrNotFound
	case 1:
		return matches[0], nil
	default:
		return nil, ErrAmbiguousAlias
	}
}

func (s *AccountService) requireMember(ctx context.Context, userID, accountID string) error {
	member, err := s.repo.GetMember(ctx, accountID, userID)
	if err != nil {
		return err
	}
	if member != nil {
		return nil
	}
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	if account == nil {
		return ErrNotFound
	}
	return ErrForbidden
}

// wikilinkRE matches [[target]], [[target|alias]], and [[target#heading]] --
// only target is captured; alias/heading are Obsidian-style suffixes that
// don't change which document the link points at.
var wikilinkRE = regexp.MustCompile(`\[\[([^\[\]|#]+)(?:[|#][^\[\]]*)?\]\]`)

// parseWikilinks extracts normalized, deduplicated (order-preserving) link
// targets from markdown. This is the data source for a future graph view --
// no edge items are stored yet, just this list on the document itself.
func parseWikilinks(markdown string) []string {
	matches := wikilinkRE.FindAllStringSubmatch(markdown, -1)
	seen := make(map[string]bool, len(matches))
	links := make([]string, 0, len(matches))
	for _, m := range matches {
		target := strings.TrimSpace(m[1])
		if target == "" || seen[target] {
			continue
		}
		seen[target] = true
		links = append(links, target)
	}
	return links
}

// encodeS3CopySourceKey percent-encodes an S3 key for use in a CopyObject
// CopySource (the x-amz-copy-source header requires URL-encoding), escaping
// each "/"-delimited segment independently so the separators between
// segments stay literal "/" rather than becoming "%2F" -- url.PathEscape
// on the whole key at once would encode those too.
func encodeS3CopySourceKey(key string) string {
	segments := strings.Split(key, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return strings.Join(segments, "/")
}

func toDocumentDTO(d *model.AccountDocument) model.AccountDocumentDTO {
	// UpdatedAt is a new field -- documents written before this shipped have
	// a zero value. `omitempty` doesn't help here (Go's encoding/json never
	// omits a zero-value struct like time.Time), so fall back to CreatedAt
	// instead of surfacing "0001-01-01" to clients.
	updatedAt := d.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = d.CreatedAt
	}
	return model.AccountDocumentDTO{
		DocID: d.DocID, Title: d.Title, DocType: d.DocType, Path: d.Path,
		Links: d.Links, FileName: d.FileName, SourceUserID: d.SourceUserID,
		CreatedAt: d.CreatedAt, UpdatedAt: updatedAt,
	}
}

// validateDocRequest is the single check shared by create (putDoc) and
// update (updateDoc): a title, and exactly one of markdown or a fileKey (a
// doc is either a note/blog or a slide, never both, never neither). It does
// NOT check fileKey ownership -- callers do that separately via
// validateFileKeyOwnership, since update only needs the check when the
// fileKey is actually changing (see updateDoc).
// trimmedMarkdown reads req.Markdown (nil-safe) and normalizes it. Trimming
// so a whitespace-only markdown ("   ") can't slip past a mutual-exclusivity
// check as "no markdown" while still getting stored as non-empty Content on
// what's meant to be an empty-body slide.
func trimmedMarkdown(req *model.PutDocumentRequest) string {
	if req.Markdown == nil {
		return ""
	}
	return strings.TrimSpace(*req.Markdown)
}

func validateDocRequest(req *model.PutDocumentRequest) error {
	if strings.TrimSpace(req.Title) == "" {
		return ErrInvalidInput
	}
	markdown := trimmedMarkdown(req)
	hasMarkdown := markdown != ""
	if req.FileKey == "" && !hasMarkdown {
		return ErrInvalidInput
	}
	if req.FileKey != "" && hasMarkdown {
		return ErrInvalidInput
	}
	if hasMarkdown {
		if hasTtobakOriginMarker(markdown) {
			return ErrLoopGuard
		}
		if len(markdown) > maxInlineDocBytes {
			return ErrInvalidInput
		}
	}
	return nil
}

// validateFileKeyOwnership is the only place a client-supplied S3 key is
// trusted (see ADR-020) -- it must be prefixed with the caller's own
// docs/{userID}/ segment.
func validateFileKeyOwnership(userID, fileKey string) error {
	if fileKey != "" && !ownsFileKey(userID, fileKey) {
		return ErrForbidden
	}
	return nil
}

// ownsFileKey reports whether fileKey lives under userID's own docs/
// prefix -- the key path itself is the source of truth for who a slide's
// S3 object belongs to. A document's SourceUserID is NOT a safe proxy for
// this: it's fixed at doc creation and never updated when a later editor
// replaces the fileKey, so after such a replacement it can point at a
// completely different user's file than the one now stored on the doc.
func ownsFileKey(userID, fileKey string) bool {
	return strings.HasPrefix(fileKey, "docs/"+userID+"/")
}

// putDoc is the shared create core for both account-scoped (accountID set)
// and personal (accountID empty, pk is model.PrefixUser+userID) documents. A
// slide upload carries FileKey instead of Markdown -- ownership of that S3
// key is enforced here (must be under docs/{userID}/) since it's the only
// place a client-supplied key gets trusted.
func (s *AccountService) putDoc(ctx context.Context, userID, pk, accountID string, req *model.PutDocumentRequest, entityType string) (*model.AccountDocumentDTO, error) {
	if err := validateDocRequest(req); err != nil {
		return nil, err
	}
	if err := validateFileKeyOwnership(userID, req.FileKey); err != nil {
		return nil, err
	}
	markdown := trimmedMarkdown(req)
	now := time.Now().UTC()
	docID := uuid.NewString()
	doc := &model.AccountDocument{
		PK: pk, SK: model.PrefixDoc + docID,
		AccountID: accountID, DocID: docID, Title: strings.TrimSpace(req.Title),
		DocType: req.DocType, Path: req.Path, Content: markdown,
		Links:   parseWikilinks(markdown),
		FileKey: req.FileKey, FileName: req.FileName, MimeType: req.MimeType, FileSize: req.FileSize,
		SourceUserID: userID, TtobakOrigin: false,
		CreatedAt: now, UpdatedAt: now, EntityType: entityType,
	}
	if err := s.repo.PutAccountDocument(ctx, doc); err != nil {
		return nil, err
	}
	dto := toDocumentDTO(doc)
	return &dto, nil
}

func (s *AccountService) listDocs(ctx context.Context, pk, docType string) ([]model.AccountDocumentDTO, error) {
	docs, err := s.repo.ListAccountDocuments(ctx, pk)
	if err != nil {
		return nil, err
	}
	out := make([]model.AccountDocumentDTO, 0, len(docs))
	for _, d := range docs {
		if docType != "" && d.DocType != docType {
			continue
		}
		out = append(out, toDocumentDTO(&d))
	}
	return out, nil
}

func (s *AccountService) getDoc(ctx context.Context, pk, docID string) (*model.AccountDocument, error) {
	doc, err := s.repo.GetAccountDocument(ctx, pk, docID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, ErrNotFound
	}
	return doc, nil
}

// updateDoc always updates title; docType/path/body/file fields are each
// independently "omit to preserve" -- an empty/absent value on any of them
// means "don't touch this field", not "clear it". This is required, not
// just lenient: AccountDocumentDetail.FileKey is json:"-" (never returned to
// a client), so a caller that GETs a slide and PUTs back only the fields it
// actually saw (title, docType, path) cannot possibly know the original
// fileKey to resend it -- an always-full-replace design made that resave
// return 400 or accidentally convert the slide into an empty note, and made
// a title-only edit silently blank out docType/path too.
//
// Markdown is the one field where "omit" and "explicit empty" must be
// distinguishable (see PutDocumentRequest.Markdown doc) -- a non-nil
// pointer to "" is an explicit "clear this note's body", honored as such,
// not silently ignored.
//
// The fileKey ownership check only runs when the fileKey is actually
// changing to something new: an account slide can be edited (title, etc.)
// by any member, not just the member who originally uploaded it, so
// re-asserting "fileKey must be under docs/{editingUser}/" on every save of
// an unchanged (here: omitted, meaning unchanged) fileKey would incorrectly
// 403 a non-uploader's edit.
func (s *AccountService) updateDoc(ctx context.Context, userID, pk, docID string, req *model.PutDocumentRequest) (*model.AccountDocumentDTO, error) {
	existing, err := s.getDoc(ctx, pk, docID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Title) == "" {
		return nil, ErrInvalidInput
	}
	bodyChanging := req.Markdown != nil || req.FileKey != ""
	markdown := trimmedMarkdown(req)
	if bodyChanging {
		if req.FileKey != "" && markdown != "" {
			return nil, ErrInvalidInput // a doc is a note/blog or a slide, never both
		}
		if req.FileKey == "" && markdown == "" && existing.FileKey != "" {
			// The doc is currently a slide and the caller is explicitly
			// touching the body (Markdown key present, or FileKey sent)
			// but supplies neither replacement markdown nor a fileKey --
			// silently landing there would irreversibly delete the S3
			// object below while leaving a document with no content and
			// no file, a state create() never allows. Require the caller
			// to be explicit: send non-empty markdown to convert to a
			// note, or a new fileKey to replace the file.
			return nil, ErrInvalidInput
		}
		if markdown != "" {
			if hasTtobakOriginMarker(markdown) {
				return nil, ErrLoopGuard
			}
			if len(markdown) > maxInlineDocBytes {
				return nil, ErrInvalidInput
			}
		}
		if req.FileKey != "" && req.FileKey != existing.FileKey {
			if err := validateFileKeyOwnership(userID, req.FileKey); err != nil {
				return nil, err
			}
		}
	}
	existing.Title = strings.TrimSpace(req.Title)
	fields := map[string]interface{}{"title": existing.Title}
	if req.DocType != "" {
		existing.DocType = req.DocType
		fields["docType"] = existing.DocType
	}
	if req.Path != "" {
		existing.Path = req.Path
		fields["path"] = existing.Path
	}
	oldFileKey := existing.FileKey
	if bodyChanging {
		// The caller is actually changing the body -- full replace of the
		// content/file fields (so e.g. a slide->note conversion correctly
		// clears the old file fields, and vice versa; and req.Markdown
		// pointing to "" correctly clears an existing note to empty).
		existing.Content = markdown
		existing.Links = parseWikilinks(markdown)
		existing.FileKey, existing.FileName, existing.MimeType, existing.FileSize = req.FileKey, req.FileName, req.MimeType, req.FileSize
		fields["content"] = existing.Content
		fields["links"] = existing.Links
		fields["fileKey"] = existing.FileKey
		fields["fileName"] = existing.FileName
		fields["mimeType"] = existing.MimeType
		fields["fileSize"] = existing.FileSize
	}
	existing.UpdatedAt = time.Now().UTC()
	fields["updatedAt"] = existing.UpdatedAt
	// Deliberately NOT PutAccountDocument (whole-item overwrite) and
	// deliberately NOT SET-ing "publicShareToken" in fields: that field
	// has its own independent writers (CreateUserDocPublicShare /
	// RevokeUserDocPublicShare), and a whole-item Put -- even one that
	// re-reads the token right before writing -- still clobbers whatever
	// landed in the gap between that re-read and the write.
	var removeFields []string
	if bodyChanging && oldFileKey != "" && existing.FileKey == "" {
		// This edit turns a file-backed doc into a markdown one (slide ->
		// note): remove publicShareToken unconditionally, in the SAME
		// atomic UpdateItem as clearing fileKey -- not gated on the
		// getDoc-time snapshot's token value. A concurrent
		// CreateUserDocPublicShare can mint a token after that snapshot
		// read but before this write lands; a snapshot-gated clear would
		// miss it, leaving a token that's inert only until some *future*
		// edit reattaches a file, at which point it would unauthenticated-
		// expose that new file to whoever holds the old token. Whichever
		// of {this write, a concurrent mint} lands second determines the
		// outcome regardless of read-time ordering -- see
		// SetPublicShareTokenIfAbsent's fileKey<>"" condition for the
		// other half of closing this race (a mint landing *after* this
		// clear must also fail, not just one landing before).
		removeFields = append(removeFields, "publicShareToken")
	}
	oldValues, err := s.repo.UpdateAccountDocumentFields(ctx, pk, docID, fields, removeFields)
	if err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return nil, ErrNotFound // deleted concurrently between our getDoc and this write
		}
		return nil, err
	}
	if removedToken := oldValues["publicShareToken"]; removedToken != "" {
		// Best-effort: the field removal above already made the token
		// unusable (ResolvePublicShare's own doc lookup will never find
		// this token as the doc's live PublicShareToken again); a failure
		// here just leaves an inert orphaned PublicShare pointer.
		if err := s.repo.DeletePublicShare(ctx, removedToken); err != nil {
			log.Printf("cleanup public share pointer for doc %s during file->markdown conversion: %v", docID, err)
		}
	}
	if oldFileKey != "" && oldFileKey != existing.FileKey && ownsFileKey(userID, oldFileKey) && s.s3 != nil {
		// Best-effort, same as deleteDoc: the DB row is already committed to
		// the new fileKey, so a failure here just leaves the superseded S3
		// object orphaned rather than blocking (or un-doing) the update.
		// ownsFileKey (not SourceUserID -- see deleteDoc's comment) mirrors
		// deleteDoc's check: oldFileKey is scoped to its own owner, not to
		// the account, so only that owner's edit can trigger its cleanup
		// (ponytail: same accepted orphan trade-off as deleteDoc when a
		// non-owner member replaces it).
		if _, err := s.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(s.bucketName),
			Key:    aws.String(oldFileKey),
		}); err != nil {
			log.Printf("cleanup superseded S3 object for doc %s (key %s): %v", docID, oldFileKey, err)
		}
		if sidecarKey := SidecarPDFKey(oldFileKey); sidecarKey != "" {
			if _, err := s.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: aws.String(s.bucketName),
				Key:    aws.String(sidecarKey),
			}); err != nil {
				log.Printf("cleanup superseded PDF sidecar for doc %s (key %s): %v", docID, sidecarKey, err)
			}
		}
	}
	dto := toDocumentDTO(existing)
	return &dto, nil
}

func (s *AccountService) PutDocument(ctx context.Context, userID, accountID string, req *model.PutDocumentRequest) (*model.AccountDocumentDTO, error) {
	if err := s.requireMember(ctx, userID, accountID); err != nil {
		return nil, err
	}
	return s.putDoc(ctx, userID, model.PrefixAccount+accountID, accountID, req, model.EntityTypeAccountDoc)
}

func (s *AccountService) ListAccountDocuments(ctx context.Context, userID, accountID, docType string) ([]model.AccountDocumentDTO, error) {
	if err := s.requireMember(ctx, userID, accountID); err != nil {
		return nil, err
	}
	return s.listDocs(ctx, model.PrefixAccount+accountID, docType)
}

func (s *AccountService) GetAccountDocument(ctx context.Context, userID, accountID, docID string) (*model.AccountDocumentDetail, error) {
	if err := s.requireMember(ctx, userID, accountID); err != nil {
		return nil, err
	}
	doc, err := s.getDoc(ctx, model.PrefixAccount+accountID, docID)
	if err != nil {
		return nil, err
	}
	dto := toDocumentDTO(doc)
	// PublicShareToken must come from doc directly, not toDocumentDTO -- it
	// lives on AccountDocumentDetail (json:"publicShareToken"), not the
	// embedded AccountDocumentDTO. Without it, DocDetailClient.tsx's
	// setPublicToken(detail.publicShareToken) always sees undefined on
	// load, so an already-shared slide shows "공개 링크 만들기" (create) again
	// after a refresh instead of "공개 링크 해제" (revoke) + the copy button.
	return &model.AccountDocumentDetail{AccountDocumentDTO: dto, Content: doc.Content, FileKey: doc.FileKey, PublicShareToken: doc.PublicShareToken}, nil
}

func (s *AccountService) UpdateAccountDocument(ctx context.Context, userID, accountID, docID string, req *model.PutDocumentRequest) (*model.AccountDocumentDTO, error) {
	if err := s.requireMember(ctx, userID, accountID); err != nil {
		return nil, err
	}
	return s.updateDoc(ctx, userID, model.PrefixAccount+accountID, docID, req)
}

// deleteDoc maps the repository's conditional-check failure (item didn't
// exist) to ErrNotFound, so a delete of an already-gone/never-existed docID
// returns 404 as documented, rather than a bare 204. If the document was a
// slide AND the caller owns its current fileKey (docs/{userID}/ prefix),
// its S3 object is also removed -- otherwise a delete only clears the
// DynamoDB item, leaving a (possibly confidential) file in the bucket
// indefinitely with nothing left in the app pointing at it.
//
// The ownership check matters for account docs: FileKey is scoped to
// docs/{its own owner}/, not to the account, but any account member can
// delete an account doc. Without this check, a non-uploader member could
// trigger a DeleteObject on a key that's actually another user's -- and if
// that user (deliberately or by coincidence) reused the same key on an
// unrelated personal document, this delete would destroy it too, which
// account-level delete permission was never meant to authorize. This must
// check the key's OWN prefix, not doc.SourceUserID -- SourceUserID is
// fixed at doc creation and never updated when a later editor replaces the
// fileKey, so it can drift to point at a different user than the one the
// current fileKey actually belongs to.
// ponytail: this leaves such a slide's S3 object orphaned when a non-
// owner member deletes it (no cleanup at all, not just skipped-for-now)
// -- accepted since the safe alternative (fixing it) needs a real owner/
// reference-count check the domain doesn't have yet. Upgrade path: an S3
// lifecycle rule on docs/, or a fileKey usage-count check before delete.
func (s *AccountService) deleteDoc(ctx context.Context, userID, pk, docID string) error {
	doc, err := s.getDoc(ctx, pk, docID)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteAccountDocument(ctx, pk, docID); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return ErrNotFound // deleted concurrently between our Get and Delete
		}
		return err
	}
	if doc.PublicShareToken != "" {
		// Best-effort, same reasoning as the S3 cleanup below: the DynamoDB
		// doc item is already gone, so a failure here just leaves an
		// orphaned PUBSHARE# pointer (ResolvePublicShare's getDoc lookup on
		// the now-deleted doc will 404 it, so it's inert, not exploitable)
		// rather than blocking the delete the caller already committed to.
		if err := s.repo.DeletePublicShare(ctx, doc.PublicShareToken); err != nil {
			log.Printf("cleanup public share pointer for deleted doc %s: %v", docID, err)
		}
	}
	if doc.FileKey != "" && ownsFileKey(userID, doc.FileKey) && s.s3 != nil {
		// Best-effort: the DynamoDB item is already gone, so a failure here
		// just leaves an orphaned S3 object rather than blocking (or
		// un-doing) a delete the caller already committed to.
		if _, err := s.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(s.bucketName),
			Key:    aws.String(doc.FileKey),
		}); err != nil {
			log.Printf("cleanup S3 object for deleted doc %s (key %s): %v", docID, doc.FileKey, err)
		}
		if sidecarKey := SidecarPDFKey(doc.FileKey); sidecarKey != "" {
			if _, err := s.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: aws.String(s.bucketName),
				Key:    aws.String(sidecarKey),
			}); err != nil {
				log.Printf("cleanup PDF sidecar for deleted doc %s (key %s): %v", docID, sidecarKey, err)
			}
		}
	}
	return nil
}

func (s *AccountService) DeleteAccountDocument(ctx context.Context, userID, accountID, docID string) error {
	if err := s.requireMember(ctx, userID, accountID); err != nil {
		return err
	}
	return s.deleteDoc(ctx, userID, model.PrefixAccount+accountID, docID)
}

// Personal (account-less) documents: ownership is implicit in the PK, which
// is always built from the authenticated userID -- no membership check needed.

func (s *AccountService) PutUserDocument(ctx context.Context, userID string, req *model.PutDocumentRequest) (*model.AccountDocumentDTO, error) {
	return s.putDoc(ctx, userID, model.PrefixUser+userID, "", req, model.EntityTypeUserDoc)
}

// ListUserDocuments returns the caller's own documents plus the ones other
// users shared directly with them (read-only; see ShareUserDocumentByEmail).
// A shared doc is READ through the owner's partition rather than copied, so
// the recipient always sees the current content.
func (s *AccountService) ListUserDocuments(ctx context.Context, userID, docType string) ([]model.AccountDocumentDTO, error) {
	out, err := s.listDocs(ctx, model.PrefixUser+userID, docType)
	if err != nil {
		return nil, err
	}
	shares, err := s.repo.ListDocSharesForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, sh := range shares {
		doc, err := s.repo.GetAccountDocument(ctx, model.PrefixUser+sh.OwnerID, sh.MeetingID)
		if err != nil {
			return nil, err
		}
		// A doc the owner deleted leaves its share rows behind (no cascade);
		// skip rather than fail the whole list.
		if doc == nil || (docType != "" && doc.DocType != docType) {
			continue
		}
		dto := toDocumentDTO(doc)
		dto.SharedBy = sh.OwnerEmail
		out = append(out, dto)
	}
	return out, nil
}

func (s *AccountService) GetUserDocument(ctx context.Context, userID, docID string) (*model.AccountDocumentDetail, error) {
	doc, err := s.getDoc(ctx, model.PrefixUser+userID, docID)
	if errors.Is(err, ErrNotFound) {
		return s.getSharedUserDocument(ctx, userID, docID)
	}
	if err != nil {
		return nil, err
	}
	dto := toDocumentDTO(doc)
	return &model.AccountDocumentDetail{AccountDocumentDTO: dto, Content: doc.Content, FileKey: doc.FileKey, PublicShareToken: doc.PublicShareToken}, nil
}

// getSharedUserDocument resolves a document shared TO userID. PublicShareToken
// is deliberately not surfaced: minting/revoking a public link is the owner's
// call alone, and the recipient has no need for the token itself.
func (s *AccountService) getSharedUserDocument(ctx context.Context, userID, docID string) (*model.AccountDocumentDetail, error) {
	share, err := s.repo.GetDocShare(ctx, userID, docID)
	if err != nil {
		return nil, err
	}
	if share == nil {
		return nil, ErrNotFound
	}
	doc, err := s.getDoc(ctx, model.PrefixUser+share.OwnerID, docID)
	if err != nil {
		return nil, err
	}
	dto := toDocumentDTO(doc)
	dto.SharedBy = share.OwnerEmail
	return &model.AccountDocumentDetail{AccountDocumentDTO: dto, Content: doc.Content, FileKey: doc.FileKey}, nil
}

// ShareUserDocumentByEmail shares a personal document with one other user by
// reference (contrast ShareUserDocumentToAccount, which copies): the
// recipient reads the owner's item, so edits propagate and a revoke actually
// revokes. Read-only by construction -- CreateDocShare hardcodes
// PermissionRead and no update/delete path accepts a non-owner caller.
func (s *AccountService) ShareUserDocumentByEmail(ctx context.Context, ownerID, ownerEmail, docID, targetEmail string) (*model.Share, error) {
	if _, err := s.getDoc(ctx, model.PrefixUser+ownerID, docID); err != nil {
		return nil, err
	}
	targetUser, err := s.repo.GetUserByEmail(ctx, targetEmail)
	if err != nil {
		return nil, err
	}
	if targetUser == nil {
		return nil, ErrUserNotFound
	}
	if targetUser.UserID == ownerID {
		return nil, ErrSelfShare
	}
	return s.repo.CreateDocShare(ctx, docID, ownerID, ownerEmail, targetUser.UserID, targetEmail)
}

// RevokeUserDocShare removes a per-user document share (owner only).
func (s *AccountService) RevokeUserDocShare(ctx context.Context, ownerID, docID, sharedToID string) error {
	if _, err := s.getDoc(ctx, model.PrefixUser+ownerID, docID); err != nil {
		return err
	}
	return s.repo.DeleteDocShare(ctx, sharedToID, docID)
}

// ListUserDocShares lists who a document is shared with (owner only).
func (s *AccountService) ListUserDocShares(ctx context.Context, ownerID, docID string) ([]model.ShareResponse, error) {
	if _, err := s.getDoc(ctx, model.PrefixUser+ownerID, docID); err != nil {
		return nil, err
	}
	shares, err := s.repo.ListDocSharesForDoc(ctx, docID)
	if err != nil {
		return nil, err
	}
	out := make([]model.ShareResponse, 0, len(shares))
	for _, sh := range shares {
		out = append(out, model.ShareResponse{UserID: sh.SharedToID, Email: sh.Email, Permission: sh.Permission})
	}
	return out, nil
}

func (s *AccountService) UpdateUserDocument(ctx context.Context, userID, docID string, req *model.PutDocumentRequest) (*model.AccountDocumentDTO, error) {
	return s.updateDoc(ctx, userID, model.PrefixUser+userID, docID, req)
}

func (s *AccountService) DeleteUserDocument(ctx context.Context, userID, docID string) error {
	return s.deleteDoc(ctx, userID, model.PrefixUser+userID, docID)
}

// ShareUserDocumentToAccount copies a personal document into an account's
// document list. A slide's S3 object is COPIED to a fresh key under the
// sharer's own docs/{userID}/ prefix rather than referenced by the original
// key: deleteDoc and updateDoc both delete/replace the S3 object whenever
// their caller owns its key, so a same-key "link" would leave the shared
// copy pointing at a file the original owner can delete or overwrite out
// from under it. Copying also gives the shared copy its own PPTX->PDF
// sidecar conversion for free (the copy re-triggers the S3 upload event).
func (s *AccountService) ShareUserDocumentToAccount(ctx context.Context, userID, docID, accountID string) (*model.AccountDocumentDTO, error) {
	doc, err := s.getDoc(ctx, model.PrefixUser+userID, docID)
	if err != nil {
		return nil, err
	}
	if err := s.requireMember(ctx, userID, accountID); err != nil {
		return nil, err
	}

	req := &model.PutDocumentRequest{
		Title:   doc.Title,
		DocType: doc.DocType,
		Path:    doc.Path,
	}

	if doc.FileKey != "" {
		if s.s3 == nil {
			return nil, errors.New("s3 client not configured")
		}
		base := doc.FileKey
		if idx := strings.LastIndex(base, "/"); idx >= 0 {
			base = base[idx+1:]
		}
		// generateID() (not just the millisecond timestamp) guarantees
		// uniqueness -- sharing the same doc to two accounts (or re-sharing
		// it) within the same millisecond would otherwise collide on an
		// identical key, so the second CopyObject silently overwrites the
		// first share's object; deleting either share later then destroys
		// the file the other share still references.
		newKey := fmt.Sprintf("docs/%s/%d_%s_%s", userID, time.Now().UnixMilli(), generateID(), base)
		// x-amz-copy-source (what CopySource becomes) must be URL-encoded --
		// sanitizeFileName only replaces whitespace/path separators in the
		// *uploaded* name, so a Korean or otherwise non-ASCII original
		// fileName (routine for this product) survives into doc.FileKey and
		// would break an unencoded CopySource. Escaped per path segment (not
		// the whole key at once) so the "/" separators between segments
		// stay literal instead of becoming %2F.
		copySource := fmt.Sprintf("%s/%s", s.bucketName, encodeS3CopySourceKey(doc.FileKey))
		if _, err := s.s3.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket:     aws.String(s.bucketName),
			Key:        aws.String(newKey),
			CopySource: aws.String(copySource),
		}); err != nil {
			return nil, fmt.Errorf("failed to copy slide object: %w", err)
		}
		req.FileKey = newKey
		req.FileName = doc.FileName
		req.MimeType = doc.MimeType
		req.FileSize = doc.FileSize
	} else {
		md := doc.Content
		req.Markdown = &md
	}

	return s.putDoc(ctx, userID, model.PrefixAccount+accountID, accountID, req, model.EntityTypeAccountDoc)
}

// CreateUserDocPublicShare mints (or returns the existing) unauthenticated
// share token for a personal SLIDE document. Idempotent -- calling it again
// on an already-shared doc returns the same token rather than minting a
// second one (which would leave the first token orphaned but still valid).
// Markdown docs are out of scope for now (see ErrInvalidInput below);
// nothing about the token/pointer design prevents adding them later.
func (s *AccountService) CreateUserDocPublicShare(ctx context.Context, userID, docID string) (string, error) {
	doc, err := s.getDoc(ctx, model.PrefixUser+userID, docID)
	if err != nil {
		return "", err
	}
	if doc.FileKey == "" {
		return "", ErrInvalidInput // slides only
	}
	if doc.PublicShareToken != "" {
		return doc.PublicShareToken, nil
	}
	token := generateID() // 32 hex chars, crypto/rand -- see research.go
	if err := s.repo.PutPublicShare(ctx, &model.PublicShare{
		PK: model.PrefixPubShare + token, SK: model.SKPubShare,
		Token: token, DocPK: model.PrefixUser + userID, DocID: docID,
		CreatedAt: time.Now().UTC(), EntityType: model.EntityTypePubShare,
	}); err != nil {
		return "", err
	}
	// Conditional: guards against a concurrent double-mint (two requests
	// both read PublicShareToken=="" above before either writes) --
	// without this, the second doc write would silently clobber the
	// first's token, leaving that caller holding a token whose pointer
	// exists but no longer matches the doc (see SetPublicShareTokenIfAbsent).
	if err := s.repo.SetPublicShareTokenIfAbsent(ctx, doc.PK, docID, token); err != nil {
		if delErr := s.repo.DeletePublicShare(ctx, token); delErr != nil {
			log.Printf("cleanup orphaned public share pointer %s: %v", token, delErr)
		}
		if !errors.Is(err, repository.ErrConditionFailed) {
			return "", err
		}
		// The condition can fail for two different reasons -- distinguish
		// them instead of assuming it's always "lost a double-mint race":
		fresh, getErr := s.getDoc(ctx, model.PrefixUser+userID, docID)
		if getErr != nil {
			return "", getErr
		}
		if fresh.FileKey == "" {
			// A concurrent file->markdown conversion cleared fileKey
			// between our upfront check and this write -- not a race
			// against another mint. Returning fresh.PublicShareToken
			// here (== "" in this case) would hand back an empty string
			// as if it were a valid token instead of surfacing the error
			// the upfront FileKey=="" check would have given had it run
			// a moment later.
			return "", ErrInvalidInput
		}
		// Otherwise: lost the race -- another request's token is now
		// canonical; hand the caller that one instead of a token whose
		// pointer we just deleted.
		return fresh.PublicShareToken, nil
	}
	return token, nil
}

// RevokeUserDocPublicShare deletes the share pointer and clears the doc's
// token. A no-op (not an error) if the doc was never shared.
func (s *AccountService) RevokeUserDocPublicShare(ctx context.Context, userID, docID string) error {
	doc, err := s.getDoc(ctx, model.PrefixUser+userID, docID)
	if err != nil {
		return err
	}
	if doc.PublicShareToken == "" {
		return nil
	}
	// Clear the field FIRST, delete the pointer SECOND -- not the reverse.
	// If the pointer were deleted first, a CreateUserDocPublicShare racing
	// in between would read the still-unchanged token via its own
	// idempotent "already shared" path and hand a caller a token whose
	// pointer is already gone (dead on arrival). Clearing first means
	// that window never exists: by the time the field reads as cleared,
	// the pointer is guaranteed to still be valid, so the worst case this
	// ordering can produce is an inert orphan pointer (fails closed via
	// ResolvePublicShare's own token-match check) -- not a token handed
	// to a caller that's already broken.
	//
	// Conditional REMOVE on just the token field, not PutAccountDocument of
	// the whole snapshot: a plain overwrite would clobber a concurrent
	// CreateUserDocPublicShare's newer token (or any other field edited
	// between our getDoc and this write) with stale data. A condition
	// failure means the token already changed out from under us -- this
	// revoke's specific intent (invalidate the token we read) has been
	// superseded, not failed, so it's not an error.
	if err := s.repo.ClearPublicShareTokenIfMatches(ctx, doc.PK, docID, doc.PublicShareToken); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return nil
		}
		return err
	}
	if err := s.repo.DeletePublicShare(ctx, doc.PublicShareToken); err != nil {
		log.Printf("cleanup public share pointer for doc %s: %v", docID, err)
	}
	return nil
}

// ResolvePublicShare is the ONLY entry point the unauthenticated
// /api/public/docs/{token} route may call -- it does no caller-identity
// check by design (that's the point of a public link), so nothing else in
// AccountService should reuse this lookup path for an authenticated flow.
func (s *AccountService) ResolvePublicShare(ctx context.Context, token string) (*model.AccountDocument, error) {
	share, err := s.repo.GetPublicShare(ctx, token)
	if err != nil {
		return nil, err
	}
	if share == nil {
		return nil, ErrNotFound
	}
	doc, err := s.getDoc(ctx, share.DocPK, share.DocID)
	if err != nil {
		return nil, err
	}
	if doc.FileKey == "" || doc.PublicShareToken != token {
		// Defensive: a doc that's been re-saved without a fileKey (shouldn't
		// happen for a slide) or whose token was revoked/rotated after this
		// pointer's read but before this check -- either way, not shareable.
		return nil, ErrNotFound
	}
	return doc, nil
}
