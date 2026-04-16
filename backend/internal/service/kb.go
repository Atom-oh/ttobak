package service

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagent"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
)

// KBService handles Knowledge Base operations
type KBService struct {
	s3Client           *s3.Client
	presignClient      *s3.PresignClient
	bedrockAgentClient *bedrockagent.Client
	kbBucketName       string
	assetsBucketName   string // Source bucket for cross-bucket copy
	kbID               string // Bedrock Knowledge Base ID
	dataSourceID       string // Bedrock Data Source ID
}

// NewKBService creates a new KB service
func NewKBService(
	s3Client *s3.Client,
	bedrockAgentClient *bedrockagent.Client,
	kbBucketName string,
	kbID string,
	dataSourceID string,
) *KBService {
	presignClient := s3.NewPresignClient(s3Client)
	return &KBService{
		s3Client:           s3Client,
		presignClient:      presignClient,
		bedrockAgentClient: bedrockAgentClient,
		kbBucketName:       kbBucketName,
		kbID:               kbID,
		dataSourceID:       dataSourceID,
	}
}

// SetAssetsBucketName sets the source bucket name for cross-bucket copy operations.
func (s *KBService) SetAssetsBucketName(name string) {
	s.assetsBucketName = name
}

// GetPresignedURL generates a presigned URL for KB file upload
func (s *KBService) GetPresignedURL(ctx context.Context, userID, fileName, fileType string) (*model.KBUploadResponse, error) {
	// Sanitize filename
	safeName := strings.ReplaceAll(fileName, " ", "_")
	safeName = strings.ReplaceAll(safeName, "/", "_")
	safeName = strings.ReplaceAll(safeName, "\\", "_")

	// Add timestamp for uniqueness
	timestamp := time.Now().UnixMilli()
	s3Key := fmt.Sprintf("kb/%s/%d_%s", userID, timestamp, safeName)

	expiresIn := 1 * time.Hour
	presignedURL, err := s.presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.kbBucketName),
		Key:         aws.String(s3Key),
		ContentType: aws.String(fileType),
	}, s3.WithPresignExpires(expiresIn))
	if err != nil {
		return nil, fmt.Errorf("failed to generate presigned URL: %w", err)
	}

	return &model.KBUploadResponse{
		UploadURL: presignedURL.URL,
		Key:       s3Key,
		ExpiresIn: int(expiresIn.Seconds()),
	}, nil
}

// SyncKB triggers Bedrock KB ingestion job
func (s *KBService) SyncKB(ctx context.Context, userID string) (*model.KBSyncResponse, error) {
	if s.bedrockAgentClient == nil || s.kbID == "" || s.dataSourceID == "" {
		return &model.KBSyncResponse{
			Status:  "skipped",
			Message: "Knowledge Base not configured",
		}, nil
	}

	result, err := s.bedrockAgentClient.StartIngestionJob(ctx, &bedrockagent.StartIngestionJobInput{
		KnowledgeBaseId: aws.String(s.kbID),
		DataSourceId:    aws.String(s.dataSourceID),
		Description:     aws.String(fmt.Sprintf("Sync triggered by user %s", userID)),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to start ingestion job: %w", err)
	}

	jobID := ""
	if result.IngestionJob != nil && result.IngestionJob.IngestionJobId != nil {
		jobID = *result.IngestionJob.IngestionJobId
	}

	return &model.KBSyncResponse{
		Status:  "started",
		JobID:   jobID,
		Message: "Knowledge Base sync started",
	}, nil
}

// ListFiles lists files in user's KB prefix
func (s *KBService) ListFiles(ctx context.Context, userID string) (*model.KBFilesResponse, error) {
	prefix := fmt.Sprintf("kb/%s/", userID)

	result, err := s.s3Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.kbBucketName),
		Prefix: aws.String(prefix),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list KB files: %w", err)
	}

	files := make([]model.KBFileResponse, 0, len(result.Contents))
	for _, obj := range result.Contents {
		if obj.Key == nil {
			continue
		}
		// Extract filename from key (remove prefix)
		fileName := strings.TrimPrefix(*obj.Key, prefix)
		if fileName == "" {
			continue
		}

		// Use the key as fileId (base64 or just the key)
		fileID := filepath.Base(*obj.Key)

		var size int64
		if obj.Size != nil {
			size = *obj.Size
		}

		var lastModified string
		if obj.LastModified != nil {
			lastModified = obj.LastModified.Format(time.RFC3339)
		}

		// Infer file type from extension
		fileType := "application/octet-stream"
		ext := strings.ToLower(filepath.Ext(fileName))
		switch ext {
		case ".pdf":
			fileType = "application/pdf"
		case ".md":
			fileType = "text/markdown"
		case ".ppt", ".pptx":
			fileType = "application/vnd.ms-powerpoint"
		case ".doc", ".docx":
			fileType = "application/msword"
		}

		files = append(files, model.KBFileResponse{
			FileID:       fileID,
			FileName:     fileName,
			FileType:     fileType,
			Size:         size,
			LastModified: lastModified,
		})
	}

	return &model.KBFilesResponse{Files: files}, nil
}

// DeleteFile deletes a KB file
func (s *KBService) DeleteFile(ctx context.Context, userID, fileID string) error {
	// fileID is the filename part, reconstruct the full key
	// We need to find the exact key
	prefix := fmt.Sprintf("kb/%s/", userID)

	// List to find the exact file
	result, err := s.s3Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.kbBucketName),
		Prefix: aws.String(prefix),
	})
	if err != nil {
		return fmt.Errorf("failed to list KB files: %w", err)
	}

	// Find matching file
	var keyToDelete string
	for _, obj := range result.Contents {
		if obj.Key != nil && strings.HasSuffix(*obj.Key, fileID) {
			keyToDelete = *obj.Key
			break
		}
	}

	if keyToDelete == "" {
		return fmt.Errorf("file not found")
	}

	_, err = s.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.kbBucketName),
		Key:    aws.String(keyToDelete),
	})
	if err != nil {
		return fmt.Errorf("failed to delete KB file: %w", err)
	}

	return nil
}

// CopyAttachmentToKB copies a file from the assets bucket to the KB bucket.
// sourceKey is the S3 key in the assets bucket (e.g. "files/{userId}/{meetingId}/{filename}").
// The file is placed under "kb/{userID}/{filename}" in the KB bucket.
func (s *KBService) CopyAttachmentToKB(ctx context.Context, userID, sourceKey string) error {
	if s.assetsBucketName == "" {
		return fmt.Errorf("assets bucket not configured")
	}
	if s.kbBucketName == "" {
		return fmt.Errorf("KB bucket not configured")
	}

	// Extract filename from source key
	parts := strings.Split(sourceKey, "/")
	fileName := parts[len(parts)-1]
	if fileName == "" {
		return fmt.Errorf("invalid source key: %s", sourceKey)
	}

	destKey := fmt.Sprintf("kb/%s/%s", userID, fileName)
	copySource := fmt.Sprintf("%s/%s", s.assetsBucketName, sourceKey)

	_, err := s.s3Client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(s.kbBucketName),
		Key:        aws.String(destKey),
		CopySource: aws.String(copySource),
	})
	if err != nil {
		return fmt.Errorf("failed to copy attachment to KB: %w", err)
	}

	return nil
}
