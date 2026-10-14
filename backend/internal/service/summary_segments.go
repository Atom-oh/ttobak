package service

import (
	"encoding/json"
	"github.com/ttobak/backend/internal/model"
)

func meetingTranscriptSegments(meeting *model.Meeting, text string) []speakerSegment {
	_, variant := selectMeetingTranscript(meeting)
	if !model.TranscriptTimingMatches(meeting, variant) {
		return nil
	}
	return transcriptSegmentsForText(text, meeting.TranscriptSegments)
}

func timedSpeakerSegments(segments []speakerSegment) []speakerSegment {
	var timed []speakerSegment
	for _, segment := range segments {
		if !segment.Untimed {
			timed = append(timed, segment)
		}
	}
	return timed
}

func (s speakerSegment) MarshalJSON() ([]byte, error) {
	if !s.Untimed {
		type plain speakerSegment
		return json.Marshal(plain(s))
	}
	return json.Marshal(struct {
		ID      string `json:"id,omitempty"`
		Speaker string `json:"speaker"`
		Text    string `json:"text"`
		Untimed bool   `json:"untimed"`
	}{s.ID, s.Speaker, s.Text, true})
}
