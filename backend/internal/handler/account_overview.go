package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type accountOverviewAPI interface {
	Signals(ctx context.Context, userID, accountID string, now time.Time) (*model.AccountSignals, error)
	Flow(ctx context.Context, userID, accountID string, now time.Time) (*model.AccountFlow, error)
	Graph(ctx context.Context, userID, accountID string) (*model.AccountGraph, error)
}

// AccountOverviewHandler serves the account page's derived views (direct
// members only).
type AccountOverviewHandler struct {
	service accountOverviewAPI
	now     func() time.Time
}

func NewAccountOverviewHandler(s accountOverviewAPI) *AccountOverviewHandler {
	return &AccountOverviewHandler{service: s, now: time.Now}
}

func accountOverviewError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request")
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Account not found")
	default:
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Internal server error")
	}
}

// Signals handles GET /api/accounts/{accountId}/signals. It takes no query.
func (h *AccountOverviewHandler) Signals(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" {
		accountOverviewError(w, service.ErrInvalidInput)
		return
	}
	signals, err := h.service.Signals(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"), h.now())
	if err != nil {
		accountOverviewError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, signals)
}

// Flow handles GET /api/accounts/{accountId}/flow. It takes no query.
func (h *AccountOverviewHandler) Flow(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" {
		accountOverviewError(w, service.ErrInvalidInput)
		return
	}
	flow, err := h.service.Flow(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"), h.now())
	if err != nil {
		accountOverviewError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, flow)
}

// Graph handles GET /api/accounts/{accountId}/graph. It takes no query.
func (h *AccountOverviewHandler) Graph(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" {
		accountOverviewError(w, service.ErrInvalidInput)
		return
	}
	graph, err := h.service.Graph(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"))
	if err != nil {
		accountOverviewError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, graph)
}
