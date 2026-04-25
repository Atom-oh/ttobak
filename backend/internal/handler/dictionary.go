package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

// DictionaryHandler handles dictionary-related requests
type DictionaryHandler struct {
	dictService *service.DictionaryService
}

// NewDictionaryHandler creates a new dictionary handler
func NewDictionaryHandler(dictService *service.DictionaryService) *DictionaryHandler {
	return &DictionaryHandler{
		dictService: dictService,
	}
}

// GetDictionary handles GET /api/settings/dictionary
func (h *DictionaryHandler) GetDictionary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	result, err := h.dictService.GetDictionary(ctx, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Failed to get dictionary")
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// UpdateDictionary handles PUT /api/settings/dictionary
func (h *DictionaryHandler) UpdateDictionary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	var req model.UpdateDictionaryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	if req.Terms == nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "terms is required")
		return
	}

	// Validate terms
	for _, t := range req.Terms {
		if t.Phrase == "" {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Each term must have a phrase")
			return
		}
	}

	result, err := h.dictService.UpdateDictionary(ctx, userID, req.Terms)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Failed to update dictionary")
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// DeleteTerm handles DELETE /api/settings/dictionary/term
func (h *DictionaryHandler) DeleteTerm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	var req model.DeleteTermRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	if req.Phrase == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "phrase is required")
		return
	}

	err := h.dictService.DeleteTerm(ctx, userID, req.Phrase)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Term not found")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Failed to delete term")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
