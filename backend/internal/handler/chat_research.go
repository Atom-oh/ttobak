package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

// ResearchChatHandler handles research chat HTTP requests.
type ResearchChatHandler struct {
	chatRepo    *repository.ChatRepository
	researchSvc *service.ResearchService
}

// NewResearchChatHandler creates a new ResearchChatHandler.
func NewResearchChatHandler(chatRepo *repository.ChatRepository, researchSvc *service.ResearchService) *ResearchChatHandler {
	return &ResearchChatHandler{
		chatRepo:    chatRepo,
		researchSvc: researchSvc,
	}
}

// ListMessages handles GET /api/research/{researchId}/chat
func (h *ResearchChatHandler) ListMessages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	researchID := chi.URLParam(r, "researchId")

	if strings.Contains(researchID, "..") || strings.Contains(researchID, "/") {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid researchId")
		return
	}

	// Verify ownership before returning messages
	if _, err := h.researchSvc.GetResearchDetail(ctx, researchID, userID); err != nil {
		if errors.Is(err, service.ErrForbidden) {
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
			return
		}
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Research not found")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "failed to verify research ownership")
		return
	}

	messages, err := h.chatRepo.ListMessages(ctx, researchID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "failed to list messages")
		return
	}

	writeJSON(w, http.StatusOK, &model.ChatMessagesResponse{Messages: messages})
}

// SendMessage handles POST /api/research/{researchId}/chat
func (h *ResearchChatHandler) SendMessage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	researchID := chi.URLParam(r, "researchId")

	if strings.Contains(researchID, "..") || strings.Contains(researchID, "/") {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid researchId")
		return
	}

	// Verify ownership before saving message
	if _, err := h.researchSvc.GetResearchDetail(ctx, researchID, userID); err != nil {
		if errors.Is(err, service.ErrForbidden) {
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
			return
		}
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Research not found")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "failed to verify ownership")
		return
	}

	var req model.SendChatMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	if strings.TrimSpace(req.Content) == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "content is required")
		return
	}

	if len(req.Content) > 10000 {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "content too long")
		return
	}

	// Generate message ID (16 bytes hex)
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "failed to generate message ID")
		return
	}
	msgID := hex.EncodeToString(idBytes)
	now := time.Now().UTC().Format(time.RFC3339)

	// Save user message
	msg := &model.ChatMessage{
		MsgID:     msgID,
		Role:      "user",
		Content:   req.Content,
		Action:    req.Action,
		CreatedAt: now,
	}

	if err := h.chatRepo.SaveMessage(ctx, researchID, msg); err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "failed to save message")
		return
	}

	// Trigger action based on request
	var actionErr error
	switch req.Action {
	case "", "chat":
		actionErr = h.researchSvc.TriggerAgentRespond(ctx, researchID, userID)
	case "approve":
		actionErr = h.researchSvc.ApproveResearch(ctx, researchID, userID)
	case "request_subpage":
		_, actionErr = h.researchSvc.CreateSubPage(ctx, userID, researchID, req.Content)
	default:
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid action")
		return
	}

	if actionErr != nil {
		if errors.Is(actionErr, service.ErrForbidden) {
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
			return
		}
		if errors.Is(actionErr, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Research not found")
			return
		}
		if errors.Is(actionErr, service.ErrStatusMismatch) {
			writeError(w, http.StatusConflict, "CONFLICT", "Research status has changed (already approved or no longer in planning)")
			return
		}
		// Log but don't fail — the message was saved successfully
		// The SFN trigger is async; caller can retry
		writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"messageId": msgID,
			"warning":   "action trigger failed, will retry",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"messageId": msgID,
	})
}

// ListSubPages handles GET /api/research/{researchId}/subpages
func (h *ResearchChatHandler) ListSubPages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	researchID := chi.URLParam(r, "researchId")

	if strings.Contains(researchID, "..") || strings.Contains(researchID, "/") {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid researchId")
		return
	}

	subpages, err := h.researchSvc.ListSubPages(ctx, userID, researchID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "failed to list sub-pages")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"subpages": subpages,
	})
}
