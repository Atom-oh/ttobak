package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

const calendarHTTPSource = "b7aa27f9-9878-4f52-9b54-a3b73a941d31"
const calendarHTTPBody = `{"batchId":"90ec2d25-a32e-43a5-96a9-e203859b7d19","expectedRevision":0,"windowStart":"2026-11-01T00:00:00Z","windowEnd":"2026-12-01T00:00:00Z","complete":true,"mutations":[]}`

type calendarAPIFake struct {
	calls    int
	userID   string
	sourceID string
	request  model.CalendarSyncRequest
	query    model.CalendarQuery
	cursor   string
	limit    int32
	err      error
	page     *model.CalendarEventPage
}

func (fake *calendarAPIFake) Sync(_ context.Context, userID, sourceID string, request model.CalendarSyncRequest) (*model.CalendarSyncReceipt, error) {
	fake.calls++
	fake.userID, fake.sourceID, fake.request = userID, sourceID, request
	return &model.CalendarSyncReceipt{BatchID: request.BatchID, Revision: request.ExpectedRevision + 1, Completed: request.Complete}, fake.err
}

func (fake *calendarAPIFake) ListEvents(_ context.Context, userID string, query model.CalendarQuery) (*model.CalendarEventPage, error) {
	fake.calls++
	fake.userID, fake.query = userID, query
	if fake.page != nil {
		return fake.page, fake.err
	}
	return &model.CalendarEventPage{Events: []model.CalendarEvent{}}, fake.err
}

func (fake *calendarAPIFake) ListSources(_ context.Context, userID, cursor string, limit int32) (*model.CalendarSourcePage, error) {
	fake.calls++
	fake.userID, fake.cursor, fake.limit = userID, cursor, limit
	return &model.CalendarSourcePage{Sources: []model.CalendarSourceStatus{}}, fake.err
}

func calendarHTTPRouter(api *calendarAPIFake, userID string, verifiedAuth bool) http.Handler {
	handler := NewCalendarHandler(api)
	router := chi.NewRouter()
	router.Use(middleware.RequestLog)
	if verifiedAuth {
		router.Use(middleware.Auth)
	} else if userID != "" {
		router.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), middleware.UserIDKey, userID)))
			})
		})
	}
	router.Post("/api/calendar/sources/{sourceId}/sync", handler.Sync)
	router.Get("/api/calendar/events", handler.ListEvents)
	router.Get("/api/calendar/sources", handler.ListSources)
	return router
}

func calendarHTTPRequest(api *calendarAPIFake, userID, method, target, body string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	calendarHTTPRouter(api, userID, false).ServeHTTP(response, httptest.NewRequest(method, target, strings.NewReader(body)))
	return response
}

func TestCalendarHTTPRequiresAuthenticationAndVerifiedIdentity(test *testing.T) {
	for _, target := range []string{"/api/calendar/sources/" + calendarHTTPSource + "/sync", "/api/calendar/events", "/api/calendar/sources"} {
		method := http.MethodGet
		if strings.HasSuffix(target, "/sync") {
			method = http.MethodPost
		}
		for _, verifiedAuth := range []bool{false, true} {
			api := &calendarAPIFake{}
			request := httptest.NewRequest(method, target, strings.NewReader(calendarHTTPBody))
			request.Header.Set("X-User-ID", "owner")
			if verifiedAuth {
				request.Header.Set("Authorization", "Bearer forged-token")
			}
			response := httptest.NewRecorder()
			calendarHTTPRouter(api, "", verifiedAuth).ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized || api.calls != 0 {
				test.Fatalf("unauthorized %s reached service: %d", target, response.Code)
			}
		}
	}
	api := &calendarAPIFake{}
	response := calendarHTTPRequest(api, "verified-owner", http.MethodPost, "/api/calendar/sources/"+calendarHTTPSource+"/sync", calendarHTTPBody)
	if response.Code != http.StatusOK || api.userID != "verified-owner" || api.sourceID != calendarHTTPSource {
		test.Fatalf("trusted identity/source not used: %d", response.Code)
	}
}

