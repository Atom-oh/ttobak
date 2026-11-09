package service

import (
	"context"
	"errors"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const (
	maxMentionResolveMeetings = 100
	peopleWriteAttempts       = 2
)

var peopleMentionKey = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidMentionKey reports whether key is a 32-character lowercase hex hash.
func ValidMentionKey(key string) bool { return peopleMentionKey.MatchString(key) }

// manualSourceField is the provenance field of a manual edge of each role.
var manualSourceField = map[string]string{
	model.PeopleRoleParticipant: model.PeopleSourceParticipants,
	model.PeopleRoleSpeaker:     model.PeopleSourceSpeakerMap,
	model.PeopleRoleAssignee:    model.PeopleSourceActionItems,
	model.PeopleRoleMentions:    model.PeopleSourceInsights,
}

func mapPeopleWriteError(err error) error {
	switch {
	case errors.Is(err, repository.ErrAccountLinkAccess):
		return ErrForbidden
	case errors.Is(err, repository.ErrAccountLinkTarget):
		return ErrInvalidInput
	}
	return err
}

// BindEdge stores a manual edge (spec E4). It replaces an auto edge of the
// same key and returns an existing manual edge unchanged.
func (s *AccountPeopleService) BindEdge(ctx context.Context, userID, accountID string, in model.PeopleEdgeInput) (*model.PeopleLink, error) {
	k := model.PeopleLinkKey{MeetingID: in.MeetingID, ToKind: in.ToKind, ToID: in.ToID, Role: in.Role}
	if model.ValidatePeopleLinkKey(k) != nil || len(in.SourceRef) > model.MaxPeopleSourceRefBytes ||
		!utf8.ValidString(in.SourceRef) || hasControlOrFormat(in.SourceRef) {
		return nil, ErrInvalidInput
	}
	v, err := s.viewer(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	published, _, err := v.meeting(ctx, in.MeetingID)
	if err != nil {
		return nil, err
	}
	if published == nil {
		return nil, ErrNotFound
	}
	link := &model.PeopleLink{AccountID: accountID, MeetingID: k.MeetingID, ToKind: k.ToKind, ToID: k.ToID, Role: k.Role,
		Origin: model.LinkOriginManual, SourceField: manualSourceField[k.Role], SourceRef: in.SourceRef,
		CreatedBy: userID, CreatedAt: s.now().UTC().Format(time.RFC3339)}
	if k.Role == model.PeopleRoleSpeaker {
		owner, id, _ := splitMeetingLinkID(in.MeetingID)
		meeting, err := s.repo.GetMeeting(ctx, owner, id)
		if err != nil {
			return nil, err
		}
		if !meetingPublishedTo(meeting) || meeting.AccountID != accountID || meeting.UserID != owner || meeting.MeetingID != id {
			return nil, ErrNotFound
		}
		name, mapped := meeting.SpeakerMap[in.SourceRef]
		if in.SourceRef == "" || (!mapped && !defaultSpeakerLabel.MatchString(in.SourceRef)) {
			return nil, ErrInvalidInput
		}
		if name = strings.TrimSpace(name); !hasControlOrFormat(name) {
			link.Mention = capRunes(name, model.MaxPeopleMentionRunes)
		}
	}
	for attempt := 0; attempt < peopleWriteAttempts; attempt++ {
		err := s.repo.PutPeopleLink(ctx, link, userID)
		if err == nil {
			return link, nil
		}
		if !errors.Is(err, repository.ErrAccountLinkExists) {
			return nil, mapPeopleWriteError(err)
		}
		existing, err := s.repo.GetPeopleLink(ctx, accountID, k)
		if err != nil {
			return nil, err
		}
		if existing != nil && existing.Origin == model.LinkOriginManual {
			return existing, nil
		}
		// Removed or replaced concurrently: try once more.
	}
	return nil, ErrConflict
}

// UnbindEdge removes an edge of a published meeting. Removing an auto edge
// writes a deny row so the resolver never re-creates it.
func (s *AccountPeopleService) UnbindEdge(ctx context.Context, userID, accountID string, k model.PeopleLinkKey) error {
	if model.ValidatePeopleLinkKey(k) != nil {
		return ErrInvalidInput
	}
	v, err := s.viewer(ctx, accountID, userID)
	if err != nil {
		return err
	}
	published, _, err := v.meeting(ctx, k.MeetingID)
	if err != nil {
		return err
	}
	if published == nil {
		return ErrNotFound
	}
	for attempt := 0; attempt < peopleWriteAttempts; attempt++ {
		existing, err := s.repo.GetPeopleLink(ctx, accountID, k)
		if err != nil {
			return err
		}
		if existing == nil {
			return ErrNotFound
		}
		err = s.repo.DeletePeopleLink(ctx, accountID, userID, k, existing.Origin, existing.Origin == model.LinkOriginAuto)
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

// visibleMentionRows reads one hash's rows (bounded) visible to the viewer.
func (s *AccountPeopleService) visibleMentionRows(ctx context.Context, v *peopleViewer, hash string) ([]model.UnresolvedMention, bool, error) {
	rows, more, err := s.repo.ListUnresolvedMentionsByHash(ctx, v.accountID, hash, maxMentionResolveMeetings)
	if err != nil {
		return nil, false, err
	}
	visible := rows[:0]
	for _, r := range rows {
		ok, err := v.visibleMention(ctx, r)
		if err != nil {
			return nil, false, err
		}
		if ok && r.Hash == hash {
			visible = append(visible, r)
		}
	}
	return visible, more, nil
}

// ResolveMentionGroup binds every visible row of one mention group to a
// member or contact with manual edges and removes those rows (spec E5).
func (s *AccountPeopleService) ResolveMentionGroup(ctx context.Context, userID, accountID, hash string, in model.ResolveMentionInput) (*model.ResolveMentionResult, error) {
	if !ValidMentionKey(hash) || (in.Kind != model.LinkKindMember && in.Kind != model.LinkKindContact) ||
		!validLinkPlainID(in.ID) || strings.Contains(in.ID, ":") {
		return nil, ErrInvalidInput
	}
	v, err := s.viewer(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	if _, ok, err := v.label(ctx, in.Kind, in.ID); err != nil || !ok {
		if err == nil {
			err = ErrInvalidInput
		}
		return nil, err
	}
	rows, more, err := s.visibleMentionRows(ctx, v, hash)
	if err != nil {
		return nil, err
	}
	out := &model.ResolveMentionResult{Remaining: more}
	createdAt := s.now().UTC().Format(time.RFC3339)
	for _, r := range rows {
		link := &model.PeopleLink{AccountID: accountID, MeetingID: r.MeetingID, ToKind: in.Kind, ToID: in.ID, Role: r.Role,
			Origin: model.LinkOriginManual, SourceField: r.SourceField, Mention: r.Mention, SourceRef: r.SourceRef,
			CreatedBy: userID, CreatedAt: createdAt}
		if model.ValidatePeopleLinkKey(link.Key()) != nil {
			continue
		}
		err := s.repo.PutPeopleLink(ctx, link, userID)
		if errors.Is(err, repository.ErrAccountLinkExists) {
			// Bound by hand already (another text?): alias this text to it.
			err = s.repo.AddPeopleLinkAlias(ctx, accountID, userID, link.Key(), hash)
			if errors.Is(err, repository.ErrConditionFailed) && !errors.Is(err, repository.ErrAccountLinkAccess) {
				// Edge gone or full: keep the row listed.
				continue
			}
		}
		switch {
		case err == nil:
			out.Written++
		case errors.Is(err, repository.ErrAccountLinkTarget):
			// The meeting was unpublished since the visibility read.
			continue
		default:
			return nil, mapPeopleWriteError(err)
		}
		if err := s.repo.DeleteUnresolvedMention(ctx, accountID, hash, r.MeetingID, r.Role); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// IgnoreMentionGroup marks a mention as "not a person" account-wide and
// removes its visible rows. Later resolver runs skip the hash.
func (s *AccountPeopleService) IgnoreMentionGroup(ctx context.Context, userID, accountID, hash string) error {
	if !ValidMentionKey(hash) {
		return ErrInvalidInput
	}
	v, err := s.viewer(ctx, accountID, userID)
	if err != nil {
		return err
	}
	if err := s.repo.PutMentionIgnore(ctx, accountID, userID, hash); err != nil {
		return mapPeopleWriteError(err)
	}
	rows, _, err := s.visibleMentionRows(ctx, v, hash)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := s.repo.DeleteUnresolvedMention(ctx, accountID, hash, r.MeetingID, r.Role); err != nil {
			return err
		}
	}
	return nil
}

// publishPeopleResolve sends a best-effort resolve request: a failure is
// logged with IDs and a code and never fails the caller's saved change.
func publishPeopleResolve(ctx context.Context, fn func(context.Context, model.PeopleResolveRequested) error, e model.PeopleResolveRequested) {
	if fn == nil {
		return
	}
	if err := fn(ctx, e); err != nil {
		log.Printf("people resolve publish failed (non-fatal): account=%s meeting=%s reason=%s code=%s",
			e.AccountID, e.MeetingID, e.Reason, PeopleRunErrorCode(err))
	}
}
