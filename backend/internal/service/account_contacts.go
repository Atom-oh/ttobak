package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type accountContactsRepo interface {
	GetAccount(context.Context, string) (*model.Account, error)
	GetMember(context.Context, string, string) (*model.AccountMember, error)
	GetAccountContact(context.Context, string, string) (*model.AccountContact, error)
	ListAccountContacts(context.Context, string, int, string) ([]model.AccountContact, string, error)
	WriteAccountContact(context.Context, string, string, *model.AccountContact, string) error
	ListSpeakerTeamMembers(context.Context, string, int, string) ([]model.AccountMember, string, error)
	GetUserByID(context.Context, string) (*model.User, error)
}

type AccountContactsService struct {
	repo accountContactsRepo
	// peopleResolve publishes one account PeopleResolveRequested after a
	// contact create or update (best-effort).
	peopleResolve func(context.Context, model.PeopleResolveRequested) error
}

// ContactMutateOptions tunes one contact write. DeferPeopleResolve skips the
// people resolve request; a bulk import sends one request at its end instead.
type ContactMutateOptions struct {
	DeferPeopleResolve bool
	// sfdc is set only by the SFDC sync; every other path cannot set or
	// change the SFDC fields (ADR-021, SFDC contacts).
	sfdc *sfdcWrite
}

// sfdcWrite lets one write carry SFDC fields. base is the stored contact an
// update was computed from; it pins the existing link, which never changes.
type sfdcWrite struct {
	base *model.AccountContact
}

// copySfdcFields copies the four SFDC fields from src to dst.
func copySfdcFields(dst *model.AccountContact, src model.AccountContact) {
	dst.Source, dst.SfdcContactID, dst.SfdcAccountID, dst.SfdcLeftAt = src.Source, src.SfdcContactID, src.SfdcAccountID, src.SfdcLeftAt
}

// SetPeopleResolvePublisher wires the contact-change people resolve publisher.
func (s *AccountContactsService) SetPeopleResolvePublisher(fn func(context.Context, model.PeopleResolveRequested) error) {
	s.peopleResolve = fn
}

var ErrContactConflict = errors.New("contact changed concurrently")

// ErrContactsUnavailable means contact encryption is not configured; contact
// reads and writes fail closed rather than handle plaintext (ADR-021, contact encryption).
var ErrContactsUnavailable = repository.ErrContactEncryptionUnavailable

func NewAccountContactsService(repo accountContactsRepo) *AccountContactsService {
	return &AccountContactsService{repo: repo}
}

func (s *AccountContactsService) authorize(ctx context.Context, account, user string) error {
	_, err := s.authorizedAccount(ctx, account, user)
	return err
}

// authorizedAccount checks direct membership and returns the account row.
func (s *AccountContactsService) authorizedAccount(ctx context.Context, account, user string) (*model.Account, error) {
	if validateAccountHierarchyIDs(account, "") != nil || validateAccountHierarchyIDs(user, "") != nil {
		return nil, ErrInvalidInput
	}
	a, err := s.repo.GetAccount(ctx, account)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, ErrForbidden
	}
	m, err := s.repo.GetMember(ctx, account, user)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, ErrForbidden
	}
	return a, nil
}

type contactCursor struct {
	Account string `json:"a"`
	ID      string `json:"i"`
}

