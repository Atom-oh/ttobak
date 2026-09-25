package service

import (
	"github.com/ttobak/backend/internal/model"
	"testing"
)

func TestExpiredRecordingRemainsRecoverableWithoutReplacingExistingWork(t *testing.T) {
	for _, status := range []string{model.StatusRecording, model.StatusError} {
		meeting := &model.Meeting{Status: status}
		if !CanRecoverRecording(meeting) {
			t.Fatalf("%s recording must remain recoverable", status)
		}
		meeting.AudioKey = "audio/owner/meeting/finished.webm"
		if CanRecoverRecording(meeting) {
			t.Fatal("must not recover an old checkpoint over finished audio")
		}
		meeting.AudioKey = ""
		meeting.TranscriptA = "Existing transcript"
		if CanRecoverRecording(meeting) {
			t.Fatal("must preserve an existing transcript")
		}
	}
	if CanRecoverRecording(&model.Meeting{Status: model.StatusTranscribing}) {
		t.Fatal("must not interrupt transcription")
	}
}
