package service

import (
	"errors"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestAudioCropEligibility(t *testing.T) {
	request := model.AudioCropRequest{RequestID: "52717e0a-1d01-4548-aa46-04dc8cbec845", StartSeconds: 0, EndSeconds: 3600}
	valid := func() *model.Meeting {
		return &model.Meeting{UserID: "owner", MeetingID: "source", Status: model.StatusDone,
			AudioKey: "audio/owner/source/recording.webm", Duration: 18000}
	}
	tests := []struct {
		name   string
		change func(*model.Meeting, *model.AudioCropRequest)
		want   error
	}{
		{"one hour from five hours", func(*model.Meeting, *model.AudioCropRequest) {}, nil},
		{"other owner", func(meeting *model.Meeting, _ *model.AudioCropRequest) { meeting.UserID = "other" }, ErrForbidden},
		{"other meeting key", func(meeting *model.Meeting, _ *model.AudioCropRequest) {
			meeting.AudioKey = "audio/owner/other/recording.webm"
		}, ErrForbidden},
		{"traversal", func(meeting *model.Meeting, _ *model.AudioCropRequest) {
			meeting.AudioKey = "audio/owner/source/../recording.webm"
		}, ErrForbidden},
		{"multiple parts", func(meeting *model.Meeting, _ *model.AudioCropRequest) { meeting.AudioPartCount = 2 }, ErrInvalidInput},
		{"active recording", func(meeting *model.Meeting, _ *model.AudioCropRequest) { meeting.Status = model.StatusRecording }, ErrInvalidInput},
		{"empty range", func(_ *model.Meeting, request *model.AudioCropRequest) { request.EndSeconds = 0 }, ErrInvalidInput},
		{"negative start", func(_ *model.Meeting, request *model.AudioCropRequest) { request.StartSeconds = -1 }, ErrInvalidInput},
		{"stale duration after replacement", func(meeting *model.Meeting, request *model.AudioCropRequest) {
			meeting.Status = model.StatusError
			meeting.Duration = 300
			request.EndSeconds = 3600
		}, nil},
		{"over six hours", func(_ *model.Meeting, request *model.AudioCropRequest) { request.EndSeconds = 21601 }, ErrInvalidInput},
		{"beyond first day", func(_ *model.Meeting, request *model.AudioCropRequest) {
			request.StartSeconds = 86000
			request.EndSeconds = 86401
		}, ErrInvalidInput},
		{"invalid request id", func(_ *model.Meeting, request *model.AudioCropRequest) { request.RequestID = "invalid" }, ErrInvalidInput},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			meeting, input := valid(), request
			test.change(meeting, &input)
			key, err := validateAudioCrop(meeting, "owner", input)
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v; want %v", err, test.want)
			}
			if err == nil && key != meeting.AudioKey {
				t.Fatal("wrong source audio")
			}
		})
	}
}
