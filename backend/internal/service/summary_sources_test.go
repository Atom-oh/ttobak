package service

import (
	"context"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestBatchSummaryIgnoresLinkedMeetings(t *testing.T) {
	for _, writer := range []string{"0", "1"} {
		t.Run("writer="+writer, func(t *testing.T) {
			t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", writer)
			m := &model.Meeting{MeetingID: "m", UserID: "owner", Status: model.StatusSummarizing,
				TranscriptA: "[Speaker 0 1.0sec~2.0sec] synthetic line", LinkedMeetingIDs: []string{"prev"}}
			linkedReads := 0
			request, _, _, err := invokeObservedNoteSourceFixture(t, m,
				`{"content":[{"type":"text","text":"# Notes\n\nSynthetic overview sentence."}],"stop_reason":"end_turn"}`,
				func(_, body string) {
					if strings.Contains(body, "MEETING#prev") {
						linkedReads++
					}
				},
				func(s *BedrockService) (string, error) {
					return s.SummarizeTranscript(context.Background(), m.MeetingID, m.UserID, "")
				})
			if err != nil {
				t.Fatal(err)
			}
			if linkedReads != 0 {
				t.Fatalf("linked predecessor was read %d times", linkedReads)
			}
			if prompt := request.System + request.Messages[0].Content[0].Text; strings.Contains(prompt, "PREV-SECRET") || strings.Contains(prompt, "연관 회의") {
				t.Fatal("linked predecessor entered the prompt")
			}
		})
	}
}

func TestSharingSafePrepareKeepsImageAnalysis(t *testing.T) {
	t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", "1")
	snapshot := &model.SummarySnapshot{SharingSafe: true,
		Meeting: &model.Meeting{MeetingID: "m", UserID: "owner", Content: "ko note", LinkedMeetingIDs: []string{"prev"}},
		Attachments: []model.Attachment{{AttachmentID: "a", Type: model.AttachTypeDiagram, ProcessedContent: "diagram text", Description: "chart"}}}
	meeting, attachments, _, err := (&ResummaryService{}).prepare(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 || attachments[0].ProcessedContent != "diagram text" || attachments[0].Description != "chart" {
		t.Fatalf("image analysis dropped: %+v", attachments)
	}
	if meeting.Content != "" || len(meeting.LinkedMeetingIDs) != 0 {
		t.Fatalf("prepared meeting kept Korean note or links: %+v", meeting)
	}
}
