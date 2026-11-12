package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

const skillPackageBodyMaxBytes = 4 * 1024

// SkillPackageAPI receives only the verified JWT user; org scope routes are
// mounted behind middleware.RequireAdmin.
type SkillPackageAPI interface {
	Get(ctx context.Context, userID string) (*service.SkillPackageView, error)
	StartUpload(ctx context.Context, userID, scope string) (*model.SkillPackageUpload, error)
	CompleteUpload(ctx context.Context, userID, scope, uploadID string) (*service.SkillPackageResult, error)
	DeletePersonal(ctx context.Context, userID string) (*service.SkillPackageResult, error)
	ListOrgVersions(ctx context.Context) ([]model.SkillPackage, error)
	RollbackOrg(ctx context.Context, userID, sha string) (*model.SkillPackage, error)
}

type SkillPackageHandler struct {
	api SkillPackageAPI
}

func NewSkillPackageHandler(api SkillPackageAPI) *SkillPackageHandler {
	return &SkillPackageHandler{api: api}
}

// Routes (cmd/api/main.go): GET /api/skill-packages and the personal routes
// for every authenticated user; the org routes only inside the
// middleware.RequireAdmin group.

// StartPersonalUpload, CompletePersonalUpload, StartOrgUpload and
// CompleteOrgUpload bind the package scope to the route.
func (h *SkillPackageHandler) StartPersonalUpload(w http.ResponseWriter, r *http.Request) {
	h.startUpload(model.SkillPackageScopePersonal)(w, r)
}

func (h *SkillPackageHandler) CompletePersonalUpload(w http.ResponseWriter, r *http.Request) {
	h.completeUpload(model.SkillPackageScopePersonal)(w, r)
}

func (h *SkillPackageHandler) StartOrgUpload(w http.ResponseWriter, r *http.Request) {
	h.startUpload(model.SkillPackageScopeOrg)(w, r)
}

func (h *SkillPackageHandler) CompleteOrgUpload(w http.ResponseWriter, r *http.Request) {
	h.completeUpload(model.SkillPackageScopeOrg)(w, r)
}

func skillPackageUser(w http.ResponseWriter, r *http.Request) string {
	w.Header().Set("Cache-Control", "private, no-store")
	userID := middleware.GetUserID(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, model.ErrCodeUnauthorized, "Authentication required")
	}
	return userID
}

// decodeSkillPackageBody reads one bounded JSON object without unknown fields.
func decodeSkillPackageBody(r *http.Request, target any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, skillPackageBodyMaxBytes+1))
	if err != nil || len(body) > skillPackageBodyMaxBytes {
		return service.ErrInvalidInput
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.More() {
		return service.ErrInvalidInput
	}
	return nil
}

func writeSkillPackageError(w http.ResponseWriter, err error) {
	var pkg *service.SkillPackageError
	switch {
	case errors.Is(err, service.ErrInvalidPackage) && errors.As(err, &pkg):
		writeError(w, http.StatusBadRequest, "INVALID_PACKAGE", pkg.Error())
	case errors.Is(err, service.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid request")
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "skill package not found")
	case errors.Is(err, repository.ErrConditionFailed):
		writeError(w, http.StatusConflict, model.ErrCodeConflict, "the package changed; reload and retry")
	default:
		log.Printf("skill package request failed: %v", err)
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "skill package request failed")
	}
}

func (h *SkillPackageHandler) Get(w http.ResponseWriter, r *http.Request) {
	userID := skillPackageUser(w, r)
	if userID == "" {
		return
	}
	view, err := h.api.Get(r.Context(), userID)
	if err != nil {
		writeSkillPackageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *SkillPackageHandler) startUpload(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := skillPackageUser(w, r)
		if userID == "" {
			return
		}
		upload, err := h.api.StartUpload(r.Context(), userID, scope)
		if err != nil {
			writeSkillPackageError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, upload)
	}
}

func (h *SkillPackageHandler) completeUpload(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := skillPackageUser(w, r)
		if userID == "" {
			return
		}
		var request struct {
			UploadID string `json:"uploadId"`
		}
		if err := decodeSkillPackageBody(r, &request); err != nil {
			writeSkillPackageError(w, err)
			return
		}
		result, err := h.api.CompleteUpload(r.Context(), userID, scope, request.UploadID)
		if err != nil {
			writeSkillPackageError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func (h *SkillPackageHandler) DeletePersonal(w http.ResponseWriter, r *http.Request) {
	userID := skillPackageUser(w, r)
	if userID == "" {
		return
	}
	result, err := h.api.DeletePersonal(r.Context(), userID)
	if err != nil {
		writeSkillPackageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *SkillPackageHandler) ListOrgVersions(w http.ResponseWriter, r *http.Request) {
	if skillPackageUser(w, r) == "" {
		return
	}
	versions, err := h.api.ListOrgVersions(r.Context())
	if err != nil {
		writeSkillPackageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions})
}

func (h *SkillPackageHandler) RollbackOrg(w http.ResponseWriter, r *http.Request) {
	userID := skillPackageUser(w, r)
	if userID == "" {
		return
	}
	var request struct {
		SHA256 string `json:"sha256"`
	}
	if err := decodeSkillPackageBody(r, &request); err != nil {
		writeSkillPackageError(w, err)
		return
	}
	row, err := h.api.RollbackOrg(r.Context(), userID, request.SHA256)
	if err != nil {
		writeSkillPackageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}
