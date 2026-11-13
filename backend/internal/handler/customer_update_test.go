package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type fakeCustomerUpdateService struct {
	err        error
	limit      int
	cursor     string
	accountID  string
	enabled    bool
	gotEnabled bool
}

func (f *fakeCustomerUpdateService) AddSource(context.Context, string, *model.AddUpdateSourceRequest) (*model.UpdateSource, error) {
	return &model.UpdateSource{SourceID: "s1"}, f.err
}
func (f *fakeCustomerUpdateService) ListSources(context.Context, string) ([]model.UpdateSource, error) {
	return []model.UpdateSource{{SourceID: "s1"}}, f.err
}
func (f *fakeCustomerUpdateService) UpdateSource(context.Context, string, string, *model.PatchUpdateSourceRequest) error {
	return f.err
}
func (f *fakeCustomerUpdateService) DeleteSource(context.Context, string, string) error { return f.err }
func (f *fakeCustomerUpdateService) UploadLinks(context.Context, string, *model.UploadUpdateLinksRequest) (*model.UploadUpdateLinksResponse, error) {
	return &model.UploadUpdateLinksResponse{Accepted: 2}, f.err
}
func (f *fakeCustomerUpdateService) ListDrafts(_ context.Context, _, accountID string, limit int, cursor string) (*model.UpdateDraftsPage, error) {
	f.accountID, f.limit, f.cursor = accountID, limit, cursor
	return &model.UpdateDraftsPage{Drafts: []model.UpdateDraft{{URLHash: "h"}}}, f.err
}
func (f *fakeCustomerUpdateService) PatchDraft(context.Context, string, string, string, *model.PatchUpdateDraftRequest) (*model.UpdateDraft, error) {
	return &model.UpdateDraft{Revision: 3}, f.err
}
func (f *fakeCustomerUpdateService) GetAccountSetting(context.Context, string, string) (bool, error) {
	return true, f.err
}
func (f *fakeCustomerUpdateService) PutAccountSetting(_ context.Context, _, _ string, enabled bool) error {
	f.enabled, f.gotEnabled = enabled, true
	return f.err
}

func customerUpdateRouter(f *fakeCustomerUpdateService, user string) http.Handler {
	h := NewCustomerUpdateHandler(f)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if user != "" {
				req = req.WithContext(context.WithValue(req.Context(), middleware.UserIDKey, user))
			}
			next.ServeHTTP(w, req)
		})
	})
	r.Get("/api/customer-updates/sources", h.ListSources)
	r.Post("/api/customer-updates/sources", h.AddSource)
	r.Patch("/api/customer-updates/sources/{sourceId}", h.PatchSource)
	r.Delete("/api/customer-updates/sources/{sourceId}", h.DeleteSource)
	r.Post("/api/customer-updates/links", h.UploadLinks)
	r.Get("/api/customer-updates/drafts", h.ListDrafts)
	r.Get("/api/accounts/{accountId}/customer-update-drafts", h.ListAccountDrafts)
	r.Patch("/api/customer-updates/drafts/{accountId}/{urlHash}", h.PatchDraft)
	r.Get("/api/accounts/{accountId}/customer-update-setting", h.GetSetting)
	r.Put("/api/accounts/{accountId}/customer-update-setting", h.PutSetting)
	return r
}

func doCU(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var cuHash = strings.Repeat("a", 64)

func TestCustomerUpdateMissingUser(t *testing.T) {
	h := customerUpdateRouter(&fakeCustomerUpdateService{}, "")
	for _, c := range [][2]string{
		{"GET", "/api/customer-updates/sources"},
		{"POST", "/api/customer-updates/sources"},
		{"PATCH", "/api/customer-updates/sources/s1"},
		{"DELETE", "/api/customer-updates/sources/s1"},
		{"POST", "/api/customer-updates/links"},
		{"GET", "/api/customer-updates/drafts"},
		{"GET", "/api/accounts/a1/customer-update-drafts"},
		{"PATCH", "/api/customer-updates/drafts/a1/" + cuHash},
		{"GET", "/api/accounts/a1/customer-update-setting"},
		{"PUT", "/api/accounts/a1/customer-update-setting"},
	} {
		if rec := doCU(h, c[0], c[1], `{}`); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: got %d want 401", c[0], c[1], rec.Code)
		}
	}
}

