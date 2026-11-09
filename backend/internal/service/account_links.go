package service

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// ErrLinkSuppressed means an auto link was removed by a member earlier and
// must not be re-created for the same pair. Callers skip it silently.
var ErrLinkSuppressed = errors.New("account link suppressed")

const (
	accountLinksPageSize      = 100
	techLinkSourceID          = "__tech__"
	maxAutoLinkEvidenceRunes  = 300
	maxAutoLinkModelIDBytes   = 200
	accountLinkCreateAttempts = 2
)

type accountLinksRepo interface {
	GetAccount(context.Context, string) (*model.Account, error)
	GetMember(context.Context, string, string) (*model.AccountMember, error)
	GetProject(context.Context, string) (*model.Project, error)
	GetMeetingPublication(context.Context, string, string) (*model.Meeting, error)
	GetAccountEvent(context.Context, string, string) (*model.AccountEvent, string, error)
	PutAccountLink(context.Context, *model.AccountLink, string) error
	GetAccountLink(context.Context, string, model.AccountLinkInput) (*model.AccountLink, error)
	DeleteAccountLink(context.Context, string, string, model.AccountLinkInput, bool) error
	ListAccountLinks(context.Context, string, string, int32) ([]model.AccountLink, *string, error)
}

type accountLinkDocuments interface {
	GetDocument(ctx context.Context, sourceID, docHash string) (*model.CrawledDocument, error)
}

// accountLinkSources lists the account's revalidated news sources
// (AccountNewsService.accountSources).
type accountLinkSources interface {
	accountSources(ctx context.Context, accountID string) ([]model.CrawlerSource, error)
}

// AccountLinksService manages typed links between account items. Every
// request checks direct membership; reads revalidate each endpoint.
type AccountLinksService struct {
	repo    accountLinksRepo
	docs    accountLinkDocuments
	sources accountLinkSources
	now     func() time.Time
}

func NewAccountLinksService(repo accountLinksRepo, docs accountLinkDocuments, news *AccountNewsService) *AccountLinksService {
	return &AccountLinksService{repo: repo, docs: docs, sources: news, now: time.Now}
}

func (s *AccountLinksService) authorize(ctx context.Context, accountID, userID string) (*model.AccountMember, error) {
	if validateAccountHierarchyIDs(accountID, "") != nil || validateAccountHierarchyIDs(userID, "") != nil {
		return nil, ErrInvalidInput
	}
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrForbidden
	}
	member, err := s.repo.GetMember(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	if member == nil {
		return nil, ErrForbidden
	}
	return member, nil
}

func validLinkPlainID(id string) bool { return validateAccountHierarchyIDs(id, "") == nil }

// splitLinkDocID splits {sourceId}:{docHash}.
func splitLinkDocID(id string) (string, string, bool) {
	source, hash, ok := strings.Cut(id, ":")
	return source, hash, ok && validNewsSourceID(hash)
}

// splitMeetingLinkID splits {ownerUserId}:{meetingId}.
func splitMeetingLinkID(id string) (string, string, bool) {
	owner, meeting, ok := strings.Cut(id, ":")
	return owner, meeting, ok && validLinkPlainID(owner) && validLinkPlainID(meeting)
}

// validateLinkShape checks the pair and each kind's ID grammar, with no reads.
func validateLinkShape(in model.AccountLinkInput) error {
	if model.ValidateAccountLink(in) != nil {
		return ErrInvalidInput
	}
	ok := false
	switch in.FromKind {
	case model.LinkKindNews:
		source, _, valid := splitLinkDocID(in.FromID)
		ok = valid && validNewsSourceID(source)
	case model.LinkKindTech:
		source, _, valid := splitLinkDocID(in.FromID)
		ok = valid && source == techLinkSourceID
	case model.LinkKindEvent:
		ok = validContactID(in.FromID)
	}
	switch in.ToKind {
	case model.LinkKindProject:
		ok = ok && validLinkPlainID(in.ToID)
	case model.LinkKindMeeting:
		_, _, valid := splitMeetingLinkID(in.ToID)
		ok = ok && valid
	default:
		ok = false
	}
	if !ok {
		return ErrInvalidInput
	}
	return nil
}

// sourceSet returns the IDs of the account's current, revalidated sources.
func (s *AccountLinksService) sourceSet(ctx context.Context, accountID string) (map[string]bool, error) {
	sources, err := s.sources.accountSources(ctx, accountID)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(sources))
	for _, src := range sources {
		set[src.SourceID] = true
	}
	return set, nil
}

