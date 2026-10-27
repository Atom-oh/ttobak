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

var (
	dictTermIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	// dictLogIDPattern matches model.DictTimeLayout, the audit row's stamp.
	dictLogIDPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}Z$`)
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

	result, err := h.dictService.GetDictionaryWithTerms(ctx, userID)
	if err != nil {
		writeDictionaryError(w, err, "Failed to get dictionary")
		return
	}

	writeJSON(w, http.StatusOK, result)
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
	case errors.Is(err, service.ErrForbidden), errors.Is(err, repository.ErrNotAccountMember):
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Not a member of this account")
	case errors.Is(err, repository.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid dictionary request")
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

// queryVersion reads the optional ?version= of a DELETE.
func queryVersion(w http.ResponseWriter, r *http.Request) (*int64, bool) {
	raw := r.URL.Query().Get("version")
	if raw == "" {
		return nil, true
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 1 {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "version must be a positive integer")
		return nil, false
	}
	return &v, true
}

// DeleteDictTerm handles DELETE /api/settings/dictionary/terms/{termId}
// (tombstone). An optional ?version= guards against a stale view.
func (h *DictionaryHandler) DeleteDictTerm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	termID, ok := dictTermID(w, r)
	if !ok {
		return
	}
	version, ok := queryVersion(w, r)
	if !ok {
		return
	}
	result, err := h.dictService.TombstoneTerm(ctx, middleware.GetUserID(ctx), termID, version)
	if err != nil {
		writeDictionaryError(w, err, "Failed to delete term")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// ListLayerTerms handles GET /api/settings/dictionary/layers/{layer}/terms
// (layer personal, common or account:{id}; an Account needs membership).
//
// With ?deleted=1 (common only; any other layer or value is 400) it lists
// Common tombstones with their last snapshot instead.
func (h *DictionaryHandler) ListLayerTerms(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if v, present := r.URL.Query()["deleted"]; present {
		if len(v) != 1 || v[0] != "1" || chi.URLParam(r, "layer") != model.DictLayerCommon {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "deleted=1 is supported on the common layer only")
			return
		}
		items, err := h.dictService.ListDeletedCommon(ctx)
		if err != nil {
			writeDictionaryError(w, err, "Failed to list deleted terms")
			return
		}
		writeJSON(w, http.StatusOK, model.DictDeletedTermsResponse{Items: items})
		return
	}
	items, err := h.dictService.ListLayer(ctx, middleware.GetUserID(ctx), chi.URLParam(r, "layer"))
	if err != nil {
		writeDictionaryError(w, err, "Failed to list terms")
		return
	}
	writeJSON(w, http.StatusOK, model.DictLayerTermsResponse{Items: items})
}

// CreateLayerTerm handles POST .../layers/{layer}/terms (personal or Account).
func (h *DictionaryHandler) CreateLayerTerm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req model.CreateDictTermRequest
	if !decodeStrict(w, r, dictTermMaxBytes, &req) {
		return
	}
	result, err := h.dictService.CreateLayerTerm(ctx, middleware.GetUserID(ctx), chi.URLParam(r, "layer"), req)
	if err != nil {
		writeDictionaryError(w, err, "Failed to create term")
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

// UpdateLayerTerm handles PATCH .../layers/{layer}/terms/{termId}; a Common
// edit is audited.
func (h *DictionaryHandler) UpdateLayerTerm(w http.ResponseWriter, r *http.Request) {
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
	result, err := h.dictService.UpdateLayerTerm(ctx, middleware.GetUserID(ctx), chi.URLParam(r, "layer"), termID, req)
	if err != nil {
		writeDictionaryError(w, err, "Failed to update term")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// DeleteLayerTerm handles DELETE .../layers/{layer}/terms/{termId}[?version=]
// (tombstone; a Common delete is audited).
func (h *DictionaryHandler) DeleteLayerTerm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	termID, ok := dictTermID(w, r)
	if !ok {
		return
	}
	version, ok := queryVersion(w, r)
	if !ok {
		return
	}
	result, err := h.dictService.DeleteLayerTerm(ctx, middleware.GetUserID(ctx), chi.URLParam(r, "layer"), termID, version)
	if err != nil {
		writeDictionaryError(w, err, "Failed to delete term")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// ListTermLog handles GET .../layers/{layer}/terms/{termId}/log: the Common
// audit list, newest first. Other layers are not audited (404).
func (h *DictionaryHandler) ListTermLog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	termID, ok := dictTermID(w, r)
	if !ok {
		return
	}
	rows, err := h.dictService.ListTermLog(ctx, chi.URLParam(r, "layer"), termID)
	if err != nil {
		writeDictionaryError(w, err, "Failed to list term log")
		return
	}
	items := make([]model.DictTermLogView, 0, len(rows))
	for _, row := range rows {
		items = append(items, service.TermLogView(row))
	}
	writeJSON(w, http.StatusOK, model.DictTermLogResponse{Items: items})
}

// RestoreTerm handles POST .../layers/common/terms/{termId}/log/{logId}/restore:
// the term goes back to its state before that change.
func (h *DictionaryHandler) RestoreTerm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	termID, ok := dictTermID(w, r)
	if !ok {
		return
	}
	logID := chi.URLParam(r, "logId")
	if !dictLogIDPattern.MatchString(logID) {
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Log entry not found")
		return
	}
	result, err := h.dictService.RestoreLayerTerm(ctx, middleware.GetUserID(ctx), chi.URLParam(r, "layer"), termID, logID)
	if err != nil {
		writeDictionaryError(w, err, "Failed to restore term")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
