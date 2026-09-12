package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/ttobak/backend/internal/model"
)

// Summary evidence is bounded independently of the full result/text endpoint.
// Retained results from failed/pending attempts are not supplied as current facts.
func (s *AttachmentTextService) summaryAttachments(ctx context.Context, ownerID, meetingID string) ([]model.Attachment, error) {
	meeting, _, err := s.meetings.checkAccess(ctx, ownerID, meetingID)
	if err != nil {
		return nil, err
	}
	if meeting == nil {
		return nil, ErrNotFound
	}
	attachments, err := s.repo.ListAttachments(ctx, meetingID)
	if err != nil {
		return nil, err
	}
	remaining, loaded := 64*1024, 0
	for i := range attachments {
		att := &attachments[i]
		if att.Type != model.AttachTypeDocument || att.Status != model.AttachStatusDone || attachmentFormat(att.OriginalKey) == "" || remaining == 0 || loaded == 20 {
			continue
		}
		current, canonical, err := s.authorized(ctx, ownerID, meetingID, att.AttachmentID, false)
		if err != nil {
			att.SummaryOmitted = true
			continue
		}
		state, err := s.describe(ctx, current, canonical)
		if err != nil {
			att.SummaryOmitted = true
			continue
		}
		*att = *canonical
		if state.Status != model.AttachmentTextSucceeded && state.Status != model.AttachmentTextPartial {
			att.SummaryOmitted = true
			continue
		}
		result, _, err := s.readResult(ctx, current, canonical, state)
		if err != nil {
			att.SummaryOmitted = true
			continue
		}
		fresh, nowAtt, err := s.authorized(ctx, ownerID, meetingID, att.AttachmentID, false)
		if err != nil {
			att.SummaryOmitted = true
			continue
		}
		latest, err := s.describe(ctx, fresh, nowAtt)
		if err != nil {
			att.SummaryOmitted = true
			continue
		}
		if attachmentRevision(state) != attachmentRevision(latest) || nowAtt.OriginalKey != att.OriginalKey {
			att.SummaryOmitted = true
			continue
		}
		if result.Source.RunID != state.RunID {
			att.SummaryOmitted = true
			continue
		}
		copy := *result
		copy.Units = nil
		att.ExtractedText = &copy
		allowance := min(remaining, 16*1024)
		for _, unit := range result.Units {
			if len(copy.Units) == 50 {
				att.SummaryExcerpted = true
				break
			}
			runes := []rune(unit.Text)
			lo, hi := 0, len(runes)
			for lo < hi {
				n := (lo + hi + 1) / 2
				candidate := unit
				candidate.Text = string(runes[:n])
				copy.Units = append(copy.Units, candidate)
				size := len(documentEvidence(*att))
				copy.Units = copy.Units[:len(copy.Units)-1]
				if size <= allowance {
					lo = n
				} else {
					hi = n - 1
				}
			}
			if lo == 0 {
				att.SummaryExcerpted = true
				break
			}
			unit.Text = string(runes[:lo])
			copy.Units = append(copy.Units, unit)
			if lo < len(runes) {
				att.SummaryExcerpted = true
				break
			}
		}
		if len(copy.Units) == 0 {
			att.ExtractedText = nil
			att.SummaryOmitted = true
			continue
		}
		remaining -= len(documentEvidence(*att))
		att.ExtractedRevision = attachmentRevision(state)
		loaded++
	}
	return attachments, nil
}

func documentEvidence(att model.Attachment) string {
	result := att.ExtractedText
	if result == nil {
		return ""
	}
	// Marshal as data: document closing tags and instructions cannot escape
	// the envelope. Document TS markers do not establish audio provenance.
	evidence := struct {
		Kind         string                     `json:"kind"`
		AttachmentID string                     `json:"attachmentId"`
		Name         string                     `json:"name"`
		Source       model.AttachmentTextSource `json:"source"`
		Scope        string                     `json:"scope"`
		Complete     bool                       `json:"complete"`
		Excerpted    bool                       `json:"excerpted"`
		Units        []model.AttachmentTextUnit `json:"units"`
	}{"DOCUMENT", att.AttachmentID, att.FileName, result.Source, result.Scope, result.Complete, att.SummaryExcerpted, result.Units}
	body, err := json.Marshal(evidence)
	if err != nil {
		return ""
	}
	return "\n<DOCUMENT>\n" + string(body) + "\n</DOCUMENT>\n"
}
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
		if att.Type != model.AttachTypeDocument || att.Status != model.AttachStatusDone {
			continue
		}
		name := sanitizeMarkdownText(att.FileName)
		switch {
		case att.SummaryOmitted:
			notes = append(notes, fmt.Sprintf("- %s: 검증할 수 없는 문서 근거를 제외했습니다. 추출 상태 또는 인용을 확인한 뒤 다시 요약하세요.", name))
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
	return "\n\n### 문서 근거 범위\n" + strings.Join(notes, "\n")
}

// Resolve only model-selected unit indexes that exist in supplied document
// evidence. Positions and attachment links come from the parser, never the model.
func resolveDocumentCitations(content string, attachments []model.Attachment) (string, error) {
	marker := regexp.MustCompile(`\[DOC:([A-Za-z0-9_-]{1,128}):([0-9]{1,6})\]`)
	audioMarker := regexp.MustCompile(`\[TS:\d+\]`)
	audioLink := regexp.MustCompile(`\[[^\]]*\]\(transcript://[^)]*\)`)
	evidence := map[string]int{}
	for i, att := range attachments {
		if att.ExtractedText != nil {
			evidence[att.AttachmentID] = i
		}
	}
	paragraphs := strings.Split(content, "\n\n")
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
				att.SummaryOmitted = true
				return ""
			}
			location := att.ExtractedText.Units[index].Location
			if !validDocumentLocation(att.ExtractedText.Format, location) {
				invalid = true
				att.SummaryOmitted = true
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
	result := strings.Join(kept, "\n\n")
	if strings.TrimSpace(result) == "" {
		return "", ErrInvalidAnalysisResponse
	}
	if omitted {
		result += "\n\n> 문서 인용을 확인할 수 없는 문단을 제외했습니다."
	}
	return result, nil
}
