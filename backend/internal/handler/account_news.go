package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type accountNewsAPI interface {
	Create(ctx context.Context, userID, accountID string, req *model.CreateAccountNewsSourceRequest) (*model.CrawlerSource, error)
	List(ctx context.Context, userID, accountID string) ([]model.CrawlerSource, error)
	Delete(ctx context.Context, userID, accountID, sourceID string) error
}

// AccountNewsHandler serves account-owned news sources (direct members only).
type AccountNewsHandler struct{ service accountNewsAPI }

func NewAccountNewsHandler(s accountNewsAPI) *AccountNewsHandler {
	return &AccountNewsHandler{service: s}
}

func accountNewsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request")
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
	case errors.Is(err, service.ErrAccountMerging):
		writeAccountMerging(w)
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "News source not found")
	default:
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Internal server error")
	}
}

func (h *AccountNewsHandler) List(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" {
		accountNewsError(w, service.ErrInvalidInput)
		return
	}
	sources, err := h.service.List(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"))
	if err != nil {
		accountNewsError(w, err)
		return
	}
	if sources == nil {
		sources = []model.CrawlerSource{}
	}
	writeJSON(w, http.StatusOK, model.AccountNewsSourcesResponse{Sources: sources})
}

func (h *AccountNewsHandler) Create(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var req model.CreateAccountNewsSourceRequest
	if err := decoder.Decode(&req); err != nil {
		accountNewsError(w, service.ErrInvalidInput)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		accountNewsError(w, service.ErrInvalidInput)
		return
	}
	src, err := h.service.Create(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"), &req)
	if err != nil {
		accountNewsError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, src)
}

func (h *AccountNewsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" {
		accountNewsError(w, service.ErrInvalidInput)
		return
	}
	err := h.service.Delete(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"), chi.URLParam(r, "sourceId"))
	if err != nil {
		accountNewsError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
