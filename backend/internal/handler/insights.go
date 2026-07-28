package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

// kbSyncer is the subset of KBService.SyncKB DeleteDocument needs to trigger a
// best-effort ingestion refresh after removing a document. A narrow interface
// keeps this handler decoupled from the full KBService and easy to mock in tests.
type kbSyncer interface {
	SyncKB(ctx context.Context, userID string) (*model.KBSyncResponse, error)
}

// InsightsHandler handles insights/documents listing requests
type InsightsHandler struct {
	insightsService *service.InsightsService
	kbSyncer        kbSyncer
}

// NewInsightsHandler creates a new insights handler
func NewInsightsHandler(insightsService *service.InsightsService, kbSyncer kbSyncer) *InsightsHandler {
	return &InsightsHandler{
		insightsService: insightsService,
		kbSyncer:        kbSyncer,
	}
}

// ListInsights handles GET /api/insights
func (h *InsightsHandler) ListInsights(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	docType := r.URL.Query().Get("type")
	source := r.URL.Query().Get("source")
	svc := r.URL.Query().Get("service")
	sortBy := r.URL.Query().Get("sort")

	var tags []string
	if t := r.URL.Query().Get("tags"); t != "" {
		for _, tag := range strings.Split(t, ",") {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				tags = append(tags, tag)
			}
		}
	}

	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			page = v
		}
	}

	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}

	result, err := h.insightsService.ListInsights(ctx, docType, source, svc, tags, sortBy, page, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// GetDocumentContent handles GET /api/insights/{sourceId}/{docHash}
func (h *InsightsHandler) GetDocumentContent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sourceID := chi.URLParam(r, "sourceId")
	docHash := chi.URLParam(r, "docHash")

	if sourceID == "" || docHash == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "sourceId and docHash are required")
		return
	}
	if strings.Contains(sourceID, "..") || strings.Contains(sourceID, "/") ||
		strings.Contains(docHash, "..") || strings.Contains(docHash, "/") {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid sourceId or docHash")
		return
	}

	result, err := h.insightsService.GetDocumentDetail(ctx, sourceID, docHash)
	if err != nil {
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "document content not found")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// DeleteDocument handles DELETE /api/insights/{sourceId}/{docHash}
func (h *InsightsHandler) DeleteDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	sourceID := chi.URLParam(r, "sourceId")
	docHash := chi.URLParam(r, "docHash")

	if sourceID == "" || docHash == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "sourceId and docHash are required")
		return
	}
	if strings.Contains(sourceID, "..") || strings.Contains(sourceID, "/") ||
		strings.Contains(docHash, "..") || strings.Contains(docHash, "/") {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid sourceId or docHash")
		return
	}

	err := h.insightsService.DeleteDocument(ctx, userID, middleware.IsAdmin(ctx), sourceID, docHash)
	if err != nil {
		if errors.Is(err, service.ErrForbidden) {
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "not a subscriber of this source")
			return
		}
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "document not found")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "internal error")
		return
	}

	// Best-effort KB re-sync: the deleted document's S3 object is gone, but
	// the Bedrock KB vector index only reconciles on the next ingestion job.
	// Kick one off now so search doesn't keep surfacing a deleted document
	// until the next daily crawl/manual sync -- a failure here must not turn
	// a successful delete into an error response.
	if h.kbSyncer != nil {
		if _, syncErr := h.kbSyncer.SyncKB(ctx, userID); syncErr != nil {
			log.Printf("insights: post-delete KB sync failed for source=%s docHash=%s: %v", sourceID, docHash, syncErr)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}
