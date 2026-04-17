package handler

import (
	"net/http"
	"strconv"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

// InsightsHandler handles insights/documents listing requests
type InsightsHandler struct {
	insightsService *service.InsightsService
}

// NewInsightsHandler creates a new insights handler
func NewInsightsHandler(insightsService *service.InsightsService) *InsightsHandler {
	return &InsightsHandler{
		insightsService: insightsService,
	}
}

// ListInsights handles GET /api/insights
func (h *InsightsHandler) ListInsights(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	docType := r.URL.Query().Get("type")
	source := r.URL.Query().Get("source")
	svc := r.URL.Query().Get("service")

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

	result, err := h.insightsService.ListInsights(ctx, docType, source, svc, page, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}
