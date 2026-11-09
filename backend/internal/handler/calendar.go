package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

type CalendarAPI interface {
	Sync(context.Context, string, string, model.CalendarSyncRequest) (*model.CalendarSyncReceipt, error)
	ListEvents(context.Context, string, model.CalendarQuery) (*model.CalendarEventPage, error)
	ListSources(context.Context, string, string, int32) (*model.CalendarSourcePage, error)
	ListAccountCalendar(context.Context, string, string, model.CalendarQuery) (*model.AccountCalendarPage, error)
	SetEventSharing(context.Context, string, string, string, model.CalendarSharingRequest) (*model.CalendarEvent, error)
	ShareDirectory(context.Context, string) (*model.CalendarShareDirectory, error)
}

const calendarShareDirectoryMaxBytes = 64 * 1024

var calendarHTTPIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type CalendarHandler struct {
	service CalendarAPI
}

func NewCalendarHandler(service CalendarAPI) *CalendarHandler {
	return &CalendarHandler{service: service}
}

func (handler *CalendarHandler) Sync(writer http.ResponseWriter, request *http.Request) {
	userID := calendarHTTPUser(writer, request)
	if userID == "" {
		return
	}
	if _, err := calendarHTTPQuery(request); err != nil {
		writeCalendarError(writer, err)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 64*1024))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(writer, http.StatusRequestEntityTooLarge, model.ErrCodeBadRequest, "Calendar request exceeds 64 KiB")
		} else {
			writeCalendarError(writer, service.ErrInvalidInput)
		}
		return
	}
	var input model.CalendarSyncRequest
	if !utf8.Valid(body) || !validCalendarJSON(json.NewDecoder(bytes.NewReader(body)), 0) {
		writeCalendarError(writer, service.ErrInvalidInput)
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		writeCalendarError(writer, service.ErrInvalidInput)
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		writeCalendarError(writer, service.ErrInvalidInput)
		return
	}
	for _, field := range []string{"batchId", "expectedRevision", "windowStart", "windowEnd", "complete", "mutations"} {
		if _, exists := fields[field]; !exists {
			writeCalendarError(writer, service.ErrInvalidInput)
			return
		}
	}
	result, err := handler.service.Sync(request.Context(), userID, chi.URLParam(request, "sourceId"), input)
	if err != nil {
		writeCalendarError(writer, err)
		return
	}
	writeCalendarJSON(writer, result)
}

func (handler *CalendarHandler) ListEvents(writer http.ResponseWriter, request *http.Request) {
	userID := calendarHTTPUser(writer, request)
	if userID == "" {
		return
	}
	values, err := calendarHTTPQuery(request, "from", "to", "accountId", "limit", "cursor")
	if err != nil {
		writeCalendarError(writer, err)
		return
	}
	from, fromErr := time.Parse(time.RFC3339Nano, values.Get("from"))
	to, toErr := time.Parse(time.RFC3339Nano, values.Get("to"))
	limit, limitErr := calendarHTTPLimit(values)
	if fromErr != nil || toErr != nil || limitErr != nil {
		writeCalendarError(writer, service.ErrInvalidInput)
		return
	}
	page, err := handler.service.ListEvents(request.Context(), userID, model.CalendarQuery{
		From: from, To: to, AccountID: values.Get("accountId"), Limit: limit, Cursor: values.Get("cursor"),
	})
	if err != nil {
		writeCalendarError(writer, err)
		return
	}
	writeCalendarJSON(writer, page)
}

func (handler *CalendarHandler) ListSources(writer http.ResponseWriter, request *http.Request) {
	userID := calendarHTTPUser(writer, request)
	if userID == "" {
		return
	}
	values, err := calendarHTTPQuery(request, "limit", "cursor")
	if err != nil {
		writeCalendarError(writer, err)
		return
	}
	limit, err := calendarHTTPLimit(values)
	if err != nil {
		writeCalendarError(writer, err)
		return
	}
	page, err := handler.service.ListSources(request.Context(), userID, values.Get("cursor"), limit)
	if err != nil {
		writeCalendarError(writer, err)
		return
	}
	writeCalendarJSON(writer, page)
}

