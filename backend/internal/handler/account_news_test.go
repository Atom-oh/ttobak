package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type accountNewsStub struct {
	err                     error
	calls                   int
	user, account, sourceID string
	req                     *model.CreateAccountNewsSourceRequest
	update                  *model.UpdateAccountNewsSourceRequest
	cursor                  string
	limit                   int
}

func (s *accountNewsStub) Create(_ context.Context, user, account string, req *model.CreateAccountNewsSourceRequest) (*model.CrawlerSource, error) {
	s.calls++
	s.user, s.account, s.req = user, account, req
	if s.err != nil {
		return nil, s.err
	}
	return &model.CrawlerSource{SourceID: "acct_a_00000001", AccountID: account}, nil
}
func (s *accountNewsStub) List(_ context.Context, user, account string) ([]model.CrawlerSource, error) {
	s.calls++
	s.user, s.account = user, account
	return nil, s.err
}
func (s *accountNewsStub) Delete(_ context.Context, user, account, sourceID string) error {
	s.calls++
	s.user, s.account, s.sourceID = user, account, sourceID
	return s.err
}

func (s *accountNewsStub) Update(_ context.Context, user, account, sourceID string, req *model.UpdateAccountNewsSourceRequest) (*model.NewsSourceItem, error) {
	s.calls++
	s.user, s.account, s.sourceID, s.update = user, account, sourceID, req
	if s.err != nil {
		return nil, s.err
	}
	return &model.NewsSourceItem{SourceID: sourceID, Kind: model.NewsSourceKindAccount, AccountID: account, CanEdit: true}, nil
}
func (s *accountNewsStub) ListAll(_ context.Context, user, cursor string, limit int) (*model.NewsSourcesResponse, error) {
	s.calls++
	s.user, s.cursor, s.limit = user, cursor, limit
	if s.err != nil {
		return nil, s.err
	}
	return &model.NewsSourcesResponse{Sources: []model.NewsSourceItem{{SourceID: "s", Kind: model.NewsSourceKindCommon, CanEdit: true}}}, nil
}

func (s *accountNewsStub) CollectNow(_ context.Context, user, account, sourceID string) (*model.NewsCollectResponse, error) {
	s.calls++
	s.user, s.account, s.sourceID = user, account, sourceID
	if s.err != nil {
		return nil, s.err
	}
	return &model.NewsCollectResponse{RequestID: "r1", RequestedAt: "2026-11-12T01:00:00Z", NextAllowedAt: "2026-11-12T02:00:00Z"}, nil
}

func accountNewsRouter(s *accountNewsStub) http.Handler {
	h := NewAccountNewsHandler(s)
	r := chi.NewRouter()
	r.Get("/accounts/{accountId}/news-sources", h.List)
	r.Post("/accounts/{accountId}/news-sources", h.Create)
	r.Put("/accounts/{accountId}/news-sources/{sourceId}", h.Update)
	r.Delete("/accounts/{accountId}/news-sources/{sourceId}", h.Delete)
	r.Get("/news-sources", h.ListAll)
	r.Post("/accounts/{accountId}/news-sources/{sourceId}/collect", h.Collect)
	return r
}

