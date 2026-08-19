package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

// ShareHandler handles meeting sharing requests
type ShareHandler struct {
	meetingService *service.MeetingService
}

// NewShareHandler creates a new share handler
func NewShareHandler(meetingService *service.MeetingService) *ShareHandler {
	return &ShareHandler{
		meetingService: meetingService,
	}
}

// ShareMeeting handles POST /api/meetings/{meetingId}/share
func (h *ShareHandler) ShareMeeting(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	userEmail := middleware.GetUserEmail(ctx)
	meetingID := chi.URLParam(r, "meetingId")

	if meetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Meeting ID is required")
		return
	}

	var req model.ShareMeetingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	if req.Email == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Email is required")
		return
	}

	if req.Permission == "" {
		req.Permission = model.PermissionRead // Default to read
	}

	if req.Permission != model.PermissionRead && req.Permission != model.PermissionEdit {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Permission must be 'read' or 'edit'")
		return
	}

	share, pending, err := h.meetingService.ShareMeetingByEmail(ctx, userID, userEmail, meetingID, req.Email, req.Permission)
	if err != nil {
		switch err.Error() {
		case "forbidden":
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Only owner can share")
			return
		case "not found":
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
			return
		case "user not found":
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "User not found")
			return
		case "cannot share with yourself":
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Cannot share meeting with yourself")
			return
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
			return
		}
	}

	var response model.SharedWithResponse
	if pending {
		response = model.SharedWithResponse{
			SharedWith: model.ShareResponse{
				Email:      req.Email,
				Permission: req.Permission,
				Pending:    true,
			},
		}
	} else {
		response = model.SharedWithResponse{
			SharedWith: model.ShareResponse{
				UserID:     share.SharedToID,
				Email:      share.Email,
				Permission: share.Permission,
			},
		}
	}

	writeJSON(w, http.StatusOK, response)
}

// ShareToAccount handles POST /api/meetings/{meetingId}/share-account.
func (h *ShareHandler) ShareToAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	userEmail := middleware.GetUserEmail(ctx)
	meetingID := chi.URLParam(r, "meetingId")
	if meetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Meeting ID is required")
		return
	}
	var req model.ShareToAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AccountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "accountId is required")
		return
	}
	res, err := h.meetingService.ShareMeetingToAccount(ctx, userID, userEmail, meetingID, req.AccountID)
	if err != nil {
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
	writeJSON(w, http.StatusOK, res)
}

// RevokeShare handles DELETE /api/meetings/{meetingId}/share/{userId}
func (h *ShareHandler) RevokeShare(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ownerID := middleware.GetUserID(ctx)
	meetingID := chi.URLParam(r, "meetingId")
	sharedToID := chi.URLParam(r, "userId")

	if meetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Meeting ID is required")
		return
	}

	if sharedToID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "User ID is required")
		return
	}

	err := h.meetingService.RevokeShare(ctx, ownerID, meetingID, sharedToID)
	if err != nil {
		switch err.Error() {
		case "forbidden":
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Only owner can revoke share")
			return
		case "not found":
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
			return
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// SearchUsers handles GET /api/users/search?q={email-prefix}
func (h *ShareHandler) SearchUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query().Get("q")

	if query == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Search query is required")
		return
	}

	users, err := h.meetingService.SearchUsers(ctx, query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	response := model.UserSearchListResponse{
		Users: users,
	}

	writeJSON(w, http.StatusOK, response)
}
