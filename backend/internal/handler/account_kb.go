package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type accountKBAPI interface {
	List(ctx context.Context, userID, accountID, kind, cursor string) (*model.AccountKBPage, error)
	DownloadURL(ctx context.Context, userID, accountID, refID string) (string, error)
	Untag(ctx context.Context, userID, accountID, refID string) error
}

// AccountKBHandler serves the account KB list (direct members only).
type AccountKBHandler struct {
	service accountKBAPI
}

func NewAccountKBHandler(s accountKBAPI) *AccountKBHandler {
	return &AccountKBHandler{service: s}
}

func accountKBError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request")
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "KB item not found")
	default:
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Internal server error")
	}
}

// List handles GET /api/accounts/{accountId}/kb?kind=&cursor=.
func (h *AccountKBHandler) List(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q := r.URL.Query()
	if !onlyQueryKeys(r, "kind", "cursor") || q.Get("kind") == "" {
		accountKBError(w, service.ErrInvalidInput)
		return
	}
	page, err := h.service.List(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"), q.Get("kind"), q.Get("cursor"))
	if err != nil {
		accountKBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// Download handles GET /api/accounts/{accountId}/kb/{refId}/download.
func (h *AccountKBHandler) Download(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" {
		accountKBError(w, service.ErrInvalidInput)
		return
	}
	u, err := h.service.DownloadURL(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"), chi.URLParam(r, "refId"))
	if err != nil {
		accountKBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": u})
}

// Untag handles DELETE /api/accounts/{accountId}/kb/{refId}. It removes the
// account ref only; the file stays in the uploader's KB.
func (h *AccountKBHandler) Untag(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" {
		accountKBError(w, service.ErrInvalidInput)
		return
	}
	if err := h.service.Untag(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"), chi.URLParam(r, "refId")); err != nil {
		accountKBError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
