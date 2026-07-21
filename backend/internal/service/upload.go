package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// UploadService handles file upload operations
type UploadService struct {
	s3Client      *s3.Client
	presignClient *s3.PresignClient
	ebClient      *eventbridge.Client
	repo          *repository.DynamoDBRepository
	bucketName    string
}

// NewUploadService creates a new upload service
func NewUploadService(
	s3Client *s3.Client,
	repo *repository.DynamoDBRepository,
	bucketName string,
	ebClient ...*eventbridge.Client,
) *UploadService {
	presignClient := s3.NewPresignClient(s3Client)
	svc := &UploadService{
		s3Client:      s3Client,
		presignClient: presignClient,
		repo:          repo,
		bucketName:    bucketName,
	}
	if len(ebClient) > 0 {
		svc.ebClient = ebClient[0]
	}
	return svc
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
		meetingID := req.MeetingID
		if meetingID == "" {
			meetingID = uuid.New().String()
		}
		if req.TotalParts > 1 {
			if req.TotalParts > model.MaxAudioParts {
				return nil, fmt.Errorf("totalParts exceeds maximum of %d", model.MaxAudioParts)
			}
			if req.PartIndex < 0 || req.PartIndex >= req.TotalParts {
				return nil, fmt.Errorf("partIndex %d out of range [0, %d)", req.PartIndex, req.TotalParts)
			}
			s3Key = fmt.Sprintf("audio/%s/%s/part_%03d_%s", userID, meetingID, req.PartIndex, s.sanitizeFileName(req.FileName))
		} else {
			s3Key = fmt.Sprintf("audio/%s/%s/%s", userID, meetingID, s.sanitizeFileName(req.FileName))
		}
	case "image":
		if req.MeetingID == "" {
			return nil, fmt.Errorf("meetingId is required for image uploads")
		}
		s3Key = fmt.Sprintf("images/%s/%s/%s", userID, req.MeetingID, s.sanitizeFileName(req.FileName))
	case "file":
		if req.MeetingID == "" {
			return nil, fmt.Errorf("meetingId is required for file uploads")
		}
		s3Key = fmt.Sprintf("files/%s/%s/%s", userID, req.MeetingID, s.sanitizeFileName(req.FileName))
	case "doc":
		// No meetingId -- a slide document isn't tied to a meeting. The
		// document create call itself (accountApi/docApi put with this
		// fileKey) is the "upload complete" record; there's no separate
		// /api/upload/complete step for this category.
		s3Key = fmt.Sprintf("docs/%s/%s", userID, s.sanitizeFileName(req.FileName))
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
	// Verify meeting ownership before completing upload
	meeting, err := s.repo.GetMeetingByID(ctx, req.MeetingID)
	if err != nil {
		return err
	}
	if meeting == nil {
		return fmt.Errorf("meeting not found")
	}
	if meeting.UserID != userID {
		return fmt.Errorf("forbidden: you do not own this meeting")
	}

	switch req.Category {
	case "audio":
		if req.TotalParts > 1 {
			if req.TotalParts > model.MaxAudioParts {
				return fmt.Errorf("totalParts exceeds maximum of %d", model.MaxAudioParts)
			}
			if req.PartIndex < 0 || req.PartIndex >= req.TotalParts {
				return fmt.Errorf("partIndex %d out of range [0, %d)", req.PartIndex, req.TotalParts)
			}
			// Lazy pre-allocation: create empty audioKeys list on first multi-file call
			if err := s.repo.PreAllocateAudioKeys(ctx, meeting.UserID, meeting.MeetingID, req.TotalParts); err != nil {
				return err
			}
			// Idempotent index-based set
			return s.repo.SetAudioKeyAtIndex(ctx, meeting.UserID, meeting.MeetingID, req.Key, req.PartIndex)
		}
		// Single-file: existing flow
		return s.repo.UpdateMeetingFields(ctx, meeting.UserID, meeting.MeetingID, map[string]interface{}{
			"audioKey": req.Key,
			"status":   model.StatusTranscribing,
		})

	case "image":
		// Create attachment record
		attachType := model.AttachTypePhoto
		att, err := s.repo.CreateAttachment(ctx, req.MeetingID, userID, req.Key, attachType)
		if err != nil {
			return err
		}
		// Store file metadata if provided
		if req.FileName != "" || req.FileSize > 0 || req.MimeType != "" {
			att.FileName = req.FileName
			att.FileSize = req.FileSize
			att.MimeType = req.MimeType
			if err := s.repo.UpdateAttachment(ctx, att); err != nil {
				return err
			}
		}
		// Emit custom EventBridge event so process-image Lambda runs
		// AFTER the attachment record exists in DynamoDB
		return s.emitImageUploadEvent(ctx, req.MeetingID, userID, req.Key)

	case "file":
		// Create attachment record — no Bedrock processing, mark as done immediately
		attachType := inferAttachTypeFromMime(req.MimeType)
		att, err := s.repo.CreateAttachment(ctx, req.MeetingID, userID, req.Key, attachType)
		if err != nil {
			return err
		}
		att.FileName = req.FileName
		att.FileSize = req.FileSize
		att.MimeType = req.MimeType
		att.Status = model.AttachStatusDone
		return s.repo.UpdateAttachment(ctx, att)

	default:
		return fmt.Errorf("unsupported category: %s", req.Category)
	}
}

