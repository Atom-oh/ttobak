package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type accountEventsRepo interface {
	GetAccount(context.Context, string) (*model.Account, error)
	GetMember(context.Context, string, string) (*model.AccountMember, error)
	GetAccountEvent(context.Context, string, string) (*model.AccountEvent, string, error)
	ListAccountEvents(context.Context, string, string, string, bool, int, string) ([]model.AccountEvent, string, error)
	WriteAccountEvent(context.Context, string, string, *model.AccountEvent, string, string, string, string) error
	ExistingAccountContactIDs(context.Context, string, []string) (map[string]bool, error)
}

// AccountEventInput is the client-editable part of an event.
type AccountEventInput struct {
	Revision  string                `json:"revision,omitempty"`
	Title     string                `json:"title"`
	StartAt   string                `json:"startAt"`
	EndAt     string                `json:"endAt,omitempty"`
	AllDay    bool                  `json:"allDay,omitempty"`
	Location  string                `json:"location,omitempty"`
	Attendees []model.EventAttendee `json:"attendees,omitempty"`
	Notes     string                `json:"notes,omitempty"`
}

// AccountEventsService manages events for direct account members only, like
// contacts: attendee data is third-party personal data (ADR-021, contact encryption).
type AccountEventsService struct {
	repo accountEventsRepo
	now  func() time.Time
}

var ErrEventConflict = errors.New("event changed concurrently")

// ErrEventsUnavailable means event encryption is not configured.
var ErrEventsUnavailable = repository.ErrEventEncryptionUnavailable

const (
	eventTimeLayout   = "2006-01-02T15:04:05Z"
	maxEventAttendees = 50
	maxEventSpan      = 31 * 24 * time.Hour
	// A serialized event must fit one bridge read (14,000-byte response cap)
	// with room for the page envelope, so every path uses the same bound.
	maxEventJSONBytes = 12000
	eventRangeStart   = "0001-01-01T00:00:00Z"
	eventRangeEnd     = "9999-12-31T23:59:59Z"
)

func NewAccountEventsService(repo accountEventsRepo) *AccountEventsService {
	return &AccountEventsService{repo: repo, now: time.Now}
}

func (s *AccountEventsService) authorize(ctx context.Context, account, user string) error {
	if validateAccountHierarchyIDs(account, "") != nil || validateAccountHierarchyIDs(user, "") != nil {
		return ErrInvalidInput
	}
	a, err := s.repo.GetAccount(ctx, account)
	if err != nil {
		return err
	}
	if a == nil {
		return ErrForbidden
	}
	m, err := s.repo.GetMember(ctx, account, user)
	if err != nil {
		return err
	}
	if m == nil {
		return ErrForbidden
	}
	return nil
}

// eventTime parses RFC 3339 and normalizes to UTC seconds.
func eventTime(v string) (time.Time, string, error) {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil || t.Year() < 1970 || t.Year() > 9998 {
		return time.Time{}, "", ErrInvalidInput
	}
	t = t.UTC().Truncate(time.Second)
	return t, t.Format(eventTimeLayout), nil
}

func boundedText(v string, max int) bool { return len(v) <= max && utf8.ValidString(v) }

// normalizeEvent validates syntax only and returns the contact IDs to resolve;
// it performs no repository access, so unauthorized callers learn nothing.
func normalizeEvent(in AccountEventInput) (model.AccountEvent, []string, error) {
	e := model.AccountEvent{Title: strings.TrimSpace(in.Title), AllDay: in.AllDay, Location: in.Location, Notes: in.Notes}
	if e.Title == "" || !boundedText(e.Title, 256) || !boundedText(e.Location, 512) || !boundedText(e.Notes, 8192) {
		return e, nil, ErrInvalidInput
	}
	start, startKey, err := eventTime(in.StartAt)
	if err != nil {
		return e, nil, err
	}
	e.StartAt = startKey
	if in.EndAt != "" {
		end, endKey, err := eventTime(in.EndAt)
		if err != nil || end.Before(start) || end.Sub(start) > maxEventSpan {
			return e, nil, ErrInvalidInput
		}
		e.EndAt = endKey
	}
	if len(in.Attendees) > maxEventAttendees {
		return e, nil, ErrInvalidInput
	}
	e.Attendees = make([]model.EventAttendee, 0, len(in.Attendees))
	refs := []string{}
	for _, a := range in.Attendees {
		a.Name = strings.TrimSpace(a.Name)
		a.Email = strings.TrimSpace(a.Email)
		if a.Name == "" || !boundedText(a.Name, 256) || !boundedText(a.Email, 256) || (a.Email != "" && !strings.Contains(a.Email, "@")) {
			return e, nil, ErrInvalidInput
		}
		if a.ContactID != "" {
			if !validContactID(a.ContactID) {
				return e, nil, ErrInvalidInput
			}
			refs = append(refs, a.ContactID)
		}
		e.Attendees = append(e.Attendees, a)
	}
	return e, refs, nil
}

// requireContacts resolves attendee references; call it only after authorization.
func (s *AccountEventsService) requireContacts(ctx context.Context, account string, refs []string) error {
	if len(refs) == 0 {
		return nil
	}
	found, err := s.repo.ExistingAccountContactIDs(ctx, account, refs)
	if err != nil {
		return err
	}
	for _, id := range refs {
		if !found[id] {
			return ErrInvalidInput
		}
	}
	return nil
}

