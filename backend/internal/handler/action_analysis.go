package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

type actionAnalysisService interface {
	Get(context.Context, string, string) (*service.ActionItemsResponse, error)
	Request(context.Context, string, string) (*service.ActionItemsResponse, error)
	SetCompleted(context.Context, string, string, string, bool) (*service.ActionItemsResponse, error)
}

type ActionItemsHandler struct{ service actionAnalysisService }

func NewActionItemsHandler(s *service.ActionItemsAnalysisService) *ActionItemsHandler {
	return &ActionItemsHandler{service: s}
}

func (h *ActionItemsHandler) Get(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Get(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "meetingId"))
	writeActionItemsResponse(w, http.StatusOK, result, err)
}

func (h *ActionItemsHandler) Retry(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Request(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "meetingId"))
	writeActionItemsResponse(w, http.StatusAccepted, result, err)
}

func (h *ActionItemsHandler) SetCompleted(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Completed *bool `json:"completed"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || req.Completed == nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "completed must be an explicit boolean")
		return
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	result, err := h.service.SetCompleted(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "meetingId"), chi.URLParam(r, "itemId"), *req.Completed)
	writeActionItemsResponse(w, http.StatusOK, result, err)
}

func writeActionItemsResponse(w http.ResponseWriter, status int, result *service.ActionItemsResponse, err error) {
	switch {
	case err == nil:
		writeJSON(w, status, result)
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting or action item not found")
	case errors.Is(err, service.ErrNoAnalysisSource):
		writeError(w, http.StatusConflict, model.ErrCodeConflict, "저장된 회의 요약이 있어야 액션 아이템을 추출할 수 있습니다.")
	case errors.Is(err, repository.ErrConditionFailed):
		writeError(w, http.StatusConflict, model.ErrCodeConflict, "회의록이나 액션 아이템이 변경되었습니다. 새로고침 후 다시 시도해 주세요.")
	default:
		log.Printf("Action item request failed: %v", err)
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "액션 아이템을 처리하지 못했습니다. 다시 시도해 주세요.")
	}
}
