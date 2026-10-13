package model

import "testing"

func TestTranscriptQualityFollowsCurrentVariantAndBytes(t *testing.T) {
	meeting := &Meeting{TranscriptA: "a", TranscriptB: "b", TranscriptSegments: "segments"}
	quality := &TranscriptQuality{Refinement: "validated", SpeakerProvenance: "acoustic",
		TextSHA256: TranscriptHash("a"), SegmentsSHA256: TranscriptHash("segments")}
	meeting.TranscriptQualityA = quality
	if MatchingTranscriptQuality(meeting, "A").Refinement != "validated" {
		t.Fatal("current A lost status")
	}
	if MatchingTranscriptQuality(meeting, "B").Refinement != "unknown" {
		t.Fatal("A status leaked onto B")
	}
	meeting.TranscriptA = "human correction"
	if MatchingTranscriptQuality(meeting, "A").Refinement != "edited" {
		t.Fatal("human edit inherited verified status")
	}
	meeting.TranscriptA, meeting.TranscriptSegments = "a", "new segments"
	if MatchingTranscriptQuality(meeting, "A").Timing != "unavailable" || TranscriptTimingMatches(meeting, "A") {
		t.Fatal("different segments inherited timing")
	}
	if quality.Refinement != "validated" {
		t.Fatal("status lookup mutated stored quality")
	}
}
