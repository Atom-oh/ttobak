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
	ErrInvalidInput   = errors.New("invalid input")
	ErrMemberExists   = errors.New("member already exists")
	ErrAmbiguousAlias = errors.New("alias maps to multiple accounts")
	ErrLoopGuard      = errors.New("document originated from ttobak (loop guard)")
)

const maxInlineDocBytes = 300 * 1024 // mirror repo transcript inline threshold

// hasTtobakOriginMarker reports whether markdown carries a ttobak_id key in a
// leading YAML frontmatter block (i.e. TTOBAK-origin → must not be re-ingested).
func hasTtobakOriginMarker(markdown string) bool {
	s := strings.TrimSpace(markdown)
	if !strings.HasPrefix(s, "---") {
		return false
	}
	rest := s[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return false
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "ttobak_id:") {
			return true
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
	ListAccountMembers(ctx context.Context, accountID string) ([]model.AccountMember, error)
	ListAccountsForUser(ctx context.Context, userID string) ([]model.AccountMember, error)
	GetUserByEmail(ctx context.Context, email string) (*model.User, error)
	ListMeetingRefsForAccount(ctx context.Context, accountID string) ([]model.MeetingRef, error)
	ListInsightsForAccount(ctx context.Context, accountID string) ([]model.AccountInsight, error)
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
			Type:       ins.Type,
			Text:       ins.Text,
			SourceType: ins.SourceType,
			SourceID:   ins.SourceID,
			OccurredAt: ins.OccurredAt,
			TsMarker:   ins.TsMarker,
			Entities:   ins.Entities,
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
