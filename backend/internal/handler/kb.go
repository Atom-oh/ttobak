package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

// KBHandler handles Knowledge Base-related requests
type KBHandler struct {
	kbService *service.KBService
}

// NewKBHandler creates a new KB handler
func NewKBHandler(kbService *service.KBService) *KBHandler {
	return &KBHandler{
		kbService: kbService,
	}
}

// GetPresignedURL handles POST /api/kb/upload
func (h *KBHandler) GetPresignedURL(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	var req model.KBUploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	if req.FileName == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "fileName is required")
		return
	}
	if req.FileType == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "fileType is required")
		return
	}

	result, err := h.kbService.GetPresignedURL(ctx, userID, req.FileName, req.FileType)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// SyncKB handles POST /api/kb/sync
func (h *KBHandler) SyncKB(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	result, err := h.kbService.SyncKB(ctx, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// ListFiles handles GET /api/kb/files
func (h *KBHandler) ListFiles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	result, err := h.kbService.ListFiles(ctx, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// CopyAttachment handles POST /api/kb/copy-attachment
func (h *KBHandler) CopyAttachment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	var req struct {
		MeetingID    string `json:"meetingId"`
		AttachmentID string `json:"attachmentId"`
		SourceKey    string `json:"sourceKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	if req.SourceKey == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "sourceKey is required")
		return
	}

	if err := h.kbService.CopyAttachmentToKB(ctx, userID, req.SourceKey); err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	// Trigger ingestion after copy
	if _, err := h.kbService.SyncKB(ctx, userID); err != nil {
		// Non-fatal — file is already copied
		writeJSON(w, http.StatusOK, map[string]string{"status": "copied", "ingestion": "failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "copied", "ingestion": "started"})
}

// DeleteFile handles DELETE /api/kb/files/{fileId}
func (h *KBHandler) DeleteFile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	fileID := chi.URLParam(r, "fileId")

	if fileID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "fileId is required")
		return
	}

	err := h.kbService.DeleteFile(ctx, userID, fileID)
	if err != nil {
		if err.Error() == "file not found" {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "File not found")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
