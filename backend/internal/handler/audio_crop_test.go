package handler

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/repository"
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

// A member revoked mid-request (repository.ErrSummaryAccess) is a 403, and a
// sharing-restricted summary blocks the simulator with 409, not a 500.
func TestMemberRaceErrorsAreNotInternal(t *testing.T) {
	w := httptest.NewRecorder()
	writeCropError(w, fmt.Errorf("crop: %w", repository.ErrSummaryAccess))
	if w.Code != 403 {
		t.Fatalf("crop revoked member: %d", w.Code)
	}
	w = httptest.NewRecorder()
	writeSimError(w, fmt.Errorf("sim: %w", service.ErrSummaryReadRestricted), "sim")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "SHARING_REGEN_REQUIRED") {
		t.Fatalf("sim restricted: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	writeSimError(w, fmt.Errorf("sim: %w", repository.ErrConditionFailed), "sim")
	if w.Code != 409 {
		t.Fatalf("sim unpublished meeting: %d", w.Code)
	}
}
