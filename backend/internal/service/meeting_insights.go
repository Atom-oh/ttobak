package service

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
)

// meetingFieldInsights projects stored extraction for an already-authorized
// meeting reader. Unlike account/project projections it retains evidence.
// Neither a saved array nor a successful parse certifies source freshness.
func meetingFieldInsights(raw string) ([]model.MeetingInsight, string, bool) {
	const maxItems, maxBytes = 50, 64 * 1024
	out := make([]model.MeetingInsight, 0)
	if strings.TrimSpace(raw) == "" {
		return out, "", false
	}
	var parsed []model.MeetingInsight
	if !utf8.ValidString(raw) || json.Unmarshal([]byte(raw), &parsed) != nil || parsed == nil {
		return out, "INVALID_INSIGHTS", false
	}
	truncated := false
	limit := func(value string, size int) string {
		if utf8.RuneCountInString(value) > size {
			truncated = true
			return string([]rune(value)[:size])
		}
		return value
	}
	errorCode, size := "", 2 // JSON array brackets.
	for _, item := range parsed {
		if len(out) == maxItems {
			truncated = true
			break
		}
		if !model.IsValidInsightType(item.Type) || strings.TrimSpace(item.Text) == "" {
			errorCode = "INVALID_INSIGHTS"
			continue
		}
		// Preserve stored IDs rather than parseMeetingInsights' positional
		// reassignment, which is appropriate only for new model output.
		item.ID = limit(item.ID, 128)
		item.Text = limit(item.Text, 2000)
		item.Evidence = limit(item.Evidence, 2000)
		item.Implication = limit(item.Implication, 1000)
		item.NextAction = limit(item.NextAction, 1000)
		item.TsMarker = limit(item.TsMarker, 64)
		if len(item.Entities) > 20 {
			item.Entities = item.Entities[:20]
			truncated = true
		}
		for i := range item.Entities {
			item.Entities[i] = limit(item.Entities[i], 128)
		}
		body, _ := json.Marshal(item) // Same escaping as the HTTP encoder.
		extra := len(body)
		if len(out) > 0 {
			extra++ // Separating comma.
		}
		if size+extra > maxBytes {
			truncated = true
			break
		}
		size += extra
		out = append(out, item)
	}
	return out, errorCode, truncated
}
