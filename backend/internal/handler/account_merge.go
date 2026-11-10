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

type accountMerger interface {
	Preview(ctx context.Context, requesterID, fromID, intoID string) (*model.AccountMergePreview, error)
	Start(ctx context.Context, requesterID, fromID string, req *model.StartAccountMergeRequest) (*model.AccountMergeJobDTO, error)
	Status(ctx context.Context, userID, fromID string) (*model.AccountMergeJobDTO, error)
}

// AccountMergeHandler serves the account merge routes.
type AccountMergeHandler struct {
	merge accountMerger
}

func NewAccountMergeHandler(merge accountMerger) *AccountMergeHandler {
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

// Start is POST /api/accounts/{accountId}/merge with
// {"intoAccountId","acceptUnmoved"} capped at 1,024 bytes. It records the
// merge job and fences the source; the worker moves the data. 202 returns
// the job.
func (h *AccountMergeHandler) Start(w http.ResponseWriter, r *http.Request) {
	if !middleware.GetEmailVerified(r.Context()) {
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Verify your email before merging accounts")
		return
	}
	var req model.StartAccountMergeRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || decoder.Decode(new(any)) != io.EOF {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	job, err := h.merge.Start(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"), &req)
	if err != nil {
		writeAccountMergeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

// Status is GET /api/accounts/{accountId}/merge: the source's merge job for a
// direct member of the source or of the job's target.
func (h *AccountMergeHandler) Status(w http.ResponseWriter, r *http.Request) {
	job, err := h.merge.Status(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"))
	if err != nil {
		if errors.Is(err, service.ErrForbidden) {
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "You must be a member of the source or target account")
			return
		}
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "No merge for this account")
			return
		}
		writeAccountMergeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func writeAccountMergeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrMergeJobRunning):
		writeError(w, http.StatusConflict, model.ErrCodeMergeRunning, "A merge of this account is already running")
	case errors.Is(err, service.ErrMergeContactIndexTooLarge):
		writeError(w, http.StatusConflict, model.ErrCodeContactIndexTooLarge, "The two accounts have too many contacts to merge")
	case errors.Is(err, service.ErrAccountMergeConflict):
		writeError(w, http.StatusConflict, model.ErrCodeConflict, "The accounts changed; reload and retry")
	case errors.Is(err, service.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid merge request")
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "You must own the source account and belong to the target account")
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Account not found")
	case errors.Is(err, service.ErrAccountMergeIneligible):
		// The wrapped reason is a static string naming the failed rule.
		writeError(w, http.StatusConflict, model.ErrCodeMergeIneligible, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Account merge request failed")
	}
}
