package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

func (h *MeetingHandler) CropAudio(w http.ResponseWriter, r *http.Request) {
	if h.uploadService == nil {
		writeError(w, http.StatusServiceUnavailable, model.ErrCodeInternalError, "Audio cropping is unavailable")
		return
	}
	var request model.AudioCropRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid crop request")
		return
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Only one crop request is allowed")
		return
	}
	meeting, err := h.uploadService.CropMeeting(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "meetingId"), request)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrNotFound):
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Only the owner can crop this recording")
		case errors.Is(err, service.ErrInvalidInput):
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, err.Error())
		case errors.Is(err, repository.ErrConditionFailed):
			writeError(w, http.StatusConflict, "CONFLICT", "The meeting changed; refresh and retry")
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Could not queue crop; retry the same request")
		}
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"meetingId": meeting.MeetingID, "status": meeting.Status})
}
