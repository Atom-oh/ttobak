package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

// SettingsHandler handles settings-related requests
type SettingsHandler struct {
	repo   *repository.DynamoDBRepository
	crypto *service.CryptoService
	notion *service.NotionService
}

// NewSettingsHandler creates a new settings handler
func NewSettingsHandler(repo *repository.DynamoDBRepository, crypto *service.CryptoService, notion *service.NotionService) *SettingsHandler {
	return &SettingsHandler{repo: repo, crypto: crypto, notion: notion}
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
		maskedKey := "****"
		if key, err := decryptStoredAPIKey(ctx, h.crypto, notionIntegration.APIKey); err == nil {
			maskedKey = maskAPIKey(key)
		}
		response.Notion = &model.IntegrationStatusResponse{
			Configured:   true,
			MaskedKey:    maskedKey,
			ParentPageID: notionIntegration.NotionParentID,
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

	if req.ParentPage == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "parentPage is required")
		return
	}

	// Notion internal integrations can only create pages under a page/database
	// the user has shared with the integration — never at the workspace root —
	// so a parent must be resolved and verified before we store anything.
	parentID, err := service.ParseNotionPageID(req.ParentPage)
	if err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid Notion page URL or ID")
		return
	}
	parentType, titleProperty, err := h.notion.VerifyParent(ctx, req.APIKey, parentID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrNotionInvalidAPIKey):
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Notion API key is invalid or has been revoked.")
		case errors.Is(err, service.ErrNotionUnavailable):
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Failed to verify the Notion page — Notion may be temporarily unavailable. Try again in a moment.")
		default:
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Notion page not found or not shared with the integration. Share the page with your integration (··· → Connections) and try again.")
		}
		return
	}

	// Encrypt API key if crypto service is available
	apiKeyToStore := req.APIKey
	if h.crypto != nil {
		encrypted, err := h.crypto.Encrypt(ctx, req.APIKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Failed to encrypt API key")
			return
		}
		apiKeyToStore = encrypted
	}

	integration := &model.Integration{
		PK:                  model.PrefixUser + userID,
		SK:                  model.PrefixIntegration + "notion",
		UserID:              userID,
		Service:             "notion",
		APIKey:              apiKeyToStore,
		NotionParentID:      parentID,
		NotionParentType:    parentType,
		NotionTitleProperty: titleProperty,
		ConfiguredAt:        time.Now().UTC(),
		EntityType:          "INTEGRATION",
	}

	if err := h.repo.SaveIntegration(ctx, integration); err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	maskedKey := maskAPIKey(req.APIKey)
	writeJSON(w, http.StatusOK, model.IntegrationStatusResponse{
		Configured:   true,
		MaskedKey:    maskedKey,
		ParentPageID: parentID,
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

// decryptStoredAPIKey returns the plaintext API key for a value stored via SaveNotionKey.
// With no crypto service configured, the stored value is assumed to already be plaintext.
// With a crypto service configured, a decrypt failure only falls back to the raw stored value
// when it already looks like a plaintext Notion key (covers records saved before KMS_KEY_ID
// was set) — any other decrypt failure is a real error (bad KMS config, revoked key, etc.) and
// must not be silently sent to Notion as if it were the API key.
func decryptStoredAPIKey(ctx context.Context, crypto *service.CryptoService, stored string) (string, error) {
	if crypto == nil {
		return stored, nil
	}
	decrypted, err := crypto.Decrypt(ctx, stored)
	if err == nil {
		return decrypted, nil
	}
	if isValidNotionKey(stored) {
		return stored, nil
	}
	log.Printf("decryptStoredAPIKey: KMS decrypt failed: %v", err)
	return "", fmt.Errorf("decrypt Notion API key: %w", err)
}

// maskAPIKey masks an API key for display
func maskAPIKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	// Show first 4 and last 4 characters
	return key[:4] + "****" + key[len(key)-4:]
}

// GetAllowedDomains handles GET /api/auth/allowed-domains (public, no auth required)
func (h *SettingsHandler) GetAllowedDomains(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	domains, err := h.repo.GetAllowedDomains(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}
	if domains == nil {
		domains = []string{}
	}
	writeJSON(w, http.StatusOK, model.AllowedDomainsResponse{
		Domains:  domains,
		Enforced: len(domains) > 0,
	})
}

// SaveAllowedDomains handles PUT /api/settings/allowed-domains (auth required)
func (h *SettingsHandler) SaveAllowedDomains(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	var req model.UpdateAllowedDomainsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	// Normalize domains: lowercase, trim whitespace
	for i, d := range req.Domains {
		req.Domains[i] = strings.ToLower(strings.TrimSpace(d))
	}
	// Remove empty entries
	var cleaned []string
	for _, d := range req.Domains {
		if d != "" {
			cleaned = append(cleaned, d)
		}
	}

	if err := h.repo.SaveAllowedDomains(ctx, cleaned, userID); err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, model.AllowedDomainsResponse{
		Domains:  cleaned,
		Enforced: len(cleaned) > 0,
	})
}

// isValidNotionKey validates Notion API key format
func isValidNotionKey(key string) bool {
	// Notion keys start with "ntn_" (new format) or "secret_" (old format)
	if len(key) < 10 {
		return false
	}
	return len(key) >= 4 && (key[:4] == "ntn_" || (len(key) >= 7 && key[:7] == "secret_"))
}
