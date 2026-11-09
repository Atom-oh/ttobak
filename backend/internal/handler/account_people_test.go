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

type accountPeopleStub struct {
	err           error
	calls         int
	user, account string
	arg           string
	key           model.PeopleLinkKey
	edge          model.PeopleEdgeInput
	resolve       model.ResolveMentionInput
}

func (s *accountPeopleStub) hit(user, account, arg string) {
	s.calls++
	s.user, s.account, s.arg = user, account, arg
}
func (s *accountPeopleStub) ListPeople(_ context.Context, user, account, cursor string) (*model.AccountPeoplePage, error) {
	s.hit(user, account, cursor)
	return &model.AccountPeoplePage{People: []model.AccountPerson{}}, s.err
}
func (s *accountPeopleStub) ListMentionGroups(_ context.Context, user, account, cursor string) (*model.MentionGroupsPage, error) {
	s.hit(user, account, cursor)
	return &model.MentionGroupsPage{Groups: []model.MentionGroup{}}, s.err
}
func (s *accountPeopleStub) MeetingEdges(_ context.Context, user, account, meeting string) (*model.MeetingPeopleEdges, error) {
	s.hit(user, account, meeting)
	return &model.MeetingPeopleEdges{Edges: []model.PeopleLink{}, Labels: map[string]string{}}, s.err
}
func (s *accountPeopleStub) BindEdge(_ context.Context, user, account string, in model.PeopleEdgeInput) (*model.PeopleLink, error) {
	s.hit(user, account, "")
	s.edge = in
	return &model.PeopleLink{MeetingID: in.MeetingID}, s.err
}
func (s *accountPeopleStub) UnbindEdge(_ context.Context, user, account string, k model.PeopleLinkKey) error {
	s.hit(user, account, "")
	s.key = k
	return s.err
}
func (s *accountPeopleStub) ResolveMentionGroup(_ context.Context, user, account, hash string, in model.ResolveMentionInput) (*model.ResolveMentionResult, error) {
	s.hit(user, account, hash)
	s.resolve = in
	return &model.ResolveMentionResult{Written: 1}, s.err
}
func (s *accountPeopleStub) IgnoreMentionGroup(_ context.Context, user, account, hash string) error {
	s.hit(user, account, hash)
	return s.err
}
func (s *accountPeopleStub) RequestContactsResolve(_ context.Context, user, account string) error {
	s.hit(user, account, "")
	return s.err
}

func accountPeopleRouter(s *accountPeopleStub) http.Handler {
	h := NewAccountPeopleHandler(s)
	r := chi.NewRouter()
	r.Get("/accounts/{accountId}/people", h.List)
	r.Get("/accounts/{accountId}/people/mentions", h.Mentions)
	r.Post("/accounts/{accountId}/people/mentions/{mentionKey}/resolve", h.ResolveMention)
	r.Post("/accounts/{accountId}/people/mentions/{mentionKey}/ignore", h.IgnoreMention)
	r.Get("/accounts/{accountId}/people/edges", h.Edges)
	r.Post("/accounts/{accountId}/people/edges", h.Bind)
	r.Delete("/accounts/{accountId}/people/edges", h.Unbind)
	r.Post("/accounts/{accountId}/people/resolve", h.Resolve)
	return r
}

func servePeople(s *accountPeopleStub, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserIDKey, "u1"))
	rec := httptest.NewRecorder()
	accountPeopleRouter(s).ServeHTTP(rec, req)
	return rec
}

const peopleHandlerKey = "0123456789abcdef0123456789abcdef"