// GeneratePresignedDownloadURL generates a presigned URL for file download
func (s *UploadService) GeneratePresignedDownloadURL(
	ctx context.Context,
	s3Key string,
) (string, error) {
	return s.GeneratePresignedDownloadURLWithTTL(ctx, s3Key, 1*time.Hour)
}

// PublicShareURLTTL bounds how long a presigned URL handed out via the
// unauthenticated GET /api/public/docs/{token} route stays valid -- much
// shorter than the 1-hour default used elsewhere, since it's the window an
// already-issued URL keeps working even after RevokeUserDocPublicShare
// clears the token (ADR-022).
const PublicShareURLTTL = 5 * time.Minute

// GeneratePresignedDownloadURLWithTTL is GeneratePresignedDownloadURL with a
// caller-chosen expiry, for routes that need a tighter revocation window.
func (s *UploadService) GeneratePresignedDownloadURLWithTTL(
	ctx context.Context,
	s3Key string,
	ttl time.Duration,
) (string, error) {
	presignedURL, err := s.presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(s3Key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned URL: %w", err)
	}

	return presignedURL.URL, nil
}

// docsPDFSidecarPrefix mirrors the "docs/" upload prefix, one level over --
// a converted PDF lives at docsPDFSidecarPrefix + strings.TrimPrefix(fileKey, "docs/") + ".pdf",
// e.g. docs/{uid}/123_deck.pptx -> docs-pdf/{uid}/123_deck.pptx.pdf. Kept
// outside "docs/" so the convert-doc Lambda's own PutObject never re-triggers
// the EventBridge rule that invokes it, and outside ownsFileKey's docs/
// prefix so the sidecar is never mistaken for a client-suppliable fileKey.
const docsPDFSidecarPrefix = "docs-pdf/"

// pptxExtensions are the slide file types that get a PDF sidecar converted
// by the convert-doc Lambda (see infra's EventBridge rule on the docs/ prefix).
var pptxExtensions = []string{".pptx", ".ppt"}

// SidecarPDFKey returns the deterministic PDF sidecar key for a docs/
// upload, or "" if fileKey isn't a docs/ upload or isn't a PPTX/PPT (a PDF
// upload needs no conversion; anything else -- images, audio -- has no
// sidecar concept at all).
func SidecarPDFKey(fileKey string) string {
	if !strings.HasPrefix(fileKey, "docs/") {
		return ""
	}
	lower := strings.ToLower(fileKey)
	isSlide := false
	for _, ext := range pptxExtensions {
		if strings.HasSuffix(lower, ext) {
			isSlide = true
			break
		}
	}
	if !isSlide {
		return ""
	}
	return docsPDFSidecarPrefix + strings.TrimPrefix(fileKey, "docs/") + ".pdf"
}

// GeneratePreviewPDFURL returns a presigned GET URL for fileKey's PDF
// sidecar if the conversion has completed, or ("", nil) if fileKey isn't a
// PPTX/PPT upload or the conversion hasn't finished yet -- callers treat
// both as "no preview available" rather than an error (the doc is still
// perfectly usable via its download link either way).
func (s *UploadService) GeneratePreviewPDFURL(ctx context.Context, fileKey string) (string, error) {
	return s.generatePreviewPDFURLWithTTL(ctx, fileKey, 1*time.Hour)
}

