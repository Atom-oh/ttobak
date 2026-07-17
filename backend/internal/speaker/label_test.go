package speaker

import "testing"

func TestNamespace(t *testing.T) {
	cases := []struct {
		label     string
		partIndex int
		want      string
	}{
		{"spk_2", 0, "spk_2"},
		{"spk_0", 1, "spk_1000000"},
		{"spk_3", 2, "spk_2000003"},
		{"화자A", 1, "화자A"},                // non-spk_ labels pass through unchanged
		{"spk_5000000", 0, "spk_999999"}, // out-of-range n clamps within part 0's own range
		{"spk_-1", 1, "spk_1000000"},     // negative n clamps to 0 rather than underflowing into part 0's range
	}
	for _, c := range cases {
		got := Namespace(c.label, c.partIndex)
		if got != c.want {
			t.Errorf("Namespace(%q, %d) = %q, want %q", c.label, c.partIndex, got, c.want)
		}
	}
}

func TestNamespace_NoCollisionAcrossParts(t *testing.T) {
	// The bug this function fixes: part 0's spk_0 and part 1's spk_0 must
	// never end up equal after namespacing.
	part0 := Namespace("spk_0", 0)
	part1 := Namespace("spk_0", 1)
	if part0 == part1 {
		t.Errorf("expected distinct labels, got both %q", part0)
	}
}

func TestReplaceLabel(t *testing.T) {
	cases := []struct {
		name        string
		text        string
		label       string
		replacement string
		want        string
	}{
		{
			name:        "no prefix collision with namespaced label",
			text:        "[spk_1]\nhello\n\n[spk_1000000]\nworld",
			label:       "spk_1",
			replacement: "Alice",
			want:        "[Alice]\nhello\n\n[spk_1000000]\nworld",
		},
		{
			name:        "no prefix collision spk_1 vs spk_10",
			text:        "[spk_1] hi [spk_10] there",
			label:       "spk_1",
			replacement: "Bob",
			want:        "[Bob] hi [spk_10] there",
		},
		{
			name:        "multiple occurrences all replaced",
			text:        "spk_2 said hi. later spk_2 said bye.",
			label:       "spk_2",
			replacement: "Carol",
			want:        "Carol said hi. later Carol said bye.",
		},
		{
			name:        "empty label is a no-op",
			text:        "spk_1 stays",
			label:       "",
			replacement: "X",
			want:        "spk_1 stays",
		},
		{
			// Regression: Go's regexp \b is ASCII-only and never fires at a
			// Hangul boundary, so a naive `\b`-based implementation silently
			// no-ops on this real production label (rawFallbackSegments'
			// default, matched by the frontend's UNMAPPED_PATTERN).
			name:        "Korean fallback label 화자A is matched and replaced",
			text:        "[화자A]\nhello",
			label:       "화자A",
			replacement: "Dave",
			want:        "[Dave]\nhello",
		},
		{
			name:        "adjacent occurrences separated by a single character are both replaced",
			text:        "spk_1 spk_1",
			label:       "spk_1",
			replacement: "Eve",
			want:        "Eve Eve",
		},
		{
			// Regression: treating all Hangul as a generic wordChar (the
			// earlier Korean-fallback-label fix) also blocked the common case
			// of a Korean particle glued directly onto a spk_N label in
			// LLM-generated summaries/action items -- a case the old
			// (boundary-unaware) strings.ReplaceAll handled fine.
			name:        "Korean particle attached directly to spk_N label still matches",
			text:        "spk_1이 문서를 작성했다",
			label:       "spk_1",
			replacement: "Kim",
			want:        "Kim이 문서를 작성했다",
		},
		{
			name:        "Korean particle attached directly to 화자X label still matches",
			text:        "화자A는 회의를 시작했다",
			label:       "화자A",
			replacement: "Kim",
			want:        "Kim는 회의를 시작했다",
		},
		{
			name:        "spk_1 still doesn't match inside spk_10 even with a Hangul suffix",
			text:        "spk_10이 말했다",
			label:       "spk_1",
			replacement: "Kim",
			want:        "spk_10이 말했다",
		},
	}
	for _, c := range cases {
		got := ReplaceLabel(c.text, c.label, c.replacement)
		if got != c.want {
			t.Errorf("%s: ReplaceLabel(%q, %q, %q) = %q, want %q", c.name, c.text, c.label, c.replacement, got, c.want)
		}
	}
}
