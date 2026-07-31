// Package duration holds the audio-duration fallback chain shared by the
// summarize Lambda's single-transcript and multi-part merge paths. Keeping it
// here -- rather than inline in each path -- is what makes the two agree by
// construction instead of by convention (the single-transcript path silently
// dropped duration to 0 for AWS-Transcribe-fallback, Nova Sonic and
// pre-ADR-019 transcripts, all of which lack whisper_metadata.duration_seconds,
// while the merge path already had the fallback).
package duration

import "math"

// MaxReasonableSeconds bounds every candidate at 24 hours. A meeting recording
// longer than a day is not realistic; a value above this is far more likely a
// corrupt or adversarial duration field in the transcript JSON than a real
// audio length, and accepting it would offset every subsequent part's
// timestamps into nonsense.
const MaxReasonableSeconds = 86400

// Source values returned by Resolve, describing which tier was used.
const (
	SourceWhisperMetadata = ""            // primary: whisper_metadata.duration_seconds
	SourceSegmentEnd      = "segment_end" // last refined transcript segment's EndTime
	SourceWhisperEnd      = "whisper_end" // last raw Whisper segment's End
	SourceUnknown         = "unknown"     // no usable candidate; seconds is 0
)

// Resolve picks the best available audio duration in seconds, in preference
// order:
//  1. whisperMetadataSeconds (whisper_metadata.duration_seconds — true decoded
//     audio length, including trailing silence)
//  2. lastSegmentEndSeconds (last refined transcript segment's EndTime —
//     understates by any trailing silence)
//  3. lastWhisperSegmentEndSeconds (last raw Whisper segment's End — covers
//     refinement failure as well as missing metadata)
//  4. 0, when none of the above are usable
//
// Each candidate must be finite and within (0, MaxReasonableSeconds] to be
// accepted, which rejects NaN, negatives and pathological +Inf/huge values from
// a corrupt or adversarial transcript JSON; a rejected candidate falls through
// to the next tier. source describes which tier was used (see the Source*
// constants) so callers can log the degradation.
func Resolve(whisperMetadataSeconds, lastSegmentEndSeconds, lastWhisperSegmentEndSeconds float64) (seconds float64, source string) {
	switch {
	case usable(whisperMetadataSeconds):
		return whisperMetadataSeconds, SourceWhisperMetadata
	case usable(lastSegmentEndSeconds):
		return lastSegmentEndSeconds, SourceSegmentEnd
	case usable(lastWhisperSegmentEndSeconds):
		return lastWhisperSegmentEndSeconds, SourceWhisperEnd
	default:
		return 0, SourceUnknown
	}
}

// usable reports whether v is a plausible audio duration. The !IsNaN check is
// implicit in the comparisons (any comparison with NaN is false), but stated
// explicitly here so the intent survives a future edit.
func usable(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0 && v <= MaxReasonableSeconds
}
