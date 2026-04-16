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
)

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("failed to load AWS config: %v", err)
	}

	bedrockRegion := os.Getenv("BEDROCK_REGION")
	if bedrockRegion == "" {
		bedrockRegion = "us-west-2"
	}

	bedrockCfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion(bedrockRegion))
	if err != nil {
		log.Fatalf("failed to load Bedrock config: %v", err)
	}

	dynamoClient := dynamodb.NewFromConfig(cfg)
	s3Client := s3.NewFromConfig(cfg)
	bedrockClient := bedrockruntime.NewFromConfig(bedrockCfg)

	tableName := os.Getenv("TABLE_NAME")
	if tableName == "" {
		tableName = "ttobak-main"
	}

	repo = repository.NewDynamoDBRepository(dynamoClient, tableName)
	bedrockService = service.NewBedrockService(bedrockClient, s3Client, repo)
}

// imageUploadEvent represents the custom EventBridge event from upload/complete
type imageUploadEvent struct {
	Detail struct {
		Bucket    string `json:"bucket"`
		Key       string `json:"key"`
		MeetingID string `json:"meetingId"`
		UserID    string `json:"userId"`
	} `json:"detail"`
}

// Handler processes custom EventBridge events for completed image uploads
func Handler(ctx context.Context, raw json.RawMessage) error {
	var event imageUploadEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return fmt.Errorf("failed to unmarshal EventBridge event: %w", err)
	}

	bucket := event.Detail.Bucket
	key := event.Detail.Key
	userID := event.Detail.UserID
	meetingID := event.Detail.MeetingID

	// URL decode the key (handles both + and %XX encoding)
	if decoded, err := url.QueryUnescape(key); err == nil {
		key = decoded
	}

	log.Printf("Processing image: bucket=%s, key=%s", bucket, key)

	// Only process image files
	if !strings.HasPrefix(key, "images/") {
		log.Printf("Skipping non-image file: %s", key)
		return nil
	}

	if userID == "" || meetingID == "" {
		// Fallback: extract from key path
		userID, meetingID = service.ExtractInfoFromImageKey(key)
	}
	if userID == "" || meetingID == "" {
		log.Printf("Could not extract user/meeting ID from key: %s", key)
		return nil
	}

	log.Printf("Processing image for user %s, meeting %s", userID, meetingID)

	// Analyze the image with Bedrock Vision
	classification, analysis, err := bedrockService.AnalyzeImage(ctx, bucket, key)
	if err != nil {
		log.Printf("Failed to analyze image: %v", err)
		return nil
	}

	log.Printf("Image classified as: %s", classification)

	// Find and update the attachment record
	if err := updateAttachmentByKey(ctx, meetingID, key, classification, analysis); err != nil {
		log.Printf("Failed to update attachment: %v", err)
		return nil
	}

	log.Printf("Successfully analyzed image: %s", key)
	return nil
}

func updateAttachmentByKey(ctx context.Context, meetingID, originalKey, attachType, processedContent string) error {
	attachments, err := repo.ListAttachments(ctx, meetingID)
	if err != nil {
		return err
	}

	for _, att := range attachments {
		if att.OriginalKey == originalKey {
			att.Type = attachType
			att.ProcessedContent = processedContent
			att.Status = model.AttachStatusDone
			return repo.UpdateAttachment(ctx, &att)
		}
	}

	log.Printf("No attachment found with originalKey=%s for meeting=%s", originalKey, meetingID)
	return nil
}

func main() {
	lambda.Start(Handler)
}
