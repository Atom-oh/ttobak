package handler

import (
	"errors"
	"log"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/service"
)

type MeetingReadingHandler struct {
	service *service.MeetingReadingService
}

func NewMeetingReadingHandler(s *service.MeetingReadingService) *MeetingReadingHandler {
	return &MeetingReadingHandler{service: s}
}

func (h *MeetingReadingHandler) Get(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	userID := middleware.GetUserID(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Authentication required")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid reading query")
		return
	}
	body, err := h.service.Read(r.Context(), userID, chi.URLParam(r, "meetingId"), query)
	if err != nil {
		status, code, message := http.StatusInternalServerError, "READING_UNAVAILABLE", "Meeting reading is unavailable"
		switch {
		case errors.Is(err, service.ErrForbidden):
			status, code, message = http.StatusForbidden, "FORBIDDEN", "Access denied"
		case errors.Is(err, service.ErrNotFound):
			status, code, message = http.StatusNotFound, "NOT_FOUND", "Meeting not found"
		case errors.Is(err, service.ErrInvalidReadingOptions):
			status, code, message = http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid reading options"
		case errors.Is(err, service.ErrInvalidReadingCursor):
			status, code, message = http.StatusBadRequest, "INVALID_CURSOR", "Invalid reading cursor"
		case errors.Is(err, service.ErrStaleReadingCursor):
			status, code, message = http.StatusConflict, "STALE_CURSOR", service.ErrStaleReadingCursor.Error()
		case errors.Is(err, service.ErrNoReadingTranscript):
			status, code, message = http.StatusBadRequest, "NO_TRANSCRIPT", service.ErrNoReadingTranscript.Error()
		case errors.Is(err, service.ErrReadingTimeRange):
			status, code, message = http.StatusBadRequest, "TIME_RANGE_UNAVAILABLE", service.ErrReadingTimeRange.Error()
		default:
			log.Printf("Meeting reading failed: %v", err)
		}
		writeError(w, status, code, message)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
