package handler

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

const (
	customerUpdateSourceMaxBytes  = 8 * 1024
	customerUpdateLinksMaxBytes   = 512 * 1024
	customerUpdateDraftMaxBytes   = 16 * 1024
	customerUpdateSettingMaxBytes = 1024

	customerUpdateDefaultLimit = 30
	customerUpdateMaxLimit     = 50
)

var customerUpdateURLHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// customerUpdateAPI is the service surface used by CustomerUpdateHandler.
type customerUpdateAPI interface {
	AddSource(ctx context.Context, userID string, req *model.AddUpdateSourceRequest) (*model.UpdateSource, error)
	ListSources(ctx context.Context, userID string) ([]model.UpdateSource, error)
	UpdateSource(ctx context.Context, userID, sourceID string, req *model.PatchUpdateSourceRequest) error
	DeleteSource(ctx context.Context, userID, sourceID string) error
	UploadLinks(ctx context.Context, userID string, req *model.UploadUpdateLinksRequest) (*model.UploadUpdateLinksResponse, error)
	ListDrafts(ctx context.Context, userID, accountID string, limit int, cursor string) (*model.UpdateDraftsPage, error)
	PatchDraft(ctx context.Context, userID, accountID, urlHash string, req *model.PatchUpdateDraftRequest) (*model.UpdateDraft, error)
	GetAccountSetting(ctx context.Context, userID, accountID string) (bool, error)
	PutAccountSetting(ctx context.Context, userID, accountID string, enabled bool) error
}

// CustomerUpdateHandler serves the customer update draft routes.
type CustomerUpdateHandler struct {
	svc customerUpdateAPI
}

// NewCustomerUpdateHandler creates a customer update handler.
func NewCustomerUpdateHandler(svc customerUpdateAPI) *CustomerUpdateHandler {
	return &CustomerUpdateHandler{svc: svc}
}

type customerUpdateSetting struct {
	Enabled *bool `json:"enabled"`
}

// cuUser returns the caller or writes 401.
func cuUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	userID := middleware.GetUserID(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, model.ErrCodeUnauthorized, "Unauthorized")
		return "", false
	}
	return userID, true
}

// cuWriteServiceError maps service errors to HTTP responses without echoing
// internal error text.
func cuWriteServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request")
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Forbidden")
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Not found")
	case errors.Is(err, service.ErrConflict):
		writeError(w, http.StatusConflict, model.ErrCodeConflict, "Conflict")
	default:
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "internal error")
	}
}

func cuLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n < 1 {
		return customerUpdateDefaultLimit
	}
	if n > customerUpdateMaxLimit {
		return customerUpdateMaxLimit
	}
	return n
}

