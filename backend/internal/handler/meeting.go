package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

// MeetingHandler handles meeting-related requests
type MeetingHandler struct {
	meetingService *service.MeetingService
	uploadService  *service.UploadService
	repo           *repository.DynamoDBRepository
}

// NewMeetingHandler creates a new meeting handler
func NewMeetingHandler(meetingService *service.MeetingService, repo *repository.DynamoDBRepository, uploadService ...*service.UploadService) *MeetingHandler {
	h := &MeetingHandler{
		meetingService: meetingService,
		repo:           repo,
	}
	if len(uploadService) > 0 {
		h.uploadService = uploadService[0]
	}
	return h
}

// ListMeetings handles GET /api/meetings?tab={all|shared}&cursor={lastKey}&limit={20}
func (h *MeetingHandler) ListMeetings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	// Ensure user profile exists
	email := middleware.GetUserEmail(ctx)
	name := middleware.GetUserName(ctx)
	if email != "" {
		h.repo.GetOrCreateUser(ctx, userID, email, name)
	}

	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "all"
	}
	cursor := r.URL.Query().Get("cursor")

	var limit int32 = 20
	// Could parse limit from query if needed

	result, err := h.meetingService.ListMeetings(ctx, userID, tab, cursor, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// CreateMeeting handles POST /api/meetings
func (h *MeetingHandler) CreateMeeting(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	var req model.CreateMeetingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	if req.Title == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Title is required")
		return
	}

	// Parse date or use current time
	var date time.Time
	if req.Date != "" {
		var err error
		date, err = time.Parse(time.RFC3339, req.Date)
		if err != nil {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid date format")
			return
		}
	} else {
		date = time.Now().UTC()
	}

	// Ensure user profile exists
	email := middleware.GetUserEmail(ctx)
	name := middleware.GetUserName(ctx)
	if email != "" {
		h.repo.GetOrCreateUser(ctx, userID, email, name)
	}

	meeting, err := h.meetingService.CreateMeeting(ctx, userID, req.Title, date, req.Participants, req.SttProvider)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	response := map[string]interface{}{
		"meetingId":    meeting.MeetingID,
		"title":        meeting.Title,
		"date":         meeting.Date.Format(time.RFC3339),
		"status":       meeting.Status,
		"participants": meeting.Participants,
		"content":      meeting.Content,
		"createdAt":    meeting.CreatedAt.Format(time.RFC3339),
	}

	writeJSON(w, http.StatusCreated, response)
}

// GetMeeting handles GET /api/meetings/{meetingId}
func (h *MeetingHandler) GetMeeting(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	meetingID := chi.URLParam(r, "meetingId")

	if meetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Meeting ID is required")
		return
	}

	result, err := h.meetingService.GetMeetingDetail(ctx, userID, meetingID)
	if err != nil {
		if errors.Is(err, service.ErrForbidden) {
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
			return
		}
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	// Auto-expire stuck transcribing/summarizing status after 30 minutes
	if (result.Status == model.StatusTranscribing || result.Status == model.StatusSummarizing) &&
		result.UpdatedAt != "" {
		if updatedAt, parseErr := time.Parse(time.RFC3339, result.UpdatedAt); parseErr == nil {
			if time.Since(updatedAt) > 30*time.Minute {
				result.Status = model.StatusError
				// Also update in DynamoDB so it doesn't stay stuck
				if meeting, mErr := h.repo.GetMeetingByID(ctx, meetingID); mErr == nil && meeting != nil {
					meeting.Status = model.StatusError
					h.repo.UpdateMeeting(ctx, meeting)
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// UpdateMeeting handles PUT /api/meetings/{meetingId}
func (h *MeetingHandler) UpdateMeeting(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	meetingID := chi.URLParam(r, "meetingId")

	if meetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Meeting ID is required")
		return
	}

	var req model.UpdateMeetingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	result, err := h.meetingService.UpdateMeeting(ctx, userID, meetingID, &req)
	if err != nil {
		if errors.Is(err, service.ErrForbidden) {
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied - read-only permission")
			return
		}
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// DeleteMeeting handles DELETE /api/meetings/{meetingId}
func (h *MeetingHandler) DeleteMeeting(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	meetingID := chi.URLParam(r, "meetingId")

	if meetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Meeting ID is required")
		return
	}

	err := h.meetingService.DeleteMeeting(ctx, userID, meetingID)
	if err != nil {
		if errors.Is(err, service.ErrForbidden) {
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Only owner can delete")
			return
		}
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// UpdateSpeakers handles PUT /api/meetings/{meetingId}/speakers
func (h *MeetingHandler) UpdateSpeakers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	meetingID := chi.URLParam(r, "meetingId")

	if meetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Meeting ID is required")
		return
	}

	var req model.UpdateSpeakersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	if len(req.SpeakerMap) == 0 {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Speaker map is required")
		return
	}

	result, err := h.meetingService.UpdateSpeakers(ctx, userID, meetingID, &req)
	if err != nil {
		if errors.Is(err, service.ErrForbidden) {
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
			return
		}
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// SelectTranscript handles PUT /api/meetings/{meetingId}/transcript
func (h *MeetingHandler) SelectTranscript(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	meetingID := chi.URLParam(r, "meetingId")

	if meetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Meeting ID is required")
		return
	}

	var req model.SelectTranscriptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	if req.Selected != "A" && req.Selected != "B" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Selected must be 'A' or 'B'")
		return
	}

	err := h.meetingService.SelectTranscript(ctx, userID, meetingID, req.Selected)
	if err != nil {
		if errors.Is(err, service.ErrForbidden) {
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
			return
		}
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("{}"))
}

// GetAudioURL handles GET /api/meetings/{meetingId}/audio
// Returns a fresh presigned download URL for the meeting's audio file
func (h *MeetingHandler) GetAudioURL(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	meetingID := chi.URLParam(r, "meetingId")

	if meetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Meeting ID is required")
		return
	}

	// Verify ownership via GetMeetingDetail
	result, err := h.meetingService.GetMeetingDetail(ctx, userID, meetingID)
	if err != nil {
		if errors.Is(err, service.ErrForbidden) {
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
			return
		}
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	if result.AudioKey == "" {
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "No audio file for this meeting")
		return
	}

	if h.uploadService == nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Upload service not configured")
		return
	}

	audioURL, err := h.uploadService.GeneratePresignedDownloadURL(ctx, result.AudioKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Failed to generate audio URL")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"audioUrl": audioURL})
}

// RecoverMeeting handles POST /api/meetings/{meetingId}/recover
// Recovers a crashed recording by copying the progress checkpoint to a final audio file
func (h *MeetingHandler) RecoverMeeting(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	meetingID := chi.URLParam(r, "meetingId")

	if meetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Meeting ID is required")
		return
	}

	if h.uploadService == nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Upload service not configured")
		return
	}

	err := h.uploadService.RecoverMeeting(ctx, userID, meetingID)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
			return
		}
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"meetingId": meetingID, "status": "transcribing"})
}

// writeJSON writes a JSON response
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// writeError writes an error response in API spec format
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(model.NewErrorResponse(code, message))
}
