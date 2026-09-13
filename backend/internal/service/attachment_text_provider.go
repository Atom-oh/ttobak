package service

import (
	"context"
	"github.com/ttobak/backend/internal/model"
	"strings"
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
		if att.Type != model.AttachTypeDocument || att.Status != model.AttachStatusDone || attachmentFormat(att.OriginalKey) == "" {
			continue
		}
		if remaining == 0 || loaded == 20 {
			att.SummaryExcerpted = true
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
			if strings.TrimSpace(unit.Text) == "" {
				continue
			}
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
			continue
		}
		remaining -= len(documentEvidence(*att))
		att.ExtractedRevision = attachmentRevision(state)
		att.SummaryTextState = state
		loaded++
	}
	return attachments, nil
}
