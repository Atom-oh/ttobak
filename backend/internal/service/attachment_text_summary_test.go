package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

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

func TestDocumentClaimsKeepValidBulletsLinksAndInputCoverage(t *testing.T) {
	attachments := []model.Attachment{{AttachmentID: "a", FileName: "근거.pdf", Type: model.AttachTypeDocument, Status: model.AttachStatusDone,
		ExtractedRevision: "revision", ExtractedText: &model.AttachmentTextResult{Format: "pdf", Units: []model.AttachmentTextUnit{
			{Text: "검증된 자료", Location: map[string]interface{}{"kind": "page", "page": float64(1)}},
		}}}}
	content, err := resolveDocumentCitations("## 결정 사항\n- 확인된 결정입니다.\n- 허위 주장 [DOC:a:9]\n- 검증된 문서 결정 [DOC:a:0]", attachments)
	if err != nil || !strings.Contains(content, "확인된 결정") || !strings.Contains(content, "검증된 문서 결정") || strings.Contains(content, "허위 주장") {
		t.Fatalf("valid list content lost: %q %v", content, err)
	}
	if !strings.Contains(buildAttachmentLinkSections(attachments), "attachment://a") ||
		!strings.Contains(summaryAttachmentSnapshot(content, attachments), `"a":"revision"`) {
		t.Fatal("citation rejection removed the uploaded file or input coverage")
	}
	attachments[0].SummaryOmitted = true
	if !strings.Contains(buildAttachmentLinkSections(attachments), "attachment://a") {
		t.Fatal("unavailable evidence hid a completed upload")
	}
}

func TestDocumentCitationPreservesMarkdown(t *testing.T) {
	prefix := "# 요약\n- 결정\n- 담당\n\n```sh\n# 주석\n\n- 코드\n```\n\n"
	for _, suffix := range []string{"본문\n", "- 허위 주장 [DOC:missing:0]\n"} {
		got, err := resolveDocumentCitations(prefix+suffix, nil)
		if err != nil || !strings.HasPrefix(got, prefix) || suffix == "본문\n" && got != prefix+suffix {
			t.Fatalf("markdown changed: %q %v", got, err)
		}
	}
}

func TestSummaryRejectsHeadingOnlyWithoutDocumentInstructions(t *testing.T) {
	if _, err := resolveDocumentCitations("# 회의록\n\n## 개요\n\n- 허위 주장 [DOC:missing:0]", nil); !errors.Is(err, ErrInvalidAnalysisResponse) {
		t.Fatal("heading-only output succeeded")
	}
	meeting := &model.Meeting{MeetingID: "m", UserID: "owner", TranscriptA: noteSourcePlainA}
	request, _, _, err := summarizeNoteSourceResponse(t, meeting, `{"content":[{"type":"text","text":"검증된 회의 내용"}],"stop_reason":"end_turn"}`)
	if err != nil || strings.Contains(request.System, "DOCUMENT 근거:") {
		t.Fatalf("document instructions appeared without evidence: %v", err)
	}
}

func trailerTestAttachments() []model.Attachment {
	return []model.Attachment{
		{AttachmentID: "img", FileName: "slide.png", Type: "image", Status: model.AttachStatusDone, ProcessedContent: "chart analysis"},
		{AttachmentID: "doc", FileName: "spec.pdf", Type: model.AttachTypeDocument, Status: model.AttachStatusDone},
	}
}

// An English note gets an English machine trailer; the Korean helpers keep
// the historical headings and notices byte-for-byte.
func TestAttachmentTrailerFollowsNoteLanguage(t *testing.T) {
	attachments := trailerTestAttachments()
	korean := buildAttachmentLinkSections(attachments) + summaryAttachmentNotice(attachments)
	if korean != buildAttachmentLinkSectionsFor(attachments, model.NoteLanguageKorean)+summaryAttachmentNoticeFor(attachments, model.NoteLanguageKorean) {
		t.Fatal("Korean trailer changed")
	}
	for _, want := range []string{"\n## 첨부 이미지\n", "\n## 첨부 문서\n", "\n\n### 첨부 근거 범위\n", "- spec.pdf: 문서 본문은 이 요약에 반영되지 않았습니다. 추출 상태 확인 후 다시 요약하세요."} {
		if !strings.Contains(korean, want) {
			t.Fatalf("Korean trailer lost %q: %s", want, korean)
		}
	}
	english := buildAttachmentLinkSectionsFor(attachments, model.NoteLanguageEnglish) + summaryAttachmentNoticeFor(attachments, model.NoteLanguageEnglish)
	for _, want := range []string{"\n## Attached images\n", "![slide.png](attachment://img)", "\n## Attached documents\n", "- [spec.pdf](attachment://doc)", "\n\n### Attachment evidence coverage\n", "- spec.pdf: The document text is not reflected in these notes."} {
		if !strings.Contains(english, want) {
			t.Fatalf("English trailer missing %q: %s", want, english)
		}
	}
	if strings.ContainsFunc(english, func(r rune) bool { return r >= 0xAC00 && r <= 0xD7A3 }) {
		t.Fatalf("English trailer contains Korean: %s", english)
	}
}

func TestEnglishSnapshotAppendsEnglishTrailer(t *testing.T) {
	meeting := &model.Meeting{UserID: "owner", MeetingID: "m", Status: model.StatusDone, TranscriptA: noteSourceGroupedA, TranscriptSegments: noteSourceSegments}
	for language, heading := range map[string]string{model.NoteLanguageEnglish: "## Attached images", model.NoteLanguageKorean: "## 첨부 이미지"} {
		_, content, _, err := invokeNoteSourceFixture(t, meeting,
			`{"content":[{"type":"text","text":"# Meeting Notes\n\n## Overview\nDiscussed the plan [TS:12]"}],"stop_reason":"end_turn"}`,
			func(s *BedrockService) (string, error) {
				return s.generateSummarySnapshot(context.Background(), meeting, trailerTestAttachments(), "", false, language)
			})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(content, heading) {
			t.Fatalf("%s note trailer missing %q: %s", language, heading, content)
		}
	}
}