func (handler *CalendarHandler) ListAccountCalendar(writer http.ResponseWriter, request *http.Request) {
	userID := calendarHTTPUser(writer, request)
	if userID == "" {
		return
	}
	values, err := calendarHTTPQuery(request, "from", "to", "limit", "cursor")
	if err != nil {
		writeCalendarError(writer, err)
		return
	}
	from, fromErr := time.Parse(time.RFC3339Nano, values.Get("from"))
	to, toErr := time.Parse(time.RFC3339Nano, values.Get("to"))
	limit, limitErr := calendarHTTPLimit(values)
	if fromErr != nil || toErr != nil || limitErr != nil {
		writeCalendarError(writer, service.ErrInvalidInput)
		return
	}
	page, err := handler.service.ListAccountCalendar(request.Context(), userID, chi.URLParam(request, "accountId"), model.CalendarQuery{
		From: from, To: to, Limit: limit, Cursor: values.Get("cursor"),
	})
	if err != nil {
		writeCalendarError(writer, err)
		return
	}
	writeCalendarJSON(writer, page)
}

func (handler *CalendarHandler) SetEventSharing(writer http.ResponseWriter, request *http.Request) {
	userID := calendarHTTPUser(writer, request)
	if userID == "" {
		return
	}
	if _, err := calendarHTTPQuery(request); err != nil {
		writeCalendarError(writer, err)
		return
	}
	sourceID, eventID := chi.URLParam(request, "sourceId"), chi.URLParam(request, "eventId")
	if parsed, err := uuid.Parse(sourceID); err != nil || parsed == uuid.Nil || !calendarHTTPIdentifier.MatchString(eventID) {
		writeCalendarError(writer, service.ErrInvalidInput)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 4*1024))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(writer, http.StatusRequestEntityTooLarge, model.ErrCodeBadRequest, "Calendar request exceeds 4 KiB")
		} else {
			writeCalendarError(writer, service.ErrInvalidInput)
		}
		return
	}
	var input model.CalendarSharingRequest
	if !utf8.Valid(body) || !validCalendarJSONWith(json.NewDecoder(bytes.NewReader(body)), 0, validCalendarSharingField) {
		writeCalendarError(writer, service.ErrInvalidInput)
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.Mode == "" {
		writeCalendarError(writer, service.ErrInvalidInput)
		return
	}
	event, err := handler.service.SetEventSharing(request.Context(), userID, sourceID, eventID, input)
	if err != nil {
		writeCalendarError(writer, err)
		return
	}
	writeCalendarJSON(writer, event)
}

func (handler *CalendarHandler) ShareDirectory(writer http.ResponseWriter, request *http.Request) {
	userID := calendarHTTPUser(writer, request)
	if userID == "" {
		return
	}
	if _, err := calendarHTTPQuery(request); err != nil {
		writeCalendarError(writer, err)
		return
	}
	directory, err := handler.service.ShareDirectory(request.Context(), userID)
	if err != nil {
		writeCalendarError(writer, err)
		return
	}
	writeCalendarJSONLimit(writer, directory, calendarShareDirectoryMaxBytes)
}

func calendarHTTPUser(writer http.ResponseWriter, request *http.Request) string {
	writer.Header().Set("Cache-Control", "private, no-store")
	userID := middleware.GetUserID(request.Context())
	if userID == "" {
		writeError(writer, http.StatusUnauthorized, model.ErrCodeUnauthorized, "Authentication required")
	}
	return userID
}

func calendarHTTPQuery(request *http.Request, allowed ...string) (url.Values, error) {
	if len(request.URL.RawQuery) > 8192 {
		return nil, service.ErrInvalidInput
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return nil, service.ErrInvalidInput
	}
	allowlist := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		allowlist[name] = true
	}
	for name, entries := range values {
		if !allowlist[name] || len(entries) != 1 || entries[0] == "" {
			return nil, service.ErrInvalidInput
		}
	}
	return values, nil
}

