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

// DocumentHandler serves personal (account-less) documents -- PK: USER#{userId}.
// Ownership is implicit in the PK (always built from the authenticated
// userID), so unlike AccountHandler's document methods there's no membership
// check -- but ErrNotFound (missing doc) and ErrForbidden (a fileKey not
// prefixed docs/{userID}/) can still occur and map to 404/403 as usual.
type DocumentHandler struct {
	accountService *service.AccountService
	uploadService  *service.UploadService
}

func NewDocumentHandler(accountService *service.AccountService, uploadService *service.UploadService) *DocumentHandler {
	return &DocumentHandler{accountService: accountService, uploadService: uploadService}
}

func (h *DocumentHandler) PutDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	var req model.PutDocumentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	dto, err := h.accountService.PutUserDocument(ctx, userID, &req)
	if err != nil {
		writeDocumentServiceError(w, err, "title and markdown required (<=300KB)")
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

func (h *DocumentHandler) ListDocuments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	list, err := h.accountService.ListUserDocuments(ctx, userID, r.URL.Query().Get("docType"))
	if err != nil {
		writeDocumentServiceError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"documents": list})
}

func (h *DocumentHandler) GetDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	docID := chi.URLParam(r, "docId")
	if docID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Document ID is required")
		return
	}
	detail, err := h.accountService.GetUserDocument(ctx, userID, docID)
	if err != nil {
		writeDocumentServiceError(w, err, "")
		return
	}
	if detail.FileKey != "" {
		if url, presignErr := h.uploadService.GeneratePresignedDownloadURL(ctx, detail.FileKey); presignErr == nil {
			detail.DownloadURL = url
		}
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *DocumentHandler) UpdateDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	docID := chi.URLParam(r, "docId")
	if docID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Document ID is required")
		return
	}
	var req model.PutDocumentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	dto, err := h.accountService.UpdateUserDocument(ctx, userID, docID, &req)
	if err != nil {
		writeDocumentServiceError(w, err, "title required, markdown <=300KB")
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (h *DocumentHandler) DeleteDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	docID := chi.URLParam(r, "docId")
	if docID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Document ID is required")
		return
	}
	if err := h.accountService.DeleteUserDocument(ctx, userID, docID); err != nil {
		writeDocumentServiceError(w, err, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeDocumentServiceError maps the account-document sentinel errors shared
// by both AccountHandler and DocumentHandler's document methods to HTTP
// responses. invalidInputMsg is shown for ErrInvalidInput (its meaning
// differs between create/list/update).
func writeDocumentServiceError(w http.ResponseWriter, err error, invalidInputMsg string) {
	switch {
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Document not found")
	case errors.Is(err, service.ErrLoopGuard):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Document originated from TTOBAK (loop guard)")
	case errors.Is(err, service.ErrInvalidInput):
		msg := invalidInputMsg
		if msg == "" {
			msg = "invalid input"
		}
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, msg)
	default:
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
	}
}
