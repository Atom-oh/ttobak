package model

import "testing"

func TestNormalizeProjectStage(t *testing.T) {
	tests := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"technical validation", "Technical Validation", true},
		{"  CLOSED lost ", "Closed Lost", true},
		{"Prospect", "Prospect", true},
		{"", "", true},
		{"POC", "", false},
		{"Technical  Validation", "", false},
	}
	for _, tc := range tests {
		got, ok := NormalizeProjectStage(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("NormalizeProjectStage(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestIsTerminalStage(t *testing.T) {
	for _, stage := range ProjectStages {
		want := stage == "Launched" || stage == "Closed Lost"
		if IsTerminalStage(stage) != want {
			t.Errorf("IsTerminalStage(%q) != %v", stage, want)
		}
	}
	if IsTerminalStage("") || IsTerminalStage("POC") {
		t.Error("unset and unmapped stages are not terminal")
	}
}
