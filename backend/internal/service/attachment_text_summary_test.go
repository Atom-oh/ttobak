package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestAttachmentSummaryRevisionAndBoundedDocumentEvidence(t *testing.T) {
	s, r, storage, result := readingFixture(t)
	r.attachment.FileName = "설계.md"
	r.attachments["m"] = []model.Attachment{*r.attachment}
	result.Units[0].Text = strings.Repeat("<한국어>&", 8000)
	result.Metrics.TextBytes = len(result.Units[0].Text)
	storage.body, _ = json.Marshal(result)
	attachments, err := s.summaryAttachments(context.Background(), "owner", "m")
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 || !attachments[0].SummaryExcerpted {
		t.Fatal("missing excerpt indication")
	}
	evidence := documentEvidence(attachments[0])
	if len(evidence) > 16*1024 {
		t.Fatalf("encoded document evidence exceeds budget: %d", len(evidence))
	}
	context := buildAttachmentContext(attachments)
	if !strings.Contains(context, "<DOCUMENT>") || strings.Contains(context, "첨부 이미지") {
		t.Fatalf("wrong source classification: %.100s", context)
	}
	meeting := r.meetings[meetingKey("owner", "m")]
	meeting.Content = "new summary"
	meeting.AttachmentSummarySources = summaryAttachmentSnapshot(meeting.Content, attachments)
	status := attachmentStatus(meeting, r.attachment, r.state)
	if status.NeedsResummary || !status.SummaryExcerpted {
		t.Fatalf("%+v", status)
	}
	r.state.RunID = "new"
	r.state.Status = "queued"
	if !attachmentStatus(meeting, r.attachment, r.state).NeedsResummary {
		t.Fatal("new run appeared included in old summary")
	}
	if !strings.Contains(summaryAttachmentNotice([]model.Attachment{*r.attachment}), "다시 요약") {
		t.Fatal("missing late/unavailable evidence notice")
	}
}

func TestSummaryUsesDocumentProvenanceThroughActualModelRequest(t *testing.T) {
	text, r, _, _ := readingFixture(t)
	r.attachment.FileName = "요구사항.md"
	r.attachments["m"] = []model.Attachment{*r.attachment}
	meeting := r.meetings[meetingKey("owner", "m")]
	meeting.TranscriptA = noteSourceGroupedA
	meeting.TranscriptSegments = noteSourceSegments
	request, content, writes, err := invokeNoteSourceFixture(t, meeting,
		`{"content":[{"type":"text","text":"문서 근거 [DOC:a:0] [TS:12]\n\n회의 발언 [TS:12]"}],"stop_reason":"end_turn"}`,
		func(s *BedrockService) (string, error) {
			s.SetAttachmentTextService(text)
			return s.SummarizeTranscript(context.Background(), "m", "owner", "")
		})
	if err != nil {
		t.Fatal(err)
	}
	prompt := request.Messages[0].Content[0].Text
	if !strings.Contains(prompt, `"kind":"DOCUMENT"`) || !strings.Contains(prompt, "한국어 문서") || !strings.Contains(request.System, "DOCUMENT 전용 근거") {
		t.Fatal("verified document data/instructions missing")
	}
	paragraphs := strings.Split(content, "\n\n")
	if strings.Contains(paragraphs[0], "transcript://") || !strings.Contains(paragraphs[0], "attachment://a") || !strings.Contains(paragraphs[1], "transcript://") {
		t.Fatalf("cross-source citation: %s", content)
	}
	if writes != 1 {
		t.Fatalf("summary/snapshot not saved together: %d", writes)
	}
}
