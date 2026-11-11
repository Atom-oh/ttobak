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
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

const performanceBodyMaxBytes = 64 * 1024

var performanceYearPattern = regexp.MustCompile(`^[0-9]{4}$`)

// PerformanceAPI is owner-only: every call receives the verified JWT user and
// no other user identifier.
type PerformanceAPI interface {
	Sync(ctx context.Context, userID, source string, request model.PerformanceSyncRequest) (*model.PerformanceSyncReceipt, error)
	ListSources(ctx context.Context, userID string) (*model.PerformanceSourceList, error)
	GetGoals(ctx context.Context, userID string, year int) (*model.PerformanceGoals, error)
	PutGoals(ctx context.Context, userID string, year int, text string) (*model.PerformanceGoals, error)
	ListMappings(ctx context.Context, userID string) (*model.PerformanceMappingList, error)
	PutMapping(ctx context.Context, userID, sfdcAccountID, accountID string) (*model.AccountMapping, error)
	ListMissed(ctx context.Context, userID, period string) (*model.PerformanceMissedList, error)
	PutReport(ctx context.Context, userID, periodKey string, report model.PerformanceReport) (*model.PerformanceReportReceipt, error)
	GetReport(ctx context.Context, userID, periodKey string) (*model.PerformanceReport, error)
	ListReports(ctx context.Context, userID string) (*model.PerformanceReportList, error)
	DeleteReport(ctx context.Context, userID, periodKey string) error
}

type PerformanceHandler struct {
	service PerformanceAPI
}

func NewPerformanceHandler(service PerformanceAPI) *PerformanceHandler {
	return &PerformanceHandler{service: service}
}

func (handler *PerformanceHandler) Sync(writer http.ResponseWriter, request *http.Request) {
	userID := calendarHTTPUser(writer, request)
	if userID == "" {
		return
	}
	if _, err := calendarHTTPQuery(request); err != nil {
		writePerformanceError(writer, err)
		return
	}
	var input model.PerformanceSyncRequest
	if !decodePerformanceBody(writer, request, &input, "batchId", "expectedRevision", "complete", "mutations") {
		return
	}
	result, err := handler.service.Sync(request.Context(), userID, chi.URLParam(request, "source"), input)
	if err != nil {
		writePerformanceError(writer, err)
		return
	}
	writePerformanceJSON(writer, result)
}

func (handler *PerformanceHandler) ListSources(writer http.ResponseWriter, request *http.Request) {
	userID := calendarHTTPUser(writer, request)
	if userID == "" {
		return
	}
	if _, err := calendarHTTPQuery(request); err != nil {
		writePerformanceError(writer, err)
		return
	}
	result, err := handler.service.ListSources(request.Context(), userID)
	if err != nil {
		writePerformanceError(writer, err)
		return
	}
	writePerformanceJSON(writer, result)
}

func (handler *PerformanceHandler) GetGoals(writer http.ResponseWriter, request *http.Request) {
	userID := calendarHTTPUser(writer, request)
	if userID == "" {
		return
	}
	year, ok := performanceHTTPYear(writer, request)
	if !ok {
		return
	}
	result, err := handler.service.GetGoals(request.Context(), userID, year)
	if err != nil {
		writePerformanceError(writer, err)
		return
	}
	writePerformanceJSON(writer, result)
}

func (handler *PerformanceHandler) PutGoals(writer http.ResponseWriter, request *http.Request) {
	userID := calendarHTTPUser(writer, request)
	if userID == "" {
		return
	}
	year, ok := performanceHTTPYear(writer, request)
	if !ok {
		return
	}
	var input model.PerformanceGoalsInput
	if !decodePerformanceBody(writer, request, &input, "text") {
		return
	}
	result, err := handler.service.PutGoals(request.Context(), userID, year, *input.Text)
	if err != nil {
		writePerformanceError(writer, err)
		return
	}
	writePerformanceJSON(writer, result)
}