func TestCustomerUpdateBodyLimits(t *testing.T) {
	h := customerUpdateRouter(&fakeCustomerUpdateService{}, "u1")
	pad := func(n int) string { return `{"label":"` + strings.Repeat("x", n) + `"}` }
	cases := []struct{ method, path string }{
		{"POST", "/api/customer-updates/sources"},
		{"PATCH", "/api/customer-updates/sources/s1"},
		{"POST", "/api/customer-updates/links"},
		{"PATCH", "/api/customer-updates/drafts/a1/" + cuHash},
		{"PUT", "/api/accounts/a1/customer-update-setting"},
	}
	for _, c := range cases {
		if rec := doCU(h, c.method, c.path, pad(600*1024)); rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s oversized: got %d want 400", c.method, c.path, rec.Code)
		}
	}
	// Just over the 8 KiB source limit is rejected, under it is accepted.
	if rec := doCU(h, "POST", "/api/customer-updates/sources", pad(9000)); rec.Code != http.StatusBadRequest {
		t.Errorf("9000-byte source body: got %d want 400", rec.Code)
	}
	if rec := doCU(h, "POST", "/api/customer-updates/sources", pad(7000)); rec.Code != http.StatusCreated {
		t.Errorf("7000-byte source body: got %d want 201", rec.Code)
	}
	// Unknown fields are rejected.
	if rec := doCU(h, "POST", "/api/customer-updates/sources", `{"bogus":1}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field: got %d", rec.Code)
	}
}

func TestCustomerUpdateErrorMapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
		code string
	}{
		{service.ErrForbidden, 403, model.ErrCodeForbidden},
		{service.ErrNotFound, 404, model.ErrCodeNotFound},
		{service.ErrConflict, 409, model.ErrCodeConflict},
		{service.ErrInvalidInput, 400, model.ErrCodeBadRequest},
		{context.DeadlineExceeded, 500, model.ErrCodeInternalError},
	}
	for _, c := range cases {
		h := customerUpdateRouter(&fakeCustomerUpdateService{err: c.err}, "u1")
		reqs := [][3]string{
			{"GET", "/api/customer-updates/sources", ""},
			{"POST", "/api/customer-updates/sources", `{}`},
			{"PATCH", "/api/customer-updates/sources/s1", `{}`},
			{"DELETE", "/api/customer-updates/sources/s1", ""},
			{"POST", "/api/customer-updates/links", `{}`},
			{"GET", "/api/customer-updates/drafts", ""},
			{"GET", "/api/accounts/a1/customer-update-drafts", ""},
			{"PATCH", "/api/customer-updates/drafts/a1/" + cuHash, `{}`},
			{"GET", "/api/accounts/a1/customer-update-setting", ""},
			{"PUT", "/api/accounts/a1/customer-update-setting", `{"enabled":true}`},
		}
		for _, q := range reqs {
			rec := doCU(h, q[0], q[1], q[2])
			if rec.Code != c.want || !strings.Contains(rec.Body.String(), `"`+c.code+`"`) {
				t.Errorf("%v %s %s: got %d %s", c.err, q[0], q[1], rec.Code, rec.Body.String())
			}
			if c.want == 500 && strings.Contains(rec.Body.String(), "deadline") {
				t.Errorf("internal error text leaked: %s", rec.Body.String())
			}
		}
	}
}

func TestCustomerUpdateBadURLHash(t *testing.T) {
	h := customerUpdateRouter(&fakeCustomerUpdateService{}, "u1")
	for _, bad := range []string{"abc", strings.Repeat("A", 64), strings.Repeat("g", 64), cuHash + "0"} {
		if rec := doCU(h, "PATCH", "/api/customer-updates/drafts/a1/"+bad, `{}`); rec.Code != http.StatusBadRequest {
			t.Errorf("hash %q: got %d want 400", bad, rec.Code)
		}
	}
}

func TestCustomerUpdateSuccessBodies(t *testing.T) {
	f := &fakeCustomerUpdateService{}
	h := customerUpdateRouter(f, "u1")
	check := func(rec *httptest.ResponseRecorder, code int, sub string) {
		t.Helper()
		if rec.Code != code || !strings.Contains(rec.Body.String(), sub) {
			t.Errorf("got %d %s want %d containing %s", rec.Code, rec.Body.String(), code, sub)
		}
	}
	check(doCU(h, "GET", "/api/customer-updates/sources", ""), 200, `"sourceId":"s1"`)
	check(doCU(h, "POST", "/api/customer-updates/links", `{"sourceId":"s1","links":[]}`), 200, `"accepted":2`)
	check(doCU(h, "PATCH", "/api/customer-updates/sources/s1", `{"label":"x"}`), 200, `"status":"updated"`)
	if rec := doCU(h, "DELETE", "/api/customer-updates/sources/s1", ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete: got %d", rec.Code)
	}
	check(doCU(h, "PATCH", "/api/customer-updates/drafts/a1/"+cuHash, `{"action":"edit"}`), 200, `"revision":3`)
	check(doCU(h, "GET", "/api/accounts/a1/customer-update-setting", ""), 200, `"enabled":true`)
	check(doCU(h, "PUT", "/api/accounts/a1/customer-update-setting", `{"enabled":true}`), 200, `"enabled":true`)
	if !f.gotEnabled || !f.enabled {
		t.Errorf("setting not forwarded")
	}
	if rec := doCU(h, "PUT", "/api/accounts/a1/customer-update-setting", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("missing enabled: got %d want 400", rec.Code)
	}
}

func TestCustomerUpdateListLimits(t *testing.T) {
	cases := []struct {
		q    string
		want int
	}{
		{"", 30}, {"?limit=10", 10}, {"?limit=500", 50}, {"?limit=0", 30}, {"?limit=-3", 30}, {"?limit=abc", 30},
	}
	for _, c := range cases {
		f := &fakeCustomerUpdateService{}
		h := customerUpdateRouter(f, "u1")
		if rec := doCU(h, "GET", "/api/customer-updates/drafts"+c.q, ""); rec.Code != 200 {
			t.Fatalf("%s: %d", c.q, rec.Code)
		}
		if f.limit != c.want || f.accountID != "" {
			t.Errorf("%q: limit=%d account=%q want %d", c.q, f.limit, f.accountID, c.want)
		}
	}
	f := &fakeCustomerUpdateService{}
	h := customerUpdateRouter(f, "u1")
	doCU(h, "GET", "/api/accounts/a9/customer-update-drafts?limit=5&cursor=zz", "")
	if f.accountID != "a9" || f.limit != 5 || f.cursor != "zz" {
		t.Errorf("account list args: %+v", f)
	}
}
