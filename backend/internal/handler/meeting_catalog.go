package handler

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/service"
)

type MeetingCatalogHandler struct {
	service *service.MeetingCatalogService
}

func NewMeetingCatalogHandler(s *service.MeetingCatalogService) *MeetingCatalogHandler {
	return &MeetingCatalogHandler{s}
}

func (h *MeetingCatalogHandler) Get(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := middleware.GetUserID(r.Context())
	if user == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Authentication required")
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid meeting catalog reading query")
		return
	}
	identity := service.MeetingCatalogIdentity{UserID: user, Email: middleware.GetUserEmail(r.Context()), Name: middleware.GetUserName(r.Context()), EmailVerified: middleware.GetEmailVerified(r.Context())}
	body, err := h.service.Read(r.Context(), identity, q)
	if err != nil {
		status, code, message := http.StatusInternalServerError, "READING_UNAVAILABLE", "Meeting catalog reading is unavailable"
		switch {
		case errors.Is(err, service.ErrForbidden):
			status, code, message = http.StatusForbidden, "FORBIDDEN", "Access denied"
		case errors.Is(err, service.ErrNotFound):
			status, code, message = http.StatusNotFound, "NOT_FOUND", "Meeting catalog not found"
		case errors.Is(err, service.ErrInvalidReadingOptions):
			status, code, message = http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid meeting catalog reading options"
		case errors.Is(err, service.ErrInvalidReadingCursor):
			status, code, message = http.StatusBadRequest, "INVALID_CURSOR", "Invalid meeting catalog reading cursor"
		case errors.Is(err, service.ErrStaleReadingCursor):
			status, code, message = http.StatusConflict, "STALE_CURSOR", "Meeting catalog source or access changed; restart without cursor"
		}
		writeError(w, status, code, message)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
