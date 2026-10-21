package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/ttobak/backend/internal/connector"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

type ConnectorSettingsHandler struct {
	service *service.ConnectorSettingsService
}

func NewConnectorSettingsHandler(s *service.ConnectorSettingsService) *ConnectorSettingsHandler {
	return &ConnectorSettingsHandler{service: s}
}
func connectorSettingsError(w http.ResponseWriter, err error) {
	status, code := 503, "CONNECTOR_UNAVAILABLE"
	switch {
	case errors.Is(err, service.ErrForbidden):
		status, code = 403, "FORBIDDEN"
	case errors.Is(err, service.ErrInvalidInput):
		status, code = 400, "BAD_REQUEST"
	case errors.Is(err, repository.ErrConditionFailed):
		status, code = 409, "CONNECTOR_CONFLICT"
	case errors.Is(err, connector.ErrInvalidGrant):
		status, code = 409, "CONNECTOR_CONFIGURATION_CHANGED"
	}
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": "Connector settings could not be updated; reload current settings"}})
}
func connectorSettingsBody(w http.ResponseWriter, r *http.Request, value any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		connectorSettingsError(w, service.ErrInvalidInput)
		return false
	}
	return true
}
func (h *ConnectorSettingsHandler) List(w http.ResponseWriter, r *http.Request) {
	views, err := h.service.List(r.Context(), middleware.GetUserID(r.Context()))
	if err != nil {
		connectorSettingsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connectors": views})
}
func (h *ConnectorSettingsHandler) UpdateApproval(w http.ResponseWriter, r *http.Request) {
	var request service.ConnectorApprovalRequest
	if !connectorSettingsBody(w, r, &request) {
		return
	}
	generation, err := h.service.UpdateApproval(r.Context(), middleware.GetUserID(r.Context()), request)
	if err != nil {
		connectorSettingsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"generation": generation})
}
func (h *ConnectorSettingsHandler) UpdatePolicy(w http.ResponseWriter, r *http.Request) {
	if !middleware.IsAdmin(r.Context()) {
		connectorSettingsError(w, service.ErrForbidden)
		return
	}
	var request service.ConnectorPolicyRequest
	if !connectorSettingsBody(w, r, &request) {
		return
	}
	generation, err := h.service.UpdatePolicy(r.Context(), middleware.GetUserID(r.Context()), true, request)
	if err != nil {
		connectorSettingsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"generation": generation})
}
