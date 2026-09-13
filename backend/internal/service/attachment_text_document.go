package service

import (
	"encoding/json"
	"github.com/ttobak/backend/internal/model"
)

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
		Format       string                     `json:"format"`
		Scope        string                     `json:"scope"`
		Complete     bool                       `json:"complete"`
		Excerpted    bool                       `json:"excerpted"`
		Units        []model.AttachmentTextUnit `json:"units"`
	}{"DOCUMENT", att.AttachmentID, att.FileName, result.Format, result.Scope, result.Complete, att.SummaryExcerpted, result.Units}
	body, err := json.Marshal(evidence)
	if err != nil {
		return ""
	}
	return "\n<DOCUMENT>\n" + string(body) + "\n</DOCUMENT>\n"
}
