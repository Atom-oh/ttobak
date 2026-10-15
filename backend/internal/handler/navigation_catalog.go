package handler

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/service"
)

type NavigationCatalogHandler struct {
	service *service.NavigationCatalogService
	kind    string
}

func NewNavigationCatalogHandler(s *service.NavigationCatalogService, kind string) *NavigationCatalogHandler {
	return &NavigationCatalogHandler{s, kind}
}

func (h *NavigationCatalogHandler) Get(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := middleware.GetUserID(r.Context())
	if user == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Authentication required")
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid navigation query")
		return
	}
	body, err := h.service.Read(r.Context(), user, h.kind, q)
	if err != nil {
		status, code, message := http.StatusInternalServerError, "READING_UNAVAILABLE", "Navigation catalog is unavailable"
		switch {
		case errors.Is(err, service.ErrForbidden):
			status, code, message = http.StatusForbidden, "FORBIDDEN", "Access denied"
		case errors.Is(err, service.ErrInvalidReadingOptions):
			status, code, message = http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid navigation options"
		case errors.Is(err, service.ErrInvalidReadingCursor):
			status, code, message = http.StatusBadRequest, "INVALID_CURSOR", "Invalid navigation cursor"
		case errors.Is(err, service.ErrStaleReadingCursor):
			status, code, message = http.StatusConflict, "STALE_CURSOR", "Navigation source or access changed; restart without cursor"
		}
		writeError(w, status, code, message)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