func (s *AccountLinksService) document(ctx context.Context, sourceID, docHash, docType string) (bool, error) {
	doc, err := s.docs.GetDocument(ctx, sourceID, docHash)
	if errors.Is(err, repository.ErrInvalidDocument) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return doc != nil && doc.QualityStatus != "DELETED" && doc.Type == docType, nil
}

func (s *AccountLinksService) meetingPublished(ctx context.Context, accountID, id string) (*model.Meeting, error) {
	owner, meetingID, _ := splitMeetingLinkID(id)
	meeting, err := s.repo.GetMeetingPublication(ctx, owner, meetingID)
	if err != nil {
		return nil, err
	}
	if !meetingPublishedToAccount(meeting, accountID, owner, meetingID) {
		return nil, nil
	}
	return meeting, nil
}

// validateEndpoints checks that both endpoints currently exist in the
// account. A failed check is ErrInvalidInput, revealing nothing more.
func (s *AccountLinksService) validateEndpoints(ctx context.Context, accountID string, in model.AccountLinkInput) error {
	ok := false
	var err error
	switch in.FromKind {
	case model.LinkKindNews:
		source, hash, _ := splitLinkDocID(in.FromID)
		var set map[string]bool
		if set, err = s.sourceSet(ctx, accountID); err != nil {
			return err
		}
		if set[source] {
			ok, err = s.document(ctx, source, hash, "news")
		}
	case model.LinkKindTech:
		_, hash, _ := splitLinkDocID(in.FromID)
		ok, err = s.document(ctx, techLinkSourceID, hash, "tech")
	case model.LinkKindEvent:
		var event *model.AccountEvent
		event, _, err = s.repo.GetAccountEvent(ctx, accountID, in.FromID)
		ok = event != nil && event.EventID == in.FromID
	}
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidInput
	}
	switch in.ToKind {
	case model.LinkKindProject:
		project, err := s.repo.GetProject(ctx, in.ToID)
		if err != nil {
			return err
		}
		if project == nil || !contains(project.AccountIDs, accountID) {
			return ErrInvalidInput
		}
	case model.LinkKindMeeting:
		meeting, err := s.meetingPublished(ctx, accountID, in.ToID)
		if err != nil {
			return err
		}
		if meeting == nil {
			return ErrInvalidInput
		}
	}
	return nil
}

// Link creates a manual link. An existing link for the pair (manual or auto)
// is returned unchanged.
func (s *AccountLinksService) Link(ctx context.Context, userID, accountID string, in model.AccountLinkInput) (*model.AccountLink, error) {
	if err := validateLinkShape(in); err != nil {
		return nil, err
	}
	if _, err := s.authorize(ctx, accountID, userID); err != nil {
		return nil, err
	}
	if err := s.validateEndpoints(ctx, accountID, in); err != nil {
		return nil, err
	}
	link := &model.AccountLink{AccountID: accountID, FromKind: in.FromKind, FromID: in.FromID, ToKind: in.ToKind, ToID: in.ToID,
		Origin: model.LinkOriginManual, CreatedBy: userID, CreatedAt: s.now().UTC().Format(time.RFC3339)}
	for attempt := 0; attempt < accountLinkCreateAttempts; attempt++ {
		err := s.repo.PutAccountLink(ctx, link, userID)
		switch {
		case err == nil:
			return link, nil
		case errors.Is(err, repository.ErrAccountLinkAccess):
			return nil, ErrForbidden
		case errors.Is(err, repository.ErrAccountLinkTarget):
			return nil, ErrInvalidInput
		case errors.Is(err, repository.ErrAccountLinkExists):
			existing, err := s.repo.GetAccountLink(ctx, accountID, in)
			if err != nil {
				return nil, err
			}
			if existing != nil {
				return existing, nil
			}
			// Removed concurrently: try once more.
		default:
			return nil, err
		}
	}
	return nil, ErrConflict
}

