package service

import (
	"errors"

	"github.com/ttobak/backend/internal/model"
)

var ErrRecordingRecoveryCleanup = errors.New("rejected recording recovery cleanup failed")

var ErrRecordingCheckpointMissing = errors.New("recording checkpoint missing")

func CanRecoverRecording(meeting *model.Meeting) bool {
	return meeting != nil && meeting.AudioCrop == nil &&
		(meeting.Status == model.StatusRecording || meeting.Status == model.StatusError) &&
		len(meeting.GetEffectiveAudioKeys()) == 0 && meeting.AudioPartCount == 0 &&
		meeting.TranscriptA == "" && meeting.TranscriptB == "" && meeting.Content == ""
}
