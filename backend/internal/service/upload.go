package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// UploadService handles file upload operations
type UploadService struct {
	s3Client      *s3.Client
	presignClient *s3.PresignClient
	repo          *repository.DynamoDBRepository
	bucketName    string
}

// NewUploadService creates a new upload service
func NewUploadService(
	s3Client *s3.Client,
	repo *repository.DynamoDBRepository,
	bucketName string,
) *UploadService {
	presignClient := s3.NewPresignClient(s3Client)
	return &UploadService{
		s3Client:      s3Client,
		presignClient: presignClient,
		repo:          repo,
		bucketName:    bucketName,
	}
}

// GeneratePresignedUploadURL generates a presigned URL for file upload
func (s *UploadService) GeneratePresignedUploadURL(
	ctx context.Context,
	userID string,
	req *model.PresignedURLRequest,
) (*model.PresignedURLResponse, error) {
	// Generate S3 key based on category
	var s3Key string
	switch req.Category {
	case "audio":
		meetingID := uuid.New().String()
		s3Key = fmt.Sprintf("audio/%s/%s/%s", userID, meetingID, s.sanitizeFileName(req.FileName))
	case "image":
		if req.MeetingID == "" {
			return nil, fmt.Errorf("meetingId is required for image uploads")
		}
		s3Key = fmt.Sprintf("images/%s/%s/%s", userID, req.MeetingID, s.sanitizeFileName(req.FileName))
	default:
		return nil, fmt.Errorf("unsupported category: %s", req.Category)
	}

	// Generate presigned URL (valid for 1 hour)
	expiresIn := 1 * time.Hour
	presignedURL, err := s.presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucketName),
		Key:         aws.String(s3Key),
		ContentType: aws.String(req.FileType),
	}, s3.WithPresignExpires(expiresIn))
	if err != nil {
		return nil, fmt.Errorf("failed to generate presigned URL: %w", err)
	}

	return &model.PresignedURLResponse{
		UploadURL: presignedURL.URL,
		Key:       s3Key,
		ExpiresIn: int(expiresIn.Seconds()),
	}, nil
}

// CompleteUpload handles upload completion notification
func (s *UploadService) CompleteUpload(ctx context.Context, userID string, req *model.UploadCompleteRequest) error {
	switch req.Category {
	case "audio":
		// Update meeting with audio key
		meeting, err := s.repo.GetMeetingByID(ctx, req.MeetingID)
		if err != nil {
			return err
		}
		if meeting == nil {
			return fmt.Errorf("meeting not found")
		}
		meeting.AudioKey = req.Key
		meeting.Status = model.StatusTranscribing
		return s.repo.UpdateMeeting(ctx, meeting)

	case "image":
		// Create attachment record
		// Determine type based on filename or default
		attachType := model.AttachTypePhoto
		_, err := s.repo.CreateAttachment(ctx, req.MeetingID, userID, req.Key, attachType)
		return err

	default:
		return fmt.Errorf("unsupported category: %s", req.Category)
	}
}

// GeneratePresignedDownloadURL generates a presigned URL for file download
func (s *UploadService) GeneratePresignedDownloadURL(
	ctx context.Context,
	s3Key string,
) (string, error) {
	expiresIn := 1 * time.Hour
	presignedURL, err := s.presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(s3Key),
	}, s3.WithPresignExpires(expiresIn))
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned URL: %w", err)
	}

	return presignedURL.URL, nil
}

// sanitizeFileName removes or replaces invalid characters from filenames
func (s *UploadService) sanitizeFileName(fileName string) string {
	// Replace spaces with underscores
	fileName = strings.ReplaceAll(fileName, " ", "_")

	// Remove any path separators
	fileName = strings.ReplaceAll(fileName, "/", "_")
	fileName = strings.ReplaceAll(fileName, "\\", "_")

	// Add timestamp prefix to ensure uniqueness
	timestamp := time.Now().UnixMilli()
	return fmt.Sprintf("%d_%s", timestamp, fileName)
}

// ExtractInfoFromImageKey extracts user and meeting info from image S3 key
// Expected format: images/{userID}/{meetingID}/{filename}
func ExtractInfoFromImageKey(key string) (userID, meetingID string) {
	parts := strings.Split(key, "/")
	if len(parts) >= 4 && parts[0] == "images" {
		return parts[1], parts[2]
	}
	return "", ""
}