func (handler *PerformanceHandler) ListMappings(writer http.ResponseWriter, request *http.Request) {
	userID := calendarHTTPUser(writer, request)
	if userID == "" {
		return
	}
	if _, err := calendarHTTPQuery(request); err != nil {
		writePerformanceError(writer, err)
		return
	}
	result, err := handler.service.ListMappings(request.Context(), userID)
	if err != nil {
		writePerformanceError(writer, err)
		return
	}
	writePerformanceJSON(writer, result)
}

func (handler *PerformanceHandler) PutMapping(writer http.ResponseWriter, request *http.Request) {
	userID := calendarHTTPUser(writer, request)
	if userID == "" {
		return
	}
	if _, err := calendarHTTPQuery(request); err != nil {
		writePerformanceError(writer, err)
		return
	}
	var input model.PerformanceMappingInput
	if !decodePerformanceBody(writer, request, &input, "accountId") {
		return
	}
	result, err := handler.service.PutMapping(request.Context(), userID, chi.URLParam(request, "sfdcAccountId"), input.AccountID)
	if err != nil {
		writePerformanceError(writer, err)
		return
	}
	writePerformanceJSON(writer, result)
}

func (handler *PerformanceHandler) ListMissed(writer http.ResponseWriter, request *http.Request) {
	userID := calendarHTTPUser(writer, request)
	if userID == "" {
		return
	}
	values, err := calendarHTTPQuery(request, "period")
	if err != nil {
		writePerformanceError(writer, err)
		return
	}
	result, err := handler.service.ListMissed(request.Context(), userID, values.Get("period"))
	if err != nil {
		writePerformanceError(writer, err)
		return
	}
	writePerformanceJSON(writer, result)
}

// performanceReportFields are the report's required top-level fields.
// missingSources and highlights may be absent; nested optional fields (tables,
// cards, note, estimates) may be absent too. Null is never accepted.
var performanceReportFields = []string{"schemaVersion", "periodKey", "period", "alias", "generatedAt", "model", "partial",
	"summary", "ledger", "sections", "actions", "dataBasis"}

func (handler *PerformanceHandler) PutReport(writer http.ResponseWriter, request *http.Request) {
	userID := calendarHTTPUser(writer, request)
	if userID == "" {
		return
	}
	if _, err := calendarHTTPQuery(request); err != nil {
		writePerformanceError(writer, err)
		return
	}
	var input model.PerformanceReport
	if !decodePerformanceBodyWithin(writer, request, performanceReportBodyLimits, &input, performanceReportFields...) {
		return
	}
	result, err := handler.service.PutReport(request.Context(), userID, chi.URLParam(request, "periodKey"), input)
	if err != nil {
		writePerformanceError(writer, err)
		return
	}
	writePerformanceJSON(writer, result)
}

func (handler *PerformanceHandler) GetReport(writer http.ResponseWriter, request *http.Request) {
	userID := calendarHTTPUser(writer, request)
	if userID == "" {
		return
	}
	if _, err := calendarHTTPQuery(request); err != nil {
		writePerformanceError(writer, err)
		return
	}
	result, err := handler.service.GetReport(request.Context(), userID, chi.URLParam(request, "periodKey"))
	if err != nil {
		writePerformanceError(writer, err)
		return
	}
	writePerformanceJSON(writer, result)
}

func (handler *PerformanceHandler) ListReports(writer http.ResponseWriter, request *http.Request) {
	userID := calendarHTTPUser(writer, request)
	if userID == "" {
		return
	}
	if _, err := calendarHTTPQuery(request); err != nil {
		writePerformanceError(writer, err)
		return
	}
	result, err := handler.service.ListReports(request.Context(), userID)
	if err != nil {
		writePerformanceError(writer, err)
		return
	}
	writePerformanceJSON(writer, result)
}

