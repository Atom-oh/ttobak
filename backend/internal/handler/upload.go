package handler

import (
	"encoding/json"
	"net/http"

	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

// UploadHandler handles file upload requests
type UploadHandler struct {
	uploadService *service.UploadService
}

// NewUploadHandler creates a new upload handler
func NewUploadHandler(uploadService *service.UploadService) *UploadHandler {
	return &UploadHandler{
		uploadService: uploadService,
	}
}

// GetPresignedURL handles POST /api/upload/presigned
func (h *UploadHandler) GetPresignedURL(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	var req model.PresignedURLRequest
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

	if req.Category == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "category is required")
		return
	}

	if req.Category != "audio" && req.Category != "image" && req.Category != "file" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "category must be 'audio', 'image', or 'file'")
		return
	}

	response, err := h.uploadService.GeneratePresignedUploadURL(ctx, userID, &req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, response)
}

// UploadComplete handles POST /api/upload/complete
func (h *UploadHandler) UploadComplete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	var req model.UploadCompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	if req.MeetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "meetingId is required")
		return
	}

	if req.Key == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "key is required")
		return
	}

	if req.Category == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "category is required")
		return
	}

	err := h.uploadService.CompleteUpload(ctx, userID, &req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	response := model.UploadCompleteResponse{
		Status: "processing",
	}

	writeJSON(w, http.StatusOK, response)
}
