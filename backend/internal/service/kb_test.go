package service

import (
	"errors"
	"testing"
)

func TestValidateAssetOwnership(t *testing.T) {
	const me = "user-123"
	cases := []struct {
		name      string
		sourceKey string
		userID    string
		wantErr   bool
	}{
		{"owned file", "files/user-123/meeting-1/deck.pdf", me, false},
		{"owned audio", "audio/user-123/meeting-1/clip.m4a", me, false},
		{"owned image", "images/user-123/meeting-1/shot.png", me, false},
		{"other user's file (IDOR)", "files/other-user/meeting-1/secret.pdf", me, true},
		{"other user's audio (IDOR)", "audio/other-user/meeting-1/secret.m4a", me, true},
		{"prefix-confusion not-quite-owner", "files/user-1234/meeting-1/x.pdf", me, true},
		{"path traversal", "files/user-123/../other-user/secret.pdf", me, true},
		{"unknown prefix", "kb/user-123/x.pdf", me, true},
		{"empty userID", "files/user-123/meeting-1/deck.pdf", "", true},
		{"empty source key", "", me, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAssetOwnership(tc.sourceKey, tc.userID)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !errors.Is(err, ErrForbidden) {
					t.Fatalf("expected ErrForbidden, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}
