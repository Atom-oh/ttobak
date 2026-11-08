package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ttobak/backend/internal/connector"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

type ConnectorContactHandler struct {
	service *service.ConnectorContactService
}

func NewConnectorContactHandler(s *service.ConnectorContactService) *ConnectorContactHandler {
	return &ConnectorContactHandler{s}
}
func (h *ConnectorContactHandler) Create(ctx context.Context, p connector.Principal, account string, raw json.RawMessage, replayOnly bool) connector.InvokeResult {
	receipt, err := h.service.Create(ctx, p, account, raw, replayOnly)
	if err == nil {
		body, _ := json.Marshal(receipt)
		return connector.InvokeResult{StatusCode: 200, Body: string(body)}
	}
	status, code := 503, "CONNECTOR_WRITE_UNCONFIRMED"
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		status, code = 400, "BAD_REQUEST"
	case errors.Is(err, service.ErrForbidden), errors.Is(err, connector.ErrCapabilityDenied), errors.Is(err, repository.ErrContactAccess):
		status, code = 403, "CAPABILITY_DENIED"
	case errors.Is(err, repository.ErrConnectorReceiptMissing):
		status, code = 404, "RECEIPT_NOT_FOUND"
	case errors.Is(err, repository.ErrConditionFailed):
		status, code = 409, "CONNECTOR_CONFLICT"
	case errors.Is(err, service.ErrContactsUnavailable):
		// Sealing fails before any write, so this outcome is definitive.
		body, _ := json.Marshal(map[string]any{"error": map[string]string{"code": "CONTACTS_UNAVAILABLE", "message": "Contacts are temporarily unavailable"}})
		return connector.InvokeResult{StatusCode: 503, Body: string(body)}
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": "Contact creation could not be confirmed"}})
	return connector.InvokeResult{StatusCode: status, Body: string(body)}
}
