package model

import (
	"crypto/sha256"
	"encoding/hex"
)

// TranscriptQuality attests structural processing only, never semantic
// completeness, ASR accuracy, or the identity of a real person.
type TranscriptQuality struct {
	Refinement         string   `json:"refinement" dynamodbav:"refinement"` // validated, partial, raw, not_requested, unknown, edited
	Degraded           bool     `json:"degraded" dynamodbav:"degraded"`
	SpeakerProvenance  string   `json:"speakerProvenance" dynamodbav:"speakerProvenance"` // acoustic, text_inferred, mixed, unknown
	TotalChunks        int      `json:"totalChunks,omitempty" dynamodbav:"totalChunks,omitempty"`
	FallbackChunks     int      `json:"fallbackChunks,omitempty" dynamodbav:"fallbackChunks,omitempty"`
	Reasons            []string `json:"reasons,omitempty" dynamodbav:"reasons,omitempty"`
	DiarizationEnabled *bool    `json:"diarizationEnabled,omitempty" dynamodbav:"diarizationEnabled,omitempty"`
	DetectedSpeakers   int      `json:"detectedSpeakers,omitempty" dynamodbav:"detectedSpeakers,omitempty"`
	TextSHA256         string   `json:"-" dynamodbav:"textSHA256"`
	SegmentsSHA256     string   `json:"-" dynamodbav:"segmentsSHA256"`
}

func TranscriptHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// MatchingTranscriptQuality never attaches an old processing result to edited
// or independently selected text/segments. Legacy rows are explicitly unknown.
func MatchingTranscriptQuality(meeting *Meeting, variant string) *TranscriptQuality {
	quality, text := meeting.TranscriptQualityA, meeting.TranscriptA
	if variant == "B" {
		quality, text = meeting.TranscriptQualityB, meeting.TranscriptB
	}
	if quality == nil {
		return &TranscriptQuality{Refinement: "unknown", SpeakerProvenance: "unknown"}
	}
	if quality.TextSHA256 != TranscriptHash(text) {
		return &TranscriptQuality{Refinement: "edited", SpeakerProvenance: "unknown"}
	}
	if quality.SegmentsSHA256 != TranscriptHash(meeting.TranscriptSegments) {
		return &TranscriptQuality{Refinement: "unknown", SpeakerProvenance: "unknown"}
	}
	copy := *quality
	return &copy
}