func TestCalendarHTTPSyncStrictOriginalBodyLimits(test *testing.T) {
	for _, scenario := range []string{"64 KiB", "64 KiB plus one", "malformed", "null", "trailing JSON", "unknown caller", "unknown source", "unknown mutation field", "unknown event field", "missing revision", "null complete", "null mutations", "null event", "null event field", "duplicate key", "duplicate mutation key", "duplicate event key", "invalid UTF8"} {
		test.Run(scenario, func(test *testing.T) {
			body := calendarHTTPBody
			expected := http.StatusBadRequest
			switch scenario {
			case "64 KiB":
				body += strings.Repeat(" ", 65536-len(body))
				expected = http.StatusOK
			case "64 KiB plus one":
				body += strings.Repeat(" ", 65537-len(body))
				expected = http.StatusRequestEntityTooLarge
			case "malformed":
				body = "{"
			case "null":
				body = "null"
			case "trailing JSON":
				body += "{}"
			case "unknown caller":
				body = strings.Replace(body, `"mutations":[]`, `"mutations":[],"userId":"other"`, 1)
			case "unknown source":
				body = strings.Replace(body, `"mutations":[]`, `"mutations":[],"sourceId":"other"`, 1)
			case "unknown mutation field":
				body = strings.Replace(body, `"mutations":[]`, `"mutations":[{"op":"remove","externalId":"synthetic-first","complete":true}]`, 1)
			case "unknown event field":
				body = strings.Replace(body, `"mutations":[]`, `"mutations":[{"op":"upsert","event":{"body":"PRIVATE"}}]`, 1)
			case "missing revision":
				body = strings.Replace(body, `"expectedRevision":0,`, "", 1)
			case "null complete":
				body = strings.Replace(body, `"complete":true`, `"complete":null`, 1)
			case "null mutations":
				body = strings.Replace(body, `"mutations":[]`, `"mutations":null`, 1)
			case "null event":
				body = strings.Replace(body, `"mutations":[]`, `"mutations":[{"op":"upsert","event":null}]`, 1)
			case "null event field":
				body = strings.Replace(body, `"mutations":[]`, `"mutations":[{"op":"upsert","event":{"title":null}}]`, 1)
			case "duplicate key":
				body = strings.Replace(body, `"complete":true`, `"complete":false,"complete":true`, 1)
			case "duplicate mutation key":
				body = strings.Replace(body, `"mutations":[]`, `"mutations":[{"op":"remove","externalId":"synthetic-first","externalId":"synthetic-second"}]`, 1)
			case "duplicate event key":
				body = strings.Replace(body, `"mutations":[]`, `"mutations":[{"op":"upsert","event":{"title":"First","title":"Second"}}]`, 1)
			case "invalid UTF8":
				body = strings.Replace(body, `"batchId":"`, `"batchId":"`+string([]byte{0xff}), 1)
			}
			api := &calendarAPIFake{}
			response := calendarHTTPRequest(api, "owner", http.MethodPost, "/api/calendar/sources/"+calendarHTTPSource+"/sync", body)
			if response.Code != expected {
				test.Fatalf("status = %d, want %d", response.Code, expected)
			}
			if expected != http.StatusOK && api.calls != 0 {
				test.Fatal("invalid original JSON reached service")
			}
		})
	}
}

