package service

import (
	"encoding/json"
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

func TestSummaryRejectsHeadingOnlyAndKeepsDocumentIdentityOutOfPrompt(t *testing.T) {
	if _, err := resolveDocumentCitations("# 회의록\n\n## 개요\n\n- 허위 주장 [DOC:missing:0]", nil); !errors.Is(err, ErrInvalidAnalysisResponse) {
		t.Fatal("heading-only output succeeded")
	}
	_, r, _, result := readingFixture(t)
	attachment := *r.attachment
	attachment.ExtractedText = result
	body := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(documentEvidence(attachment)), "<DOCUMENT>\n"), "\n</DOCUMENT>")
	var wire map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		t.Fatal(err)
	}
	allowed := "|kind|attachmentId|name|format|scope|complete|excerpted|units|"
	for key := range wire {
		if !strings.Contains(allowed, "|"+key+"|") {
			t.Fatalf("private storage identity reached model evidence: %s", key)
		}
	}
	meeting := &model.Meeting{MeetingID: "m", UserID: "owner", TranscriptA: noteSourcePlainA}
	request, _, _, err := summarizeNoteSourceResponse(t, meeting, `{"content":[{"type":"text","text":"검증된 회의 내용"}],"stop_reason":"end_turn"}`)
	if err != nil || strings.Contains(request.System, "DOCUMENT 근거:") {
		t.Fatalf("document instructions appeared without evidence: %v", err)
	}
}
