package service

import "testing"

// Synthetic timeline only; no meeting content.
var anchorBoundSegments = []speakerSegment{
	{ID: "seg-a", Speaker: "spk_0", Text: "alpha", StartTime: 10, EndTime: 14},
	{ID: "seg-b", Speaker: "spk_1", Text: "bravo", StartTime: 20, EndTime: 23.5},
	{ID: "untimed-1", Speaker: "spk_0", Text: "charlie", Untimed: true},
	{ID: "seg-c", Speaker: "spk_0", Text: "delta", StartTime: 60, EndTime: 61},
}

func TestResolveTranscriptAnchorsDistanceBound(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		segments []speakerSegment
		want     string
	}{
		{"exact start links", "x [TS:10]", anchorBoundSegments, "x [00:10](transcript://seg-a)"},
		{"within bound links", "x [TS:12]", anchorBoundSegments, "x [00:12](transcript://seg-a)"},
		{"bound is inclusive", "x [TS:18]", anchorBoundSegments, "x [00:18](transcript://seg-b)"},
		{"just past bound stays plain", "x [TS:15]", anchorBoundSegments, "x (00:15)"},
		{"gap between segments stays plain", "x [TS:40]", anchorBoundSegments, "x (00:40)"},
		{"beyond last segment end stays plain", "x [TS:62]", anchorBoundSegments, "x (01:02)"},
		{"far beyond timeline stays plain", "x [TS:999]", anchorBoundSegments, "x (16:39)"},
		{"untimed row never anchors", "x [TS:0]", anchorBoundSegments, "x (00:00)"},
		{"mixed markers keep surrounding text", "a [TS:20] b [TS:300] c", anchorBoundSegments,
			"a [00:20](transcript://seg-b) b (05:00) c"},
		{"no segments strips marker", "x [TS:10]", nil, "x "},
		{"only untimed strips marker", "x [TS:10]",
			[]speakerSegment{{ID: "u", Untimed: true}}, "x "},
		{"missing ID within bound is plain label", "x [TS:10]",
			[]speakerSegment{{StartTime: 10, EndTime: 11}}, "x (00:10)"},
		{"inverted end uses start as end", "x [TS:31]",
			[]speakerSegment{{ID: "s", StartTime: 30, EndTime: 0}}, "x (00:31)"},
		{"inverted end still links at start", "x [TS:30]",
			[]speakerSegment{{ID: "s", StartTime: 30, EndTime: 0}}, "x [00:30](transcript://s)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveTranscriptAnchors(tc.content, tc.segments); got != tc.want {
				t.Fatalf("resolveTranscriptAnchors(%q) = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}

func TestNearestSegmentDistanceBound(t *testing.T) {
	cases := []struct {
		name     string
		segments []speakerSegment
		target   float64
		wantID   string // "" means nil
	}{
		{"nearest within bound", anchorBoundSegments, 21, "seg-b"},
		{"inclusive bound", anchorBoundSegments, 8, "seg-a"},
		{"outside bound is nil", anchorBoundSegments, 40, ""},
		{"beyond last end is nil", anchorBoundSegments, 62, ""},
		{"marker rounded up from the last end links", anchorBoundSegments, 61.5, "seg-c"},
		{"at last end links", anchorBoundSegments, 61, "seg-c"},
		{"untimed segment is skipped", []speakerSegment{
			{ID: "untimed-1", Untimed: true},
			{ID: "seg-a", StartTime: 1, EndTime: 3},
		}, 0, "seg-a"},
		{"only untimed is nil", []speakerSegment{{ID: "untimed-1", Untimed: true}}, 0, ""},
		{"empty is nil", nil, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nearestSegment(tc.segments, tc.target)
			gotID := ""
			if got != nil {
				gotID = got.ID
			}
			if gotID != tc.wantID {
				t.Fatalf("nearestSegment(%v) = %q, want %q", tc.target, gotID, tc.wantID)
			}
		})
	}
}

func TestParseSimRequirementsFarMarkerHasNoEvidence(t *testing.T) {
	raw := `[{"key":"monthlyActiveUsers","value":"100000","required":true,"tsMarker":"[TS:40]"}]`
	out := parseSimRequirements(raw, anchorBoundSegments)
	if len(out) != 1 {
		t.Fatalf("requirement must survive an unresolvable marker, got %#v", out)
	}
	if out[0].Evidence != "" {
		t.Fatalf("far marker must not fabricate evidence, got %q", out[0].Evidence)
	}
}
