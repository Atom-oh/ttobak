package handler

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

type attachmentTextService interface {
	GetStatus(context.Context, string, string, string) (*model.AttachmentTextStatus, error)
	Request(context.Context, string, string, string) (*model.AttachmentTextStatus, error)
	Read(context.Context, string, string, string, string, int) (*service.AttachmentTextPage, error)
}
type AttachmentTextHandler struct{ service attachmentTextService }

func NewAttachmentTextHandler(s *service.AttachmentTextService) *AttachmentTextHandler {
	return &AttachmentTextHandler{service: s}
}
func (h *AttachmentTextHandler) Status(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeAttachmentError(w, service.ErrInvalidInput)
		return
	}
	result, err := h.service.GetStatus(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "meetingId"), chi.URLParam(r, "attachmentId"))
	if err != nil {
		writeAttachmentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (h *AttachmentTextHandler) Retry(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeAttachmentError(w, service.ErrInvalidInput)
		return
	}
	result, err := h.service.Request(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "meetingId"), chi.URLParam(r, "attachmentId"))
	if err != nil {
		writeAttachmentError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}
func (h *AttachmentTextHandler) Read(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeAttachmentError(w, service.ErrInvalidInput)
		return
	}
	for key, values := range query {
		if (key != "cursor" && key != "pageSize") || len(values) != 1 {
			writeAttachmentError(w, service.ErrInvalidInput)
			return
		}
	}
	size := 0
	if query.Has("pageSize") {
		size, err = strconv.Atoi(query.Get("pageSize"))
		if err != nil || size < 1 || size > 6000 {
			writeAttachmentError(w, service.ErrInvalidInput)
			return
		}
	}
	result, err := h.service.Read(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "meetingId"), chi.URLParam(r, "attachmentId"), query.Get("cursor"), size)
	if err != nil {
		writeAttachmentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func writeAttachmentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid attachment request")
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Attachment not found")
	case errors.Is(err, service.ErrUnsupportedAttachment):
		writeError(w, http.StatusUnprocessableEntity, "UNSUPPORTED_FORMAT", "Only PDF, PPTX, DOCX and Markdown support text extraction")
	case errors.Is(err, service.ErrAttachmentCursor), errors.Is(err, service.ErrAttachmentSourceChanged), errors.Is(err, repository.ErrConditionFailed):
		writeError(w, http.StatusConflict, model.ErrCodeConflict, "Attachment changed; restart reading or retry extraction")
	case errors.Is(err, service.ErrAttachmentUnavailable):
		writeError(w, http.StatusConflict, "TEXT_UNAVAILABLE", "Verified attachment text is not available")
	default:
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Attachment extraction could not be processed")
	}
}
