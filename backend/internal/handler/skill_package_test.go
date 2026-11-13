package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

type fakeSkillPackageAPI struct {
	calls    []string
	complete error
}

func (f *fakeSkillPackageAPI) Get(_ context.Context, userID string) (*service.SkillPackageView, error) {
	f.calls = append(f.calls, "get:"+userID)
	return &service.SkillPackageView{Downloads: map[string]string{}}, nil
}

func (f *fakeSkillPackageAPI) StartUpload(_ context.Context, userID, scope string) (*model.SkillPackageUpload, error) {
	f.calls = append(f.calls, "start:"+scope+":"+userID)
	return &model.SkillPackageUpload{UploadID: "x"}, nil
}

func (f *fakeSkillPackageAPI) CompleteUpload(_ context.Context, userID, scope, uploadID string) (*service.SkillPackageResult, error) {
	f.calls = append(f.calls, "complete:"+scope+":"+userID+":"+uploadID)
	if f.complete != nil {
		return nil, f.complete
	}
	return &service.SkillPackageResult{Warnings: []string{}}, nil
}

func (f *fakeSkillPackageAPI) DeletePersonal(_ context.Context, userID string) (*service.SkillPackageResult, error) {
	f.calls = append(f.calls, "delete:"+userID)
	return &service.SkillPackageResult{Warnings: []string{}}, nil
}

func (f *fakeSkillPackageAPI) ListOrgVersions(context.Context) ([]model.SkillPackage, error) {
	f.calls = append(f.calls, "versions")
	return []model.SkillPackage{}, nil
}

func (f *fakeSkillPackageAPI) RollbackOrg(_ context.Context, userID, sha string) (*model.SkillPackage, error) {
	f.calls = append(f.calls, "rollback:"+sha)
	return &model.SkillPackage{}, nil
}

func skillPackageRequest(api *fakeSkillPackageAPI, method, path, body string, groups []string) *httptest.ResponseRecorder {
	// The same registration as cmd/api/main.go.
	h := NewSkillPackageHandler(api)
	router := chi.NewRouter()
	router.Get("/api/skill-packages", h.Get)
	router.Post("/api/skill-packages/personal/upload", h.StartPersonalUpload)
	router.Post("/api/skill-packages/personal/complete", h.CompletePersonalUpload)
	router.Delete("/api/skill-packages/personal", h.DeletePersonal)
	router.Group(func(r chi.Router) {
		r.Use(middleware.RequireAdmin)
		r.Post("/api/skill-packages/org/upload", h.StartOrgUpload)
		r.Post("/api/skill-packages/org/complete", h.CompleteOrgUpload)
		r.Get("/api/skill-packages/org/versions", h.ListOrgVersions)
		r.Post("/api/skill-packages/org/rollback", h.RollbackOrg)
	})
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	ctx := context.WithValue(request.Context(), middleware.UserIDKey, "alice")
	ctx = context.WithValue(ctx, middleware.UserGroupsKey, groups)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request.WithContext(ctx))
	return recorder
}

func TestSkillPackageOrgRoutesRequireAdmin(t *testing.T) {
	routes := [][2]string{{"POST", "/api/skill-packages/org/upload"}, {"POST", "/api/skill-packages/org/complete"},
		{"GET", "/api/skill-packages/org/versions"}, {"POST", "/api/skill-packages/org/rollback"}}
	for _, route := range routes {
		api := &fakeSkillPackageAPI{}
		if got := skillPackageRequest(api, route[0], route[1], `{}`, []string{"users"}).Code; got != http.StatusForbidden || len(api.calls) != 0 {
			t.Fatalf("%v: status %d calls %v", route, got, api.calls)
		}
	}
	api := &fakeSkillPackageAPI{}
	if got := skillPackageRequest(api, "GET", "/api/skill-packages/org/versions", "", []string{"admins"}).Code; got != http.StatusOK {
		t.Fatalf("admin status %d", got)
	}
}

func TestSkillPackagePersonalRoutesUseTheCaller(t *testing.T) {
	api := &fakeSkillPackageAPI{}
	skillPackageRequest(api, "GET", "/api/skill-packages", "", nil)
	skillPackageRequest(api, "POST", "/api/skill-packages/personal/upload", "", nil)
	skillPackageRequest(api, "POST", "/api/skill-packages/personal/complete", `{"uploadId":"abc"}`, nil)
	skillPackageRequest(api, "DELETE", "/api/skill-packages/personal", "", nil)
	want := "get:alice,start:personal:alice,complete:personal:alice:abc,delete:alice"
	if got := strings.Join(api.calls, ","); got != want {
		t.Fatalf("calls = %s", got)
	}
}

func TestSkillPackageCompleteErrors(t *testing.T) {
	cases := map[error]int{
		errors.Join(service.ErrInvalidPackage, &service.SkillPackageError{Rule: service.RuleUnsafePath, Detail: "../x"}): http.StatusBadRequest,
		service.ErrNotFound:           http.StatusNotFound,
		service.ErrInvalidInput:       http.StatusBadRequest,
		repository.ErrConditionFailed: http.StatusConflict,
	}
	for failure, status := range cases {
		recorder := skillPackageRequest(&fakeSkillPackageAPI{complete: failure}, "POST", "/api/skill-packages/personal/complete", `{"uploadId":"abc"}`, nil)
		if recorder.Code != status {
			t.Fatalf("%v: status %d", failure, recorder.Code)
		}
		if errors.Is(failure, service.ErrInvalidPackage) && !strings.Contains(recorder.Body.String(), `"INVALID_PACKAGE"`) ||
			errors.Is(failure, service.ErrInvalidPackage) && !strings.Contains(recorder.Body.String(), "unsafe-path: ../x") {
			t.Fatalf("body = %s", recorder.Body.String())
		}
	}
}

func TestSkillPackageBodyLimit(t *testing.T) {
	api := &fakeSkillPackageAPI{}
	body := `{"uploadId":"` + strings.Repeat("a", 5000) + `"}`
	if got := skillPackageRequest(api, "POST", "/api/skill-packages/personal/complete", body, nil).Code; got != http.StatusBadRequest || len(api.calls) != 0 {
		t.Fatalf("status %d calls %v", got, api.calls)
	}
	if got := skillPackageRequest(api, "POST", "/api/skill-packages/personal/complete", `{"uploadId":"a","x":1}`, nil).Code; got != http.StatusBadRequest {
		t.Fatalf("unknown field status %d", got)
	}
}
