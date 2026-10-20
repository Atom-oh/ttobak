package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestUpdateSpeakersSharedReaderAndInputBounds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		shared bool
		status int
	}{
		{"read share", `{"speakerMap":{"spk_0":"김팀장"}}`, true, http.StatusOK},
		{"no share", `{"speakerMap":{"spk_0":"김팀장"}}`, false, http.StatusNotFound},
		{"unknown label", `{"speakerMap":{"summary":"changed"}}`, true, http.StatusBadRequest},
		{"empty map", `{"speakerMap":{}}`, true, http.StatusBadRequest},
		{"long name", `{"speakerMap":{"spk_0":"` + strings.Repeat("a", 101) + `"}}`, true, http.StatusBadRequest},
		{"multiple JSON values", `{"speakerMap":{"spk_0":"Kim"}} {}`, true, http.StatusBadRequest},
		{"large body", `{"speakerMap":{"spk_0":"` + strings.Repeat("a", 128*1024) + `"}}`, true, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, repo := newStubMeetingHandler()
			repo.addMeeting(&model.Meeting{UserID: "owner", MeetingID: "meeting", Status: model.StatusDone, Content: "spk_0 summary"})
			if tc.shared {
				repo.shares["reader|meeting"] = &model.Share{Permission: model.PermissionRead}
			}
			r := withChiParam(withUserCtx(httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(tc.body)), "reader"), "meetingId", "meeting")
			w := httptest.NewRecorder()
			h.UpdateSpeakers(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d, want=%d body=%s", w.Code, tc.status, w.Body)
			}
			if w.Code == http.StatusOK {
				if repo.meetingsByID["meeting"].Content != "김팀장 summary" {
					t.Fatal("shared reader's rename was not persisted")
				}
			} else {
				if repo.meetingsByID["meeting"].Content != "spk_0 summary" {
					t.Fatal("failed request changed meeting")
				}
				var response struct {
					Error struct {
						Code    string `json:"code"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Error.Code == "" || response.Error.Message == "" {
					t.Fatal("missing structured error response")
				}
			}
		})
	}
}
