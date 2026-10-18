package service

import (
	"encoding/json"
	"os"
	"testing"
)

// TestTermKeySharedVectors pins NormalizeTermKey/TermKey to the vectors the
// frontend's normalizeTermKey test reads (frontend/scripts/dictionary.test.mjs).
func TestTermKeySharedVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/term-key/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Vectors []struct{ Input, Normalized, TermKey string }
	}
	if err := json.Unmarshal(raw, &file); err != nil || len(file.Vectors) == 0 {
		t.Fatalf("bad vectors file: %v", err)
	}
	for _, v := range file.Vectors {
		if got := NormalizeTermKey(v.Input); got != v.Normalized {
			t.Errorf("NormalizeTermKey(%q) = %q, want %q", v.Input, got, v.Normalized)
		}
		if got := TermKey(v.Input); got != v.TermKey {
			t.Errorf("TermKey(%q) = %q, want %q", v.Input, got, v.TermKey)
		}
	}
}
