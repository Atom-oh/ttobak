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

type accountMergePreviewer interface {
	Preview(ctx context.Context, requesterID, fromID, intoID string) (*model.AccountMergePreview, error)
}

// AccountMergeHandler serves the account merge routes.
type AccountMergeHandler struct {
	merge accountMergePreviewer
}

func NewAccountMergeHandler(merge accountMergePreviewer) *AccountMergeHandler {
	return &AccountMergeHandler{merge: merge}
}

// Preview is GET /api/accounts/{accountId}/merge-preview?into={id}. It is
// read-only and its response is bounded by fixed category lists.
func (h *AccountMergeHandler) Preview(w http.ResponseWriter, r *http.Request) {
	into := r.URL.Query().Get("into")
	if into == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "The into query parameter is required")
		return
	}
	preview, err := h.merge.Preview(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"), into)
	if err != nil {
		writeAccountMergeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func writeAccountMergeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid merge accounts")
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "You must own the source account and belong to the target account")
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Account not found")
	case errors.Is(err, service.ErrAccountMergeIneligible):
		// The wrapped reason is a static string naming the failed rule.
		writeError(w, http.StatusConflict, model.ErrCodeMergeIneligible, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Merge preview failed")
	}
}
