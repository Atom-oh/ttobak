package handler

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/service"
)

type DocumentReadingHandler struct {
	service *service.DocumentReadingService
}

func NewDocumentReadingHandler(s *service.DocumentReadingService) *DocumentReadingHandler {
	return &DocumentReadingHandler{service: s}
}

func (h *DocumentReadingHandler) Get(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	userID := middleware.GetUserID(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Authentication required")
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid reading query")
		return
	}
	body, err := h.service.Read(r.Context(), userID, chi.URLParam(r, "accountId"), chi.URLParam(r, "docId"), q)
	if err != nil {
		status, code, message := http.StatusInternalServerError, "READING_UNAVAILABLE", "Document reading is unavailable"
		switch {
		case errors.Is(err, service.ErrForbidden):
			status, code, message = http.StatusForbidden, "FORBIDDEN", "Access denied"
		case errors.Is(err, service.ErrNotFound):
			status, code, message = http.StatusNotFound, "NOT_FOUND", "Document not found"
		case errors.Is(err, service.ErrInvalidReadingOptions):
			status, code, message = http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid reading options"
		case errors.Is(err, service.ErrInvalidReadingCursor):
			status, code, message = http.StatusBadRequest, "INVALID_CURSOR", "Invalid reading cursor"
		case errors.Is(err, service.ErrStaleReadingCursor):
			status, code, message = http.StatusConflict, "STALE_CURSOR", service.ErrStaleReadingCursor.Error()
		}
		writeError(w, status, code, message)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