func (handler *PerformanceHandler) DeleteReport(writer http.ResponseWriter, request *http.Request) {
	userID := calendarHTTPUser(writer, request)
	if userID == "" {
		return
	}
	if _, err := calendarHTTPQuery(request); err != nil {
		writePerformanceError(writer, err)
		return
	}
	if err := handler.service.DeleteReport(request.Context(), userID, chi.URLParam(request, "periodKey")); err != nil {
		writePerformanceError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func performanceHTTPYear(writer http.ResponseWriter, request *http.Request) (int, bool) {
	values, err := calendarHTTPQuery(request, "year")
	if err != nil || !performanceYearPattern.MatchString(values.Get("year")) {
		writePerformanceError(writer, service.ErrInvalidInput)
		return 0, false
	}
	year, err := strconv.Atoi(values.Get("year"))
	if err != nil {
		writePerformanceError(writer, service.ErrInvalidInput)
		return 0, false
	}
	return year, true
}

// performanceBodyLimits bounds one strict JSON body.
type performanceBodyLimits struct {
	maxBytes int64
	maxDepth int
	tooLarge string
}

var (
	performanceSyncBodyLimits = performanceBodyLimits{maxBytes: performanceBodyMaxBytes, maxDepth: 4, tooLarge: "Performance request exceeds 64 KiB"}
	// A report nests report > sections > section > tables > table > rows > row > cell.
	performanceReportBodyLimits = performanceBodyLimits{maxBytes: model.PerformanceReportMaxBytes, maxDepth: 7, tooLarge: "Performance report exceeds 256 KiB"}
)

// decodePerformanceBody reads at most 64 KiB of strict JSON: valid UTF-8, no
// duplicate keys or nulls, no unknown fields, no trailing data, and every
// required top-level field present. It writes the error response itself.
func decodePerformanceBody(writer http.ResponseWriter, request *http.Request, target any, required ...string) bool {
	return decodePerformanceBodyWithin(writer, request, performanceSyncBodyLimits, target, required...)
}

// decodePerformanceBodyWithin is decodePerformanceBody with explicit limits.
// The byte cap applies while reading, before any decoding.
func decodePerformanceBodyWithin(writer http.ResponseWriter, request *http.Request, limits performanceBodyLimits, target any, required ...string) bool {
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, limits.maxBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(writer, http.StatusRequestEntityTooLarge, model.ErrCodeBadRequest, limits.tooLarge)
		} else {
			writePerformanceError(writer, service.ErrInvalidInput)
		}
		return false
	}
	if !utf8.Valid(body) || !validPerformanceJSON(json.NewDecoder(bytes.NewReader(body)), 0, limits.maxDepth) {
		writePerformanceError(writer, service.ErrInvalidInput)
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		writePerformanceError(writer, service.ErrInvalidInput)
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		writePerformanceError(writer, service.ErrInvalidInput)
		return false
	}
	for _, field := range required {
		if _, exists := fields[field]; !exists {
			writePerformanceError(writer, service.ErrInvalidInput)
			return false
		}
	}
	return true
}

// validPerformanceJSON rejects nulls, duplicate object keys and nesting
// deeper than maxDepth. Field names are checked by the decoder.
func validPerformanceJSON(decoder *json.Decoder, depth, maxDepth int) bool {
	if depth > maxDepth {
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
			if err != nil || !isString || seen[name] {
				return false
			}
			seen[name] = true
		}
		if !validPerformanceJSON(decoder, depth+1, maxDepth) {
			return false
		}
	}
	closing, err := decoder.Token()
	return err == nil && (delimiter == '{' && closing == json.Delim('}') || delimiter == '[' && closing == json.Delim(']'))
}

func writePerformanceJSON(writer http.ResponseWriter, value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		writePerformanceError(writer, err)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(encoded)
}

func writePerformanceError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInput), errors.Is(err, repository.ErrInvalidPerformanceBatch),
		errors.Is(err, repository.ErrInvalidPerformanceInput), errors.Is(err, model.ErrInvalidPerformanceReport):
		writeError(writer, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid performance request")
	case errors.Is(err, service.ErrForbidden):
		writeError(writer, http.StatusForbidden, model.ErrCodeForbidden, "Performance access denied")
	case errors.Is(err, service.ErrNotFound):
		writeError(writer, http.StatusNotFound, model.ErrCodeNotFound, "Performance resource not found")
	case errors.Is(err, repository.ErrConditionFailed):
		writeError(writer, http.StatusConflict, model.ErrCodeConflict, "Performance revision or batch conflict")
	default:
		writeError(writer, http.StatusInternalServerError, model.ErrCodeInternalError, "Performance request failed")
	}
}
