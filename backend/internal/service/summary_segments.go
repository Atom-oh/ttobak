package service

import "github.com/ttobak/backend/internal/model"

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
