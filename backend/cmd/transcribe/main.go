package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecsTypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/transcribe"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

var (
	transcribeService *service.TranscribeService
	repo              *repository.DynamoDBRepository
	ecsClient         *ecs.Client
	whisperCluster    string
	whisperTaskDef    string
	whisperContainer  string
)

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("failed to load AWS config: %v", err)
	}

	dynamoClient := dynamodb.NewFromConfig(cfg)
	s3Client := s3.NewFromConfig(cfg)
	transcribeClient := transcribe.NewFromConfig(cfg)

	tableName := os.Getenv("TABLE_NAME")
	if tableName == "" {
		tableName = "ttobak-main"
	}
	outputBucket := os.Getenv("BUCKET_NAME")
	if outputBucket == "" {
		outputBucket = "ttobak-assets"
	}

	repo = repository.NewDynamoDBRepositoryWithS3(dynamoClient, tableName, s3Client, outputBucket)
	transcribeService = service.NewTranscribeService(transcribeClient, s3Client, repo, outputBucket)

	ecsClient = ecs.NewFromConfig(cfg)
	whisperCluster = os.Getenv("WHISPER_CLUSTER")
	whisperTaskDef = os.Getenv("WHISPER_TASK_DEF")
	whisperContainer = os.Getenv("WHISPER_CONTAINER")
	if whisperContainer == "" {
		whisperContainer = "whisper"
	}
}

// Handler processes EventBridge S3 events for new audio uploads
func Handler(ctx context.Context, raw json.RawMessage) error {
	var event model.EventBridgeS3Event
	if err := json.Unmarshal(raw, &event); err != nil {
		return fmt.Errorf("failed to unmarshal EventBridge event: %w", err)
	}

	bucket := event.Detail.Bucket.Name
	key := event.Detail.Object.Key

	// URL decode the key (handles both + and %XX encoding)
	if decoded, err := url.QueryUnescape(key); err == nil {
		key = decoded
	}

	log.Printf("Processing S3 event: bucket=%s, key=%s", bucket, key)

	// Only process audio files
	if !strings.HasPrefix(key, "audio/") {
		log.Printf("Skipping non-audio file: %s", key)
		return nil
	}

	// Skip checkpoint files (periodic saves during recording, not final audio)
	if strings.Contains(key, "checkpoint_") {
		log.Printf("Skipping checkpoint file: %s", key)
		return nil
	}

	// Skip progress files (cumulative checkpoint for crash recovery, not final audio)
	if strings.Contains(key, "recording_progress") {
		log.Printf("Skipping progress file: %s", key)
		return nil
	}

	// Skip realtime-aggregated audio (already transcribed in realtime by ECS whisper)
	if strings.Contains(key, "realtime_") {
		log.Printf("Skipping realtime audio file (already transcribed): %s", key)
		return nil
	}

	// Extract meeting ID from key
	// Expected format: audio/{userID}/{meetingID}/{filename}
	meetingID := service.ExtractMeetingIDFromAudioKey(key)
	if meetingID == "" {
		log.Printf("Could not extract meeting ID from key: %s", key)
		return nil
	}

	log.Printf("Starting transcription for meeting: %s", meetingID)

	// Read meeting record to check sttProvider selection
	meeting, err := repo.GetMeetingByID(ctx, meetingID)
	if err != nil {
		log.Printf("Failed to get meeting record: %v", err)
	}

	sttProvider := "transcribe"
	if meeting != nil && meeting.SttProvider != "" {
		sttProvider = meeting.SttProvider
	}

	// Extract userID from key: audio/{userID}/{meetingID}/{filename}
	parts := strings.Split(key, "/")
	userID := ""
	if len(parts) >= 3 {
		userID = parts[1]
	}

	var jobName string
	switch sttProvider {
	case "whisper":
		if whisperCluster == "" || whisperTaskDef == "" {
			log.Printf("Whisper not configured, falling back to Transcribe")
			jobName, err = transcribeService.StartTranscriptionJob(ctx, meetingID, bucket, key)
		} else {
			log.Printf("Using Whisper GPU for meeting: %s", meetingID)
			err = startWhisperTask(ctx, meetingID, userID, key)
			jobName = "whisper-ecs-" + meetingID
		}
	case "nova-sonic":
		log.Printf("Using Nova Sonic transcription for meeting: %s", meetingID)
		jobName, err = transcribeService.StartNovaSonicTranscription(ctx, meetingID, bucket, key)
	default:
		jobName, err = transcribeService.StartTranscriptionJob(ctx, meetingID, bucket, key)
	}
	if err != nil {
		log.Printf("Failed to start transcription job: %v", err)
		if meeting != nil {
			meeting.Status = model.StatusError
			repo.UpdateMeeting(ctx, meeting)
		}
		return fmt.Errorf("failed to start transcription job: %w", err)
	}

	log.Printf("Started transcription job: %s (provider=%s) for meeting: %s", jobName, sttProvider, meetingID)

	return nil
}

func startWhisperTask(ctx context.Context, meetingID, userID, audioKey string) error {
	result, err := ecsClient.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(whisperCluster),
		TaskDefinition: aws.String(whisperTaskDef),
		Count:          aws.Int32(1),
		CapacityProviderStrategy: []ecsTypes.CapacityProviderStrategyItem{
			{
				CapacityProvider: aws.String("ttobak-whisper-spot"),
				Weight:           1,
			},
		},
		Overrides: &ecsTypes.TaskOverride{
			ContainerOverrides: []ecsTypes.ContainerOverride{
				{
					Name: aws.String(whisperContainer),
					Environment: []ecsTypes.KeyValuePair{
						{Name: aws.String("AUDIO_KEY"), Value: aws.String(audioKey)},
						{Name: aws.String("MEETING_ID"), Value: aws.String(meetingID)},
						{Name: aws.String("USER_ID"), Value: aws.String(userID)},
					},
				},
			},
		},
	})
	if err != nil {
		return err
	}
	if len(result.Tasks) > 0 && result.Tasks[0].TaskArn != nil {
		log.Printf("ECS task started: %s", *result.Tasks[0].TaskArn)
	}
	if len(result.Failures) > 0 {
		log.Printf("ECS task placement failures: %v", result.Failures)
		return fmt.Errorf("ECS task placement failed: %s", *result.Failures[0].Reason)
	}
	return nil
}

func main() {
	lambda.Start(Handler)
}
