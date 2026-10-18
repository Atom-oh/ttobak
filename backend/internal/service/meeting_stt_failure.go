package service

import (
	"context"
	"errors"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// ErrSttDeletionUnsupported means the repository cannot perform a guarded
// failed-recording deletion; callers must never fall back to DeleteMeeting.
var ErrSttDeletionUnsupported = errors.New("guarded failed-recording deletion unavailable")

type sttFailedMeetingDeleter interface {
	DeleteSttFailedMeeting(ctx context.Context, ownerID, meetingID string, mark model.SttFailureMark) error
	DeleteSttFailedMeetingObjects(ctx context.Context, ownerID, meetingID string) (repository.SttFailedMeetingObjectsResult, error)
}

// DeleteFailedRecording is the scheduled grace deletion of an AUDIO_CORRUPT
// or NO_SPEECH meeting (ADR-031 amendment). It is never exposed through an API route.
// The owner ID comes from the row's own partition key. Rows are deleted in
// one conditional transaction that refuses any user-authored data or relation;
// owned S3 objects are removed only after that commit, and cleanup failures
// are returned with their counts rather than swallowed.
func (s *MeetingService) DeleteFailedRecording(ctx context.Context, ownerID, meetingID string, mark model.SttFailureMark) (repository.SttFailedMeetingObjectsResult, error) {
	deleter, ok := s.repo.(sttFailedMeetingDeleter)
	if !ok {
		return repository.SttFailedMeetingObjectsResult{}, ErrSttDeletionUnsupported
	}
	if !SafeSttIdentifier(ownerID) || !sttMeetingUUID.MatchString(meetingID) || !model.IsDeletableSttFailure(mark.Code) {
		return repository.SttFailedMeetingObjectsResult{}, repository.ErrConditionFailed
	}
	rowsErr := deleter.DeleteSttFailedMeeting(ctx, ownerID, meetingID, mark)
	if rowsErr != nil && !errors.Is(rowsErr, repository.ErrSttDeletionPostCommit) {
		return repository.SttFailedMeetingObjectsResult{}, rowsErr
	}
	result, err := deleter.DeleteSttFailedMeetingObjects(ctx, ownerID, meetingID)
	if err != nil && !errors.Is(err, repository.ErrSttDeletionPostCommit) {
		// The rows are already gone, so every object-cleanup failure (listing
		// included) is post-commit: callers must count it as a committed delete.
		err = errors.Join(repository.ErrSttDeletionPostCommit, err)
	}
	return result, errors.Join(rowsErr, err)
}
