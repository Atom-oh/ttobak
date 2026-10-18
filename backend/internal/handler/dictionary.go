package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

// Request bounds for dictionary routes.
const (
	dictLegacyPutMaxBytes = 256 << 10
	dictTermMaxBytes      = 4 << 10
)

var dictTermIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

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

	result, err := h.dictService.GetDictionaryWithTerms(ctx, userID)
	if err != nil {
		writeDictionaryError(w, err, "Failed to get dictionary")
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// GetLiveConfig handles GET /api/settings/dictionary/live-config: the
// caller's READY personal vocabulary names for live captions.
func (h *DictionaryHandler) GetLiveConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	writeJSON(w, http.StatusOK, h.dictService.LiveDictionaryConfig(ctx, middleware.GetUserID(ctx)))
}

// UpdateDictionary handles PUT /api/settings/dictionary
func (h *DictionaryHandler) UpdateDictionary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	var req model.UpdateDictionaryRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, dictLegacyPutMaxBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	if req.Terms == nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "terms is required")
		return
	}

	// Terms are validated by the service: unchanged and auto-row entries
	// returned by GET may legitimately lack a phrase.
	result, err := h.dictService.UpdateDictionary(ctx, userID, req.Terms)
	if err != nil {
		writeDictionaryError(w, err, "Failed to update dictionary")
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// DeleteTerm handles DELETE /api/settings/dictionary/term
func (h *DictionaryHandler) DeleteTerm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	var req model.DeleteTermRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, dictTermMaxBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	if req.Phrase == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "phrase is required")
		return
	}

	err := h.dictService.DeleteTerm(ctx, userID, req.Phrase)
	if err != nil {
		writeDictionaryError(w, err, "Failed to delete term")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// writeDictionaryError maps dictionary sentinels to the API error shape.
func writeDictionaryError(w http.ResponseWriter, err error, fallback string) {
	var input *service.TermInputError
	switch {
	case errors.As(err, &input):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, input.Error())
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Term not found")
	case errors.Is(err, service.ErrDictionaryVersionConflict):
		writeError(w, http.StatusConflict, "VERSION_CONFLICT", "The term changed; reload and try again")
	case errors.Is(err, service.ErrDictionaryFull):
		writeError(w, http.StatusConflict, "DICTIONARY_FULL", "The dictionary already has the maximum number of manual terms")
	case errors.Is(err, service.ErrDictionaryTermExists):
		writeError(w, http.StatusConflict, "TERM_EXISTS", "The term already exists")
	case errors.Is(err, service.ErrDictionaryTermNotManual):
		writeError(w, http.StatusConflict, "TERM_NOT_MANUAL", "Only manually added terms can be removed this way")
	case errors.Is(err, service.ErrLegacyDictionaryConflict):
		writeError(w, http.StatusConflict, "LEGACY_DICTIONARY_CONFLICT", "Remove duplicate terms and save the dictionary list first")
	case errors.Is(err, repository.ErrConditionFailed):
		writeError(w, http.StatusConflict, model.ErrCodeConflict, "The dictionary changed; reload and try again")
	default:
		log.Printf("dictionary request failed: %v", err)
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, fallback)
	}
}

// decodeStrict decodes exactly one JSON object with no unknown fields.
func decodeStrict(w http.ResponseWriter, r *http.Request, limit int64, dst interface{}) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil || decoder.Decode(new(interface{})) != io.EOF {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return false
	}
	return true
}

func dictTermID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "termId")
	if !dictTermIDPattern.MatchString(id) {
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Term not found")
		return "", false
	}
	return id, true
}

// CreateTerm handles POST /api/settings/dictionary/terms (create or restore).
func (h *DictionaryHandler) CreateTerm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req model.CreateDictTermRequest
	if !decodeStrict(w, r, dictTermMaxBytes, &req) {
		return
	}
	result, err := h.dictService.CreateTerm(ctx, middleware.GetUserID(ctx), req)
	if err != nil {
		writeDictionaryError(w, err, "Failed to create term")
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

// UpdateTerm handles PATCH /api/settings/dictionary/terms/{termId}.
func (h *DictionaryHandler) UpdateTerm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	termID, ok := dictTermID(w, r)
	if !ok {
		return
	}
	var req model.UpdateDictTermRequest
	if !decodeStrict(w, r, dictTermMaxBytes, &req) {
		return
	}
	if req.Version == nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "version is required")
		return
	}
	result, err := h.dictService.UpdateTerm(ctx, middleware.GetUserID(ctx), termID, req)
	if err != nil {
		writeDictionaryError(w, err, "Failed to update term")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// DeleteDictTerm handles DELETE /api/settings/dictionary/terms/{termId}
// (tombstone). An optional ?version= guards against a stale view.
func (h *DictionaryHandler) DeleteDictTerm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	termID, ok := dictTermID(w, r)
	if !ok {
		return
	}
	var version *int64
	if raw := r.URL.Query().Get("version"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 1 {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "version must be a positive integer")
			return
		}
		version = &v
	}
	result, err := h.dictService.TombstoneTerm(ctx, middleware.GetUserID(ctx), termID, version)
	if err != nil {
		writeDictionaryError(w, err, "Failed to delete term")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
