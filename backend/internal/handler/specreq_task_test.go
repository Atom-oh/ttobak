package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

var specReqTaskTestEvent = strings.Repeat("ab", 32)

type specReqTaskFake struct {
	calls  int
	err    error
	userID string
	cursor string
	limit  int32
	create model.CreateSpecReqTaskRequest
	change model.SpecReqTaskTransition
	event  string
}

func (fake *specReqTaskFake) view() *model.SpecReqTaskView {
	return &model.SpecReqTaskView{SpecReqTask: model.SpecReqTask{EventID: specReqTaskTestEvent, State: "pending", Version: 1}}
}

func (fake *specReqTaskFake) Create(_ context.Context, userID string, request model.CreateSpecReqTaskRequest) (*model.SpecReqTaskView, error) {
	fake.calls++
	fake.userID, fake.create = userID, request
	return fake.view(), fake.err
}

func (fake *specReqTaskFake) Get(_ context.Context, userID, eventID string) (*model.SpecReqTaskView, error) {
	fake.calls++
	fake.userID, fake.event = userID, eventID
	return fake.view(), fake.err
}

func (fake *specReqTaskFake) List(_ context.Context, userID, cursor string, limit int32) (*model.SpecReqTaskPage, error) {
	fake.calls++
	fake.userID, fake.cursor, fake.limit = userID, cursor, limit
	return &model.SpecReqTaskPage{Tasks: []model.SpecReqTaskView{}}, fake.err
}

func (fake *specReqTaskFake) Transition(_ context.Context, userID, eventID string, change model.SpecReqTaskTransition) (*model.SpecReqTaskView, error) {
	fake.calls++
	fake.userID, fake.event, fake.change = userID, eventID, change
	return fake.view(), fake.err
}

func specReqTaskRequest(fake *specReqTaskFake, userID, method, target, body string) *httptest.ResponseRecorder {
	handler := NewSpecReqTaskHandler(fake)
	router := chi.NewRouter()
	if userID != "" {
		router.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), middleware.UserIDKey, userID)))
			})
		})
	}
	router.Get("/api/specreq-tasks", handler.List)
	router.Get("/api/specreq-tasks/{eventId}", handler.Get)
	router.Post("/api/specreq-tasks", handler.Create)
	router.Post("/api/specreq-tasks/{eventId}/transitions", handler.Transition)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(method, target, strings.NewReader(body)))
	return response
}

const (
	specReqCreateBody     = `{"sourceId":"b7aa27f9-9878-4f52-9b54-a3b73a941d31","externalId":"x","state":"pending"}`
	specReqTransitionBody = `{"op":"close","version":1}`
)

func TestSpecReqTaskHandler_RoutesAndErrors(test *testing.T) {
	routes := []struct {
		name, method, target, body string
		success                    int
	}{
		{"list", http.MethodGet, "/api/specreq-tasks?limit=5", "", http.StatusOK},
		{"get", http.MethodGet, "/api/specreq-tasks/" + specReqTaskTestEvent, "", http.StatusOK},
		{"create", http.MethodPost, "/api/specreq-tasks", specReqCreateBody, http.StatusCreated},
		{"transition", http.MethodPost, "/api/specreq-tasks/" + specReqTaskTestEvent + "/transitions", specReqTransitionBody, http.StatusOK},
	}
	failures := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"400", service.ErrInvalidInput, 400, model.ErrCodeBadRequest},
		{"403", service.ErrForbidden, 403, model.ErrCodeForbidden},
		{"404", service.ErrNotFound, 404, model.ErrCodeNotFound},
		{"409", service.ErrConflict, 409, model.ErrCodeConflict},
		{"503", service.ErrCalendarEncryption, 503, model.ErrCodeInternalError},
	}
	for _, route := range routes {
		test.Run(route.name+"/success", func(test *testing.T) {
			fake := &specReqTaskFake{}
			response := specReqTaskRequest(fake, "owner", route.method, route.target, route.body)
			if response.Code != route.success || fake.userID != "owner" || response.Header().Get("Cache-Control") != "no-store" {
				test.Fatalf("status=%d user=%q cache=%q", response.Code, fake.userID, response.Header().Get("Cache-Control"))
			}
		})
		for _, failure := range failures {
			test.Run(route.name+"/"+failure.name, func(test *testing.T) {
				fake := &specReqTaskFake{err: failure.err}
				response := specReqTaskRequest(fake, "owner", route.method, route.target, route.body)
				var parsed model.ErrorResponse
				if json.Unmarshal(response.Body.Bytes(), &parsed) != nil || response.Code != failure.status ||
					parsed.Error.Code != failure.code || response.Header().Get("Cache-Control") != "no-store" {
					test.Fatalf("status=%d body=%s cache=%q", response.Code, response.Body.String(), response.Header().Get("Cache-Control"))
				}
			})
		}
	}
	test.Run("unauthenticated", func(test *testing.T) {
		fake := &specReqTaskFake{}
		response := specReqTaskRequest(fake, "", http.MethodGet, "/api/specreq-tasks", "")
		if response.Code != http.StatusUnauthorized || fake.calls != 0 || response.Header().Get("Cache-Control") != "no-store" {
			test.Fatalf("status=%d calls=%d", response.Code, fake.calls)
		}
	})
}

