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

type resummaryService interface {
	Get(context.Context, string, string) (*service.ResummaryResponse, error)
	Request(context.Context, string, string) (*service.ResummaryResponse, error)
}
type ResummaryHandler struct{ service resummaryService }

func NewResummaryHandler(s *service.ResummaryService) *ResummaryHandler {
	return &ResummaryHandler{service: s}
}
func (h *ResummaryHandler) Get(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" {
		writeResummary(w, 0, nil, service.ErrInvalidInput)
		return
	}
	result, err := h.service.Get(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "meetingId"))
	writeResummary(w, http.StatusOK, result, err)
}
func (h *ResummaryHandler) Request(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" {
		writeResummary(w, 0, nil, service.ErrInvalidInput)
		return
	}
	// The source is exclusively the saved canonical data. Accept no caller text.
	var request struct{}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil && err != io.EOF {
		writeResummary(w, 0, nil, service.ErrInvalidInput)
		return
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		writeResummary(w, 0, nil, service.ErrInvalidInput)
		return
	}
	result, err := h.service.Request(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "meetingId"))
	writeResummary(w, http.StatusAccepted, result, err)
}
func writeResummary(w http.ResponseWriter, status int, result *service.ResummaryResponse, err error) {
	switch {
	case err == nil:
		writeJSON(w, status, result)
	case errors.Is(err, service.ErrInvalidInput):
		writeError(w, 400, model.ErrCodeBadRequest, "Invalid re-summary request")
	case errors.Is(err, service.ErrForbidden), errors.Is(err, repository.ErrSummaryAccess):
		writeError(w, 403, model.ErrCodeForbidden, "Owner or edit permission is required")
	case errors.Is(err, service.ErrNotFound):
		writeError(w, 404, model.ErrCodeNotFound, "Meeting not found")
	case errors.Is(err, service.ErrResummaryBusy):
		writeError(w, 409, "MEETING_BUSY", "진행 중인 전사나 요약이 끝난 뒤 다시 시도해 주세요.")
	case errors.Is(err, service.ErrResummarySourcePending):
		writeError(w, 409, "SOURCE_NOT_READY", "첨부 문서의 텍스트 추출을 먼저 완료해 주세요.")
	case errors.Is(err, service.ErrResummaryNoSource):
		writeError(w, 409, "NO_SUMMARY_SOURCE", "저장된 메모, 녹취 또는 추출된 문서가 필요합니다.")
	case errors.Is(err, repository.ErrSummaryLimit):
		writeError(w, 413, "SUMMARY_TOO_LARGE", "요약할 자료가 처리 한도를 초과했습니다.")
	case errors.Is(err, repository.ErrConditionFailed):
		writeError(w, 409, model.ErrCodeConflict, "저장된 자료가 변경되었습니다. 다시 시도해 주세요.")
	case errors.Is(err, service.ErrResummaryPublish):
		writeError(w, 503, "SUMMARY_UNAVAILABLE", "요약 작업을 전달하지 못했습니다. 다시 시도해 주세요.")
	default:
		writeError(w, 500, model.ErrCodeInternalError, "요약 요청을 처리하지 못했습니다.")
	}
}
