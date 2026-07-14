package service

import (
	"context"
	"errors"
	"regexp"
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
	ListAccountMembers(ctx context.Context, accountID string) ([]model.AccountMember, error)
	ListAccountsForUser(ctx context.Context, userID string) ([]model.AccountMember, error)
	GetUserByEmail(ctx context.Context, email string) (*model.User, error)
	ListMeetingRefsForAccount(ctx context.Context, accountID string) ([]model.MeetingRef, error)
	ListInsightsForAccount(ctx context.Context, accountID string) ([]model.AccountInsight, error)
	PutAccountDocument(ctx context.Context, doc *model.AccountDocument) error
	ListAccountDocuments(ctx context.Context, pk string) ([]model.AccountDocument, error)
	GetAccountDocument(ctx context.Context, pk, docID string) (*model.AccountDocument, error)
	DeleteAccountDocument(ctx context.Context, pk, docID string) error
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
var wikilinkRE = regexp.MustCompile(`\[\[([^\[\]|#]+)`)

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

func toDocumentDTO(d *model.AccountDocument) model.AccountDocumentDTO {
	return model.AccountDocumentDTO{
		DocID: d.DocID, Title: d.Title, DocType: d.DocType, Path: d.Path,
		Links: d.Links, FileName: d.FileName, SourceUserID: d.SourceUserID,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

// putDoc is the shared create core for both account-scoped (accountID set)
// and personal (accountID empty, pk is model.PrefixUser+userID) documents. A
// slide upload carries FileKey instead of Markdown -- ownership of that S3
// key is enforced here (must be under docs/{userID}/) since it's the only
// place a client-supplied key gets trusted.
// validateDocRequest is the single check shared by create (putDoc) and
// update (updateDoc) so both enforce the same invariants: a title, either
// markdown or a fileKey (never neither), and -- critically -- that a
// caller-supplied fileKey is actually owned by userID. This is the only
// place a client-supplied S3 key is trusted (see ADR-020); skipping it on
// update would let a caller point their own doc's fileKey at another
// user's object and get a presigned download URL for it.
func validateDocRequest(userID string, req *model.PutDocumentRequest) error {
	if strings.TrimSpace(req.Title) == "" {
		return ErrInvalidInput
	}
	if req.FileKey != "" {
		if !strings.HasPrefix(req.FileKey, "docs/"+userID+"/") {
			return ErrForbidden
		}
	} else if strings.TrimSpace(req.Markdown) == "" {
		return ErrInvalidInput
	}
	if req.Markdown != "" {
		if hasTtobakOriginMarker(req.Markdown) {
			return ErrLoopGuard
		}
		if len(req.Markdown) > maxInlineDocBytes {
			return ErrInvalidInput
		}
	}
	return nil
}

func (s *AccountService) putDoc(ctx context.Context, userID, pk, accountID string, req *model.PutDocumentRequest, entityType string) (*model.AccountDocumentDTO, error) {
	if err := validateDocRequest(userID, req); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	docID := uuid.NewString()
	doc := &model.AccountDocument{
		PK: pk, SK: model.PrefixDoc + docID,
		AccountID: accountID, DocID: docID, Title: strings.TrimSpace(req.Title),
		DocType: req.DocType, Path: req.Path, Content: req.Markdown,
		Links:    parseWikilinks(req.Markdown),
		FileKey:  req.FileKey, FileName: req.FileName, MimeType: req.MimeType, FileSize: req.FileSize,
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

// updateDoc is a full replace of title/docType/path/markdown/file fields on
// an existing doc (preserving DocID/SourceUserID/CreatedAt/EntityType),
// re-running the same validateDocRequest + wikilink parsing as create --
// including the fileKey ownership check, since PUT accepts a fileKey just
// like POST does. Because it's a full replace, converting a slide back to
// a note (fileKey omitted, markdown supplied) clears the old file fields,
// and vice versa.
func (s *AccountService) updateDoc(ctx context.Context, userID, pk, docID string, req *model.PutDocumentRequest) (*model.AccountDocumentDTO, error) {
	existing, err := s.getDoc(ctx, pk, docID)
	if err != nil {
		return nil, err
	}
	if err := validateDocRequest(userID, req); err != nil {
		return nil, err
	}
	existing.Title = strings.TrimSpace(req.Title)
	existing.DocType = req.DocType
	existing.Path = req.Path
	existing.Content = req.Markdown
	existing.Links = parseWikilinks(req.Markdown)
	existing.FileKey, existing.FileName, existing.MimeType, existing.FileSize = req.FileKey, req.FileName, req.MimeType, req.FileSize
	existing.UpdatedAt = time.Now().UTC()
	if err := s.repo.PutAccountDocument(ctx, existing); err != nil {
		return nil, err
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
	return &model.AccountDocumentDetail{AccountDocumentDTO: dto, Content: doc.Content, FileKey: doc.FileKey}, nil
}

func (s *AccountService) UpdateAccountDocument(ctx context.Context, userID, accountID, docID string, req *model.PutDocumentRequest) (*model.AccountDocumentDTO, error) {
	if err := s.requireMember(ctx, userID, accountID); err != nil {
		return nil, err
	}
	return s.updateDoc(ctx, userID, model.PrefixAccount+accountID, docID, req)
}

func (s *AccountService) DeleteAccountDocument(ctx context.Context, userID, accountID, docID string) error {
	if err := s.requireMember(ctx, userID, accountID); err != nil {
		return err
	}
	return s.repo.DeleteAccountDocument(ctx, model.PrefixAccount+accountID, docID)
}

// Personal (account-less) documents: ownership is implicit in the PK, which
// is always built from the authenticated userID -- no membership check needed.

func (s *AccountService) PutUserDocument(ctx context.Context, userID string, req *model.PutDocumentRequest) (*model.AccountDocumentDTO, error) {
	return s.putDoc(ctx, userID, model.PrefixUser+userID, "", req, model.EntityTypeUserDoc)
}

func (s *AccountService) ListUserDocuments(ctx context.Context, userID, docType string) ([]model.AccountDocumentDTO, error) {
	return s.listDocs(ctx, model.PrefixUser+userID, docType)
}

func (s *AccountService) GetUserDocument(ctx context.Context, userID, docID string) (*model.AccountDocumentDetail, error) {
	doc, err := s.getDoc(ctx, model.PrefixUser+userID, docID)
	if err != nil {
		return nil, err
	}
	dto := toDocumentDTO(doc)
	return &model.AccountDocumentDetail{AccountDocumentDTO: dto, Content: doc.Content, FileKey: doc.FileKey}, nil
}

func (s *AccountService) UpdateUserDocument(ctx context.Context, userID, docID string, req *model.PutDocumentRequest) (*model.AccountDocumentDTO, error) {
	return s.updateDoc(ctx, userID, model.PrefixUser+userID, docID, req)
}

func (s *AccountService) DeleteUserDocument(ctx context.Context, userID, docID string) error {
	return s.repo.DeleteAccountDocument(ctx, model.PrefixUser+userID, docID)
}