func TestSpecReqTaskHandler_ListQuery(test *testing.T) {
	fake := &specReqTaskFake{}
	if response := specReqTaskRequest(fake, "owner", http.MethodGet, "/api/specreq-tasks?limit=20&cursor=abc", ""); response.Code != 200 || fake.limit != 20 || fake.cursor != "abc" {
		test.Fatalf("query not forwarded: %d %d %q", response.Code, fake.limit, fake.cursor)
	}
	for _, target := range []string{
		"/api/specreq-tasks?limit=0", "/api/specreq-tasks?limit=21", "/api/specreq-tasks?limit=x",
		"/api/specreq-tasks?foo=1", "/api/specreq-tasks?cursor=" + strings.Repeat("a", 4097),
	} {
		fake := &specReqTaskFake{}
		if response := specReqTaskRequest(fake, "owner", http.MethodGet, target, ""); response.Code != 400 || fake.calls != 0 {
			test.Errorf("%s: status=%d calls=%d", target, response.Code, fake.calls)
		}
	}
}

func TestSpecReqTaskHandler_RejectsBadEventID(test *testing.T) {
	for _, id := range []string{"abc", strings.Repeat("AB", 32), strings.Repeat("ab", 33), strings.Repeat("zz", 32)} {
		for _, route := range [][3]string{
			{http.MethodGet, "/api/specreq-tasks/" + id, ""},
			{http.MethodPost, "/api/specreq-tasks/" + id + "/transitions", specReqTransitionBody},
		} {
			fake := &specReqTaskFake{}
			if response := specReqTaskRequest(fake, "owner", route[0], route[1], route[2]); response.Code != 400 || fake.calls != 0 {
				test.Errorf("%s: status=%d calls=%d", route[1], response.Code, fake.calls)
			}
		}
	}
}

func TestSpecReqTaskHandler_RejectsUnknownFields(test *testing.T) {
	for _, body := range []string{
		`{"sourceId":"s","externalId":"x","state":"pending","userId":"other"}`,
		`{"sourceId":"s","externalId":"x","state":"pending"} {}`,
		`not json`,
	} {
		fake := &specReqTaskFake{}
		if response := specReqTaskRequest(fake, "owner", http.MethodPost, "/api/specreq-tasks", body); response.Code != 400 || fake.calls != 0 {
			test.Errorf("create %q: status=%d calls=%d", body, response.Code, fake.calls)
		}
	}
	fake := &specReqTaskFake{}
	response := specReqTaskRequest(fake, "owner", http.MethodPost, "/api/specreq-tasks/"+specReqTaskTestEvent+"/transitions", `{"op":"close","version":1,"extra":true}`)
	if response.Code != 400 || fake.calls != 0 {
		test.Errorf("transition: status=%d calls=%d", response.Code, fake.calls)
	}
}

func TestSpecReqTaskHandler_RejectsLargeBody(test *testing.T) {
	body := `{"op":"close","version":1,"specReqTitle":"` + strings.Repeat("a", 4096) + `"}`
	fake := &specReqTaskFake{}
	response := specReqTaskRequest(fake, "owner", http.MethodPost, "/api/specreq-tasks/"+specReqTaskTestEvent+"/transitions", body)
	if response.Code != http.StatusRequestEntityTooLarge || fake.calls != 0 || response.Header().Get("Cache-Control") != "no-store" {
		test.Fatalf("status=%d calls=%d", response.Code, fake.calls)
	}
}
