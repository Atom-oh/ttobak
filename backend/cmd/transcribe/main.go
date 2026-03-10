package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/transcribe"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

var (
	transcribeService *service.TranscribeService
	repo              *repository.DynamoDBRepository
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

	repo = repository.NewDynamoDBRepository(dynamoClient, tableName)
	transcribeService = service.NewTranscribeService(transcribeClient, s3Client, repo, outputBucket)
}

// Handler processes EventBridge S3 events for new audio uploads
func Handler(ctx context.Context, raw json.RawMessage) error {
	var event model.EventBridgeS3Event
	if err := json.Unmarshal(raw, &event); err != nil {
		return fmt.Errorf("failed to unmarshal EventBridge event: %w", err)
	}

	bucket := event.Detail.Bucket.Name
	key := event.Detail.Object.Key

	// URL decode the key
	key = strings.ReplaceAll(key, "+", " ")

	log.Printf("Processing S3 event: bucket=%s, key=%s", bucket, key)

	// Only process audio files
	if !strings.HasPrefix(key, "audio/") {
		log.Printf("Skipping non-audio file: %s", key)
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

	// Start AWS Transcribe job
	jobName, err := transcribeService.StartTranscriptionJob(ctx, meetingID, bucket, key)
	if err != nil {
		log.Printf("Failed to start transcription job: %v", err)
		meeting, _ := repo.GetMeetingByID(ctx, meetingID)
		if meeting != nil {
			meeting.Status = model.StatusError
			repo.UpdateMeeting(ctx, meeting)
		}
		return fmt.Errorf("failed to start transcription job: %w", err)
	}

	log.Printf("Started transcription job: %s for meeting: %s", jobName, meetingID)

	// Start Nova Sonic transcription as well (A/B test)
	novaJobName, err := transcribeService.StartNovaSonicTranscription(ctx, meetingID, bucket, key)
	if err != nil {
		log.Printf("Failed to start Nova Sonic transcription (falling back to standard only): %v", err)
	} else {
		log.Printf("Started Nova Sonic transcription job: %s for meeting: %s", novaJobName, meetingID)
	}

	return nil
}

func main() {
	lambda.Start(Handler)
}
