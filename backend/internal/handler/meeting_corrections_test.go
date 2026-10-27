package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

// Transcript corrections are applied silently (L3): the stored records must
// never reach a meeting API response.
func TestCorrectionsNotInMeetingAPI(t *testing.T) {
	h, repo := newStubMeetingHandler()
	now := time.Now().UTC()
	meeting := &model.Meeting{
		MeetingID: "m-1", UserID: "user-1", Title: "Meeting", Status: model.StatusDone,
		TranscriptA: "we love K-culture", Date: now, CreatedAt: now, UpdatedAt: now,
		TranscriptCorrections: []model.TranscriptCorrection{{From: "gay culture", To: "K-culture", TermKey: "k1", Segment: 0}},
	}
	repo.addMeeting(meeting)

	raw, err := json.Marshal(meeting)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("transcriptCorrections")) || bytes.Contains(raw, []byte("TranscriptCorrections")) || bytes.Contains(raw, []byte("gay culture")) {
		t.Fatalf("meeting JSON carries corrections: %s", raw)
	}

	for name, call := range map[string]func(*httptest.ResponseRecorder){
		"get": func(rr *httptest.ResponseRecorder) {
			req := withChiParam(withUserCtx(httptest.NewRequest(http.MethodGet, "/api/meetings/m-1", nil), "user-1"), "meetingId", "m-1")
			h.GetMeeting(rr, req)
		},
		"update": func(rr *httptest.ResponseRecorder) {
			req := httptest.NewRequest(http.MethodPut, "/api/meetings/m-1", bytes.NewBufferString(`{"title":"Renamed"}`))
			req.Header.Set("Content-Type", "application/json")
			h.UpdateMeeting(rr, withChiParam(withUserCtx(req, "user-1"), "meetingId", "m-1"))
		},
	} {
		rr := httptest.NewRecorder()
		call(rr)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, rr.Code, rr.Body.String())
		}
		body := rr.Body.String()
		if strings.Contains(body, "ranscriptCorrections") || strings.Contains(body, "gay culture") {
			t.Fatalf("%s response carries corrections: %s", name, body)
		}
	}
}
