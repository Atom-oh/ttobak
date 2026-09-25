package handler

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/service"
)

func TestCropRequestRejectsUnboundedUnknownAndTrailingPayloads(t *testing.T) {
	handler := &MeetingHandler{uploadService: &service.UploadService{}}
	for _, payload := range []string{
		`{"requestId":"test","sourceKey":"audio/another-user/private.wav"}`,
		`{"requestId":"test"} {"endSeconds":3600}`,
		`{"requestId":"` + strings.Repeat("a", 2048) + `"}`,
	} {
		response := httptest.NewRecorder()
		handler.CropAudio(response, httptest.NewRequest("POST", "/api/meetings/source/audio/crop", strings.NewReader(payload)))
		if response.Code != 400 {
			t.Fatalf("expected 400, got %d", response.Code)
		}
	}
}
