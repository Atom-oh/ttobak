package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestUnreadableDocumentPreservesTrustedSummaryWithNotice(t *testing.T) {
	text, r, storage, _ := readingFixture(t)
	r.attachment.FileName = "검증할 자료.md"
	r.attachments["m"] = []model.Attachment{*r.attachment}
	storage.etag = `"changed"`
	meeting := r.meetings[meetingKey("owner", "m")]
	meeting.TranscriptA = noteSourcePlainA
	request, content, writes, err := invokeNoteSourceFixture(t, meeting,
		`{"content":[{"type":"text","text":"녹취에서 확인한 예산 검토입니다."}],"stop_reason":"end_turn"}`,
		func(s *BedrockService) (string, error) {
			s.SetAttachmentTextService(text)
			return s.SummarizeTranscript(context.Background(), "m", "owner", "")
		})
	if err != nil || writes != 1 || !strings.Contains(content, "예산 검토") || !strings.Contains(content, "제외") {
		t.Fatalf("trusted summary lost after document failure: %q writes=%d err=%v", content, writes, err)
	}
	if strings.Contains(request.Messages[0].Content[0].Text, "한국어 문서") || strings.Contains(request.Messages[0].Content[0].Text, "<DOCUMENT>") {
		t.Fatal("unverified document evidence reached the model")
	}
}

func TestOmittedDocumentDoesNotSuppressLaterVerifiedEvidence(t *testing.T) {
	text, r, _, _ := readingFixture(t)
	missing := *r.attachment
	missing.AttachmentID, missing.FileName = "missing", "삭제된 자료.md"
	r.attachments["m"] = []model.Attachment{missing, *r.attachment}
	attachments, err := text.summaryAttachments(context.Background(), "owner", "m")
	if err != nil || len(attachments) != 2 || !attachments[0].SummaryOmitted || attachments[1].ExtractedText == nil {
		t.Fatalf("later verified evidence lost: %+v %v", attachments, err)
	}
	if strings.Contains(buildAttachmentContext(attachments), missing.FileName) || !strings.Contains(summaryAttachmentNotice(attachments), missing.FileName) {
		t.Fatal("omitted document must appear only in the coverage notice")
	}
}

func TestInvalidDocumentCitationDropsClaimInsteadOfOnlyMarker(t *testing.T) {
	for _, marker := range []string{"[DOC:a:9]", "[DOC:missing:0]", "[DOC:malformed]"} {
		attachments := []model.Attachment{{AttachmentID: "a", FileName: "근거.pdf", Type: model.AttachTypeDocument, Status: model.AttachStatusDone,
			ExtractedText: &model.AttachmentTextResult{Format: "pdf", Units: []model.AttachmentTextUnit{
				{Text: "검증된 자료", Location: map[string]interface{}{"kind": "page", "page": float64(1)}},
			}}}}
		content, err := resolveDocumentCitations("확인된 녹취 요약.\n\n허위 승인 주장 "+marker+" [TS:12]\n\n검증된 문서 주장 [DOC:a:0]", attachments)
		if err != nil || strings.Contains(content, "허위 승인") || strings.Contains(content, "[DOC:") ||
			!strings.Contains(content, "확인된 녹취") || !strings.Contains(content, "attachment://a") || !strings.Contains(content, "제외") {
			t.Fatalf("unverified claim survived or trusted content lost: %q %v", content, err)
		}
		if _, err := resolveDocumentCitations("허위 승인 주장 "+marker, attachments); !errors.Is(err, ErrInvalidAnalysisResponse) {
			t.Fatal("notice-only output must not become a successful summary")
		}
	}
}

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
