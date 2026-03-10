package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

var (
	bedrockService *service.BedrockService
	repo           *repository.DynamoDBRepository
	s3Client       *s3.Client
)

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("failed to load AWS config: %v", err)
	}

	// Bedrock may require a specific region
	bedrockRegion := os.Getenv("BEDROCK_REGION")
	if bedrockRegion == "" {
		bedrockRegion = "us-west-2"
	}

	bedrockCfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion(bedrockRegion))
	if err != nil {
		log.Fatalf("failed to load Bedrock config: %v", err)
	}

	dynamoClient := dynamodb.NewFromConfig(cfg)
	s3Client = s3.NewFromConfig(cfg)
	bedrockClient := bedrockruntime.NewFromConfig(bedrockCfg)

	tableName := os.Getenv("TABLE_NAME")
	if tableName == "" {
		tableName = "ttobak-main"
	}

	repo = repository.NewDynamoDBRepository(dynamoClient, tableName)
	bedrockService = service.NewBedrockService(bedrockClient, s3Client, repo)
}

// TranscribeResult represents the AWS Transcribe output JSON structure
type TranscribeResult struct {
	Results struct {
		Transcripts []struct {
			Transcript string `json:"transcript"`
		} `json:"transcripts"`
	} `json:"results"`
	Status string `json:"status"`
}

// Handler processes EventBridge S3 events for completed transcriptions
func Handler(ctx context.Context, raw json.RawMessage) error {
	var event model.EventBridgeS3Event
	if err := json.Unmarshal(raw, &event); err != nil {
		return fmt.Errorf("failed to unmarshal EventBridge event: %w", err)
	}

	bucket := event.Detail.Bucket.Name
	key := event.Detail.Object.Key

	// URL decode the key
	key = strings.ReplaceAll(key, "+", " ")

	log.Printf("Processing transcript: bucket=%s, key=%s", bucket, key)

	// Only process transcript files
	if !strings.HasPrefix(key, "transcripts/") {
		log.Printf("Skipping non-transcript file: %s", key)
		return nil
	}

	// Extract meeting ID from key
	// Expected format: transcripts/{meetingID}.json or transcripts/{meetingID}-nova.json
	meetingID := extractMeetingIDFromTranscriptKey(key)
	if meetingID == "" {
		log.Printf("Could not extract meeting ID from key: %s", key)
		return nil
	}

	isNova := strings.Contains(key, "-nova.json")

	// Download and parse transcript
	transcript, err := downloadAndParseTranscript(ctx, bucket, key)
	if err != nil {
		log.Printf("Failed to parse transcript: %v", err)
		return nil
	}

	// Update meeting with transcript
	err = updateMeetingTranscript(ctx, meetingID, transcript, isNova)
	if err != nil {
		log.Printf("Failed to update meeting with transcript: %v", err)
		return nil
	}

	log.Printf("Updated meeting %s with transcript (nova=%v)", meetingID, isNova)

	// Check if we should generate summary
	meeting, err := repo.GetMeetingByID(ctx, meetingID)
	if err != nil {
		log.Printf("Failed to get meeting: %v", err)
		return nil
	}

	// Generate summary if at least one transcript is available
	if meeting != nil && (meeting.TranscriptA != "" || meeting.TranscriptB != "") {
		if meeting.Status == model.StatusSummarizing || meeting.Status == model.StatusTranscribing {
			meeting.Status = model.StatusSummarizing
			repo.UpdateMeeting(ctx, meeting)

			content, err := bedrockService.SummarizeTranscript(ctx, meetingID)
			if err != nil {
				log.Printf("Failed to generate summary: %v", err)
				meeting.Status = model.StatusError
				repo.UpdateMeeting(ctx, meeting)
				return nil
			}

			log.Printf("Generated content for meeting %s: %d characters", meetingID, len(content))
		}
	}

	return nil
}

func downloadAndParseTranscript(ctx context.Context, bucket, key string) (string, error) {
	result, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return "", fmt.Errorf("failed to download transcript: %w", err)
	}
	defer result.Body.Close()

	data, err := io.ReadAll(result.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read transcript: %w", err)
	}

	var transcribeResult TranscribeResult
	if err := json.Unmarshal(data, &transcribeResult); err != nil {
		return "", fmt.Errorf("failed to parse transcript JSON: %w", err)
	}

	if len(transcribeResult.Results.Transcripts) > 0 {
		return transcribeResult.Results.Transcripts[0].Transcript, nil
	}

	return "", fmt.Errorf("no transcript found in result")
}

func extractMeetingIDFromTranscriptKey(key string) string {
	// Expected format: transcripts/{meetingID}.json or transcripts/{meetingID}-nova.json
	key = strings.TrimPrefix(key, "transcripts/")
	key = strings.TrimSuffix(key, ".json")
	key = strings.TrimSuffix(key, "-nova")
	return key
}

func updateMeetingTranscript(ctx context.Context, meetingID, transcript string, isNova bool) error {
	meeting, err := repo.GetMeetingByID(ctx, meetingID)
	if err != nil {
		return err
	}
	if meeting == nil {
		return fmt.Errorf("meeting not found")
	}

	if isNova {
		meeting.TranscriptB = transcript
	} else {
		meeting.TranscriptA = transcript
	}

	return repo.UpdateMeeting(ctx, meeting)
}

func main() {
	lambda.Start(Handler)
}
