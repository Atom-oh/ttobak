package service

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/speaker"
)

var defaultSpeakerLabel = regexp.MustCompile(`^(spk_[0-9]+|화자[A-Z])$`)

// A read grant permits speaker correction, not arbitrary find/replace over the
// owner's content. Accept only recorded speakers or existing fallback labels.
func validateSpeakerMapping(meeting *model.Meeting, req *model.UpdateSpeakersRequest) error {
	if req == nil || len(req.SpeakerMap) == 0 || len(req.SpeakerMap) > 100 {
		return fmt.Errorf("%w: speakerMap must contain 1 to 100 speakers", ErrInvalidInput)
	}
	known := make(map[string]bool)
	var segments []segmentSpeakerField
	if json.Unmarshal([]byte(meeting.TranscriptSegments), &segments) == nil {
		for _, segment := range segments {
			known[segment.Speaker] = true
		}
	}
	for label, name := range req.SpeakerMap {
		if label == "" || utf8.RuneCountInString(label) > 100 || strings.ContainsFunc(label, unicode.IsControl) ||
			strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 100 ||
			strings.ContainsFunc(name, unicode.IsControl) {
			return fmt.Errorf("%w: speaker labels and names must contain 1 to 100 characters without control characters", ErrInvalidInput)
		}
		if known[label] {
			continue
		}
		// Summary-only legacy meetings have no structured speaker list.
		// Boundary-aware matching also keeps spk_1 distinct from spk_1000000.
		if defaultSpeakerLabel.MatchString(label) {
			for _, text := range []string{meeting.Content, meeting.TranscriptA, meeting.TranscriptB} {
				if speaker.ReplaceLabel(text, label, "\x00") != text {
					known[label] = true
					break
				}
			}
		}
		if !known[label] {
			return fmt.Errorf("%w: speaker is no longer present in this meeting", ErrInvalidInput)
		}
	}
	return nil
}

// Rename only human-facing action fields. JSON encoding preserves quotes in
// names, and IDs, completion flags and other metadata remain untouched.
func renameActionItemSpeakers(raw string, replacer *speaker.Replacer) string {
	var items []map[string]json.RawMessage
	if raw == "" || json.Unmarshal([]byte(raw), &items) != nil {
		return raw
	}
	changed := false
	for _, item := range items {
		for _, field := range []string{"text", "assignee"} {
			var original string
			if json.Unmarshal(item[field], &original) != nil {
				continue
			}
			value := replacer.Replace(original)
			if value != original {
				item[field], _ = json.Marshal(value)
				changed = true
			}
		}
	}
	if !changed {
		return raw
	}
	updated, _ := json.Marshal(items)
	return string(updated)
}