// LinkAuto stores an auto news→meeting link for Task 10. meetingID is the
// link form {ownerUserId}:{meetingId}. It returns nil when the link already
// exists and ErrLinkSuppressed when a member removed it before. The meeting
// must be published to the account and the news must belong to one of the
// account's sources; otherwise ErrInvalidInput.
func (s *AccountLinksService) LinkAuto(ctx context.Context, accountID, newsID, meetingID, evidence, modelID string) error {
	in := model.AccountLinkInput{FromKind: model.LinkKindNews, FromID: newsID, ToKind: model.LinkKindMeeting, ToID: meetingID}
	if err := validateLinkShape(in); err != nil || validateAccountHierarchyIDs(accountID, "") != nil {
		return ErrInvalidInput
	}
	evidence = strings.TrimSpace(evidence)
	if evidence == "" || !utf8.ValidString(evidence) || utf8.RuneCountInString(evidence) > maxAutoLinkEvidenceRunes ||
		modelID == "" || len(modelID) > maxAutoLinkModelIDBytes || !utf8.ValidString(modelID) {
		return ErrInvalidInput
	}
	for _, r := range evidence + modelID {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ErrInvalidInput
		}
	}
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	if account == nil {
		return ErrNotFound
	}
	if err := s.validateEndpoints(ctx, accountID, in); err != nil {
		return err
	}
	link := &model.AccountLink{AccountID: accountID, FromKind: in.FromKind, FromID: in.FromID, ToKind: in.ToKind, ToID: in.ToID,
		Origin: model.LinkOriginAuto, CreatedAt: s.now().UTC().Format(time.RFC3339), Evidence: evidence, ModelID: modelID}
	// The system writer has no member row; the meeting-published and deny
	// checks still bind the transaction.
	err = s.repo.PutAccountLink(ctx, link, "")
	switch {
	case err == nil:
		return nil
	case errors.Is(err, repository.ErrAccountLinkAccess):
		return ErrNotFound
	case errors.Is(err, repository.ErrAccountLinkDenied):
		return ErrLinkSuppressed
	case errors.Is(err, repository.ErrAccountLinkTarget):
		return ErrInvalidInput
	case errors.Is(err, repository.ErrAccountLinkExists):
		return nil
	}
	return err
}

// Unlink removes a link. Any direct member may remove manual or auto links;
// removing an auto link records a deny row so it is not re-created.
func (s *AccountLinksService) Unlink(ctx context.Context, userID, accountID string, in model.AccountLinkInput) error {
	if model.ValidateAccountLink(in) != nil {
		return ErrInvalidInput
	}
	if _, err := s.authorize(ctx, accountID, userID); err != nil {
		return err
	}
	for attempt := 0; attempt < accountLinkCreateAttempts; attempt++ {
		existing, err := s.repo.GetAccountLink(ctx, accountID, in)
		if err != nil {
			return err
		}
		if existing == nil {
			return ErrNotFound
		}
		err = s.repo.DeleteAccountLink(ctx, accountID, userID, in, existing.Origin == model.LinkOriginAuto)
		if errors.Is(err, repository.ErrAccountLinkAccess) {
			return ErrForbidden
		}
		if !errors.Is(err, repository.ErrConditionFailed) {
			return err
		}
		// Deleted or replaced concurrently: reread once.
	}
	return ErrConflict
}

// List returns one page of the account's links whose endpoints are still in
// the account: projects still listing the account, news from the account's
// current sources and meetings still published to it. A page can hold fewer
// rows than the page size while a cursor remains.
func (s *AccountLinksService) List(ctx context.Context, userID, accountID, cursor string) ([]model.AccountLink, *string, error) {
	if len(cursor) > 2048 {
		return nil, nil, ErrInvalidInput
	}
	member, err := s.authorize(ctx, accountID, userID)
	if err != nil {
		return nil, nil, err
	}
	rows, next, err := s.repo.ListAccountLinks(ctx, accountID, cursor, accountLinksPageSize)
	if errors.Is(err, repository.ErrInvalidAccountLinkCursor) {
		return nil, nil, ErrInvalidInput
	}
	if err != nil {
		return nil, nil, err
	}
	var sources map[string]bool
	projects := map[string]bool{}
	meetings := map[string]*model.Meeting{}
	out := make([]model.AccountLink, 0, len(rows))
	for _, link := range rows {
		if validateLinkShape(link.Input()) != nil {
			continue
		}
		if link.FromKind == model.LinkKindNews {
			if sources == nil {
				if sources, err = s.sourceSet(ctx, accountID); err != nil {
					return nil, nil, err
				}
			}
			source, _, _ := splitLinkDocID(link.FromID)
			if !sources[source] {
				continue
			}
		}
		switch link.ToKind {
		case model.LinkKindProject:
			ok, seen := projects[link.ToID]
			if !seen {
				project, err := s.repo.GetProject(ctx, link.ToID)
				if err != nil {
					return nil, nil, err
				}
				ok = project != nil && contains(project.AccountIDs, accountID)
				projects[link.ToID] = ok
			}
			if !ok {
				continue
			}
		case model.LinkKindMeeting:
			meeting, seen := meetings[link.ToID]
			if !seen {
				if meeting, err = s.meetingPublished(ctx, accountID, link.ToID); err != nil {
					return nil, nil, err
				}
				meetings[link.ToID] = meeting
			}
			if meeting == nil {
				continue
			}
			if userID != meeting.UserID && accountMemberNeedsSummaryGuard(member) && !meeting.AccountSummaryMetadataShareable() {
				link.Evidence = ""
			}
		}
		out = append(out, link)
	}
	return out, next, nil
}