func TestAccountNewsHTTPRoutesAndIdentity(t *testing.T) {
	s := &accountNewsStub{}
	h := accountNewsRouter(s)
	w := contactRequest(h, "GET", "/accounts/a/news-sources", "")
	var page model.AccountNewsSourcesResponse
	if w.Code != 200 || s.user != "viewer" || s.account != "a" || w.Header().Get("Cache-Control") != "no-store" ||
		json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Sources == nil {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	if w := contactRequest(h, "POST", "/accounts/a/news-sources", `{"sourceName":"n","newsQueries":["q"],"copyFromSourceId":"src"}`); w.Code != 201 || s.req.SourceName != "n" || s.req.CopyFromSourceID != "src" || s.user != "viewer" {
		t.Fatalf("create: %d", w.Code)
	}
	if w := contactRequest(h, "DELETE", "/accounts/a/news-sources/acct_a_00000001", ""); w.Code != 204 || s.sourceID != "acct_a_00000001" {
		t.Fatalf("delete: %d", w.Code)
	}
	s.sourceID = ""
	w = contactRequest(h, "PUT", "/accounts/a/news-sources/acct_a_00000002", `{"newsQueries":["q"]}`)
	var item model.NewsSourceItem
	if w.Code != 200 || s.sourceID != "acct_a_00000002" || s.account != "a" || s.update.SourceName != nil || s.update.Aliases != nil ||
		s.update.NewsQueries == nil || (*s.update.NewsQueries)[0] != "q" || json.Unmarshal(w.Body.Bytes(), &item) != nil || item.Kind != "account" {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	w = contactRequest(h, "GET", "/news-sources", "")
	var all model.NewsSourcesResponse
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &all) != nil ||
		len(all.Sources) != 1 || !all.Sources[0].CanEdit || strings.Contains(w.Body.String(), "truncated") {
		t.Fatalf("list all: %d %s", w.Code, w.Body.String())
	}
	if w := contactRequest(h, "GET", "/news-sources?limit=7&cursor=abc", ""); w.Code != 200 || s.cursor != "abc" || s.limit != 7 {
		t.Fatalf("paging params: %d %q %d", w.Code, s.cursor, s.limit)
	}
}

func TestAccountNewsHTTPRejectsMalformedInputBeforeTheService(t *testing.T) {
	s := &accountNewsStub{}
	h := accountNewsRouter(s)
	for _, tc := range [][3]string{
		{"POST", "/accounts/a/news-sources", `{"sourceName":"n","accountId":"other"}`},
		{"POST", "/accounts/a/news-sources", `{"sourceName":"n"}{"sourceName":"m"}`},
		{"POST", "/accounts/a/news-sources", strings.Repeat("x", 65537)},
		{"GET", "/accounts/a/news-sources?x=1", ""},
		{"DELETE", "/accounts/a/news-sources/s?x=1", ""},
		{"PUT", "/accounts/a/news-sources/s", `{"sourceName":"n","accountId":"other"}`},
		{"PUT", "/accounts/a/news-sources/s", `{"customUrls":["https://x"]}`},
		{"PUT", "/accounts/a/news-sources/s?x=1", `{"sourceName":"n"}`},
		{"PUT", "/accounts/a/news-sources/s", `{"sourceName":"n"}{}`},
		{"GET", "/news-sources?x=1", ""},
		{"GET", "/news-sources?limit=0", ""},
		{"GET", "/news-sources?limit=ten", ""},
		{"GET", "/news-sources?limit=5&limit=6", ""},
		{"GET", "/news-sources?cursor=a&cursor=b", ""},
	} {
		if w := contactRequest(h, tc[0], tc[1], tc[2]); w.Code != 400 {
			t.Fatalf("%v: %d", tc, w.Code)
		}
	}
	if s.calls != 0 {
		t.Fatalf("service reached: %d", s.calls)
	}
}

func TestAccountNews_NonMemberForbidden(t *testing.T) {
	s := &accountNewsStub{err: service.ErrForbidden}
	h := accountNewsRouter(s)
	for _, tc := range [][3]string{
		{"GET", "/accounts/a/news-sources", ""},
		{"POST", "/accounts/a/news-sources", `{"sourceName":"n"}`},
		{"DELETE", "/accounts/a/news-sources/acct_a_00000001", ""},
		{"PUT", "/accounts/a/news-sources/acct_a_00000001", `{"sourceName":"n"}`},
	} {
		w := contactRequest(h, tc[0], tc[1], tc[2])
		var body struct {
			Error struct{ Code string } `json:"error"`
		}
		if w.Code != 403 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Error.Code != model.ErrCodeForbidden {
			t.Fatalf("%v: %d %s", tc, w.Code, w.Body.String())
		}
	}
	s.err = service.ErrNotFound
	if w := contactRequest(h, "DELETE", "/accounts/a/news-sources/acct_a_00000001", ""); w.Code != 404 {
		t.Fatalf("not found: %d", w.Code)
	}
	if w := contactRequest(h, "PUT", "/accounts/a/news-sources/acct_a_00000001", `{"sourceName":"n"}`); w.Code != 404 {
		t.Fatalf("update not found: %d", w.Code)
	}
}

func TestAccountNewsCollectHTTP(t *testing.T) {
	const path = "/accounts/a/news-sources/acct_a_0000abcd/collect"
	limited := &service.NewsCollectLimitError{NextAllowedAt: time.Date(2026, 11, 12, 2, 0, 0, 0, time.UTC)}
	window := &service.NewsCollectWindowError{NextAllowedAt: time.Date(2026, 11, 12, 19, 25, 0, 0, time.UTC)}
	for name, tc := range map[string]struct {
		path, body string
		err        error
		status     int
		code       string
		called     bool
	}{
		"accepted":     {path, "", nil, 202, "", true},
		"query":        {path + "?force=1", "", nil, 400, "BAD_REQUEST", false},
		"body":         {path, "{}", nil, 400, "BAD_REQUEST", false},
		"huge body":    {path, strings.Repeat("x", 2048), nil, 400, "BAD_REQUEST", false},
		"limited":      {path, "", limited, 429, "RATE_LIMITED", true},
		"wrapped":      {path, "", fmt.Errorf("x: %w", limited), 429, "RATE_LIMITED", true},
		"not found":    {path, "", service.ErrNotFound, 404, "NOT_FOUND", true},
		"non-member":   {path, "", service.ErrForbidden, 403, "FORBIDDEN", true},
		"invalid":      {path, "", service.ErrInvalidInput, 400, "BAD_REQUEST", true},
		"merging":      {path, "", service.ErrAccountMerging, 409, model.ErrCodeAccountMerging, true},
		"unavailable":  {path, "", service.ErrNewsCollectUnavailable, 503, "INTERNAL_ERROR", true},
		"daily window": {path, "", window, 409, model.ErrCodeCollectDailyWindow, true},
		"busy":         {path, "", fmt.Errorf("x: %w", service.ErrNewsCollectBusy), 409, model.ErrCodeCollectBusy, true},
		"internal":     {path, "", fmt.Errorf("boom"), 500, "INTERNAL_ERROR", true},
	} {
		t.Run(name, func(t *testing.T) {
			s := &accountNewsStub{err: tc.err}
			w := contactRequest(accountNewsRouter(s), "POST", tc.path, tc.body)
			if w.Code != tc.status || (s.calls == 1) != tc.called || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Retry-After") != "" {
				t.Fatalf("%d calls=%d %s", w.Code, s.calls, w.Body.String())
			}
			var body struct {
				Error         model.APIError `json:"error"`
				RequestID     string         `json:"requestId"`
				NextAllowedAt string         `json:"nextAllowedAt"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error.Code != tc.code {
				t.Fatalf("body: %s %v", w.Body.String(), err)
			}
			switch tc.status {
			case 202:
				if body.RequestID != "r1" || body.NextAllowedAt != "2026-11-12T02:00:00Z" || s.user != "viewer" || s.account != "a" || s.sourceID != "acct_a_0000abcd" {
					t.Fatalf("accepted: %+v %+v", body, s)
				}
			case 429:
				if body.NextAllowedAt != "2026-11-12T02:00:00Z" {
					t.Fatalf("limited: %s", w.Body.String())
				}
			default:
				if tc.code == model.ErrCodeCollectDailyWindow {
					if body.NextAllowedAt != "2026-11-12T19:25:00Z" {
						t.Fatalf("daily window: %s", w.Body.String())
					}
				} else if body.NextAllowedAt != "" {
					t.Fatalf("only 429 and the daily window carry nextAllowedAt: %s", w.Body.String())
				}
			}
		})
	}
}
