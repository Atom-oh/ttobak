package main

import "testing"

func TestNamespaceSpeakerLabel(t *testing.T) {
	cases := []struct {
		label     string
		partIndex int
		want      string
	}{
		{"spk_2", 0, "spk_2"},
		{"spk_0", 1, "spk_1000000"},
		{"spk_3", 2, "spk_2000003"},
		{"화자A", 1, "화자A"},              // non-spk_ labels pass through unchanged
		{"spk_5000000", 0, "spk_999999"}, // out-of-range n clamps within part 0's own range
		{"spk_-1", 1, "spk_1000000"},      // negative n clamps to 0 rather than underflowing into part 0's range
	}
	for _, c := range cases {
		got := namespaceSpeakerLabel(c.label, c.partIndex)
		if got != c.want {
			t.Errorf("namespaceSpeakerLabel(%q, %d) = %q, want %q", c.label, c.partIndex, got, c.want)
		}
	}
}

func TestNamespaceSpeakerLabel_NoCollisionAcrossParts(t *testing.T) {
	// The bug this function fixes: part 0's spk_0 and part 1's spk_0 must
	// never end up equal after namespacing.
	part0 := namespaceSpeakerLabel("spk_0", 0)
	part1 := namespaceSpeakerLabel("spk_0", 1)
	if part0 == part1 {
		t.Errorf("expected distinct labels, got both %q", part0)
	}
}
