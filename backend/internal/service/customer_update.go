package service

import (
	"context"
	"encoding/base64"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const (
	maxUpdateLabelRunes   = 100
	defaultUpdateDraftLim = 50
	maxUpdateDraftLimit   = 100
)

var updateURLHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type customerUpdateRepo interface {
	PutSourceIfAbsent(ctx context.Context, userID string, s *model.UpdateSource) error
	ListSources(ctx context.Context, userID string, limit int) ([]model.UpdateSource, error)
	UpdateSource(ctx context.Context, userID, sourceID string, label *string, status *string) error
	DeleteSource(ctx context.Context, userID, sourceID string) error
	PutItemsIfAbsent(ctx context.Context, userID, sourceID string, items []model.UpdateItem) (int, error)
	EnsureURLPending(ctx context.Context, urlHash, url string) error
	MarkPending(ctx context.Context, userID string) error
	GetDraft(ctx context.Context, userID, accountID, urlHash string) (*model.UpdateDraft, error)
	ListDrafts(ctx context.Context, userID, accountID string, limit int, cursor string) ([]model.UpdateDraft, string, error)
	UpdateDraftIfRevision(ctx context.Context, userID string, d *model.UpdateDraft, expectedRevision int) error
	GetAccountOpt(ctx context.Context, userID, accountID string) (bool, error)
	PutAccountOpt(ctx context.Context, userID, accountID string, enabled bool) error
}

// directMemberChecker verifies the caller is a direct member of an account.
// Inherited (ancestor or Billing) read access does not qualify.
type directMemberChecker interface {
	RequireDirectMember(ctx context.Context, userID, accountID string) error
}

type directMemberRepo interface {
	GetAccount(context.Context, string) (*model.Account, error)
	GetMember(context.Context, string, string) (*model.AccountMember, error)
}

// DirectMemberChecker implements directMemberChecker over the account repository.
type DirectMemberChecker struct{ repo directMemberRepo }

// NewDirectMemberChecker wraps the repository methods used by the check.
func NewDirectMemberChecker(repo directMemberRepo) *DirectMemberChecker {
	return &DirectMemberChecker{repo: repo}
}

// RequireDirectMember accepts only a current MEMBER row on an existing account.
func (c *DirectMemberChecker) RequireDirectMember(ctx context.Context, userID, accountID string) error {
	if !resummaryID.MatchString(userID) || validateAccountHierarchyIDs(accountID, "") != nil {
		return ErrInvalidInput
	}
	account, err := c.repo.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	if account == nil {
		return ErrNotFound
	}
	member, err := c.repo.GetMember(ctx, accountID, userID)
	if err != nil {
		return err
	}
	if member == nil || member.UserID != userID {
		return ErrForbidden
	}
	return nil
}

// CustomerUpdateService manages update sources, link uploads and drafts.
// It never logs URLs, labels or draft text.
type CustomerUpdateService struct {
	repo    customerUpdateRepo
	members directMemberChecker
	now     func() time.Time
}

// NewCustomerUpdateService creates a CustomerUpdateService; a nil now uses time.Now.
func NewCustomerUpdateService(repo customerUpdateRepo, members directMemberChecker, now func() time.Time) *CustomerUpdateService {
	if now == nil {
		now = time.Now
	}
	return &CustomerUpdateService{repo: repo, members: members, now: now}
}

func validUpdateLabel(label string) (string, bool) {
	label = strings.TrimSpace(label)
	n := utf8.RuneCountInString(label)
	return label, n >= 1 && n <= maxUpdateLabelRunes
}

// AddSource creates a source for the user, enforcing the per-user source cap.
func (s *CustomerUpdateService) AddSource(ctx context.Context, userID string, req *model.AddUpdateSourceRequest) (*model.UpdateSource, error) {
	if req == nil {
		return nil, ErrInvalidInput
	}
	label, ok := validUpdateLabel(req.Label)
	if !ok {
		return nil, ErrInvalidInput
	}
	src := &model.UpdateSource{Kind: req.Kind, Label: label, Status: model.UpdateSourceActive}
	switch req.Kind {
	case model.UpdateSourceKindRSS, model.UpdateSourceKindSite, model.UpdateSourceKindYouTube:
		if req.LocalSourceID != "" {
			return nil, ErrInvalidInput
		}
		normalized, _, err := model.NormalizeUpdateURL(req.URL)
		if err != nil {
			return nil, ErrInvalidInput
		}
		src.URL = normalized
	case model.UpdateSourceKindSlack:
		if strings.TrimSpace(req.URL) != "" || !resummaryID.MatchString(req.LocalSourceID) {
			return nil, ErrInvalidInput
		}
		src.LocalSourceID = req.LocalSourceID
	default:
		return nil, ErrInvalidInput
	}

	existing, err := s.repo.ListSources(ctx, userID, model.MaxUpdateSources)
	if err != nil {
		return nil, err
	}
	if len(existing) >= model.MaxUpdateSources {
		return nil, ErrConflict
	}
	src.SourceID = uuid.NewString()
	src.CreatedAt = s.now().UTC()
	if err := s.repo.PutSourceIfAbsent(ctx, userID, src); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return nil, ErrConflict
		}
		return nil, err
	}
	return src, nil
}