func TestCalendarHTTPSyncRejectsCaseAliasedFields(test *testing.T) {
	removeBody := strings.Replace(calendarHTTPBody, `"mutations":[]`, `"mutations":[{"op":"remove","externalId":"synthetic-first"}]`, 1)
	eventJSON := `{"externalId":"synthetic-first","title":"Synthetic event","startsAt":"2026-11-08T00:00:00Z","endsAt":"2026-11-09T00:00:00Z","timeZone":"UTC","isAllDay":true,"startDate":"2026-11-08","endDate":"2026-11-09","location":"Synthetic location","accountId":"synthetic-account"}`
	upsertBody := strings.Replace(calendarHTTPBody, `"mutations":[]`, `"mutations":[{"op":"upsert","event":`+eventJSON+`}]`, 1)
	type aliasCase struct {
		name string
		body string
	}
	cases := []aliasCase{
		{"complete alias last", strings.Replace(calendarHTTPBody, `"complete":true`, `"complete":false,"Complete":true`, 1)},
		{"complete alias first", strings.Replace(calendarHTTPBody, `"complete":true`, `"Complete":false,"complete":true`, 1)},
		{"mutations alias last", strings.Replace(calendarHTTPBody, `"mutations":[]`, `"mutations":[],"Mutations":[{"op":"remove","externalId":"synthetic-first"}]`, 1)},
		{"mutations alias first", strings.Replace(calendarHTTPBody, `"mutations":[]`, `"Mutations":[{"op":"remove","externalId":"synthetic-first"}],"mutations":[]`, 1)},
		{"mutation identity alias last", strings.Replace(removeBody, `"externalId":"synthetic-first"`, `"externalId":"synthetic-first","ExternalId":"synthetic-second"`, 1)},
		{"mutation identity alias first", strings.Replace(removeBody, `"externalId":"synthetic-first"`, `"ExternalId":"synthetic-second","externalId":"synthetic-first"`, 1)},
		{"mutation identity alias only", strings.Replace(removeBody, `"externalId":`, `"ExternalId":`, 1)},
		{"mutation op alias", strings.Replace(removeBody, `"op":"remove"`, `"op":"remove","Op":"remove"`, 1)},
		{"mutation event alias", strings.Replace(upsertBody, `"event":`+eventJSON, `"event":`+eventJSON+`,"Event":`+eventJSON, 1)},
		{"event identity alias last", strings.Replace(upsertBody, `"externalId":"synthetic-first"`, `"externalId":"synthetic-first","ExternalId":"synthetic-second"`, 1)},
		{"event identity alias first", strings.Replace(upsertBody, `"externalId":"synthetic-first"`, `"ExternalId":"synthetic-second","externalId":"synthetic-first"`, 1)},
		{"event title alias last", strings.Replace(upsertBody, `"title":"Synthetic event"`, `"title":"Synthetic event","Title":"Replacement event"`, 1)},
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(calendarHTTPBody), &fields); err != nil {
		test.Fatal(err)
	}
	for _, field := range []string{"batchId", "expectedRevision", "windowStart", "windowEnd"} {
		original := `"` + field + `":` + string(fields[field])
		alias := `"` + strings.ToUpper(field[:1]) + field[1:] + `":` + string(fields[field])
		cases = append(cases, aliasCase{"root " + field + " alias", strings.Replace(calendarHTTPBody, original, original+","+alias, 1)})
	}
	for _, field := range []string{"externalId", "title", "startsAt", "endsAt", "timeZone", "isAllDay", "startDate", "endDate", "location", "accountId"} {
		original := `"` + field + `":`
		alias := `"` + strings.ToUpper(field[:1]) + field[1:] + `":`
		cases = append(cases, aliasCase{"event " + field + " alias only", strings.Replace(upsertBody, original, alias, 1)})
	}
	for _, scenario := range cases {
		test.Run(scenario.name, func(test *testing.T) {
			api := &calendarAPIFake{}
			response := calendarHTTPRequest(api, "owner", http.MethodPost, "/api/calendar/sources/"+calendarHTTPSource+"/sync", scenario.body)
			if response.Code != http.StatusBadRequest || api.calls != 0 {
				test.Fatalf("case alias reached service: status=%d calls=%d, want status=400 calls=0", response.Code, api.calls)
			}
		})
	}
	for _, body := range []string{calendarHTTPBody, removeBody, upsertBody} {
		api := &calendarAPIFake{}
		response := calendarHTTPRequest(api, "owner", http.MethodPost, "/api/calendar/sources/"+calendarHTTPSource+"/sync", body)
		if response.Code != http.StatusOK || api.calls != 1 {
			test.Fatalf("exact field names rejected: status=%d calls=%d, want status=200 calls=1", response.Code, api.calls)
		}
	}
}

func TestCalendarHTTPQueryScopeAndParsing(test *testing.T) {
	valid := "/api/calendar/events?from=2026-11-07T00:00:00Z&to=2026-11-14T00:00:00Z"
	for _, suffix := range []string{"&userId=other", "&sourceId=guessed", "&limit=51", "&limit=0", "&limit=-1", "&limit=abc", "&limit=1&limit=2", "&from=2026-11-08T00:00:00Z"} {
		api := &calendarAPIFake{}
		response := calendarHTTPRequest(api, "owner", http.MethodGet, valid+suffix, "")
		if response.Code != http.StatusBadRequest || api.calls != 0 {
			test.Fatalf("ambiguous/forged query accepted: %s, %d", suffix, response.Code)
		}
	}
	for _, target := range []string{"/api/calendar/events", "/api/calendar/events?from=bad&to=bad", "/api/calendar/sources?accountId=other"} {
		api := &calendarAPIFake{}
		if response := calendarHTTPRequest(api, "owner", http.MethodGet, target, ""); response.Code != http.StatusBadRequest || api.calls != 0 {
			test.Fatalf("invalid query accepted: %s", target)
		}
	}
	api := &calendarAPIFake{}
	cursor := "opaque+/=value"
	response := calendarHTTPRequest(api, "owner", http.MethodGet, valid+"&accountId=account-1&limit=5&cursor="+url.QueryEscape(cursor), "")
	if response.Code != http.StatusOK || api.userID != "owner" || api.query.AccountID != "account-1" || api.query.Limit != 5 ||
		api.query.Cursor != cursor || !api.query.From.Equal(time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)) {
		test.Fatalf("query scope changed: %+v, %d", api.query, response.Code)
	}
	response = calendarHTTPRequest(api, "owner", http.MethodGet, "/api/calendar/sources?limit=50&cursor="+url.QueryEscape(cursor), "")
	if response.Code != http.StatusOK || api.limit != 50 || api.cursor != cursor {
		test.Fatal("source query lost cursor or requested limit")
	}
}

