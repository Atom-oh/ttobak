package service

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/ttobak/backend/internal/model"
)

func summaryAttachmentSnapshot(content string, attachments []model.Attachment) string {
	snapshot := attachmentSummarySources{ContentHash: actionSourceHash(content), Documents: map[string]string{}, Excerpted: map[string]bool{}}
	for _, att := range attachments {
		if att.ExtractedText != nil && att.ExtractedRevision != "" && !att.SummaryOmitted {
			snapshot.Documents[att.AttachmentID] = att.ExtractedRevision
			if att.SummaryExcerpted {
				snapshot.Excerpted[att.AttachmentID] = true
			}
		}
	}
	body, _ := json.Marshal(snapshot)
	return string(body)
}

// Append a deterministic coverage notice: model output cannot silently claim
// unprovided document text was incorporated into this saved summary.
func summaryAttachmentNotice(attachments []model.Attachment) string {
	var notes []string
	for _, att := range attachments {
		name := sanitizeMarkdownText(att.FileName)
		if att.SummaryOmitted && att.Type != model.AttachTypeDocument {
			notes = append(notes, fmt.Sprintf("- %s: 변경된 첨부 근거를 제외했습니다.", name))
			continue
		}
		if att.Type != model.AttachTypeDocument || att.Status != model.AttachStatusDone {
			continue
		}
		switch {
		case att.SummaryOmitted:
			notes = append(notes, fmt.Sprintf("- %s: 검증할 수 없는 문서 근거를 제외했습니다. 추출 상태 또는 인용을 확인한 뒤 다시 요약하세요.", name))
		case att.ExtractedText == nil && att.SummaryExcerpted:
			notes = append(notes, fmt.Sprintf("- %s: 크기 제한으로 문서 본문을 제공하지 못했습니다.", name))
		case att.ExtractedText == nil:
			notes = append(notes, fmt.Sprintf("- %s: 문서 본문은 이 요약에 반영되지 않았습니다. 추출 상태 확인 후 다시 요약하세요.", name))
		case att.SummaryExcerpted:
			notes = append(notes, fmt.Sprintf("- %s: 크기 제한에 따라 문서 본문의 일부 발췌만 반영했습니다.", name))
		case !att.ExtractedText.Complete:
			notes = append(notes, fmt.Sprintf("- %s: 부분 추출된 문서 근거를 반영했습니다.", name))
		}
	}
	if len(notes) == 0 {
		return ""
	}
	return "\n\n### 첨부 근거 범위\n" + strings.Join(notes, "\n")
}

// Resolve only model-selected unit indexes that exist in supplied document
// evidence. Positions and attachment links come from the parser, never the model.
func resolveDocumentCitations(content string, attachments []model.Attachment) (string, error) {
	if !strings.Contains(content, "[DOC:") {
		if !summaryHasBody(content) {
			return "", ErrInvalidAnalysisResponse
		}
		return content, nil
	}
	marker := regexp.MustCompile(`\[DOC:([A-Za-z0-9_-]{1,128}):([0-9]{1,6})\]`)
	audioMarker := regexp.MustCompile(`\[TS:\d+\]`)
	audioLink := regexp.MustCompile(`\[[^\]]*\]\(transcript://[^)]*\)`)
	evidence := map[string]int{}
	for i, att := range attachments {
		if att.ExtractedText != nil {
			evidence[att.AttachmentID] = i
		}
	}
	paragraphs := summaryClaimUnits(content)
	kept := make([]string, 0, len(paragraphs))
	omitted := false
	for _, paragraph := range paragraphs {
		if !strings.Contains(paragraph, "[DOC:") {
			kept = append(kept, paragraph)
			continue
		}
		invalid := false
		paragraph = audioMarker.ReplaceAllString(paragraph, "")
		paragraph = audioLink.ReplaceAllString(paragraph, "")
		paragraph = marker.ReplaceAllStringFunc(paragraph, func(match string) string {
			parts := marker.FindStringSubmatch(match)
			attachmentIndex, ok := evidence[parts[1]]
			index, err := strconv.Atoi(parts[2])
			if !ok {
				invalid = true
				return ""
			}
			att := &attachments[attachmentIndex]
			if err != nil || index >= len(att.ExtractedText.Units) {
				invalid = true
				att.CitationRejected = true
				return ""
			}
			location := att.ExtractedText.Units[index].Location
			if !validDocumentLocation(att.ExtractedText.Format, location) {
				invalid = true
				att.CitationRejected = true
				return ""
			}
			field, label := "paragraph", "문단"
			switch att.ExtractedText.Format {
			case "pdf":
				field, label = "page", "쪽"
			case "pptx":
				field, label = "slide", "슬라이드"
			}
			return fmt.Sprintf("[%s · %s %v](attachment://%s)", sanitizeMarkdownText(att.FileName), label, location[field], att.AttachmentID)
		})
		if invalid || strings.Contains(paragraph, "[DOC:") {
			omitted = true
			continue
		}
		kept = append(kept, paragraph)
	}
	result := strings.Join(kept, "")
	if !summaryHasBody(result) {
		return "", ErrInvalidAnalysisResponse
	}
	if omitted {
		result += "\n\n" + summaryCitationNotice
	}
	return result, nil
}
