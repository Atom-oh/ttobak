package service

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagent"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
)

// ErrKBFileAmbiguous means several KB objects share the requested file ID, so a
// delete by that ID cannot know which one the caller meant.
var ErrKBFileAmbiguous = errors.New("more than one KB file has this name")

// kbFileID is the file ID ListFiles exposes and DeleteFile accepts: the key's
// final path segment.
func kbFileID(key string) string { return path.Base(key) }

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

	pages := s3.NewListObjectsV2Paginator(s.s3Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.kbBucketName),
		Prefix: aws.String(prefix),
	})

	files := make([]model.KBFileResponse, 0)
	for pages.HasMorePages() {
		result, err := pages.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list KB files: %w", err)
		}
		for _, obj := range result.Contents {
			if obj.Key == nil {
				continue
			}
			// Extract filename from key (remove prefix)
			fileName := strings.TrimPrefix(*obj.Key, prefix)
			if fileName == "" {
				continue
			}

			fileID := kbFileID(*obj.Key)

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
	}

	return &model.KBFilesResponse{Files: files}, nil
}

// DeleteFile deletes the caller's KB object whose file ID is exactly fileID.
// Every listing page is searched. A name shared by more than one object is
// refused instead of deleting whichever key happened to be listed first.
func (s *KBService) DeleteFile(ctx context.Context, userID, fileID string) error {
	if fileID == "" || fileID == "." || fileID == ".." || strings.ContainsAny(fileID, `/\`) {
		return ErrInvalidInput
	}
	prefix := fmt.Sprintf("kb/%s/", userID)
	pages := s3.NewListObjectsV2Paginator(s.s3Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.kbBucketName),
		Prefix: aws.String(prefix),
	})
	var matches []string
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("failed to list KB files: %w", err)
		}
		for _, obj := range page.Contents {
			if obj.Key != nil && kbFileID(*obj.Key) == fileID {
				matches = append(matches, *obj.Key)
			}
		}
	}
	switch len(matches) {
	case 0:
		return ErrNotFound
	case 1:
	default:
		return ErrKBFileAmbiguous
	}

	if _, err := s.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.kbBucketName),
		Key:    aws.String(matches[0]),
	}); err != nil {
		return fmt.Errorf("failed to delete KB file: %w", err)
	}
	return nil
}

// validateAssetOwnership ensures sourceKey is an asset owned by userID. Every
// asset key produced by the upload service is
// "{audio|images|files}/{userID}/{meetingID}/{filename}", so the owner segment
// (second path component) MUST equal the caller's userID. Path traversal is
// rejected outright. Returns ErrForbidden on any mismatch.
func validateAssetOwnership(sourceKey, userID string) error {
	if userID == "" || strings.Contains(sourceKey, "..") {
		return fmt.Errorf("%w: invalid source key", ErrForbidden)
	}
	for _, prefix := range []string{"files/", "audio/", "images/"} {
		if strings.HasPrefix(sourceKey, prefix+userID+"/") {
			return nil
		}
	}
	return fmt.Errorf("%w: source key does not belong to caller", ErrForbidden)
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

	// Ownership gate (trust boundary): the API must not let an authenticated
	// user copy another user's asset into their own KB.
	if err := validateAssetOwnership(sourceKey, userID); err != nil {
		return err
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
