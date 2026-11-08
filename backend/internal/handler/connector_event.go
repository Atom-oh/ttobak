package handler

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ttobak/backend/internal/connector"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

type ConnectorEventHandler struct {
	service *service.ConnectorEventService
}

func NewConnectorEventHandler(s *service.ConnectorEventService) *ConnectorEventHandler {
	return &ConnectorEventHandler{s}
}

func (h *ConnectorEventHandler) Create(ctx context.Context, p connector.Principal, account string, raw json.RawMessage, replayOnly bool) connector.InvokeResult {
	receipt, err := h.service.Create(ctx, p, account, raw, replayOnly)
	if err == nil {
		body, _ := json.Marshal(receipt)
		return connector.InvokeResult{StatusCode: 200, Body: string(body)}
	}
	status, code, message := 503, "CONNECTOR_WRITE_UNCONFIRMED", "Event creation could not be confirmed"
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		status, code = 400, "BAD_REQUEST"
	case errors.Is(err, service.ErrForbidden), errors.Is(err, connector.ErrCapabilityDenied), errors.Is(err, repository.ErrEventAccess):
		status, code = 403, "CAPABILITY_DENIED"
	case errors.Is(err, repository.ErrConnectorReceiptMissing):
		status, code = 404, "RECEIPT_NOT_FOUND"
	case errors.Is(err, repository.ErrConditionFailed):
		status, code = 409, "CONNECTOR_CONFLICT"
	case errors.Is(err, service.ErrEventsUnavailable):
		// Sealing fails before any write, so this outcome is definitive.
		code, message = "EVENTS_UNAVAILABLE", "Events are temporarily unavailable"
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": message}})
	return connector.InvokeResult{StatusCode: status, Body: string(body)}
}
