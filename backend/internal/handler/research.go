package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

// ResearchHandler handles research task HTTP requests.
type ResearchHandler struct {
	researchService *service.ResearchService
	notionService   *service.NotionService
	repo            *repository.DynamoDBRepository
}

// NewResearchHandler creates a new ResearchHandler.
func NewResearchHandler(researchService *service.ResearchService, notionService *service.NotionService, repo *repository.DynamoDBRepository) *ResearchHandler {
	return &ResearchHandler{
		researchService: researchService,
		notionService:   notionService,
		repo:            repo,
	}
}

// CreateResearch handles POST /api/research
func (h *ResearchHandler) CreateResearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	var req model.CreateResearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	if strings.TrimSpace(req.Topic) == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "topic is required")
		return
	}

	research, err := h.researchService.CreateResearch(ctx, userID, &req)
	if err != nil {
		// Mode validation errors surface as non-sentinel errors
		if strings.Contains(err.Error(), "invalid mode") {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "mode must be quick, standard, or deep")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "internal error")
		return
	}

	writeJSON(w, http.StatusAccepted, research)
}

// ListResearch handles GET /api/research
func (h *ResearchHandler) ListResearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	result, err := h.researchService.ListResearch(ctx, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// GetResearchDetail handles GET /api/research/{researchId}
func (h *ResearchHandler) GetResearchDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	researchID := chi.URLParam(r, "researchId")

	if strings.Contains(researchID, "..") || strings.Contains(researchID, "/") {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid researchId")
		return
	}

	result, err := h.researchService.GetResearchDetail(ctx, researchID, userID)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Research not found")
			return
		}
		if errors.Is(err, service.ErrForbidden) {
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// DeleteResearch handles DELETE /api/research/{researchId}
func (h *ResearchHandler) DeleteResearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	researchID := chi.URLParam(r, "researchId")

	// Path traversal protection
	if strings.Contains(researchID, "..") || strings.Contains(researchID, "/") {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid researchId")
		return
	}

	err := h.researchService.DeleteResearch(ctx, researchID, userID)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Research not found")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ExportResearch handles POST /api/research/{researchId}/export
func (h *ResearchHandler) ExportResearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	researchID := chi.URLParam(r, "researchId")

	if strings.Contains(researchID, "..") || strings.Contains(researchID, "/") {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid researchId")
		return
	}

	detail, err := h.researchService.GetResearchDetail(ctx, researchID, userID)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Research not found")
			return
		}
		if errors.Is(err, service.ErrForbidden) {
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "internal error")
		return
	}

	if detail.Content == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Research has no content")
		return
	}

	integration, err := h.repo.GetIntegration(ctx, userID, "notion")
	if err != nil || integration == nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Notion not configured")
		return
	}

	title := detail.Topic
	if len(title) > 80 {
		title = title[:80]
	}

	notionURL, _, err := h.notionService.CreatePage(ctx, integration.APIKey, title, detail.Content)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Notion export failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"notionUrl": notionURL,
	})
}
