package service

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const MaxCropSourceBytes = 2 * 1024 * 1024 * 1024

func validateAudioCrop(source *model.Meeting, userID string, request model.AudioCropRequest) (string, error) {
	if _, err := uuid.Parse(request.RequestID); err != nil {
		return "", fmt.Errorf("%w: requestId must be a UUID", ErrInvalidInput)
	}
	if request.StartSeconds < 0 || request.EndSeconds <= request.StartSeconds || request.EndSeconds > 24*60*60 ||
		request.EndSeconds-request.StartSeconds > 6*60*60 {
		return "", fmt.Errorf("%w: select a positive range of at most six hours within the first 24 hours", ErrInvalidInput)
	}
	if source == nil {
		return "", ErrNotFound
	}
	if source.UserID != userID {
		return "", ErrForbidden
	}
	if source.AudioCrop.Active() || source.Status != model.StatusDone && source.Status != model.StatusError {
		return "", fmt.Errorf("%w: wait until the meeting finishes processing", ErrInvalidInput)
	}
	keys := source.GetEffectiveAudioKeys()
	if len(keys) != 1 || source.AudioPartCount > 1 {
		return "", fmt.Errorf("%w: cropping requires a single audio file", ErrInvalidInput)
	}
	key := keys[0]
	prefix := "audio/" + userID + "/" + source.MeetingID + "/"
	if !strings.HasPrefix(key, prefix) || path.Clean(key) != key || strings.ContainsAny(strings.TrimPrefix(key, prefix), "/\\") ||
		strings.Contains(key, "%") || len(key) == len(prefix) {
		return "", ErrForbidden
	}
	return key, nil
}

func (s *UploadService) SetAudioCropEnabled(enabled bool) { s.audioCropEnabled = enabled }
func (s *UploadService) AudioCropEnabled() bool           { return s.audioCropEnabled }

func (s *UploadService) CropMeeting(ctx context.Context, userID, meetingID string, request model.AudioCropRequest) (*model.Meeting, error) {
	if !s.audioCropEnabled || s.ebClient == nil {
		return nil, fmt.Errorf("%w: audio cropping is not available", ErrInvalidInput)
	}
	source, err := s.repo.MetadataView().GetMeeting(ctx, userID, meetingID)
	if err != nil {
		return nil, err
	}
	key, err := validateAudioCrop(source, userID, request)
	if err != nil {
		return nil, err
	}
	requestUUID, _ := uuid.Parse(request.RequestID)
	croppedID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(userID+"/"+meetingID+"/"+requestUUID.String())).String()
	existing, err := s.repo.MetadataView().GetMeeting(ctx, userID, croppedID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		crop := existing.AudioCrop
		if crop == nil || crop.SourceMeetingID != meetingID || crop.StartSeconds != request.StartSeconds || crop.EndSeconds != request.EndSeconds {
			return nil, repository.ErrConditionFailed
		}
		if crop.QueuedFor(existing.Status) {
			if err := s.publishAudioCrop(ctx, userID, croppedID); err != nil {
				return nil, err
			}
		}
		return existing, nil
	}
	head, err := s.s3Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucketName), Key: aws.String(key)})
	if err != nil {
		return nil, fmt.Errorf("read source audio metadata: %w", err)
	}
	if head.ContentLength == nil || *head.ContentLength <= 0 || *head.ContentLength > MaxCropSourceBytes || aws.ToString(head.ETag) == "" {
		return nil, fmt.Errorf("%w: source audio must be at most 2 GiB and have a revision", ErrInvalidInput)
	}
	if err := validateMeetingNotes(source.Notes); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	cropped := &model.Meeting{
		PK: model.PrefixUser + userID, SK: model.PrefixMeeting + croppedID,
		MeetingID: croppedID, UserID: userID, Title: source.Title + " (구간 편집)", Date: source.Date,
		Notes: source.Notes, NotesRevision: uuid.NewString(), Participants: source.Participants,
		SttProvider: "whisper", Status: model.StatusTranscribing,
		CreatedAt: now, UpdatedAt: now, GSI1PK: model.PrefixUser + userID, GSI1SK: now.Format(time.RFC3339), EntityType: "MEETING",
		AudioCrop: &model.AudioCrop{SourceMeetingID: meetingID, SourceKey: key, SourceETag: *head.ETag,
			StartSeconds: request.StartSeconds, EndSeconds: request.EndSeconds, State: "queued"},
	}
	if err := s.repo.CreateAudioCrop(ctx, source, cropped); err != nil {
		return nil, err
	}
	if err := s.publishAudioCrop(ctx, userID, croppedID); err != nil {
		return nil, err
	}
	return cropped, nil
}

func (s *UploadService) publishAudioCrop(ctx context.Context, userID, meetingID string) error {
	detail, err := json.Marshal(map[string]string{"userId": userID, "meetingId": meetingID})
	if err != nil {
		return err
	}
	result, err := s.ebClient.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: []ebtypes.PutEventsRequestEntry{{
		Source: aws.String("ttobak.audio"), DetailType: aws.String("AudioCropRequested"), Detail: aws.String(string(detail)),
	}}})
	if err != nil {
		return fmt.Errorf("queue audio crop: %w", err)
	}
	if result == nil || result.FailedEntryCount != 0 || len(result.Entries) != 1 ||
		aws.ToString(result.Entries[0].EventId) == "" || aws.ToString(result.Entries[0].ErrorCode) != "" {
		return fmt.Errorf("queue audio crop: event was not accepted; retry the same request")
	}
	return nil
}
