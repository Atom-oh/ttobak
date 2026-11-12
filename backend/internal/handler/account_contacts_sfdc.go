package handler

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

// sfdcSyncMaxBodyBytes caps one SFDC contact sync batch (ADR-021).
const sfdcSyncMaxBodyBytes = 512 * 1024

type accountContactSyncAPI interface {
	Sync(context.Context, string, string, model.SfdcContactSyncRequest) (*model.SfdcContactSyncResponse, error)
}

// AccountContactSyncHandler serves POST /api/accounts/{accountId}/contacts/sfdc-sync.
type AccountContactSyncHandler struct{ service accountContactSyncAPI }

func NewAccountContactSyncHandler(s accountContactSyncAPI) *AccountContactSyncHandler {
	return &AccountContactSyncHandler{service: s}
}

func (h *AccountContactSyncHandler) Sync(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var req model.SfdcContactSyncRequest
	if !decodeStrict(w, r, sfdcSyncMaxBodyBytes, &req) {
		return
	}
	out, err := h.service.Sync(r.Context(), chi.URLParam(r, "accountId"), middleware.GetUserID(r.Context()), req)
	if err != nil {
		contactError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

var _ accountContactSyncAPI = (*service.AccountContactSyncService)(nil)
