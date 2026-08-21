package handler

import (
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

// UserAdminHandler handles the admin-only user-management endpoints backing
// the Settings page's "사용자 관리" panel. All routes here are mounted inside
// the router's RequireAdmin group (cmd/api/main.go) — every handler in this
// file assumes the caller is already verified as an admin.
type UserAdminHandler struct {
	userAdmin *service.UserAdminService
}

// NewUserAdminHandler creates a new user admin handler
func NewUserAdminHandler(userAdmin *service.UserAdminService) *UserAdminHandler {
	return &UserAdminHandler{userAdmin: userAdmin}
}

// ListUsers handles GET /api/settings/users
func (h *UserAdminHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	resp, err := h.userAdmin.ListUsers(ctx)
	if err != nil {
		log.Printf("ListUsers failed: %v", err)
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Failed to list users")
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// DeleteUser handles DELETE /api/settings/users/{userId}
func (h *UserAdminHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actorID := middleware.GetUserID(ctx)
	targetID := chi.URLParam(r, "userId")

	resp, err := h.userAdmin.DeleteUser(ctx, actorID, targetID)
	if h.handleActionError(w, "DeleteUser", targetID, err) {
		return
	}
	auditAdminAction(actorID, targetID, "DeleteUser")

	writeJSON(w, http.StatusOK, resp)
}

// EnableUser handles PUT /api/settings/users/{userId}/enable
func (h *UserAdminHandler) EnableUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actorID := middleware.GetUserID(ctx)
	targetID := chi.URLParam(r, "userId")

	resp, err := h.userAdmin.EnableUser(ctx, targetID)
	if h.handleActionError(w, "EnableUser", targetID, err) {
		return
	}
	auditAdminAction(actorID, targetID, "EnableUser")

	writeJSON(w, http.StatusOK, resp)
}

// DisableUser handles PUT /api/settings/users/{userId}/disable
func (h *UserAdminHandler) DisableUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actorID := middleware.GetUserID(ctx)
	targetID := chi.URLParam(r, "userId")

	resp, err := h.userAdmin.DisableUser(ctx, actorID, targetID)
	if h.handleActionError(w, "DisableUser", targetID, err) {
		return
	}
	auditAdminAction(actorID, targetID, "DisableUser")

	writeJSON(w, http.StatusOK, resp)
}

// ResendInvite handles POST /api/settings/users/{userId}/resend-invite
func (h *UserAdminHandler) ResendInvite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actorID := middleware.GetUserID(ctx)
	targetID := chi.URLParam(r, "userId")

	if err := h.userAdmin.ResendInvite(ctx, targetID); err != nil {
		h.handleActionError(w, "ResendInvite", targetID, err)
		return
	}
	auditAdminAction(actorID, targetID, "ResendInvite")

	writeJSON(w, http.StatusOK, model.AdminUserActionResponse{UserID: targetID})
}

// ResetPassword handles POST /api/settings/users/{userId}/reset-password
func (h *UserAdminHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actorID := middleware.GetUserID(ctx)
	targetID := chi.URLParam(r, "userId")

	if err := h.userAdmin.ForceResetPassword(ctx, targetID); err != nil {
		h.handleActionError(w, "ResetPassword", targetID, err)
		return
	}
	auditAdminAction(actorID, targetID, "ResetPassword")

	writeJSON(w, http.StatusOK, model.AdminUserActionResponse{UserID: targetID})
}

// auditAdminAction logs a successful destructive/identity-affecting admin
// action with actor+target+action attribution. CloudTrail already records
// the underlying Cognito API calls, but only against this Lambda's own
// execution role -- it cannot attribute WHICH admin performed the action.
// This is the only place that attribution exists; do not remove without
// replacing it (e.g. a dedicated audit table).
func auditAdminAction(actorID, targetID, action string) {
	log.Printf("admin audit: actor=%s target=%s action=%s", actorID, targetID, action)
}

// handleActionError maps a UserAdminService error to an HTTP response and
// reports whether it wrote one (true = caller should return immediately).
func (h *UserAdminHandler) handleActionError(w http.ResponseWriter, op, targetID string, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, service.ErrCannotModifySelf),
		errors.Is(err, service.ErrLastAdmin),
		errors.Is(err, service.ErrInvalidStatusForAction):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, err.Error())
	case errors.Is(err, service.ErrUserNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "User not found")
	default:
		log.Printf("%s failed for %s: %v", op, targetID, err)
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Request failed")
	}
	return true
}
