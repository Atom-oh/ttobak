package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

const meetingTranslationMaxBodyBytes = 4096

type meetingPageTranslator interface {
	Translate(ctx context.Context, userID, meetingID string, req service.MeetingTranslationRequest) ([]byte, error)
}

// MeetingTranslationHandler serves POST /api/meetings/{meetingId}/translation.
// The result lives only in the response; nothing is stored.
type MeetingTranslationHandler struct {
	service meetingPageTranslator
	enabled bool
	now     func() time.Time
}

func NewMeetingTranslationHandler(s meetingPageTranslator, enabled bool) *MeetingTranslationHandler {
	return &MeetingTranslationHandler{service: s, enabled: enabled, now: time.Now}
}

func (h *MeetingTranslationHandler) Post(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// Off answers like an absent route, before identity or storage is touched.
	if !h.enabled || h.service == nil {
		writeError(w, http.StatusNotFound, "FEATURE_DISABLED", "Meeting translation is not enabled")
		return
	}
	userID := middleware.GetUserID(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, model.ErrCodeUnauthorized, "Authentication required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, meetingTranslationMaxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var req service.MeetingTranslationRequest
	err := decoder.Decode(&req)
	if err == nil {
		// Trailing whitespace past the limit is still an oversized body.
		if _, err = decoder.Token(); errors.Is(err, io.EOF) {
			err = nil
		} else if err == nil {
			err = errors.New("trailing value")
		}
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, model.ErrCodeBadRequest, "Request body is too large")
			return
		}
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	body, err := h.service.Translate(r.Context(), userID, chi.URLParam(r, "meetingId"), req)
	if err != nil {
		var limited *service.MeetingTranslationLimitError
		switch {
		case errors.As(err, &limited):
			w.Header().Set("Retry-After", retryAfterSeconds(h.now(), limited.RetryAt))
			writeError(w, http.StatusTooManyRequests, model.ErrCodeRateLimited, "Daily meeting translation limit reached; try again later")
		case errors.Is(err, service.ErrMeetingTranslationInvalid):
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "part must be notes or transcript and target ko or en")
		case errors.Is(err, service.ErrMeetingTranslationUnavailable):
			writeError(w, http.StatusBadGateway, "TRANSLATION_FAILED", "Translation failed; try again")
		case errors.Is(err, service.ErrMeetingTranslationTooLarge):
			writeError(w, http.StatusBadGateway, "TRANSLATION_TOO_LARGE", "Translated page is too large")
		default:
			status, code, message := readingErrorResponse(err)
			if status == http.StatusInternalServerError {
				log.Printf("Meeting translation failed: %v", err)
			}
			writeError(w, status, code, message)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
