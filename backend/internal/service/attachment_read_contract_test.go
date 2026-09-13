package service

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestAttachmentCursorRequiresExplicitVersionAndRevision(t *testing.T) {
	_, repo, _, result := readingFixture(t)
	for _, payload := range []string{`{"u":0,"o":1}`, `{"v":1,"u":0,"o":1}`, `{"r":"revision","u":0,"o":1}`} {
		cursor := base64.RawURLEncoding.EncodeToString([]byte(payload))
		_, err := renderAttachmentPage(&model.AttachmentTextStatus{}, repo.state, result, "revision", cursor, 10)
		if !errors.Is(err, ErrAttachmentCursor) {
			t.Fatalf("incomplete cursor accepted: %s %v", payload, err)
		}
	}
}

func TestAttachmentSourceMismatchDoesNotAdvertiseUnreadableResult(t *testing.T) {
	s, repo, _, _ := readingFixture(t)
	repo.state.SourceKey = "files/owner/m/old.md"
	status, err := s.GetStatus(context.Background(), "owner", "m", "a")
	if err != nil {
		t.Fatal(err)
	}
	if status.HasResult || status.Status != model.AttachmentTextFailed || status.ErrorCode != "SOURCE_CHANGED" {
		t.Fatalf("unreadable retained result advertised: %+v", status)
	}
}
