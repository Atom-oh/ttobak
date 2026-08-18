package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	// simService is optional (see SetSimService) so GetMeeting can attach
	// the meeting's SimRun (ADR-031) without every existing NewMeetingHandler
	// call site needing to change.
	simService *service.SimService
}

// SetSimService injects the cost/sizing simulator service (ADR-031) so
// GetMeeting can attach the meeting's current SimRun to its response.
func (h *MeetingHandler) SetSimService(s *service.SimService) {
	h.simService = s
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

// LinkToAccount handles POST /api/meetings/{meetingId}/account.
func (h *MeetingHandler) LinkToAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	meetingID := chi.URLParam(r, "meetingId")
	if meetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Meeting ID is required")
		return
	}
	var req model.LinkAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AccountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "accountId is required")
		return
	}
	if err := h.meetingService.LinkMeetingToAccount(ctx, userID, meetingID, req.AccountID); err != nil {
		switch {
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
		case errors.Is(err, service.ErrNotFound):
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"accountId": req.AccountID})
}

// ListMeetings handles GET /api/meetings?tab={all|shared}&cursor={lastKey}&limit={20}.
// `tab` defaults to "all"; "shared" returns meetings shared with the caller.
// `cursor` is the opaque last-evaluated-key from a previous page; `limit` caps
// page size at 20 items by default.
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

	// Set linked meeting IDs if provided (validate ownership to prevent cross-user leakage)
	if len(req.LinkedMeetingIDs) > 0 {
		if len(req.LinkedMeetingIDs) > 3 {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Maximum 3 linked predecessors")
			return
		}
		for _, linkedID := range req.LinkedMeetingIDs {
			if linkedID == meeting.MeetingID {
				continue
			}
			linked, lookupErr := h.repo.GetMeeting(ctx, userID, linkedID)
			if lookupErr != nil || linked == nil {
				writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, fmt.Sprintf("Linked meeting %s not found or not owned", linkedID))
				return
			}
		}
		if err := h.repo.UpdateMeetingFields(ctx, userID, meeting.MeetingID, map[string]interface{}{
			"linkedMeetingIds": req.LinkedMeetingIDs,
		}); err != nil {
			log.Printf("Failed to set linkedMeetingIds: %v", err)
		}
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

	// Generate presigned download URLs for image attachments
	if h.uploadService != nil {
		for i := range result.Attachments {
			att := &result.Attachments[i]
			if att.OriginalKey != "" {
				if url, err := h.uploadService.GeneratePresignedDownloadURL(ctx, att.OriginalKey); err == nil {
					att.URL = url
				}
			}
		}
	}

	// Attach the meeting's cost/sizing simulation state, if any (ADR-031).
	// A stuck run (Lambda died mid-write) is detected AND persisted as
	// errored inside SimService.GetSimRun itself now (mirrors isStuck's
	// read-triggered write for transcribing/summarizing meetings) --
	// ReconcileStuckSimRun here is just a defensive fallback for this one
	// response in case that persist failed, not the primary reconciliation
	// path anymore.
	if h.simService != nil {
		run, err := h.simService.GetSimRun(ctx, meetingID)
		if err != nil {
			// Best-effort attach: simRun is a secondary field on GetMeeting,
			// not worth failing the whole request over -- but a silent
			// swallow here previously gave no signal at all when this broke.
			log.Printf("GetMeeting: failed to get sim run for meeting %s: %v", meetingID, err)
		} else if run != nil {
			if service.ReconcileStuckSimRun(run) {
				run.Status = model.SimStatusError
				run.ErrorMessage = "시뮬레이션이 응답하지 않아 시간 초과로 처리되었습니다"
			}
			result.SimRun = model.ToSimRunResponse(run)
			if h.uploadService != nil {
				for i := range result.SimRun.Charts {
					c := &result.SimRun.Charts[i]
					if url, err := h.uploadService.GeneratePresignedDownloadURL(ctx, c.Key); err == nil {
						c.URL = url
					} else {
						log.Printf("GetMeeting: failed to presign sim chart %s for meeting %s: %v", c.Key, meetingID, err)
					}
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
		if errors.Is(err, service.ErrInvalidInput) {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, err.Error())
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

	if h.uploadService == nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Upload service not configured")
		return
	}

	// Multi-file: return all audio URLs
	if len(result.AudioKeys) > 0 {
		var audioUrls []string
		for _, key := range result.AudioKeys {
			if key == "" {
				continue // skip pre-allocated empty slots
			}
			url, urlErr := h.uploadService.GeneratePresignedDownloadURL(ctx, key)
			if urlErr != nil {
				writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Failed to generate audio URL")
				return
			}
			audioUrls = append(audioUrls, url)
		}
		if len(audioUrls) == 0 {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "No audio files for this meeting")
			return
		}
		writeJSON(w, http.StatusOK, model.AudioURLResponse{AudioUrls: audioUrls})
		return
	}

	// Single-file (legacy)
	if result.AudioKey == "" {
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "No audio file for this meeting")
		return
	}

	audioURL, err := h.uploadService.GeneratePresignedDownloadURL(ctx, result.AudioKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Failed to generate audio URL")
		return
	}

	writeJSON(w, http.StatusOK, model.AudioURLResponse{AudioUrl: audioURL})
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

// RediarizeMeeting handles POST /api/meetings/{meetingId}/rediarize
// Re-runs Whisper speaker diarization with a user-supplied speaker-count hint
// (see UploadService.RediarizeMeeting for the whisper-only / single-part guards).
func (h *MeetingHandler) RediarizeMeeting(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	meetingID := chi.URLParam(r, "meetingId")

	if meetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Meeting ID is required")
		return
	}

	var req model.RediarizeMeetingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	if h.uploadService == nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Upload service not configured")
		return
	}

	if err := h.uploadService.RediarizeMeeting(ctx, userID, meetingID, req.SpeakerCount); err != nil {
		switch {
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
		case errors.Is(err, service.ErrNotFound):
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
		case errors.Is(err, service.ErrInvalidInput):
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, err.Error())
		default:
			log.Printf("RediarizeMeeting failed: %v", err)
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Failed to re-diarize meeting")
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"meetingId": meetingID, "status": "transcribing"})
}

