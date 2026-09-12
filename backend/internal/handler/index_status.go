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

type indexStatusService interface {
	Meeting(context.Context, string, string) (*service.IndexStatusResponse, error)
	PersonalDocument(context.Context, string, string) (*service.IndexStatusResponse, error)
	AccountDocument(context.Context, string, string, string) (*service.IndexStatusResponse, error)
}
type IndexStatusHandler struct{ service indexStatusService }

func NewIndexStatusHandler(s *service.IndexStatusService) *IndexStatusHandler {
	return &IndexStatusHandler{service: s}
}

func (h *IndexStatusHandler) Meeting(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Meeting(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "meetingId"))
	writeIndexStatus(w, result, err)
}
func (h *IndexStatusHandler) PersonalDocument(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.PersonalDocument(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "docId"))
	writeIndexStatus(w, result, err)
}
func (h *IndexStatusHandler) AccountDocument(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.AccountDocument(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"), chi.URLParam(r, "docId"))
	writeIndexStatus(w, result, err)
}
func writeIndexStatus(w http.ResponseWriter, result *service.IndexStatusResponse, err error) {
	w.Header().Set("Cache-Control", "no-store")
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid resource identifier")
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Resource not found")
	case err != nil || result == nil:
		writeError(w, http.StatusServiceUnavailable, "INDEX_UNAVAILABLE", "Search status is temporarily unavailable")
	default:
		writeJSON(w, http.StatusOK, result)
	}
}