func validContactID(id string) bool {
	u, err := uuid.Parse(id)
	return err == nil && u != uuid.Nil && u.String() == id
}
func decodeContactCursor(account, cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	if len(cursor) > 512 {
		return "", ErrInvalidInput
	}
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", ErrInvalidInput
	}
	var c contactCursor
	if json.Unmarshal(b, &c) != nil || c.Account != account || !validContactID(c.ID) {
		return "", ErrInvalidInput
	}
	return c.ID, nil
}
func encodeContactCursor(account, id string) string {
	b, _ := json.Marshal(contactCursor{Account: account, ID: id})
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *AccountContactsService) List(ctx context.Context, account, user string, limit int, cursor string) (*model.AccountContactsPage, error) {
	if limit < 1 || limit > 25 {
		return nil, ErrInvalidInput
	}
	if err := s.authorize(ctx, account, user); err != nil {
		return nil, err
	}
	after, err := decodeContactCursor(account, cursor)
	if err != nil {
		return nil, err
	}
	contacts, next, err := s.repo.ListAccountContacts(ctx, account, limit, after)
	if err != nil {
		return nil, err
	}
	if err = s.authorize(ctx, account, user); err != nil {
		return nil, err
	}
	if contacts == nil {
		contacts = []model.AccountContact{}
	}
	page := &model.AccountContactsPage{Contacts: contacts}
	if next != "" {
		if !validContactID(next) {
			return nil, errors.New("invalid contact page key")
		}
		page.NextCursor = encodeContactCursor(account, next)
	}
	return page, nil
}

// Contact size limits; the merge mover keeps merged notes within them too.
const (
	contactNotesMaxBytes   = 8192
	contactEncodedMaxBytes = 12000
)

func validateContact(c model.AccountContact) error {
	if strings.TrimSpace(c.Name) == "" {
		return ErrInvalidInput
	}
	fields := []string{c.Name, c.Company, c.Title, c.Department, c.Email, c.WorkPhone, c.Fax, c.Phone, c.CardRegisteredAt, c.CardBook, c.Group}
	for _, v := range fields {
		if len(v) > 256 || !utf8.ValidString(v) {
			return ErrInvalidInput
		}
	}
	if len(c.Address) > 1024 || len(c.Notes) > contactNotesMaxBytes || !utf8.ValidString(c.Address) || !utf8.ValidString(c.Notes) {
		return ErrInvalidInput
	}
	return nil
}

func (s *AccountContactsService) Mutate(ctx context.Context, account, user, op, id string, c model.AccountContact) (*model.AccountContact, error) {
	return s.MutateWith(ctx, account, user, op, id, c, ContactMutateOptions{})
}

// MutateWith is Mutate with write options.
func (s *AccountContactsService) MutateWith(ctx context.Context, account, user, op, id string, c model.AccountContact, opt ContactMutateOptions) (*model.AccountContact, error) {
	if op != "create" && op != "update" && op != "delete" {
		return nil, ErrInvalidInput
	}
	if op != "create" && !validContactID(id) {
		return nil, ErrInvalidInput
	}
	if op != "create" && !validContactID(c.Revision) {
		return nil, ErrInvalidInput
	}
	if op != "delete" {
		if err := validateContact(c); err != nil {
			return nil, err
		}
	}
	if err := s.authorize(ctx, account, user); err != nil {
		return nil, err
	}
	if err := s.guardSfdcFields(ctx, account, op, id, &c, opt.sfdc); err != nil {
		return nil, err
	}
	if op == "create" {
		id = uuid.NewString()
		c.Revision = uuid.NewString()
	}
	c.PK = ""
	c.SK = ""
	c.ContactID = id
	if op != "delete" {
		encoded, err := json.Marshal(c)
		if err != nil || len(encoded) > contactEncodedMaxBytes {
			return nil, ErrInvalidInput
		}
	}
	err := s.repo.WriteAccountContact(ctx, account, user, &c, op)
	if errors.Is(err, repository.ErrContactAccess) {
		return nil, ErrForbidden
	}
	if errors.Is(err, repository.ErrConditionFailed) && op != "create" {
		if err = s.authorize(ctx, account, user); err != nil {
			return nil, err
		}
		current, getErr := s.repo.GetAccountContact(ctx, account, id)
		if getErr != nil {
			return nil, getErr
		}
		if current == nil {
			return nil, ErrNotFound
		}
		return nil, ErrContactConflict
	}
	if err != nil {
		return nil, err
	}
	if op != "delete" && !opt.DeferPeopleResolve {
		publishPeopleResolve(ctx, s.peopleResolve, model.PeopleResolveRequested{AccountID: account, Reason: model.PeopleResolveReasonContacts})
	}
	return &c, nil
}

