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

// QAHandler handles Q&A-related requests
type QAHandler struct {
	knowledgeService *service.KnowledgeService
}

// NewQAHandler creates a new QA handler
func NewQAHandler(knowledgeService *service.KnowledgeService) *QAHandler {
	return &QAHandler{
		knowledgeService: knowledgeService,
	}
}

// AskQuestion handles POST /api/meetings/{meetingId}/ask
func (h *QAHandler) AskQuestion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	meetingID := chi.URLParam(r, "meetingId")

	if meetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "meetingId is required")
		return
	}

	var req model.AskQuestionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	if req.Question == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "question is required")
		return
	}

	result, err := h.knowledgeService.Ask(ctx, userID, meetingID, req.Question)
	if err != nil {
		// Ask returns service.ErrNotFound (via resolveSharedAccessOrNotFound)
		// on no access, not the string "meeting not found" this used to
		// match on -- that comparison was already stale before this route
		// existed. Currently unused (no cmd/api route wires to AskQuestion;
		// Q&A is served by the Python Lambda), but fixed now so a future
		// reconnection doesn't turn every not-found into a 500.
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// AskLive handles POST /api/kb/ask — live Q&A with client-provided context
func (h *QAHandler) AskLive(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req model.AskLiveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	if req.Question == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "question is required")
		return
	}

	if req.Context == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "context is required")
		return
	}

	result, err := h.knowledgeService.AskLive(ctx, req.Question, req.Context)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}
