package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type ResearchShareHandler struct {
	researchService *service.ResearchService
}

func NewResearchShareHandler(researchService *service.ResearchService) *ResearchShareHandler {
	return &ResearchShareHandler{researchService: researchService}
}

func (h *ResearchShareHandler) ShareResearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	userEmail := middleware.GetUserEmail(ctx)
	researchID := chi.URLParam(r, "researchId")

	if researchID == "" || strings.Contains(researchID, "..") || strings.Contains(researchID, "/") {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid researchId")
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
		req.Permission = model.PermissionRead
	}

	if req.Permission != model.PermissionRead && req.Permission != model.PermissionEdit {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Permission must be 'read' or 'edit'")
		return
	}

	share, err := h.researchService.ShareResearchByEmail(ctx, userID, userEmail, researchID, req.Email, req.Permission)
	if err != nil {
		if errors.Is(err, service.ErrForbidden) {
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Only owner can share")
			return
		}
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Research not found")
			return
		}
		if errors.Is(err, service.ErrUserNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "User not found")
			return
		}
		if errors.Is(err, service.ErrSelfShare) {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Cannot share with yourself")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "internal error")
		return
	}

	response := model.SharedWithResponse{
		SharedWith: model.ShareResponse{
			UserID:     share.SharedToID,
			Email:      share.Email,
			Permission: share.Permission,
		},
	}

	writeJSON(w, http.StatusOK, response)
}

func (h *ResearchShareHandler) RevokeResearchShare(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ownerID := middleware.GetUserID(ctx)
	researchID := chi.URLParam(r, "researchId")
	sharedToID := chi.URLParam(r, "userId")

	if researchID == "" || strings.Contains(researchID, "..") || strings.Contains(researchID, "/") {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid researchId")
		return
	}

	if sharedToID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "User ID is required")
		return
	}

	err := h.researchService.RevokeResearchShare(ctx, ownerID, researchID, sharedToID)
	if err != nil {
		if errors.Is(err, service.ErrForbidden) {
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Only owner can revoke share")
			return
		}
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Research not found")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