func TestCalendarHTTPErrorsAreShapedAndPrivate(test *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{service.ErrInvalidInput, 400, "BAD_REQUEST"},
		{repository.ErrInvalidCalendarCursor, 400, "BAD_REQUEST"},
		{service.ErrForbidden, 403, "FORBIDDEN"},
		{service.ErrNotFound, 404, "NOT_FOUND"},
		{repository.ErrConditionFailed, 409, "CONFLICT"},
		{service.ErrCalendarEncryption, 503, "INTERNAL_ERROR"},
		{errors.New("PrivateTitle provider error credentials"), 500, "INTERNAL_ERROR"},
	}
	var logs bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldWriter)
	for _, scenario := range cases {
		api := &calendarAPIFake{err: fmt.Errorf("wrapped: %w", scenario.err)}
		response := calendarHTTPRequest(api, "owner", http.MethodPost, "/api/calendar/sources/"+calendarHTTPSource+"/sync", calendarHTTPBody)
		var body struct {
			Error struct{ Code, Message string }
		}
		if json.Unmarshal(response.Body.Bytes(), &body) != nil || response.Code != scenario.status || body.Error.Code != scenario.code {
			test.Fatalf("wrong error shape/status: %d %s", response.Code, response.Body)
		}
		if strings.Contains(response.Body.String(), "PrivateTitle") || strings.Contains(logs.String(), "PrivateTitle") {
			test.Fatal("private error text disclosed")
		}
	}
	privateBody := strings.Replace(calendarHTTPBody, `"mutations":[]`, `"mutations":[{"op":"upsert","event":{"title":"PRIVATE_BODY_MARKER"}}]`, 1)
	calendarHTTPRequest(&calendarAPIFake{}, "owner", http.MethodPost, "/api/calendar/sources/"+calendarHTTPSource+"/sync", privateBody)
	if strings.Contains(logs.String(), "PRIVATE_BODY_MARKER") || strings.Contains(logs.String(), calendarHTTPSource) {
		test.Fatal("request log captured calendar content or literal source ID")
	}
}

func TestCalendarHTTPBoundsEncodedResponsesBeforeWriting(test *testing.T) {
	for _, size := range []int{14000, 14001} {
		api := &calendarAPIFake{page: &model.CalendarEventPage{Events: []model.CalendarEvent{{Title: ""}}}}
		empty, _ := json.Marshal(api.page)
		api.page.Events[0].Title = strings.Repeat("x", size-len(empty))
		response := calendarHTTPRequest(api, "owner", http.MethodGet, "/api/calendar/events?from=2026-11-07T00:00:00Z&to=2026-11-14T00:00:00Z", "")
		expected := http.StatusOK
		if size > 14000 {
			expected = http.StatusInternalServerError
		}
		if response.Code != expected || response.Body.Len() > 14000 || !json.Valid(response.Body.Bytes()) {
			test.Fatalf("encoded response bound broken: size=%d status=%d bytes=%d", size, response.Code, response.Body.Len())
		}
		if response.Header().Get("Cache-Control") != "private, no-store" {
			test.Fatal("private calendar response can be cached")
		}
	}
}

func TestCalendarHTTPSyncAcceptsSharingFields(test *testing.T) {
	key := strings.Repeat("ab", 32)
	eventJSON := `{"externalId":"synthetic-first","title":"Synthetic event","startsAt":"2026-11-08T00:00:00Z","endsAt":"2026-11-09T00:00:00Z","timeZone":"UTC","shareAccountId":"acc_1","shareEvidence":["title"],"candidateAccountIds":["acc_2"],"private":false,"seriesKey":"` + key + `"}`
	body := strings.Replace(calendarHTTPBody, `"mutations":[]`, `"mutations":[{"op":"upsert","event":`+eventJSON+`}]`, 1)
	api := &calendarAPIFake{}
	response := calendarHTTPRequest(api, "owner", http.MethodPost, "/api/calendar/sources/"+calendarHTTPSource+"/sync", body)
	if response.Code != http.StatusOK || api.calls != 1 {
		test.Fatalf("sharing fields rejected: status=%d calls=%d", response.Code, api.calls)
	}
	alias := strings.Replace(body, `"seriesKey"`, `"SeriesKey"`, 1)
	api = &calendarAPIFake{}
	response = calendarHTTPRequest(api, "owner", http.MethodPost, "/api/calendar/sources/"+calendarHTTPSource+"/sync", alias)
	if response.Code != http.StatusBadRequest || api.calls != 0 {
		test.Fatalf("alias reached service: status=%d calls=%d", response.Code, api.calls)
	}
}
