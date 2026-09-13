package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
)

func TestNotesRevisionRequiresExpectedText(t *testing.T) {
	for _, body := range []string{
		`{"notes":"A","expectedNotesRevision":""}`,
		`{"expectedNotes":"A","expectedNotesRevision":""}`,
		`{"notes":"A","expectedNotes":"A","expectedNotesRevision":"","title":"rename"}`,
		`{"notes":"A","expectedNotes":"A","expectedNotesRevision":"` + strings.Repeat("v", 129) + `"}`,
	} {
		h, repo := newStubMeetingHandler()
		repo.addMeeting(&model.Meeting{MeetingID: "m1", UserID: "owner", Notes: "A"})
		w := httptest.NewRecorder()
		h.UpdateMeeting(w, withUserCtx(withChiParam(httptest.NewRequest("PUT", "/api/meetings/m1", strings.NewReader(body)), "meetingId", "m1"), "owner"))
		if w.Code != 400 {
			t.Errorf("incomplete/mixed revision comparison accepted: body=%s status=%d", body, w.Code)
		}
	}
}

func TestNotesMutationReturnsFreshRevision(t *testing.T) {
	h, repo := newStubMeetingHandler()
	repo.addMeeting(&model.Meeting{MeetingID: "m1", UserID: "owner", Notes: "A"})
	previous := ""
	for _, fields := range []map[string]string{
		{"notes": "A", "expectedNotes": "A", "expectedNotesRevision": ""},
		{"notes": "A", "expectedNotes": "A"},
		{"notes": "A", "notesRevision": "client-must-not-assign-this"},
	} {
		raw, _ := json.Marshal(fields)
		w := httptest.NewRecorder()
		h.UpdateMeeting(w, withUserCtx(withChiParam(httptest.NewRequest("PUT", "/api/meetings/m1", strings.NewReader(string(raw))), "meetingId", "m1"), "owner"))
		if w.Code != 200 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var result struct {
			NotesRevision string `json:"notesRevision"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if _, err := uuid.Parse(result.NotesRevision); err != nil || result.NotesRevision == previous {
			t.Fatalf("same-value/legacy notes mutation did not issue a fresh UUID: %+v", result)
		}
		previous = result.NotesRevision
		detail := httptest.NewRecorder()
		h.GetMeeting(detail, withUserCtx(withChiParam(httptest.NewRequest("GET", "/api/meetings/m1", nil), "meetingId", "m1"), "owner"))
		var detailBody map[string]any
		if err := json.Unmarshal(detail.Body.Bytes(), &detailBody); err != nil || detail.Code != 200 ||
			detailBody["notesRevision"] != previous || detailBody["supportsNotesComparison"] != true {
			t.Fatalf("detail lost the persisted revision/capability: status=%d body=%s", detail.Code, detail.Body.String())
		}
	}
}

func TestNotesReadingReturnsEmptyLegacyRevision(t *testing.T) {
	f := newReadingFixture(t)
	result := readingResult(t, f.call(t, map[string][]string{"section": {"notes"}}))
	if result["notesRevision"] != "" || len(f.s3Keys) != 0 {
		t.Fatalf("legacy metadata must return an explicit empty revision without S3: %v", result["notesRevision"])
	}
}

func TestNotesReadingRevisionFencesSameTextContinuation(t *testing.T) {
	f := newReadingFixture(t)
	f.meeting.Notes = strings.Repeat("A", 9000)
	if err := json.Unmarshal([]byte(`{"NotesRevision":"00000000-0000-4000-8000-000000000001"}`), f.meeting); err != nil {
		t.Fatal(err)
	}
	first := readingResult(t, f.call(t, map[string][]string{"section": {"notes"}, "pageSize": {"1000"}}))
	if first["notesRevision"] != "00000000-0000-4000-8000-000000000001" {
		t.Fatalf("notes metadata omitted revision: %v", first["notesRevision"])
	}
	cursor := first["page"].(map[string]any)["nextCursor"].(string)
	if err := json.Unmarshal([]byte(`{"NotesRevision":"00000000-0000-4000-8000-000000000002"}`), f.meeting); err != nil {
		t.Fatal(err)
	}
	res := f.call(t, map[string][]string{"section": {"notes"}, "pageSize": {"1000"}, "cursor": {cursor}})
	if res.StatusCode != 409 || !strings.Contains(res.Body, "STALE_CURSOR") {
		t.Fatalf("same-text fence left old continuation valid: status=%d body=%s", res.StatusCode, res.Body)
	}
	if len(f.s3Keys) != 0 {
		t.Fatalf("notes version lookup hydrated transcripts: %v", f.s3Keys)
	}
}
