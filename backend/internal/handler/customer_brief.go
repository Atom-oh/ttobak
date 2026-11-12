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
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

type customerBriefAPI interface {
	Request(context.Context, string, string) (*model.CustomerBriefResponse, error)
	Get(context.Context, string, string) (*model.CustomerBriefResponse, error)
}

// CustomerBriefHandler serves the requester's own customer brief. Only a
// current direct member reaches it; anyone else sees the same 404 as a
// missing account.
type CustomerBriefHandler struct{ service customerBriefAPI }

func NewCustomerBriefHandler(s customerBriefAPI) *CustomerBriefHandler {
	return &CustomerBriefHandler{service: s}
}

func (h *CustomerBriefHandler) Get(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" {
		writeCustomerBrief(w, 0, nil, service.ErrInvalidInput)
		return
	}
	result, err := h.service.Get(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"))
	writeCustomerBrief(w, http.StatusOK, result, err)
}

// Request queues a run. The brief is built from saved sources only, so the
// body is empty or {} (as for the English note request), at most 1 KiB.
func (h *CustomerBriefHandler) Request(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" {
		writeCustomerBrief(w, 0, nil, service.ErrInvalidInput)
		return
	}
	var request struct{}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil && err != io.EOF {
		writeCustomerBrief(w, 0, nil, service.ErrInvalidInput)
		return
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		writeCustomerBrief(w, 0, nil, service.ErrInvalidInput)
		return
	}
	result, err := h.service.Request(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"))
	writeCustomerBrief(w, http.StatusAccepted, result, err)
}

func writeCustomerBrief(w http.ResponseWriter, status int, result *model.CustomerBriefResponse, err error) {
	switch {
	case err == nil:
		writeJSON(w, status, result)
	case errors.Is(err, service.ErrInvalidInput):
		writeError(w, 400, model.ErrCodeBadRequest, "Invalid customer brief request")
	case errors.Is(err, service.ErrForbidden), errors.Is(err, service.ErrNotFound):
		writeError(w, 404, model.ErrCodeNotFound, "Account not found")
	case errors.Is(err, repository.ErrConditionFailed):
		writeError(w, 409, model.ErrCodeConflict, "브리프 요청이 변경되었습니다. 다시 시도해 주세요.")
	case errors.Is(err, service.ErrCustomerBriefPublish):
		writeError(w, 503, "BRIEF_UNAVAILABLE", "브리프 작업을 전달하지 못했습니다. 다시 시도해 주세요.")
	case errors.Is(err, repository.ErrContactEncryptionUnavailable):
		writeError(w, 503, "BRIEF_UNAVAILABLE", "브리프를 일시적으로 열 수 없습니다.")
	default:
		writeError(w, 500, model.ErrCodeInternalError, "브리프 요청을 처리하지 못했습니다.")
	}
}
