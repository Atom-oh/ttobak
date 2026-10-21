package handler

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/service"
)

type AccountReadingHandler struct {
	service *service.AccountReadingService
}

func NewAccountReadingHandler(s *service.AccountReadingService) *AccountReadingHandler {
	return &AccountReadingHandler{s}
}

func (h *AccountReadingHandler) Get(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := middleware.GetUserID(r.Context())
	if user == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Authentication required")
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid account reading query")
		return
	}
	body, err := h.service.Read(r.Context(), user, chi.URLParam(r, "accountId"), q)
	if err != nil {
		status, code, message := http.StatusInternalServerError, "READING_UNAVAILABLE", "Account reading is unavailable"
		switch {
		case errors.Is(err, service.ErrSummaryReadRestricted):
			status, code, message = http.StatusConflict, "SHARING_REGEN_REQUIRED", "The owner must regenerate sharing-safe meeting notes before these insights can be read"
		case errors.Is(err, service.ErrForbidden):
			status, code, message = http.StatusForbidden, "FORBIDDEN", "Access denied"
		case errors.Is(err, service.ErrNotFound):
			status, code, message = http.StatusNotFound, "NOT_FOUND", "Account not found"
		case errors.Is(err, service.ErrInvalidReadingOptions):
			status, code, message = http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid account reading options"
		case errors.Is(err, service.ErrInvalidReadingCursor):
			status, code, message = http.StatusBadRequest, "INVALID_CURSOR", "Invalid account reading cursor"
		case errors.Is(err, service.ErrStaleReadingCursor):
			status, code, message = http.StatusConflict, "STALE_CURSOR", "Account source or access changed; restart without cursor"
		}
		writeError(w, status, code, message)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
