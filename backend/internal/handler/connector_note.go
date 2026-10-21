package handler

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ttobak/backend/internal/connector"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

type ConnectorNoteHandler struct{ service *service.ConnectorNoteService }

func NewConnectorNoteHandler(s *service.ConnectorNoteService) *ConnectorNoteHandler {
	return &ConnectorNoteHandler{service: s}
}
func (h *ConnectorNoteHandler) Create(ctx context.Context, principal connector.Principal, raw json.RawMessage) connector.InvokeResult {
	receipt, err := h.service.Create(ctx, principal, raw)
	if err == nil {
		body, _ := json.Marshal(receipt)
		return connector.InvokeResult{StatusCode: 200, Body: string(body)}
	}
	status, code := 503, "CONNECTOR_WRITE_UNCONFIRMED"
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		status, code = 400, "BAD_REQUEST"
	case errors.Is(err, service.ErrForbidden), errors.Is(err, connector.ErrCapabilityDenied):
		status, code = 403, "CAPABILITY_DENIED"
	case errors.Is(err, repository.ErrConditionFailed):
		status, code = 409, "CONNECTOR_CONFLICT"
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]string{
		"code": code, "message": "Personal note creation could not be confirmed; retry with the same idempotency key and content",
	}})
	return connector.InvokeResult{StatusCode: status, Body: string(body)}
}
