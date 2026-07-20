package service

import "testing"

func TestSidecarPDFKey(t *testing.T) {
	cases := []struct {
		name    string
		fileKey string
		want    string
	}{
		{"pptx under docs/", "docs/user-1/123_deck.pptx", "docs-pdf/user-1/123_deck.pptx.pdf"},
		{"ppt under docs/", "docs/user-1/123_deck.ppt", "docs-pdf/user-1/123_deck.ppt.pdf"},
		{"uppercase extension", "docs/user-1/123_DECK.PPTX", "docs-pdf/user-1/123_DECK.PPTX.pdf"},
		{"pdf needs no conversion", "docs/user-1/123_deck.pdf", ""},
		{"not under docs/ prefix", "audio/user-1/meeting-1/part.pptx", ""},
		{"unrelated category", "images/user-1/meeting-1/photo.jpg", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SidecarPDFKey(c.fileKey); got != c.want {
				t.Errorf("SidecarPDFKey(%q) = %q, want %q", c.fileKey, got, c.want)
			}
		})
	}
}
