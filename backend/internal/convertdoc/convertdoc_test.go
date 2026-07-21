package convertdoc

import "testing"

func TestSanitizedEnv(t *testing.T) {
	in := []string{
		"AWS_ACCESS_KEY_ID=AKIA...",
		"AWS_SESSION_TOKEN=xyz",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI=/creds",
		"HOME=/tmp",
		"PATH=/usr/bin",
		"LANG=en_US.UTF-8",
	}
	got := SanitizedEnv(in)
	want := []string{"HOME=/tmp", "PATH=/usr/bin", "LANG=en_US.UTF-8"}
	if len(got) != len(want) {
		t.Fatalf("expected %d entries, got %d: %v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
	for _, kv := range got {
		if len(kv) >= 4 && kv[:4] == "AWS_" {
			t.Fatalf("expected no AWS_* entries, got %v", got)
		}
	}
}

func TestTruncateOutput(t *testing.T) {
	tests := []struct {
		name string
		out  []byte
		n    int
		want string
	}{
		{"under limit", []byte("short"), 100, "short"},
		{"exactly at limit", []byte("12345"), 5, "12345"},
		{"over limit", []byte("123456789"), 5, "12345...(truncated)"},
		{"empty", []byte(""), 10, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TruncateOutput(tc.out, tc.n); got != tc.want {
				t.Errorf("TruncateOutput(%q, %d) = %q, want %q", tc.out, tc.n, got, tc.want)
			}
		})
	}
}

func TestIsSlideExtension(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"docs/user-1/deck.pptx", true},
		{"docs/user-1/deck.ppt", true},
		{"docs/user-1/DECK.PPTX", true},
		{"docs/user-1/deck.pdf", false},
		{"docs/user-1/deck.docx", false},
		{"docs/user-1/deck", false},
		{"docs/user-1/C++ 소개.pptx", true},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			if got := IsSlideExtension(tc.key); got != tc.want {
				t.Errorf("IsSlideExtension(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}