// GeneratePreviewPDFURLShortLived is GeneratePreviewPDFURL with PublicShareURLTTL,
// for the unauthenticated public-share route (see PublicShareURLTTL).
func (s *UploadService) GeneratePreviewPDFURLShortLived(ctx context.Context, fileKey string) (string, error) {
	return s.generatePreviewPDFURLWithTTL(ctx, fileKey, PublicShareURLTTL)
}

func (s *UploadService) generatePreviewPDFURLWithTTL(ctx context.Context, fileKey string, ttl time.Duration) (string, error) {
	sidecarKey := SidecarPDFKey(fileKey)
	if sidecarKey == "" {
		return "", nil
	}
	if _, err := s.s3Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(sidecarKey),
	}); err != nil {
		var notFound *s3types.NotFound
		if errors.As(err, &notFound) {
			return "", nil // not converted yet -- expected, not an error for the caller
		}
		// A real error (permission denial, throttling, etc.) looks
		// identical to "not converted yet" to every caller (they all treat
		// err != nil the same as "no preview available"), so at least log
		// it -- otherwise an actual outage is indistinguishable from the
		// normal pre-conversion window.
		log.Printf("warn: HeadObject failed for PDF sidecar %s: %v", sidecarKey, err)
		return "", nil
	}
	return s.GeneratePresignedDownloadURLWithTTL(ctx, sidecarKey, ttl)
}

// inferAttachTypeFromMime determines the attachment type from the MIME type
func inferAttachTypeFromMime(mimeType string) string {
	lower := strings.ToLower(mimeType)
	switch {
	case strings.HasPrefix(lower, "video/"):
		return model.AttachTypeVideo
	case strings.HasPrefix(lower, "audio/"):
		return model.AttachTypeAudioFile
	case strings.HasPrefix(lower, "image/"):
		return model.AttachTypePhoto
	default:
		return model.AttachTypeDocument
	}
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

// emitImageUploadEvent publishes a custom EventBridge event after the
// attachment record is persisted, so process-image Lambda always finds it.
func (s *UploadService) emitImageUploadEvent(ctx context.Context, meetingID, userID, key string) error {
	if s.ebClient == nil {
		return nil // graceful no-op if EventBridge client not configured
	}
	detail, _ := json.Marshal(map[string]string{
		"bucket":    s.bucketName,
		"key":       key,
		"meetingId": meetingID,
		"userId":    userID,
	})
	source := "ttobak.upload"
	detailType := "ImageUploadCompleted"
	_, err := s.ebClient.PutEvents(ctx, &eventbridge.PutEventsInput{
		Entries: []ebtypes.PutEventsRequestEntry{{
			Source:     aws.String(source),
			DetailType: aws.String(detailType),
			Detail:     aws.String(string(detail)),
		}},
	})
	return err
}

// RecoverMeeting recovers a crashed recording by copying the progress file to a final key
// and triggering the transcription pipeline.
func (s *UploadService) RecoverMeeting(ctx context.Context, userID, meetingID string) error {
	// Verify meeting exists and belongs to user
	meeting, err := s.repo.GetMeeting(ctx, userID, meetingID)
	if err != nil {
		return fmt.Errorf("failed to get meeting: %w", err)
	}
	if meeting == nil {
		return ErrNotFound
	}
	if meeting.Status != model.StatusRecording {
		return fmt.Errorf("meeting is not in recording state (current: %s)", meeting.Status)
	}

	// Check that progress file exists in S3
	progressKey := fmt.Sprintf("audio/%s/%s/recording_progress.webm", userID, meetingID)
	_, err = s.s3Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(progressKey),
	})
	if err != nil {
		return fmt.Errorf("no recoverable audio found (progress file missing)")
	}

	// Copy progress file to a final filename (triggers EventBridge S3 event → transcribe Lambda)
	finalKey := fmt.Sprintf("audio/%s/%s/recording_recovered_%d.webm", userID, meetingID, time.Now().UnixMilli())
	_, err = s.s3Client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(s.bucketName),
		Key:        aws.String(finalKey),
		CopySource: aws.String(fmt.Sprintf("%s/%s", s.bucketName, progressKey)),
	})
	if err != nil {
		return fmt.Errorf("failed to copy progress file: %w", err)
	}

	// Atomic partial update — set audio key and transition to transcribing
	return s.repo.UpdateMeetingFields(ctx, userID, meetingID, map[string]interface{}{
		"audioKey": finalKey,
		"status":   model.StatusTranscribing,
	})
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
