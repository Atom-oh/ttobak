package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/connector"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type connectorEventRepository interface {
	GetAccount(context.Context, string) (*model.Account, error)
	GetMember(context.Context, string, string) (*model.AccountMember, error)
	ConnectorSettings(context.Context, string, string, string, string) (model.ConnectorSettings, error)
	ExistingAccountContactIDs(context.Context, string, []string) (map[string]bool, error)
	CreateConnectorEvent(context.Context, repository.ConnectorEventWrite) (repository.ConnectorEventReceipt, error)
}

// ConnectorEventService creates account events for delegated MCP clients with
// the same strict decoding, direct membership, approval and receipt rules as
// connector contact creation.
type ConnectorEventService struct {
	repo             connectorEventRepository
	audience, issuer string
	now              func() time.Time
}

func NewConnectorEventService(repo connectorEventRepository, audience, issuer string) *ConnectorEventService {
	return &ConnectorEventService{repo: repo, audience: audience, issuer: issuer, now: time.Now}
}

var connectorEventFields = []string{"title", "startAt", "endAt", "allDay", "location", "attendees", "notes"}

// decodeConnectorEvent rejects duplicate keys, nulls, unknown or server-owned
// fields and wrong JSON types at every level before normalization.
func decodeConnectorEvent(raw json.RawMessage) (AccountEventInput, error) {
	var in AccountEventInput
	fields, err := connectorContactObject(raw, connectorEventFields)
	if err != nil {
		return in, err
	}
	for key, v := range fields {
		switch key {
		case "allDay":
			var b bool
			if json.Unmarshal(v, &b) != nil {
				return in, ErrInvalidInput
			}
		case "attendees":
			var list []json.RawMessage
			if json.Unmarshal(v, &list) != nil || len(list) > maxEventAttendees {
				return in, ErrInvalidInput
			}
			for _, a := range list {
				attendee, err := connectorContactObject(a, []string{"contactId", "name", "email"})
				if err != nil {
					return in, err
				}
				for _, fv := range attendee {
					var s string
					if json.Unmarshal(fv, &s) != nil {
						return in, ErrInvalidInput
					}
				}
			}
		default:
			var s string
			if json.Unmarshal(v, &s) != nil {
				return in, ErrInvalidInput
			}
		}
	}
	if json.Unmarshal(raw, &in) != nil {
		return in, ErrInvalidInput
	}
	return in, nil
}

func (s *ConnectorEventService) Create(ctx context.Context, p connector.Principal, account string, raw []byte, replayOnly bool) (repository.ConnectorEventReceipt, error) {
	empty := repository.ConnectorEventReceipt{}
	if p.Require("api.accountEvents.create", "ttobak-api-bridge", time.Now().Unix()) != nil || p.ApprovalGeneration() <= 0 {
		return empty, ErrForbidden
	}
	if validateAccountHierarchyIDs(account, "") != nil || len(raw) > 65536 {
		return empty, ErrInvalidInput
	}
	fields, err := connectorContactObject(raw, []string{"event", "idempotencyKey"})
	if err != nil || len(fields) != 2 || fields["event"] == nil || fields["idempotencyKey"] == nil {
		return empty, ErrInvalidInput
	}
	var key string
	if json.Unmarshal(fields["idempotencyKey"], &key) != nil || key != p.RequestID() {
		return empty, ErrInvalidInput
	}
	input, err := decodeConnectorEvent(fields["event"])
	if err != nil {
		return empty, err
	}
	event, refs, err := normalizeEvent(input)
	if err != nil {
		return empty, err
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
	member, err := s.repo.GetMember(ctx, account, p.UserID())
	if err != nil {
		return empty, err
	}
	if member == nil {
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
	// Contact references are resolved only after membership and approval.
	if len(refs) > 0 {
		found, err := s.repo.ExistingAccountContactIDs(ctx, account, refs)
		if err != nil {
			return empty, err
		}
		for _, id := range refs {
			if !found[id] {
				return empty, ErrInvalidInput
			}
		}
	}
	now := s.now().UTC().Format(time.RFC3339)
	event.EventID, event.Revision = uuid.NewString(), uuid.NewString()
	event.CreatedBy, event.CreatedAt, event.UpdatedAt = p.UserID(), now, now
	if encoded, err := json.Marshal(event); err != nil || len(encoded) > maxEventJSONBytes {
		return empty, ErrInvalidInput
	}
	bound := append(append([]byte(account), 0), raw...)
	digest := sha256.Sum256(bound)
	return s.repo.CreateConnectorEvent(ctx, repository.ConnectorEventWrite{Principal: p, Audience: s.audience, Issuer: s.issuer, PolicyGeneration: view.PolicyGeneration, Account: account, ArgumentsHash: hex.EncodeToString(digest[:]), Event: event, ReplayOnly: replayOnly})
}
