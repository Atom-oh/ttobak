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
		status, code, message := readingErrorResponse(err)
		if status == http.StatusInternalServerError {
			log.Printf("Meeting reading failed: %v", err)
		}
		writeError(w, status, code, message)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// readingErrorResponse maps MeetingReadingService sentinels; translation of a
// reading page reuses it so both routes answer authorization and cursor
// failures identically.
func readingErrorResponse(err error) (int, string, string) {
	switch {
	case errors.Is(err, service.ErrSummaryReadRestricted):
		return http.StatusConflict, "SHARING_REGEN_REQUIRED", "소유자가 공유용 회의록을 재생성해야 이 내용을 읽을 수 있습니다."
	case errors.Is(err, service.ErrForbidden):
		return http.StatusForbidden, "FORBIDDEN", "Access denied"
	case errors.Is(err, service.ErrNotFound):
		return http.StatusNotFound, "NOT_FOUND", "Meeting not found"
	case errors.Is(err, service.ErrInvalidReadingOptions):
		return http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid reading options"
	case errors.Is(err, service.ErrInvalidReadingCursor):
		return http.StatusBadRequest, "INVALID_CURSOR", "Invalid reading cursor"
	case errors.Is(err, service.ErrStaleReadingCursor):
		return http.StatusConflict, "STALE_CURSOR", service.ErrStaleReadingCursor.Error()
	case errors.Is(err, service.ErrNoReadingTranscript):
		return http.StatusBadRequest, "NO_TRANSCRIPT", service.ErrNoReadingTranscript.Error()
	case errors.Is(err, service.ErrReadingTimeRange):
		return http.StatusBadRequest, "TIME_RANGE_UNAVAILABLE", service.ErrReadingTimeRange.Error()
	}
	return http.StatusInternalServerError, "READING_UNAVAILABLE", "Meeting reading is unavailable"
}
