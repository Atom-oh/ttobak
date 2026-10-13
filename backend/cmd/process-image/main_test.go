package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type fakeImages struct {
	attachments []model.Attachment
	readErr     error
	writeErr    error
	writes      int
}

func (f *fakeImages) ListAttachments(context.Context, string) ([]model.Attachment, error) {
	return f.attachments, f.readErr
}
func (f *fakeImages) SaveImageResult(_ context.Context, expected *model.Attachment, _, _ string) error {
	f.writes++
	if expected.Status != model.AttachStatusUploaded || expected.ProcessedContent != "retained" {
		panic("publication did not preserve its observed snapshot")
	}
	return f.writeErr
}

type fakeImageAnalyzer struct {
	calls   int
	err     error
	content string
	key     string
}

func (f *fakeImageAnalyzer) AnalyzeImage(_ context.Context, _, key string) (string, string, error) {
	f.calls++
	f.key = key
	return "photo", f.content, f.err
}

func imageFixture() (json.RawMessage, *fakeImages, *fakeImageAnalyzer) {
	return json.RawMessage(`{"source":"ttobak.upload","detail-type":"ImageUploadCompleted","detail":{"bucket":"assets","key":"images/u/m/a+b.png","userId":"u","meetingId":"m"}}`),
		&fakeImages{attachments: []model.Attachment{{
			AttachmentID: "a", MeetingID: "m", UserID: "u", OriginalKey: "images/u/m/a+b.png",
			Status: model.AttachStatusUploaded, Type: "image", ProcessedContent: "retained",
		}}}, &fakeImageAnalyzer{content: "synthetic output"}
}

func TestImageFailuresReachAsyncFailureHandlingWithoutProviderText(t *testing.T) {
	for _, stage := range []string{"read", "model", "write", "size"} {
		t.Run(stage, func(t *testing.T) {
			event, store, analyzer := imageFixture()
			privateErr := errors.New("sensitive generated content and provider detail")
			switch stage {
			case "read":
				store.readErr = privateErr
			case "model":
				analyzer.err = privateErr
			case "write":
				store.writeErr = privateErr
			case "size":
				analyzer.content = strings.Repeat("x", 256*1024)
			}
			err := processImage(context.Background(), event, "assets", analyzer, store)
			if err == nil || strings.Contains(err.Error(), "sensitive") || strings.Contains(err.Error(), "provider") {
				t.Fatalf("failure swallowed or unsafe message: %v", err)
			}
			if store.attachments[0].ProcessedContent != "retained" {
				t.Fatal("failure modified prior content")
			}
		})
	}
}

func TestImageReplayPreservesLiteralKeyAndIgnoresStaleResults(t *testing.T) {
	event, store, analyzer := imageFixture()
	if err := processImage(context.Background(), event, "assets", analyzer, store); err != nil {
		t.Fatal(err)
	}
	if analyzer.key != "images/u/m/a+b.png" || store.writes != 1 {
		t.Fatal("literal source key or publication lost")
	}
	store.writeErr = repository.ErrConditionFailed
	if err := processImage(context.Background(), event, "assets", analyzer, store); err != nil {
		t.Fatal("conditional rejection must be an ignored stale delivery")
	}
	for _, status := range []string{model.AttachStatusDone, "deleted"} {
		store.attachments[0].Status = status
		before := analyzer.calls
		if err := processImage(context.Background(), event, "assets", analyzer, store); err != nil || analyzer.calls != before {
			t.Fatal("completed attachment was analyzed again")
		}
	}
	store.attachments = nil
	before := analyzer.calls
	if err := processImage(context.Background(), event, "assets", analyzer, store); err != nil || analyzer.calls != before {
		t.Fatal("deleted attachment was recreated")
	}
}

func TestImageForeignEventsCannotInvokeModel(t *testing.T) {
	for _, event := range []string{
		`{"source":"other","detail-type":"ImageUploadCompleted"}`,
		`{"source":"ttobak.upload","detail-type":"ImageUploadCompleted","detail":{"bucket":"other","key":"images/u/m/a.png","userId":"u","meetingId":"m"}}`,
		`{"source":"ttobak.upload","detail-type":"ImageUploadCompleted","detail":{"bucket":"assets","key":"images/u/other/a.png","userId":"u","meetingId":"m"}}`,
		`{"source":"ttobak.upload","detail-type":"ImageUploadCompleted","detail":{"bucket":"assets","key":"images/u/m/../a.png","userId":"u","meetingId":"m"}}`,
	} {
		_, store, analyzer := imageFixture()
		_ = processImage(context.Background(), json.RawMessage(event), "assets", analyzer, store)
		if analyzer.calls != 0 || store.writes != 0 {
			t.Fatal("foreign event reached model or persistence")
		}
	}
}