// LinkMeetings handles POST /api/meetings/{meetingId}/link
func (h *MeetingHandler) LinkMeetings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	meetingID := chi.URLParam(r, "meetingId")

	if meetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Meeting ID is required")
		return
	}

	var req model.LinkMeetingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	if len(req.LinkedMeetingIDs) == 0 {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "At least one linked meeting ID is required")
		return
	}
	if len(req.LinkedMeetingIDs) > 3 {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Maximum 3 linked predecessors allowed")
		return
	}

	// Verify ownership of the parent meeting FIRST. `UpdateMeetingFields` is
	// upsert-style — without this check a user could create a phantom row at
	// `USER#caller + MEETING#someone-elses-id` (cross-user data leak is
	// blocked by the PK scoping, but the orphan row would pollute the
	// caller's list view).
	parent, err := h.repo.GetMeeting(ctx, userID, meetingID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}
	if parent == nil {
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
		return
	}

	// Verify ownership of all linked predecessors and reject cycles.
	// `buildLinkedMeetingContext` only walks one level (max 3 predecessors),
	// so infinite traversal isn't possible, but a 1-hop cycle (A→B + B→A)
	// would still produce broken breadcrumb chains and waste tokens by
	// embedding the parent's own summary back into its own prompt.
	for _, linkedID := range req.LinkedMeetingIDs {
		if linkedID == meetingID {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Cannot link a meeting to itself")
			return
		}
		linked, err := h.repo.GetMeeting(ctx, userID, linkedID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
			return
		}
		if linked == nil {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, fmt.Sprintf("Linked meeting %s not found", linkedID))
			return
		}
		// Reject 1-hop reverse references — if `linked` already lists
		// `meetingID` as one of ITS predecessors, this link would form
		// a cycle.
		for _, reverseID := range linked.LinkedMeetingIDs {
			if reverseID == meetingID {
				writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest,
					fmt.Sprintf("Cannot link to %s — that meeting already lists this one as a predecessor (cycle)", linkedID))
				return
			}
		}
	}

	err = h.repo.UpdateMeetingFields(ctx, userID, meetingID, map[string]interface{}{
		"linkedMeetingIds": req.LinkedMeetingIDs,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