func TestAccountPeopleHandler_RoutesAndErrors(t *testing.T) {
	for _, tc := range []struct {
		method, target, body string
		ok                   int
	}{
		{"GET", "/accounts/a1/people?cursor=c", "", 200},
		{"GET", "/accounts/a1/people/mentions", "", 200},
		{"POST", "/accounts/a1/people/mentions/" + peopleHandlerKey + "/resolve", `{"kind":"contact","id":"c1"}`, 200},
		{"POST", "/accounts/a1/people/mentions/" + peopleHandlerKey + "/ignore", "", 204},
		{"GET", "/accounts/a1/people/edges?meetingId=o:m", "", 200},
		{"POST", "/accounts/a1/people/edges", `{"meetingId":"o:m","toKind":"contact","toId":"c1","role":"speaker","sourceRef":"spk_0"}`, 201},
		{"DELETE", "/accounts/a1/people/edges?meetingId=o:m&toKind=contact&toId=c1&role=speaker", "", 204},
		{"POST", "/accounts/a1/people/resolve", "", 202},
	} {
		s := &accountPeopleStub{}
		rec := servePeople(s, tc.method, tc.target, tc.body)
		if rec.Code != tc.ok || s.calls != 1 || s.user != "u1" || s.account != "a1" || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s: %d calls=%d %s", tc.method, tc.target, rec.Code, s.calls, rec.Body)
		}
		for err, status := range map[error]int{service.ErrForbidden: 403, service.ErrNotFound: 404, service.ErrConflict: 409,
			service.ErrInvalidInput: 400, service.ErrContactsUnavailable: 503} {
			s := &accountPeopleStub{err: err}
			rec := servePeople(s, tc.method, tc.target, tc.body)
			var body struct {
				Error struct{ Code string } `json:"error"`
			}
			if rec.Code != status || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Error.Code == "" || rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%s %s err %v: %d %s", tc.method, tc.target, err, rec.Code, rec.Body)
			}
		}
	}
}

func TestAccountPeopleHandler_RejectsBadMentionKey(t *testing.T) {
	for _, key := range []string{"ABCDEF0123456789ABCDEF0123456789", "0123", peopleHandlerKey + "0", "zz23456789abcdef0123456789abcdef"} {
		for _, action := range []string{"resolve", "ignore"} {
			s := &accountPeopleStub{}
			rec := servePeople(s, "POST", "/accounts/a1/people/mentions/"+key+"/"+action, `{"kind":"contact","id":"c1"}`)
			if rec.Code != 400 || s.calls != 0 {
				t.Fatalf("%s %s: %d calls=%d", key, action, rec.Code, s.calls)
			}
		}
	}
	s := &accountPeopleStub{}
	if rec := servePeople(s, "POST", "/accounts/a1/people/mentions/"+peopleHandlerKey+"/resolve", `{"kind":"contact","id":"c1","x":1}`); rec.Code != 400 || s.calls != 0 {
		t.Fatalf("unknown field: %d", rec.Code)
	}
	if rec := servePeople(s, "POST", "/accounts/a1/people/edges", strings.Repeat(" ", accountPeopleMaxBody+1)+"{}"); rec.Code != 400 || s.calls != 0 {
		t.Fatalf("oversized: %d", rec.Code)
	}
}

func TestAccountPeopleHandler_UnbindQueryValidation(t *testing.T) {
	for _, q := range []string{
		"meetingId=o:m&toKind=contact&toId=c1",
		"meetingId=o:m&toKind=contact&toId=c1&role=speaker&role=speaker",
		"meetingId=o:m&toKind=contact&toId=c1&role=speaker&extra=1",
	} {
		s := &accountPeopleStub{}
		if rec := servePeople(s, "DELETE", "/accounts/a1/people/edges?"+q, ""); rec.Code != 400 || s.calls != 0 {
			t.Fatalf("%s: %d", q, rec.Code)
		}
	}
	s := &accountPeopleStub{}
	servePeople(s, "DELETE", "/accounts/a1/people/edges?meetingId=o:m&toKind=contact&toId=c1&role=speaker", "")
	if s.key != (model.PeopleLinkKey{MeetingID: "o:m", ToKind: "contact", ToID: "c1", Role: "speaker"}) {
		t.Fatalf("key %+v", s.key)
	}
}