// guardSfdcFields keeps the SFDC fields server-owned. Generic creates drop
// them; generic updates carry the stored values over, read here and pinned by
// the revision condition of the same write. The sync may set a link only where
// none exists, and never changes or clears one: a changed link would orphan
// its index row. IDs compare in canonical form.
func (s *AccountContactsService) guardSfdcFields(ctx context.Context, account, op, id string, c *model.AccountContact, sync *sfdcWrite) error {
	if op == "delete" {
		return nil
	}
	if sync == nil {
		var stored model.AccountContact
		if op == "update" {
			current, err := s.repo.GetAccountContact(ctx, account, id)
			if err != nil {
				return err
			}
			// A missing contact or another revision fails the conditional write.
			if current != nil && current.Revision == c.Revision {
				stored = *current
			}
		}
		copySfdcFields(c, stored)
		return nil
	}
	if op == "create" {
		return nil
	}
	base := sync.base
	if base == nil || base.ContactID != id || base.Revision != c.Revision {
		return ErrInvalidInput
	}
	if base.SfdcContactID != "" && (!sameSfdcID(c.SfdcContactID, base.SfdcContactID) || base.SfdcAccountID != "" && !sameSfdcID(c.SfdcAccountID, base.SfdcAccountID)) {
		return ErrContactConflict
	}
	return nil
}

// sameSfdcID reports whether a and b are valid spellings of one SFDC ID.
func sameSfdcID(a, b string) bool {
	ca, okA := model.CanonicalSfdcID(a)
	cb, okB := model.CanonicalSfdcID(b)
	return okA && okB && ca == cb
}

func (s *AccountContactsService) SpeakerTeamPage(ctx context.Context, account, user string, limit int, cursor string) (*model.SpeakerTeam, error) {
	if limit < 1 || limit > 25 {
		return nil, ErrInvalidInput
	}
	if err := s.authorize(ctx, account, user); err != nil {
		return nil, err
	}
	var after string
	if cursor != "" {
		if len(cursor) > 512 {
			return nil, ErrInvalidInput
		}
		b, err := base64.RawURLEncoding.DecodeString(cursor)
		var c struct {
			Account string `json:"a"`
			ID      string `json:"i"`
			Kind    string `json:"k"`
		}
		if err != nil || json.Unmarshal(b, &c) != nil || c.Account != account || c.Kind != "team" || c.ID == "" || validateAccountHierarchyIDs(c.ID, "") != nil {
			return nil, ErrInvalidInput
		}
		after = c.ID
	}
	members, next, err := s.repo.ListSpeakerTeamMembers(ctx, account, limit, after)
	if err != nil {
		return nil, err
	}
	out := &model.SpeakerTeam{Members: make([]model.SpeakerTeamMember, 0, len(members))}
	for _, m := range members {
		profile, err := s.repo.GetUserByID(ctx, m.UserID)
		if err != nil {
			return nil, err
		}
		email := m.Email
		name := ""
		if profile != nil {
			name = strings.TrimSpace(profile.Name)
			if email == "" {
				email = profile.Email
			}
		}
		if name == "" {
			name = email
		}
		out.Members = append(out.Members, model.SpeakerTeamMember{UserID: m.UserID, Name: name, Email: email, Role: m.Role})
	}
	if err = s.authorize(ctx, account, user); err != nil {
		return nil, err
	}
	if next != "" {
		if validateAccountHierarchyIDs(next, "") != nil {
			return nil, errors.New("invalid team page key")
		}
		b, _ := json.Marshal(struct {
			Account string `json:"a"`
			ID      string `json:"i"`
			Kind    string `json:"k"`
		}{account, next, "team"})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return out, nil
}
