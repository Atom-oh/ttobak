package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func TestMeetingDetailHasEnglishNoteWithoutInliningIt(t *testing.T) {
	for _, test := range []struct {
		contentEn string
		want      bool
	}{{"", false}, {"English note body", true}} {
		repo := newMockMeetingRepo()
		repo.addMeeting(&model.Meeting{MeetingID: "m1", UserID: "owner", Status: model.StatusDone,
			Content: "korean", ContentEn: test.contentEn, UpdatedAt: time.Now().UTC()})
		detail, err := newMeetingServiceWithRepo(repo).GetMeetingDetail(context.Background(), "owner", "m1")
		if err != nil {
			t.Fatal(err)
		}
		if detail.HasEnglishNote != test.want {
			t.Fatalf("HasEnglishNote=%v want %v", detail.HasEnglishNote, test.want)
		}
		body, _ := json.Marshal(detail)
		if !strings.Contains(string(body), `"hasEnglishNote":`) || strings.Contains(string(body), "English note body") || strings.Contains(string(body), "contentEn") {
			t.Fatalf("detail must expose only the flag: %s", body)
		}
	}
}