// ListSources returns the user's sources.
func (s *CustomerUpdateService) ListSources(ctx context.Context, userID string) ([]model.UpdateSource, error) {
	return s.repo.ListSources(ctx, userID, model.MaxUpdateSources)
}

// UpdateSource changes a source's label and/or status.
func (s *CustomerUpdateService) UpdateSource(ctx context.Context, userID, sourceID string, req *model.PatchUpdateSourceRequest) error {
	if req == nil || (req.Label == nil && req.Status == nil) || !resummaryID.MatchString(sourceID) {
		return ErrInvalidInput
	}
	var label *string
	if req.Label != nil {
		l, ok := validUpdateLabel(*req.Label)
		if !ok {
			return ErrInvalidInput
		}
		label = &l
	}
	if req.Status != nil && *req.Status != model.UpdateSourceActive && *req.Status != model.UpdateSourceDisabled {
		return ErrInvalidInput
	}
	if err := s.repo.UpdateSource(ctx, userID, sourceID, label, req.Status); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// DeleteSource removes a source; deleting an absent source succeeds.
func (s *CustomerUpdateService) DeleteSource(ctx context.Context, userID, sourceID string) error {
	if !resummaryID.MatchString(sourceID) {
		return ErrInvalidInput
	}
	return s.repo.DeleteSource(ctx, userID, sourceID)
}

// UploadLinks records links from a local Slack source. Links are normalized;
// rejected links are counted, never stored. Each new link also ensures its
// shared URL cache row, and pending work is marked once if any link was new.
func (s *CustomerUpdateService) UploadLinks(ctx context.Context, userID string, req *model.UploadUpdateLinksRequest) (*model.UploadUpdateLinksResponse, error) {
	if req == nil || !resummaryID.MatchString(req.SourceID) || len(req.Links) > model.MaxUpdateLinksPerUpload {
		return nil, ErrInvalidInput
	}
	sources, err := s.repo.ListSources(ctx, userID, model.MaxUpdateSources)
	if err != nil {
		return nil, err
	}
	var source *model.UpdateSource
	for i := range sources {
		if sources[i].SourceID == req.SourceID && sources[i].Kind == model.UpdateSourceKindSlack {
			source = &sources[i]
			break
		}
	}
	if source == nil {
		return nil, ErrNotFound
	}
	// A paused source accepts no links; the client must resume it first.
	if source.Status == model.UpdateSourceDisabled {
		return nil, ErrConflict
	}

	resp := &model.UploadUpdateLinksResponse{}
	seen := make(map[string]bool, len(req.Links))
	now := s.now().UTC()
	for _, link := range req.Links {
		normalized, hash, err := model.NormalizeUpdateURL(link.URL)
		if err != nil {
			resp.Rejected++
			continue
		}
		if seen[hash] {
			resp.Duplicate++
			continue
		}
		seen[hash] = true
		item := model.UpdateItem{URLHash: hash, URL: normalized, FirstSeenAt: now}
		if t, perr := time.Parse(time.RFC3339, link.SeenAt); perr == nil && !t.After(now) {
			item.FirstSeenAt = t.UTC()
		}
		// Write the shared URL row before the inbox item: it is an idempotent
		// put-if-absent, so a retry can never leave an item without its URL row.
		// An orphan URL row is harmless and expires by TTL.
		if err := s.repo.EnsureURLPending(ctx, hash, normalized); err != nil {
			return nil, s.markAfterPartialUpload(ctx, userID, resp.Accepted, err)
		}
		// One item per call so the created count identifies which links are new.
		created, err := s.repo.PutItemsIfAbsent(ctx, userID, req.SourceID, []model.UpdateItem{item})
		if err != nil {
			return nil, s.markAfterPartialUpload(ctx, userID, resp.Accepted, err)
		}
		if created == 0 {
			resp.Duplicate++
			continue
		}
		resp.Accepted++
	}
	// MarkPending is an idempotent put, so a retry whose links are all duplicates
	// still heals a marker lost to an earlier partial failure.
	if resp.Accepted+resp.Duplicate > 0 {
		if err := s.repo.MarkPending(ctx, userID); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

// markAfterPartialUpload returns cause; when this call already wrote items it
// first marks pending work (best effort) so they are not stranded, joining a
// MarkPending failure with cause.
func (s *CustomerUpdateService) markAfterPartialUpload(ctx context.Context, userID string, written int, cause error) error {
	if written == 0 {
		return cause
	}
	if err := s.repo.MarkPending(ctx, userID); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func updateExpired(expiresAt int64, now time.Time) bool {
	return expiresAt != 0 && expiresAt <= now.Unix()
}

// ListDrafts returns one page of the caller's drafts for one account (the
// caller must be a direct member) or for all accounts. It reads up to
// model.MaxUpdateDraftsScan rows by following repository pages, drops drafts
// past their expiry (even before TTL deletion) and drafts of accounts the
// caller no longer directly belongs to (one membership check per distinct
// account), orders open drafts first, then newest CreatedAt first (ties by
// urlHash, then accountId), and pages that list in memory. The cursor is an
// opaque offset; a malformed or out-of-range cursor returns ErrInvalidInput.
func (s *CustomerUpdateService) ListDrafts(ctx context.Context, userID, accountID string, limit int, cursor string) (*model.UpdateDraftsPage, error) {
	if limit < 1 {
		limit = defaultUpdateDraftLim
	}
	if limit > maxUpdateDraftLimit {
		limit = maxUpdateDraftLimit
	}
	offset, err := decodeDraftOffset(cursor)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	if accountID != "" {
		if err := s.members.RequireDirectMember(ctx, userID, accountID); err != nil {
			return nil, err
		}
		allowed[accountID] = true
	}
	rows, err := s.scanDrafts(ctx, userID, accountID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	visible := make([]model.UpdateDraft, 0, len(rows))
	for _, d := range rows {
		if updateExpired(d.ExpiresAt, now) {
			continue
		}
		ok, checked := allowed[d.AccountID]
		if !checked {
			err := s.members.RequireDirectMember(ctx, userID, d.AccountID)
			switch {
			case err == nil:
				ok = true
			case errors.Is(err, ErrForbidden), errors.Is(err, ErrNotFound), errors.Is(err, ErrInvalidInput):
				ok = false
			default:
				return nil, err
			}
			allowed[d.AccountID] = ok
		}
		if ok {
			d.NormalizeSlices()
			visible = append(visible, d)
		}
	}
	sortUpdateDrafts(visible)

	if offset > len(visible) {
		return nil, ErrInvalidInput
	}
	end := offset + limit
	if end > len(visible) {
		end = len(visible)
	}
	page := &model.UpdateDraftsPage{Drafts: append([]model.UpdateDraft{}, visible[offset:end]...)}
	if end < len(visible) {
		page.NextCursor = encodeDraftOffset(end)
	}
	return page, nil
}

// scanDrafts follows repository pages until the scope is drained or
// model.MaxUpdateDraftsScan rows have been read.
func (s *CustomerUpdateService) scanDrafts(ctx context.Context, userID, accountID string) ([]model.UpdateDraft, error) {
	var out []model.UpdateDraft
	next := ""
	for {
		rows, cursor, err := s.repo.ListDrafts(ctx, userID, accountID, model.MaxUpdateDraftsScan-len(out), next)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if len(out) >= model.MaxUpdateDraftsScan {
			return out[:model.MaxUpdateDraftsScan], nil
		}
		if cursor == "" || cursor == next {
			return out, nil
		}
		next = cursor
	}
}

// sortUpdateDrafts orders open drafts first, then CreatedAt descending, then
// urlHash and accountId ascending so equal timestamps order deterministically.
func sortUpdateDrafts(drafts []model.UpdateDraft) {
	sort.SliceStable(drafts, func(i, j int) bool {
		a, b := drafts[i], drafts[j]
		if ao, bo := a.Status == model.UpdateDraftNew, b.Status == model.UpdateDraftNew; ao != bo {
			return ao
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		if a.URLHash != b.URLHash {
			return a.URLHash < b.URLHash
		}
		return a.AccountID < b.AccountID
	})
}

// encodeDraftOffset renders a list offset as an opaque cursor.
func encodeDraftOffset(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

// decodeDraftOffset parses a cursor from encodeDraftOffset; "" is offset 0.
// Anything that is not a canonical non-negative decimal returns ErrInvalidInput.
func decodeDraftOffset(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	if len(cursor) > 16 {
		return 0, ErrInvalidInput
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, ErrInvalidInput
	}
	n, err := strconv.Atoi(string(raw))
	if err != nil || n < 0 || strconv.Itoa(n) != string(raw) {
		return 0, ErrInvalidInput
	}
	return n, nil
}

// PatchDraft applies one action (edit, adopt, dismiss, reopen) at the caller's
// expected revision. The model-written Body is never changed; edits go to EditedBody.
func (s *CustomerUpdateService) PatchDraft(ctx context.Context, userID, accountID, urlHash string, req *model.PatchUpdateDraftRequest) (*model.UpdateDraft, error) {
	if req == nil || !updateURLHashPattern.MatchString(urlHash) {
		return nil, ErrInvalidInput
	}
	switch req.Action {
	case model.UpdateDraftActionEdit:
		if strings.TrimSpace(req.Body) == "" || utf8.RuneCountInString(req.Body) > model.MaxDraftBodyRunes {
			return nil, ErrInvalidInput
		}
	case model.UpdateDraftActionDismiss:
		switch req.Reason {
		case model.UpdateDismissNotRelevant, model.UpdateDismissAlreadyKnown, model.UpdateDismissNotNow:
		default:
			return nil, ErrInvalidInput
		}
	case model.UpdateDraftActionAdopt, model.UpdateDraftActionReopen:
	default:
		return nil, ErrInvalidInput
	}
	if err := s.members.RequireDirectMember(ctx, userID, accountID); err != nil {
		return nil, err
	}
	cur, err := s.repo.GetDraft(ctx, userID, accountID, urlHash)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	if cur == nil || updateExpired(cur.ExpiresAt, now) {
		return nil, ErrNotFound
	}
	if cur.Revision != req.Revision {
		return nil, ErrConflict
	}

	next := *cur
	switch req.Action {
	case model.UpdateDraftActionEdit:
		next.EditedBody = req.Body
	case model.UpdateDraftActionAdopt:
		next.Status = model.UpdateDraftAdopted
		next.DismissReason = ""
	case model.UpdateDraftActionDismiss:
		next.Status = model.UpdateDraftDismissed
		next.DismissReason = req.Reason
	case model.UpdateDraftActionReopen:
		next.Status = model.UpdateDraftNew
		next.DismissReason = ""
	}
	next.UpdatedAt = now
	if err := s.repo.UpdateDraftIfRevision(ctx, userID, &next, req.Revision); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return nil, ErrConflict
		}
		return nil, err
	}
	next.Revision = req.Revision + 1
	next.NormalizeSlices()
	return &next, nil
}

// GetAccountSetting reports whether updates are enabled for an account.
func (s *CustomerUpdateService) GetAccountSetting(ctx context.Context, userID, accountID string) (bool, error) {
	if err := s.members.RequireDirectMember(ctx, userID, accountID); err != nil {
		return false, err
	}
	return s.repo.GetAccountOpt(ctx, userID, accountID)
}

// PutAccountSetting enables or disables updates for an account.
func (s *CustomerUpdateService) PutAccountSetting(ctx context.Context, userID, accountID string, enabled bool) error {
	if err := s.members.RequireDirectMember(ctx, userID, accountID); err != nil {
		return err
	}
	return s.repo.PutAccountOpt(ctx, userID, accountID, enabled)
}
