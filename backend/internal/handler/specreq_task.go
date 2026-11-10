package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

const (
	specReqTaskBodyMaxBytes   = 4 * 1024
	specReqTaskCursorMaxBytes = 4096
	specReqTaskMaxLimit       = 20
)

var specReqTaskEventID = regexp.MustCompile(`^[0-9a-f]{64}$`)

// SpecReqTaskAPI is the service surface used by the task handler.
type SpecReqTaskAPI interface {
	Create(context.Context, string, model.CreateSpecReqTaskRequest) (*model.SpecReqTaskView, error)
	Get(context.Context, string, string) (*model.SpecReqTaskView, error)
	List(context.Context, string, string, int32) (*model.SpecReqTaskPage, error)
	Transition(context.Context, string, string, model.SpecReqTaskTransition) (*model.SpecReqTaskView, error)
}

// SpecReqTaskHandler serves the private per-occurrence SpecReq task routes.
type SpecReqTaskHandler struct {
	service SpecReqTaskAPI
}

func NewSpecReqTaskHandler(service SpecReqTaskAPI) *SpecReqTaskHandler {
	return &SpecReqTaskHandler{service: service}
}

func (handler *SpecReqTaskHandler) List(writer http.ResponseWriter, request *http.Request) {
	userID := specReqTaskUser(writer, request)
	if userID == "" {
		return
	}
	values, err := calendarHTTPQuery(request, "limit", "cursor")
	if err != nil || len(values.Get("cursor")) > specReqTaskCursorMaxBytes {
		writeSpecReqTaskError(writer, service.ErrInvalidInput)
		return
	}
	var limit int32
	if values.Has("limit") {
		parsed, parseErr := strconv.ParseInt(values.Get("limit"), 10, 32)
		if parseErr != nil || parsed < 1 || parsed > specReqTaskMaxLimit {
			writeSpecReqTaskError(writer, service.ErrInvalidInput)
			return
		}
		limit = int32(parsed)
	}
	page, err := handler.service.List(request.Context(), userID, values.Get("cursor"), limit)
	if err != nil {
		writeSpecReqTaskError(writer, err)
		return
	}
	writeSpecReqTaskJSON(writer, http.StatusOK, page)
}

func (handler *SpecReqTaskHandler) Get(writer http.ResponseWriter, request *http.Request) {
	userID := specReqTaskUser(writer, request)
	if userID == "" {
		return
	}
	eventID := chi.URLParam(request, "eventId")
	if !specReqTaskEventID.MatchString(eventID) {
		writeSpecReqTaskError(writer, service.ErrInvalidInput)
		return
	}
	if _, err := calendarHTTPQuery(request); err != nil {
		writeSpecReqTaskError(writer, err)
		return
	}
	view, err := handler.service.Get(request.Context(), userID, eventID)
	if err != nil {
		writeSpecReqTaskError(writer, err)
		return
	}
	writeSpecReqTaskJSON(writer, http.StatusOK, view)
}

func (handler *SpecReqTaskHandler) Create(writer http.ResponseWriter, request *http.Request) {
	userID := specReqTaskUser(writer, request)
	if userID == "" {
		return
	}
	if _, err := calendarHTTPQuery(request); err != nil {
		writeSpecReqTaskError(writer, err)
		return
	}
	var input model.CreateSpecReqTaskRequest
	if !decodeSpecReqTaskBody(writer, request, &input) {
		return
	}
	view, err := handler.service.Create(request.Context(), userID, input)
	if err != nil {
		writeSpecReqTaskError(writer, err)
		return
	}
	writeSpecReqTaskJSON(writer, http.StatusCreated, view)
}

func (handler *SpecReqTaskHandler) Transition(writer http.ResponseWriter, request *http.Request) {
	userID := specReqTaskUser(writer, request)
	if userID == "" {
		return
	}
	eventID := chi.URLParam(request, "eventId")
	if !specReqTaskEventID.MatchString(eventID) {
		writeSpecReqTaskError(writer, service.ErrInvalidInput)
		return
	}
	if _, err := calendarHTTPQuery(request); err != nil {
		writeSpecReqTaskError(writer, err)
		return
	}
	var input model.SpecReqTaskTransition
	if !decodeSpecReqTaskBody(writer, request, &input) {
		return
	}
	view, err := handler.service.Transition(request.Context(), userID, eventID, input)
	if err != nil {
		writeSpecReqTaskError(writer, err)
		return
	}
	writeSpecReqTaskJSON(writer, http.StatusOK, view)
}

func specReqTaskUser(writer http.ResponseWriter, request *http.Request) string {
	writer.Header().Set("Cache-Control", "no-store")
	userID := middleware.GetUserID(request.Context())
	if userID == "" {
		writeError(writer, http.StatusUnauthorized, model.ErrCodeUnauthorized, "Authentication required")
	}
	return userID
}

// decodeSpecReqTaskBody reads a bounded JSON object rejecting unknown fields
// and trailing data; it writes the error response itself.
func decodeSpecReqTaskBody(writer http.ResponseWriter, request *http.Request, target any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, specReqTaskBodyMaxBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(writer, http.StatusRequestEntityTooLarge, model.ErrCodeBadRequest, "Task request exceeds 4 KiB")
		} else {
			writeSpecReqTaskError(writer, service.ErrInvalidInput)
		}
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if !utf8.Valid(body) || decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		writeSpecReqTaskError(writer, service.ErrInvalidInput)
		return false
	}
	return true
}

func writeSpecReqTaskJSON(writer http.ResponseWriter, status int, value any) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > service.CalendarResponseMaxBytes {
		writeSpecReqTaskError(writer, repository.ErrCalendarCorruptData)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(encoded)
}

func writeSpecReqTaskError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		writeError(writer, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid task request")
	case errors.Is(err, service.ErrForbidden):
		writeError(writer, http.StatusForbidden, model.ErrCodeForbidden, "Task access denied")
	case errors.Is(err, service.ErrNotFound):
		writeError(writer, http.StatusNotFound, model.ErrCodeNotFound, "Task resource not found")
	case errors.Is(err, service.ErrConflict):
		writeError(writer, http.StatusConflict, model.ErrCodeConflict, "Task state conflict")
	case errors.Is(err, service.ErrCalendarEncryption):
		writeError(writer, http.StatusServiceUnavailable, model.ErrCodeInternalError, "Task encryption unavailable")
	default:
		writeError(writer, http.StatusInternalServerError, model.ErrCodeInternalError, "Task request failed")
	}
}