// ListSources handles GET /api/customer-updates/sources
func (h *CustomerUpdateHandler) ListSources(w http.ResponseWriter, r *http.Request) {
	userID, ok := cuUser(w, r)
	if !ok {
		return
	}
	sources, err := h.svc.ListSources(r.Context(), userID)
	if err != nil {
		cuWriteServiceError(w, err)
		return
	}
	if sources == nil {
		sources = []model.UpdateSource{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": sources})
}

// AddSource handles POST /api/customer-updates/sources
func (h *CustomerUpdateHandler) AddSource(w http.ResponseWriter, r *http.Request) {
	userID, ok := cuUser(w, r)
	if !ok {
		return
	}
	var req model.AddUpdateSourceRequest
	if !decodeStrict(w, r, customerUpdateSourceMaxBytes, &req) {
		return
	}
	src, err := h.svc.AddSource(r.Context(), userID, &req)
	if err != nil {
		cuWriteServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, src)
}

// PatchSource handles PATCH /api/customer-updates/sources/{sourceId}
func (h *CustomerUpdateHandler) PatchSource(w http.ResponseWriter, r *http.Request) {
	userID, ok := cuUser(w, r)
	if !ok {
		return
	}
	var req model.PatchUpdateSourceRequest
	if !decodeStrict(w, r, customerUpdateSourceMaxBytes, &req) {
		return
	}
	if err := h.svc.UpdateSource(r.Context(), userID, chi.URLParam(r, "sourceId"), &req); err != nil {
		cuWriteServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// DeleteSource handles DELETE /api/customer-updates/sources/{sourceId}
func (h *CustomerUpdateHandler) DeleteSource(w http.ResponseWriter, r *http.Request) {
	userID, ok := cuUser(w, r)
	if !ok {
		return
	}
	if err := h.svc.DeleteSource(r.Context(), userID, chi.URLParam(r, "sourceId")); err != nil {
		cuWriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UploadLinks handles POST /api/customer-updates/links
func (h *CustomerUpdateHandler) UploadLinks(w http.ResponseWriter, r *http.Request) {
	userID, ok := cuUser(w, r)
	if !ok {
		return
	}
	var req model.UploadUpdateLinksRequest
	if !decodeStrict(w, r, customerUpdateLinksMaxBytes, &req) {
		return
	}
	resp, err := h.svc.UploadLinks(r.Context(), userID, &req)
	if err != nil {
		cuWriteServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *CustomerUpdateHandler) listDrafts(w http.ResponseWriter, r *http.Request, accountID string) {
	userID, ok := cuUser(w, r)
	if !ok {
		return
	}
	page, err := h.svc.ListDrafts(r.Context(), userID, accountID, cuLimit(r), r.URL.Query().Get("cursor"))
	if err != nil {
		cuWriteServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// ListDrafts handles GET /api/customer-updates/drafts
func (h *CustomerUpdateHandler) ListDrafts(w http.ResponseWriter, r *http.Request) {
	h.listDrafts(w, r, "")
}

// ListAccountDrafts handles GET /api/accounts/{accountId}/customer-update-drafts
func (h *CustomerUpdateHandler) ListAccountDrafts(w http.ResponseWriter, r *http.Request) {
	h.listDrafts(w, r, chi.URLParam(r, "accountId"))
}

// PatchDraft handles PATCH /api/customer-updates/drafts/{accountId}/{urlHash}
func (h *CustomerUpdateHandler) PatchDraft(w http.ResponseWriter, r *http.Request) {
	userID, ok := cuUser(w, r)
	if !ok {
		return
	}
	urlHash := chi.URLParam(r, "urlHash")
	if !customerUpdateURLHashPattern.MatchString(urlHash) {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid urlHash")
		return
	}
	var req model.PatchUpdateDraftRequest
	if !decodeStrict(w, r, customerUpdateDraftMaxBytes, &req) {
		return
	}
	draft, err := h.svc.PatchDraft(r.Context(), userID, chi.URLParam(r, "accountId"), urlHash, &req)
	if err != nil {
		cuWriteServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, draft)
}

// GetSetting handles GET /api/accounts/{accountId}/customer-update-setting
func (h *CustomerUpdateHandler) GetSetting(w http.ResponseWriter, r *http.Request) {
	userID, ok := cuUser(w, r)
	if !ok {
		return
	}
	enabled, err := h.svc.GetAccountSetting(r.Context(), userID, chi.URLParam(r, "accountId"))
	if err != nil {
		cuWriteServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": enabled})
}

// PutSetting handles PUT /api/accounts/{accountId}/customer-update-setting
func (h *CustomerUpdateHandler) PutSetting(w http.ResponseWriter, r *http.Request) {
	userID, ok := cuUser(w, r)
	if !ok {
		return
	}
	var req customerUpdateSetting
	if !decodeStrict(w, r, customerUpdateSettingMaxBytes, &req) {
		return
	}
	if req.Enabled == nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "enabled is required")
		return
	}
	if err := h.svc.PutAccountSetting(r.Context(), userID, chi.URLParam(r, "accountId"), *req.Enabled); err != nil {
		cuWriteServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": *req.Enabled})
}
