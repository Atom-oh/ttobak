package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/connector"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const MaxConnectorNoteBytes = 65536

type connectorNoteRepository interface {
	ConnectorSettings(context.Context, string, string, string, string) (model.ConnectorSettings, error)
	CreateConnectorNote(context.Context, repository.ConnectorNoteWrite) (model.ConnectorNoteReceipt, error)
}
type ConnectorNoteService struct {
	repo             connectorNoteRepository
	audience, issuer string
}

func NewConnectorNoteService(repo connectorNoteRepository, audience, issuer string) *ConnectorNoteService {
	return &ConnectorNoteService{repo: repo, audience: audience, issuer: issuer}
}

func decodeConnectorNote(raw []byte) (map[string]string, error) {
	if len(raw) == 0 || len(raw) > MaxConnectorNoteBytes || !utf8.Valid(raw) {
		return nil, ErrInvalidInput
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, ErrInvalidInput
	}
	fields := map[string]string{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		_, duplicate := fields[key]
		if err != nil || !ok || duplicate || !slices.Contains([]string{"title", "markdown", "docType", "idempotencyKey"}, key) {
			return nil, ErrInvalidInput
		}
		var value string
		// Decode through a RawMessage so JSON null cannot silently become "".
		var encoded json.RawMessage
		if decoder.Decode(&encoded) != nil || bytes.Equal(encoded, []byte("null")) || json.Unmarshal(encoded, &value) != nil {
			return nil, ErrInvalidInput
		}
		fields[key] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || len(fields) != 4 || decoder.Decode(new(any)) != io.EOF {
		return nil, ErrInvalidInput
	}
	return fields, nil
}

func (s *ConnectorNoteService) Create(ctx context.Context, principal connector.Principal, raw []byte) (model.ConnectorNoteReceipt, error) {
	empty := model.ConnectorNoteReceipt{}
	if principal.Require("api.document.putDocument", "ttobak-api-bridge", time.Now().Unix()) != nil ||
		principal.ApprovalGeneration() <= 0 {
		return empty, ErrForbidden
	}
	fields, err := decodeConnectorNote(raw)
	if err != nil {
		return empty, err
	}
	title, markdown := strings.TrimSpace(fields["title"]), strings.TrimSpace(fields["markdown"])
	if fields["idempotencyKey"] != principal.RequestID() || fields["docType"] != "note" ||
		title == "" || len(title) > 512 || markdown == "" || len(markdown) > 60000 || hasTtobakOriginMarker(markdown) {
		return empty, ErrInvalidInput
	}
	ctx, cancel := context.WithDeadline(ctx, time.Unix(principal.ExpiresAt(), 0))
	defer cancel()
	view, err := s.repo.ConnectorSettings(ctx, principal.UserID(), principal.ClientID(), s.audience, s.issuer)
	if err != nil {
		return empty, err
	}
	approval := view.Approval
	if !view.Enabled || !slices.Contains(view.Maximum, "read") || !slices.Contains(view.Maximum, "write") ||
		approval == nil || approval.Blocked || approval.Revoked || approval.Generation != principal.ApprovalGeneration() ||
		approval.ExpiresAt < principal.ExpiresAt() || !slices.Contains(approval.Capabilities, "read") ||
		!slices.Contains(approval.Capabilities, "write") {
		return empty, ErrForbidden
	}
	now := time.Now().UTC()
	// A fresh receipt must never reuse a deleted document identity: old email
	// share rows may intentionally outlive that document. Replays use the
	// stored receipt identity instead of this candidate.
	docID := uuid.NewString()
	digest := sha256.Sum256(raw)
	return s.repo.CreateConnectorNote(ctx, repository.ConnectorNoteWrite{
		Principal: principal, Audience: s.audience, Issuer: s.issuer, PolicyGeneration: view.PolicyGeneration,
		ArgumentsHash: hex.EncodeToString(digest[:]),
		Document: model.AccountDocument{
			PK: model.PrefixUser + principal.UserID(), SK: model.PrefixDoc + docID, DocID: docID,
			Title: title, Content: markdown, DocType: "note", Links: parseWikilinks(markdown),
			SourceUserID: principal.UserID(), CreatedAt: now, UpdatedAt: now, EntityType: model.EntityTypeUserDoc,
		},
	})
}
