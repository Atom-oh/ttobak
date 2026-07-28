package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// ChatHandler handles chat session requests
type ChatHandler struct {
	repo *repository.DynamoDBRepository
}

// NewChatHandler creates a new chat handler
func NewChatHandler(repo *repository.DynamoDBRepository) *ChatHandler {
	return &ChatHandler{repo: repo}
}

// ListSessions handles GET /api/chat/sessions
func (h *ChatHandler) ListSessions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	sessions, err := h.repo.ListChatSessions(ctx, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"sessions": sessions,
	})
}

// DeleteSession handles DELETE /api/chat/sessions/{sessionId}
func (h *ChatHandler) DeleteSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	sessionID := chi.URLParam(r, "sessionId")

	if sessionID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Session ID is required")
		return
	}

	if err := h.repo.DeleteChatSession(ctx, userID, sessionID); err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
