package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/service"
)

type englishStub struct {
	resummaryHandlerStub
	englishGets, englishRequests int
}

func (s *englishStub) GetEnglish(_ context.Context, u, m string) (*service.EnglishNoteResponse, error) {
	s.englishGets++
	s.user, s.meeting = u, m
	return &service.EnglishNoteResponse{ResummaryResponse: service.ResummaryResponse{Status: "succeeded"}, Content: "note", Stale: true}, s.err
}
func (s *englishStub) RequestEnglish(_ context.Context, u, m string) (*service.ResummaryResponse, error) {
	s.englishRequests++
	s.user, s.meeting = u, m
	return &service.ResummaryResponse{Status: "queued", RunID: "run"}, s.err
}

func TestEnglishNoteGetReturnsNote(t *testing.T) {
	s := &englishStub{}
	h := &ResummaryHandler{service: s}
	w := httptest.NewRecorder()
	h.GetEnglish(w, withChiParam(withUserCtx(httptest.NewRequest("GET", "/", nil), "reader"), "meetingId", "m"))
	if w.Code != 200 || s.englishGets != 1 || s.user != "reader" || s.meeting != "m" ||
		!strings.Contains(w.Body.String(), `"content":"note"`) || !strings.Contains(w.Body.String(), `"stale":true`) ||
		w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	h.GetEnglish(w, withChiParam(withUserCtx(httptest.NewRequest("GET", "/?x=1", nil), "reader"), "meetingId", "m"))
	if w.Code != 400 || s.englishGets != 1 {
		t.Fatalf("query accepted: %d", w.Code)
	}
}

func TestEnglishNotePostQueuesAndRejectsCallerBodies(t *testing.T) {
	for _, body := range []string{"", `{}`, `{"content":"x"}`, `{"sharingSafe":true}`, `{} {}`, strings.Repeat("x", 2048)} {
		s := &englishStub{}
		h := &ResummaryHandler{service: s}
		w := httptest.NewRecorder()
		h.RequestEnglish(w, withChiParam(withUserCtx(httptest.NewRequest("POST", "/", strings.NewReader(body)), "editor"), "meetingId", "m"))
		if body == "" || body == `{}` {
			if w.Code != 202 || s.englishRequests != 1 || s.user != "editor" || s.meeting != "m" {
				t.Fatalf("%d %+v", w.Code, s)
			}
		} else if w.Code != 400 || s.englishRequests != 0 {
			t.Fatalf("accepted %q: %d", body, w.Code)
		}
	}
}

func TestEnglishNoteErrorMapping(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
	}{{service.ErrForbidden, 403}, {service.ErrNotFound, 404}, {service.ErrResummaryBusy, 409}, {service.ErrSummaryReadRestricted, 409}, {service.ErrInvalidInput, 400}, {errors.New("private text"), 500}} {
		for _, post := range []bool{false, true} {
			h := &ResummaryHandler{service: &englishStub{resummaryHandlerStub: resummaryHandlerStub{}}}
			h.service.(*englishStub).err = test.err
			w := httptest.NewRecorder()
			r := withChiParam(withUserCtx(httptest.NewRequest("GET", "/", nil), "u"), "meetingId", "m")
			if post {
				h.RequestEnglish(w, r)
			} else {
				h.GetEnglish(w, r)
			}
			if w.Code != test.status || strings.Contains(w.Body.String(), "private text") {
				t.Fatalf("post=%v %v: %d %s", post, test.err, w.Code, w.Body)
			}
		}
	}
}

func TestEnglishNoteRoutesAreAuthenticated(t *testing.T) {
	src, err := os.ReadFile("../../cmd/api/main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	auth := strings.Index(text, "r.Use(middleware.AuthWithConnectorGrants")
	for _, route := range []string{`r.Get("/api/meetings/{meetingId}/notes/en", resummaryHandler.GetEnglish)`, `r.Post("/api/meetings/{meetingId}/notes/en", resummaryHandler.RequestEnglish)`} {
		at := strings.Index(text, route)
		if at < 0 || auth < 0 || at < auth {
			t.Fatalf("route missing or outside authenticated group: %s", route)
		}
	}
}
