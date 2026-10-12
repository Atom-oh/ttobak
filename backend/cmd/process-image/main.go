package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path"
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
	bucketName     string
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
	bucketName = os.Getenv("BUCKET_NAME")

	repo = repository.NewDynamoDBRepository(dynamoClient, tableName)
	bedrockService = service.NewBedrockService(bedrockClient, s3Client, repo)
}

// imageUploadEvent represents the custom EventBridge event from upload/complete
type imageUploadEvent struct {
	Source     string `json:"source"`
	DetailType string `json:"detail-type"`
	Detail     struct {
		Bucket    string `json:"bucket"`
		Key       string `json:"key"`
		MeetingID string `json:"meetingId"`
		UserID    string `json:"userId"`
	} `json:"detail"`
}

type imageAnalyzer interface {
	AnalyzeImage(context.Context, string, string) (string, string, error)
}

type imageRepository interface {
	ListAttachments(context.Context, string) ([]model.Attachment, error)
	SaveImageResult(context.Context, *model.Attachment, string, string) error
}

// Handler processes custom EventBridge events for completed image uploads
func Handler(ctx context.Context, raw json.RawMessage) error {
	return processImage(ctx, raw, bucketName, bedrockService, repo)
}

func processImage(ctx context.Context, raw json.RawMessage, expectedBucket string, analyzer imageAnalyzer, store imageRepository) error {
	// Errors reach Lambda's asynchronous failure destination. Keep them closed:
	// provider responses, object names and generated content must not enter logs.
	if len(raw) > 32*1024 {
		return errors.New("image event too large")
	}
	var event imageUploadEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return errors.New("invalid image event")
	}
	if event.Source != "ttobak.upload" || event.DetailType != "ImageUploadCompleted" {
		return nil
	}
	bucket := event.Detail.Bucket
	key := event.Detail.Key
	userID := event.Detail.UserID
	meetingID := event.Detail.MeetingID
	// The upload-complete producer emits the literal key, not a URL-encoded key.
	prefix := "images/" + userID + "/" + meetingID + "/"
	if expectedBucket == "" || bucket != expectedBucket || userID == "" || meetingID == "" ||
		strings.ContainsAny(userID+meetingID, "/\\") || !strings.HasPrefix(key, prefix) ||
		len(key) <= len(prefix) || path.Clean(key) != key || strings.Contains(key, "\\") {
		return errors.New("invalid image identity")
	}
	attachments, err := store.ListAttachments(ctx, meetingID)
	if err != nil {
		return errors.New("image attachment read failed")
	}
	for _, att := range attachments {
		if att.OriginalKey != key || att.MeetingID != meetingID || att.UserID != userID {
			continue
		}
		if att.Status != model.AttachStatusUploaded && att.Status != model.AttachStatusProcessing {
			return nil
		}
		classification, analysis, err := analyzer.AnalyzeImage(ctx, bucket, key)
		if err != nil {
			return errors.New("image analysis failed")
		}
		if len(classification)+len(analysis) > 256*1024 {
			return errors.New("image result too large")
		}
		err = store.SaveImageResult(ctx, &att, classification, analysis)
		if errors.Is(err, repository.ErrConditionFailed) {
			return nil // Removed, changed or completed while the model was running.
		}
		if err != nil {
			return errors.New("image publication failed")
		}
		return nil
	}
	return nil // Deleted attachments and unrelated/stale deliveries stay ignored.
}

func main() {
	lambda.Start(Handler)
}