func (s *AccountEventsService) Mutate(ctx context.Context, account, user, op, id string, in AccountEventInput) (*model.AccountEvent, error) {
	if op != "create" && op != "update" && op != "delete" {
		return nil, ErrInvalidInput
	}
	if op != "create" && (!validContactID(id) || !validContactID(in.Revision)) {
		return nil, ErrInvalidInput
	}
	if op == "create" && in.Revision != "" {
		return nil, ErrInvalidInput
	}
	var next model.AccountEvent
	var refs []string
	if op != "delete" {
		normalized, ids, err := normalizeEvent(in)
		if err != nil {
			return nil, err
		}
		next, refs = normalized, ids
	}
	if err := s.authorize(ctx, account, user); err != nil {
		return nil, err
	}
	if err := s.requireContacts(ctx, account, refs); err != nil {
		return nil, err
	}
	now := s.now().UTC().Format(time.RFC3339)
	previousSK := ""
	switch op {
	case "create":
		next.EventID, next.Revision = uuid.NewString(), uuid.NewString()
		next.CreatedBy, next.CreatedAt, next.UpdatedAt = user, now, now
	default:
		current, sk, err := s.repo.GetAccountEvent(ctx, account, id)
		if err != nil {
			return nil, err
		}
		if current == nil {
			return nil, ErrNotFound
		}
		if current.Revision != in.Revision {
			return nil, ErrEventConflict
		}
		previousSK = sk
		if op == "update" {
			next.EventID, next.Revision = id, uuid.NewString()
			next.CreatedBy, next.CreatedAt, next.UpdatedAt = current.CreatedBy, current.CreatedAt, now
		} else {
			next = *current
		}
	}
	if op != "delete" {
		encoded, err := json.Marshal(next)
		if err != nil || len(encoded) > maxEventJSONBytes {
			return nil, ErrInvalidInput
		}
	}
	newSK := ""
	if op != "delete" {
		newSK = repository.EventSortKey(next.StartAt, next.EventID)
	}
	err := s.repo.WriteAccountEvent(ctx, account, user, &next, op, newSK, previousSK, in.Revision)
	if errors.Is(err, repository.ErrEventAccess) {
		return nil, ErrForbidden
	}
	if errors.Is(err, repository.ErrConditionFailed) {
		if op == "create" {
			return nil, err
		}
		if err := s.authorize(ctx, account, user); err != nil {
			return nil, err
		}
		current, _, getErr := s.repo.GetAccountEvent(ctx, account, id)
		if getErr != nil {
			return nil, getErr
		}
		if current == nil {
			return nil, ErrNotFound
		}
		return nil, ErrEventConflict
	}
	if err != nil {
		return nil, err
	}
	return &next, nil
}

// Get returns one event to a direct member.
func (s *AccountEventsService) Get(ctx context.Context, account, user, id string) (*model.AccountEvent, error) {
	if !validContactID(id) {
		return nil, ErrInvalidInput
	}
	if err := s.authorize(ctx, account, user); err != nil {
		return nil, err
	}
	e, _, err := s.repo.GetAccountEvent(ctx, account, id)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, ErrNotFound
	}
	if err := s.authorize(ctx, account, user); err != nil {
		return nil, err
	}
	return e, nil
}

// EventListOptions selects events starting in [From, To). Empty From means
// now for ascending order and the beginning of time for descending order.
type EventListOptions struct {
	From, To   string
	Descending bool
	Limit      int
	Cursor     string
}

type eventCursor struct {
	Account string `json:"a"`
	From    string `json:"f"`
	To      string `json:"t"`
	Desc    bool   `json:"d,omitempty"`
	After   string `json:"k"`
}

func (s *AccountEventsService) List(ctx context.Context, account, user string, o EventListOptions) (*model.AccountEventsPage, error) {
	if o.Limit < 1 || o.Limit > 25 {
		return nil, ErrInvalidInput
	}
	from, to := eventRangeStart, eventRangeEnd
	if o.From != "" {
		_, key, err := eventTime(o.From)
		if err != nil {
			return nil, err
		}
		from = key
	} else if !o.Descending {
		from = s.now().UTC().Truncate(time.Second).Format(eventTimeLayout)
	}
	if o.To != "" {
		_, key, err := eventTime(o.To)
		if err != nil {
			return nil, err
		}
		to = key
	}
	if to <= from {
		return nil, ErrInvalidInput
	}
	after := ""
	if o.Cursor != "" {
		if len(o.Cursor) > 1024 {
			return nil, ErrInvalidInput
		}
		raw, err := base64.RawURLEncoding.DecodeString(o.Cursor)
		var c eventCursor
		if err != nil || json.Unmarshal(raw, &c) != nil || c.Account != account || c.Desc != o.Descending || !strings.HasPrefix(c.After, "EVENT#") {
			return nil, ErrInvalidInput
		}
		// The cursor carries the resolved range so "now" does not drift between pages.
		if (o.From != "" && c.From != from) || (o.To != "" && c.To != to) {
			return nil, ErrInvalidInput
		}
		from, to, after = c.From, c.To, c.After
	}
	if err := s.authorize(ctx, account, user); err != nil {
		return nil, err
	}
	events, next, err := s.repo.ListAccountEvents(ctx, account, from, to, o.Descending, o.Limit, after)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, account, user); err != nil {
		return nil, err
	}
	if events == nil {
		events = []model.AccountEvent{}
	}
	page := &model.AccountEventsPage{Events: events}
	if next != "" {
		raw, _ := json.Marshal(eventCursor{Account: account, From: from, To: to, Desc: o.Descending, After: next})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}
