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

type accountLinksAPI interface {
	Link(ctx context.Context, userID, accountID string, in model.AccountLinkInput) (*model.AccountLink, error)
	Unlink(ctx context.Context, userID, accountID string, in model.AccountLinkInput) error
	List(ctx context.Context, userID, accountID, cursor string) ([]model.AccountLink, *string, error)
}

// AccountLinksHandler serves typed account links (direct members only).
type AccountLinksHandler struct{ service accountLinksAPI }

func NewAccountLinksHandler(s accountLinksAPI) *AccountLinksHandler {
	return &AccountLinksHandler{service: s}
}

func accountLinksError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request")
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Link not found")
	case errors.Is(err, service.ErrAccountMerging):
		writeAccountMerging(w)
	case errors.Is(err, service.ErrConflict):
		writeError(w, http.StatusConflict, model.ErrCodeConflict, "Link changed concurrently, retry")
	default:
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Internal server error")
	}
}

// onlyQueryKeys reports whether every query key is allowed and single-valued.
func onlyQueryKeys(r *http.Request, allowed ...string) bool {
	query := r.URL.Query()
	for key, values := range query {
		ok := false
		for _, a := range allowed {
			ok = ok || key == a
		}
		if !ok || len(values) != 1 {
			return false
		}
	}
	return true
}

func (h *AccountLinksHandler) List(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !onlyQueryKeys(r, "cursor") {
		accountLinksError(w, service.ErrInvalidInput)
		return
	}
	links, next, err := h.service.List(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"), r.URL.Query().Get("cursor"))
	if err != nil {
		accountLinksError(w, err)
		return
	}
	resp := model.AccountLinksResponse{Links: links}
	if resp.Links == nil {
		resp.Links = []model.AccountLink{}
	}
	if next != nil {
		resp.NextCursor = *next
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *AccountLinksHandler) Create(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" {
		accountLinksError(w, service.ErrInvalidInput)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var in model.AccountLinkInput
	if err := decoder.Decode(&in); err != nil {
		accountLinksError(w, service.ErrInvalidInput)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		accountLinksError(w, service.ErrInvalidInput)
		return
	}
	link, err := h.service.Link(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"), in)
	if err != nil {
		accountLinksError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, link)
}

func (h *AccountLinksHandler) Delete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	keys := []string{"fromKind", "fromId", "toKind", "toId"}
	query := r.URL.Query()
	if !onlyQueryKeys(r, keys...) || len(query) != len(keys) {
		accountLinksError(w, service.ErrInvalidInput)
		return
	}
	in := model.AccountLinkInput{FromKind: query.Get("fromKind"), FromID: query.Get("fromId"), ToKind: query.Get("toKind"), ToID: query.Get("toId")}
	if err := h.service.Unlink(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"), in); err != nil {
		accountLinksError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
