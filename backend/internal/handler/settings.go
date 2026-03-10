package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// SettingsHandler handles settings-related requests
type SettingsHandler struct {
	repo *repository.DynamoDBRepository
}

// NewSettingsHandler creates a new settings handler
func NewSettingsHandler(repo *repository.DynamoDBRepository) *SettingsHandler {
	return &SettingsHandler{
		repo: repo,
	}
}

// GetIntegrations handles GET /api/settings/integrations
func (h *SettingsHandler) GetIntegrations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	response := model.IntegrationsResponse{}

	// Check Notion integration
	notionIntegration, err := h.repo.GetIntegration(ctx, userID, "notion")
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	if notionIntegration != nil {
		maskedKey := maskAPIKey(notionIntegration.APIKey)
		response.Notion = &model.IntegrationStatusResponse{
			Configured: true,
			MaskedKey:  maskedKey,
		}
	} else {
		response.Notion = &model.IntegrationStatusResponse{
			Configured: false,
		}
	}

	writeJSON(w, http.StatusOK, response)
}

// SaveNotionKey handles PUT /api/settings/integrations/notion
func (h *SettingsHandler) SaveNotionKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	var req model.IntegrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	if req.APIKey == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "apiKey is required")
		return
	}

	// Validate Notion API key format (should start with "ntn_" or "secret_")
	if !isValidNotionKey(req.APIKey) {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid Notion API key format")
		return
	}

	integration := &model.Integration{
		PK:           model.PrefixUser + userID,
		SK:           model.PrefixIntegration + "notion",
		UserID:       userID,
		Service:      "notion",
		APIKey:       req.APIKey, // In production, this should be encrypted
		ConfiguredAt: time.Now().UTC(),
		EntityType:   "INTEGRATION",
	}

	if err := h.repo.SaveIntegration(ctx, integration); err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	maskedKey := maskAPIKey(req.APIKey)
	writeJSON(w, http.StatusOK, model.IntegrationStatusResponse{
		Configured: true,
		MaskedKey:  maskedKey,
	})
}

// DeleteNotionKey handles DELETE /api/settings/integrations/notion
func (h *SettingsHandler) DeleteNotionKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	if err := h.repo.DeleteIntegration(ctx, userID, "notion"); err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// maskAPIKey masks an API key for display
func maskAPIKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	// Show first 4 and last 4 characters
	return key[:4] + "****" + key[len(key)-4:]
}

// isValidNotionKey validates Notion API key format
func isValidNotionKey(key string) bool {
	// Notion keys start with "ntn_" (new format) or "secret_" (old format)
	if len(key) < 10 {
		return false
	}
	return len(key) >= 4 && (key[:4] == "ntn_" || (len(key) >= 7 && key[:7] == "secret_"))
}