func calendarHTTPLimit(values url.Values) (int32, error) {
	if !values.Has("limit") {
		return 0, nil
	}
	limit, err := strconv.ParseInt(values.Get("limit"), 10, 32)
	if err != nil || limit < 1 || limit > 50 {
		return 0, service.ErrInvalidInput
	}
	return int32(limit), nil
}

func validCalendarJSON(decoder *json.Decoder, depth int) bool {
	return validCalendarJSONWith(decoder, depth, validCalendarJSONField)
}

func validCalendarSharingField(depth int, name string) bool {
	return depth == 0 && (name == "mode" || name == "accountId")
}

func validCalendarJSONWith(decoder *json.Decoder, depth int, allowed func(int, string) bool) bool {
	if depth > 8 {
		return false
	}
	token, err := decoder.Token()
	if err != nil || token == nil {
		return false
	}
	delimiter, container := token.(json.Delim)
	if !container {
		return true
	}
	if delimiter != '{' && delimiter != '[' {
		return false
	}
	seen := make(map[string]bool)
	for decoder.More() {
		if delimiter == '{' {
			key, err := decoder.Token()
			name, isString := key.(string)
			if err != nil || !isString || seen[name] || !allowed(depth, name) {
				return false
			}
			seen[name] = true
		}
		if !validCalendarJSONWith(decoder, depth+1, allowed) {
			return false
		}
	}
	closing, err := decoder.Token()
	return err == nil && (delimiter == '{' && closing == json.Delim('}') || delimiter == '[' && closing == json.Delim(']'))
}

func validCalendarJSONField(depth int, name string) bool {
	switch depth {
	case 0:
		return name == "batchId" || name == "expectedRevision" || name == "windowStart" ||
			name == "windowEnd" || name == "complete" || name == "mutations"
	case 2:
		return name == "op" || name == "event" || name == "externalId"
	case 3:
		return name == "externalId" || name == "title" || name == "startsAt" || name == "endsAt" ||
			name == "timeZone" || name == "isAllDay" || name == "startDate" || name == "endDate" ||
			name == "location" || name == "organizerDomain" || name == "accountId" || name == "shareAccountId" ||
			name == "shareEvidence" || name == "candidateAccountIds" || name == "private" || name == "seriesKey"
	default:
		return false
	}
}

func writeCalendarJSON(writer http.ResponseWriter, value any) {
	writeCalendarJSONLimit(writer, value, service.CalendarResponseMaxBytes)
}

func writeCalendarJSONLimit(writer http.ResponseWriter, value any, limit int) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > limit {
		writeCalendarError(writer, repository.ErrCalendarCorruptData)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(encoded)
}

func writeCalendarError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInput), errors.Is(err, repository.ErrInvalidCalendarBatch),
		errors.Is(err, repository.ErrInvalidCalendarQuery), errors.Is(err, repository.ErrInvalidCalendarCursor):
		writeError(writer, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid calendar request")
	case errors.Is(err, service.ErrForbidden):
		writeError(writer, http.StatusForbidden, model.ErrCodeForbidden, "Calendar access denied")
	case errors.Is(err, service.ErrNotFound):
		writeError(writer, http.StatusNotFound, model.ErrCodeNotFound, "Calendar resource not found")
	case errors.Is(err, service.ErrAccountMerging):
		writeAccountMerging(writer)
	case errors.Is(err, repository.ErrConditionFailed):
		writeError(writer, http.StatusConflict, model.ErrCodeConflict, "Calendar revision or batch conflict")
	case errors.Is(err, service.ErrCalendarEncryption):
		writeError(writer, http.StatusServiceUnavailable, model.ErrCodeInternalError, "Calendar encryption unavailable")
	default:
		writeError(writer, http.StatusInternalServerError, model.ErrCodeInternalError, "Calendar request failed")
	}
}
