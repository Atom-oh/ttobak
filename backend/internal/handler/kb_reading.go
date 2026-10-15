package handler

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/service"
)

func (h *KBHandler) ReadFiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := middleware.GetUserID(r.Context())
	if user == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Authentication required")
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid KB catalog query")
		return
	}
	body, err := h.kbService.ReadFiles(r.Context(), user, q)
	if err != nil {
		status, code, message := http.StatusInternalServerError, "READING_UNAVAILABLE", "Knowledge Base files are unavailable"
		switch {
		case errors.Is(err, service.ErrForbidden):
			status, code, message = http.StatusForbidden, "FORBIDDEN", "Access denied"
		case errors.Is(err, service.ErrInvalidReadingOptions):
			status, code, message = http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid KB catalog options"
		case errors.Is(err, service.ErrInvalidReadingCursor):
			status, code, message = http.StatusBadRequest, "INVALID_CURSOR", "Invalid KB catalog cursor"
		case errors.Is(err, service.ErrStaleReadingCursor):
			status, code, message = http.StatusConflict, "STALE_CURSOR", "KB files changed; restart without cursor"
		}
		writeError(w, status, code, message)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
