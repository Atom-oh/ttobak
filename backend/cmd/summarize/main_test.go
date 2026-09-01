package main

import "testing"

func TestExtractMeetingIDFromTranscriptKey(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want string
	}{
		{"plain transcript", "transcripts/abc-123.json", "abc-123"},
		{"nova transcript", "transcripts/abc-123-nova.json", "abc-123"},
		{"part key rejected", "transcripts/abc-123_part_001.json", ""},
		// The repository's overflow spill writes transcripts/{id}/{field}.txt
		// under the same EventBridge-matched prefix — nested keys must never
		// be mistaken for a meeting id (a garbage id skips the status guard
		// and errors the invocation on every long meeting's pipeline run).
		{"spill transcriptA key rejected", "transcripts/abc-123/transcriptA.txt", ""},
		{"spill segments key rejected", "transcripts/abc-123/transcriptSegments.txt", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractMeetingIDFromTranscriptKey(c.key); got != c.want {
				t.Fatalf("extractMeetingIDFromTranscriptKey(%q) = %q, want %q", c.key, got, c.want)
			}
		})
	}
}
