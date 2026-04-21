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

// CrawlerHandler handles crawler source management requests
type CrawlerHandler struct {
	crawlerService *service.CrawlerService
}

// NewCrawlerHandler creates a new crawler handler
func NewCrawlerHandler(crawlerService *service.CrawlerService) *CrawlerHandler {
	return &CrawlerHandler{
		crawlerService: crawlerService,
	}
}

// ListSources handles GET /api/crawler/sources
func (h *CrawlerHandler) ListSources(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	result, err := h.crawlerService.ListSources(ctx, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// AddSource handles POST /api/crawler/sources
func (h *CrawlerHandler) AddSource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	var req model.AddCrawlerSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	if req.SourceName == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "sourceName is required")
		return
	}

	result, err := h.crawlerService.AddSource(ctx, userID, &req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, result)
}

// UpdateSource handles PUT /api/crawler/sources/{sourceId}
func (h *CrawlerHandler) UpdateSource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	sourceID := chi.URLParam(r, "sourceId")

	if sourceID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "sourceId is required")
		return
	}

	var req model.UpdateCrawlerSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	err := h.crawlerService.UpdateSource(ctx, userID, sourceID, &req)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Source subscription not found")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// Unsubscribe handles DELETE /api/crawler/sources/{sourceId}
func (h *CrawlerHandler) Unsubscribe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	sourceID := chi.URLParam(r, "sourceId")

	if sourceID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "sourceId is required")
		return
	}

	err := h.crawlerService.Unsubscribe(ctx, userID, sourceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetHistory handles GET /api/crawler/sources/{sourceId}/history
func (h *CrawlerHandler) GetHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sourceID := chi.URLParam(r, "sourceId")

	if sourceID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "sourceId is required")
		return
	}

	result, err := h.crawlerService.GetHistory(ctx, sourceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, result)
}
