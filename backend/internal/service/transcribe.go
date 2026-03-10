package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/transcribe"
	"github.com/aws/aws-sdk-go-v2/service/transcribe/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// TranscribeService handles speech-to-text operations
type TranscribeService struct {
	transcribeClient *transcribe.Client
	s3Client         *s3.Client
	repo             *repository.DynamoDBRepository
	outputBucket     string
}

// NewTranscribeService creates a new transcribe service
func NewTranscribeService(
	transcribeClient *transcribe.Client,
	s3Client *s3.Client,
	repo *repository.DynamoDBRepository,
	outputBucket string,
) *TranscribeService {
	return &TranscribeService{
		transcribeClient: transcribeClient,
		s3Client:         s3Client,
		repo:             repo,
		outputBucket:     outputBucket,
	}
}

// StartTranscriptionJob starts an AWS Transcribe job for the given audio file
func (s *TranscribeService) StartTranscriptionJob(ctx context.Context, meetingID, bucket, key string) (string, error) {
	// Determine media format from key
	mediaFormat := s.getMediaFormat(key)
	if mediaFormat == "" {
		return "", fmt.Errorf("unsupported audio format")
	}

	// Create unique job name
	jobName := fmt.Sprintf("ttobak-%s", meetingID)
	mediaURI := fmt.Sprintf("s3://%s/%s", bucket, key)

	input := &transcribe.StartTranscriptionJobInput{
		TranscriptionJobName: aws.String(jobName),
		LanguageCode:         types.LanguageCodeKoKr, // Korean default
		Media: &types.Media{
			MediaFileUri: aws.String(mediaURI),
		},
		MediaFormat:      types.MediaFormat(mediaFormat),
		OutputBucketName: aws.String(s.outputBucket),
		OutputKey:        aws.String(fmt.Sprintf("transcripts/%s.json", meetingID)),
		Settings: &types.Settings{
			ShowSpeakerLabels: aws.Bool(true),
			MaxSpeakerLabels:  aws.Int32(10),
		},
	}

	_, err := s.transcribeClient.StartTranscriptionJob(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to start transcription job: %w", err)
	}

	// Update meeting status
	if err := s.updateMeetingStatus(ctx, meetingID, model.StatusTranscribing); err != nil {
		// Log but don't fail
		fmt.Printf("failed to update meeting status: %v\n", err)
	}

	return jobName, nil
}

// StartNovaSonicTranscription starts Nova Sonic transcription (placeholder - falls back to standard Transcribe)
// Nova Sonic would use Amazon Transcribe Streaming with the nova-sonic model
func (s *TranscribeService) StartNovaSonicTranscription(ctx context.Context, meetingID, bucket, key string) (string, error) {
	// Nova Sonic transcription would use streaming API
	// For now, this is a placeholder that uses standard Transcribe as fallback
	// In production, this would use Amazon Transcribe Streaming API with WebSocket

	jobName := fmt.Sprintf("ttobak-nova-%s", meetingID)
	mediaFormat := s.getMediaFormat(key)
	if mediaFormat == "" {
		return "", fmt.Errorf("unsupported audio format")
	}

	mediaURI := fmt.Sprintf("s3://%s/%s", bucket, key)

	input := &transcribe.StartTranscriptionJobInput{
		TranscriptionJobName: aws.String(jobName),
		LanguageCode:         types.LanguageCodeKoKr,
		Media: &types.Media{
			MediaFileUri: aws.String(mediaURI),
		},
		MediaFormat:      types.MediaFormat(mediaFormat),
		OutputBucketName: aws.String(s.outputBucket),
		OutputKey:        aws.String(fmt.Sprintf("transcripts/%s-nova.json", meetingID)),
		Settings: &types.Settings{
			ShowSpeakerLabels: aws.Bool(true),
			MaxSpeakerLabels:  aws.Int32(10),
		},
	}

	_, err := s.transcribeClient.StartTranscriptionJob(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to start nova transcription job: %w", err)
	}

	return jobName, nil
}

// GetTranscriptionResult retrieves the result of a completed transcription job
func (s *TranscribeService) GetTranscriptionResult(ctx context.Context, jobName string) (string, error) {
	result, err := s.transcribeClient.GetTranscriptionJob(ctx, &transcribe.GetTranscriptionJobInput{
		TranscriptionJobName: aws.String(jobName),
	})
	if err != nil {
		return "", fmt.Errorf("failed to get transcription job: %w", err)
	}

	if result.TranscriptionJob.TranscriptionJobStatus != types.TranscriptionJobStatusCompleted {
		return "", fmt.Errorf("transcription job not completed: %s", result.TranscriptionJob.TranscriptionJobStatus)
	}

	// The transcript is stored in S3, return the URI
	if result.TranscriptionJob.Transcript != nil && result.TranscriptionJob.Transcript.TranscriptFileUri != nil {
		return *result.TranscriptionJob.Transcript.TranscriptFileUri, nil
	}

	return "", fmt.Errorf("transcript URI not available")
}

// ProcessTranscriptionComplete handles the completion of a transcription job
func (s *TranscribeService) ProcessTranscriptionComplete(ctx context.Context, meetingID, transcriptText string, isNova bool) error {
	meeting, err := s.repo.GetMeetingByID(ctx, meetingID)
	if err != nil {
		return err
	}
	if meeting == nil {
		return fmt.Errorf("meeting not found: %s", meetingID)
	}

	if isNova {
		meeting.TranscriptB = transcriptText
	} else {
		meeting.TranscriptA = transcriptText
	}

	// Check if both transcripts are complete
	if meeting.TranscriptA != "" && meeting.TranscriptB != "" {
		meeting.Status = model.StatusSummarizing
	}

	return s.repo.UpdateMeeting(ctx, meeting)
}

// getMediaFormat determines the media format from the file key
func (s *TranscribeService) getMediaFormat(key string) string {
	lower := strings.ToLower(key)
	switch {
	case strings.HasSuffix(lower, ".mp3"):
		return "mp3"
	case strings.HasSuffix(lower, ".mp4"):
		return "mp4"
	case strings.HasSuffix(lower, ".wav"):
		return "wav"
	case strings.HasSuffix(lower, ".flac"):
		return "flac"
	case strings.HasSuffix(lower, ".ogg"):
		return "ogg"
	case strings.HasSuffix(lower, ".webm"):
		return "webm"
	case strings.HasSuffix(lower, ".m4a"):
		return "mp4"
	default:
		return ""
	}
}

// updateMeetingStatus updates the meeting status in DynamoDB
func (s *TranscribeService) updateMeetingStatus(ctx context.Context, meetingID, status string) error {
	meeting, err := s.repo.GetMeetingByID(ctx, meetingID)
	if err != nil {
		return err
	}
	if meeting == nil {
		return fmt.Errorf("meeting not found: %s", meetingID)
	}

	meeting.Status = status
	return s.repo.UpdateMeeting(ctx, meeting)
}

// ExtractMeetingIDFromAudioKey extracts the meeting ID from an S3 key
// Expected format: audio/{userID}/{meetingID}/{filename}
func ExtractMeetingIDFromAudioKey(key string) string {
	parts := strings.Split(key, "/")
	if len(parts) >= 3 && parts[0] == "audio" {
		return parts[2]
	}
	return ""
}
