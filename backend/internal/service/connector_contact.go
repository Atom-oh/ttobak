package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/connector"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type connectorContactRepository interface {
	GetAccount(context.Context, string) (*model.Account, error)
	GetMember(context.Context, string, string) (*model.AccountMember, error)
	ConnectorSettings(context.Context, string, string, string, string) (model.ConnectorSettings, error)
	CreateConnectorContact(context.Context, repository.ConnectorContactWrite) (repository.ConnectorContactReceipt, error)
}
type ConnectorContactService struct {
	repo             connectorContactRepository
	audience, issuer string
}

func NewConnectorContactService(repo connectorContactRepository, audience, issuer string) *ConnectorContactService {
	return &ConnectorContactService{repo, audience, issuer}
}

// Decode strictly so duplicate keys, nulls, server-owned fields and unknown fields
// cannot acquire a different meaning between signing, validation and storage.
func connectorContactObject(raw []byte, allowed []string) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) {
		return nil, ErrInvalidInput
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := d.Token(); err != nil || tok != json.Delim('{') {
		return nil, ErrInvalidInput
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		tok, err := d.Token()
		key, ok := tok.(string)
		if err != nil || !ok || fields[key] != nil || !slices.Contains(allowed, key) {
			return nil, ErrInvalidInput
		}
		var v json.RawMessage
		if d.Decode(&v) != nil || bytes.Equal(v, []byte("null")) {
			return nil, ErrInvalidInput
		}
		fields[key] = v
	}
	if tok, err := d.Token(); err != nil || tok != json.Delim('}') || d.Decode(new(any)) != io.EOF {
		return nil, ErrInvalidInput
	}
	return fields, nil
}
func (s *ConnectorContactService) Create(ctx context.Context, p connector.Principal, account string, raw []byte, replayOnly bool) (repository.ConnectorContactReceipt, error) {
	empty := repository.ConnectorContactReceipt{}
	if p.Require("api.accountContacts.create", "ttobak-api-bridge", time.Now().Unix()) != nil || p.ApprovalGeneration() <= 0 {
		return empty, ErrForbidden
	}
	if validateAccountHierarchyIDs(account, "") != nil || len(raw) > 65536 {
		return empty, ErrInvalidInput
	}
	fields, err := connectorContactObject(raw, []string{"contact", "idempotencyKey"})
	if err != nil || len(fields) < 2 || fields["contact"] == nil || fields["idempotencyKey"] == nil {
		return empty, ErrInvalidInput
	}
	var key string
	if json.Unmarshal(fields["idempotencyKey"], &key) != nil || key != p.RequestID() {
		return empty, ErrInvalidInput
	}
	contactFields, err := connectorContactObject(fields["contact"], []string{"name", "company", "title", "department", "email", "address", "workPhone", "fax", "phone", "cardRegisteredAt", "cardBook", "group", "notes"})
	if err != nil {
		return empty, err
	}
	for _, v := range contactFields {
		var value string
		if json.Unmarshal(v, &value) != nil {
			return empty, ErrInvalidInput
		}
	}
	var contact model.AccountContact
	if json.Unmarshal(fields["contact"], &contact) != nil || validateContact(contact) != nil {
		return empty, ErrInvalidInput
	}
	contact.ContactID, contact.Revision = uuid.NewString(), uuid.NewString()
	encoded, err := json.Marshal(contact)
	if err != nil || len(encoded) > 12000 {
		return empty, ErrInvalidInput
	}
	ctx, cancel := context.WithDeadline(ctx, time.Unix(p.ExpiresAt(), 0))
	defer cancel()
	accountRow, err := s.repo.GetAccount(ctx, account)
	if err != nil {
		return empty, err
	}
	if accountRow == nil {
		return empty, ErrForbidden
	}
	m, err := s.repo.GetMember(ctx, account, p.UserID())
	if err != nil {
		return empty, err
	}
	if m == nil {
		return empty, ErrForbidden
	}
	view, err := s.repo.ConnectorSettings(ctx, p.UserID(), p.ClientID(), s.audience, s.issuer)
	if err != nil {
		return empty, err
	}
	a := view.Approval
	if !view.Enabled || !slices.Contains(view.Maximum, "read") || !slices.Contains(view.Maximum, "write") || a == nil || a.Blocked || a.Revoked || a.Generation != p.ApprovalGeneration() || a.ExpiresAt < p.ExpiresAt() || !slices.Contains(a.Capabilities, "read") || !slices.Contains(a.Capabilities, "write") {
		return empty, ErrForbidden
	}
	bound := append(append([]byte(account), 0), raw...)
	digest := sha256.Sum256(bound)
	return s.repo.CreateConnectorContact(ctx, repository.ConnectorContactWrite{Principal: p, Audience: s.audience, Issuer: s.issuer, PolicyGeneration: view.PolicyGeneration, Account: account, ArgumentsHash: hex.EncodeToString(digest[:]), Contact: contact, ReplayOnly: replayOnly})
}
