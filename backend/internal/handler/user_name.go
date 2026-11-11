package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

// maxUserNameBody bounds the rename request body; a valid name is at most
// 64 code points (256 UTF-8 bytes) plus JSON framing.
const maxUserNameBody = 1024

// ProfileHandler serves self-service profile routes for any signed-in user.
type ProfileHandler struct {
	userAdmin *service.UserAdminService
}

// NewProfileHandler creates a self-service profile handler.
func NewProfileHandler(userAdmin *service.UserAdminService) *ProfileHandler {
	return &ProfileHandler{userAdmin: userAdmin}
}

// UpdateMyName handles PUT /api/settings/profile/name. The target is always
// the verified JWT subject; the body carries only the new name.
func (h *ProfileHandler) UpdateMyName(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, model.ErrCodeUnauthorized, "Unauthorized")
		return
	}
	writeUserNameUpdate(w, r, h.userAdmin, userID, "UpdateMyName")
}

// UpdateUserName handles PUT /api/settings/users/{userId}/name (admin-only).
func (h *UserAdminHandler) UpdateUserName(w http.ResponseWriter, r *http.Request) {
	actorID := middleware.GetUserID(r.Context())
	targetID := chi.URLParam(r, "userId")
	if writeUserNameUpdate(w, r, h.userAdmin, targetID, "UpdateUserName") {
		auditAdminAction(actorID, targetID, "UpdateUserName")
	}
}

// writeUserNameUpdate decodes the bounded body, applies the rename and writes
// the response. It reports whether the rename succeeded.
func writeUserNameUpdate(w http.ResponseWriter, r *http.Request, svc *service.UserAdminService, targetID, op string) bool {
	var req model.UpdateUserNameRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUserNameBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return false
	}

	resp, err := svc.UpdateUserName(r.Context(), targetID, req.Name)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, resp)
		return true
	case errors.Is(err, service.ErrInvalidUserName):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, err.Error())
	case errors.Is(err, service.ErrUserNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "User not found")
	case errors.Is(err, service.ErrUserNameProfileSync):
		log.Printf("%s: profile sync failed for %s: %v", op, targetID, err)
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "이름은 변경됐지만 일부 화면에 반영하지 못했습니다. 다시 저장해주세요.")
	default:
		log.Printf("%s failed for %s: %v", op, targetID, err)
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "이름을 저장하지 못했습니다")
	}
	return false
}
