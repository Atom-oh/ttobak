package duration

import (
	"math"
	"testing"
)

func TestResolve(t *testing.T) {
	tests := []struct {
		name       string
		metadata   float64
		segmentEnd float64
		whisperEnd float64
		want       float64
		wantSource string
	}{
		{
			name:       "primary metadata wins",
			metadata:   1800,
			segmentEnd: 1750,
			whisperEnd: 1740,
			want:       1800,
			wantSource: SourceWhisperMetadata,
		},
		{
			name:       "falls back to segment end when metadata missing",
			metadata:   0,
			segmentEnd: 1750,
			whisperEnd: 1740,
			want:       1750,
			wantSource: SourceSegmentEnd,
		},
		{
			name:       "falls back to whisper end when metadata and segment end missing",
			metadata:   0,
			segmentEnd: 0,
			whisperEnd: 1740,
			want:       1740,
			wantSource: SourceWhisperEnd,
		},
		{
			name:       "unknown when all tiers empty",
			metadata:   0,
			segmentEnd: 0,
			whisperEnd: 0,
			want:       0,
			wantSource: SourceUnknown,
		},
		{
			name:       "negative metadata rejected, falls through",
			metadata:   -5,
			segmentEnd: 120,
			whisperEnd: 100,
			want:       120,
			wantSource: SourceSegmentEnd,
		},
		{
			name:       "negative in every tier yields unknown",
			metadata:   -5,
			segmentEnd: -1,
			whisperEnd: -0.001,
			want:       0,
			wantSource: SourceUnknown,
		},
		{
			name:       "NaN metadata rejected, falls through",
			metadata:   math.NaN(),
			segmentEnd: 60,
			whisperEnd: 0,
			want:       60,
			wantSource: SourceSegmentEnd,
		},
		{
			name:       "NaN in every tier yields unknown",
			metadata:   math.NaN(),
			segmentEnd: math.NaN(),
			whisperEnd: math.NaN(),
			want:       0,
			wantSource: SourceUnknown,
		},
		{
			name:       "+Inf metadata rejected, falls through",
			metadata:   math.Inf(1),
			segmentEnd: 42,
			whisperEnd: 0,
			want:       42,
			wantSource: SourceSegmentEnd,
		},
		{
			name:       "-Inf rejected",
			metadata:   math.Inf(-1),
			segmentEnd: 0,
			whisperEnd: 7,
			want:       7,
			wantSource: SourceWhisperEnd,
		},
		{
			name:       "value above 24h ceiling rejected, falls through",
			metadata:   MaxReasonableSeconds + 1,
			segmentEnd: 3600,
			whisperEnd: 0,
			want:       3600,
			wantSource: SourceSegmentEnd,
		},
		{
			name:       "exactly at 24h ceiling accepted",
			metadata:   MaxReasonableSeconds,
			segmentEnd: 0,
			whisperEnd: 0,
			want:       MaxReasonableSeconds,
			wantSource: SourceWhisperMetadata,
		},
		{
			name:       "all tiers above ceiling yields unknown",
			metadata:   1e9,
			segmentEnd: 1e9,
			whisperEnd: 1e9,
			want:       0,
			wantSource: SourceUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, source := Resolve(tt.metadata, tt.segmentEnd, tt.whisperEnd)
			if got != tt.want || source != tt.wantSource {
				t.Errorf("Resolve(%v, %v, %v) = (%v, %q), want (%v, %q)",
					tt.metadata, tt.segmentEnd, tt.whisperEnd, got, source, tt.want, tt.wantSource)
			}
		})
	}
}
