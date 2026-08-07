package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path"
	"strings"
	"sync"
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

	cfSignerMu      sync.Mutex
	cfSigner        *CloudFrontSigner
	cfSignerReload  func(ctx context.Context) (*CloudFrontSigner, error)
	cfSignerRetryAt time.Time
}

// cfSignerRetryInterval bounds how often a warm Lambda instance re-attempts
// SSM lookup once the initial cold-start attempt failed (ADR-027 gap: in the
// documented deploy order the api Lambda's own deploy step can land before
// TtobakFrontendStack publishes /ttobak/cloudfront/key-pair-id, e.g. mid-CI
// or a partial manual rollout, which would otherwise pin that Lambda
// instance to the S3-presign fallback for its entire warm lifetime).
const cfSignerRetryInterval = 5 * time.Minute

// SetCloudFrontSigner switches GET download URLs from raw S3 presigns to
// CloudFront-signed /media/* URLs on the site domain (ADR-027). Left unset
// (local dev, missing SSM params), downloads keep using S3 presigns.
func (s *UploadService) SetCloudFrontSigner(signer *CloudFrontSigner) {
	s.cfSignerMu.Lock()
	defer s.cfSignerMu.Unlock()
	s.cfSigner = signer
}

// SetCloudFrontSignerReload registers the retry callback used to lazily
// re-attempt CloudFront signer setup on a later request when it wasn't ready
// at cold start. See cfSignerRetryInterval for the retry cadence.
func (s *UploadService) SetCloudFrontSignerReload(reload func(ctx context.Context) (*CloudFrontSigner, error)) {
	s.cfSignerMu.Lock()
	defer s.cfSignerMu.Unlock()
	s.cfSignerReload = reload
}

// cfSignerRetryTimeout bounds the SSM round-trip on a lazy retry so a slow
// or hanging SSM call can't stall the request path indefinitely -- the
// caller falls back to S3 presign either way, so a bounded wait is strictly
// better than an unbounded one.
const cfSignerRetryTimeout = 3 * time.Second

// cloudFrontSigner returns the active signer, lazily retrying setup (at most
// once per cfSignerRetryInterval) if it isn't configured yet. The reload
// callback's network I/O runs outside cfSignerMu -- holding a mutex across
// an SSM round-trip would serialize every concurrent download request on
// that Lambda instance behind it, not just the ones that hit the retry.
func (s *UploadService) cloudFrontSigner(ctx context.Context) *CloudFrontSigner {
	s.cfSignerMu.Lock()
	if s.cfSigner != nil || s.cfSignerReload == nil || time.Now().Before(s.cfSignerRetryAt) {
		signer := s.cfSigner
		s.cfSignerMu.Unlock()
		return signer
	}
	s.cfSignerRetryAt = time.Now().Add(cfSignerRetryInterval)
	reload := s.cfSignerReload
	s.cfSignerMu.Unlock()

	retryCtx, cancel := context.WithTimeout(ctx, cfSignerRetryTimeout)
	signer, err := reload(retryCtx)
	cancel()
	if err != nil {
		log.Printf("warn: CloudFront signer retry failed, still falling back to S3 presign: %v", err)
		s.cfSignerMu.Lock()
		defer s.cfSignerMu.Unlock()
		return s.cfSigner
	}

	s.cfSignerMu.Lock()
	defer s.cfSignerMu.Unlock()
	s.cfSigner = signer
	return s.cfSigner
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
	if signer := s.cloudFrontSigner(ctx); signer != nil {
		return signer.SignedURL(s3Key, ttl)
	}

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

// validateRediarizeEligibility is the pure decision core of RediarizeMeeting:
// ownership, whisper-only, single-part-only, and not-currently-processing
// guards. Kept side-effect-free so it can be table-tested without mocking S3
// or DynamoDB. Returns the S3 key to re-copy on success.
func validateRediarizeEligibility(meeting *model.Meeting, userID string, speakerCount int) (string, error) {
	if speakerCount < 2 || speakerCount > 20 {
		return "", fmt.Errorf("speakerCount must be between 2 and 20: %w", ErrInvalidInput)
	}
	if meeting == nil {
		return "", ErrNotFound
	}
	if meeting.SttProvider != "" && meeting.SttProvider != "whisper" {
		return "", fmt.Errorf("rediarization is only supported for whisper-transcribed meetings: %w", ErrInvalidInput)
	}
	audioKeys := meeting.GetEffectiveAudioKeys()
	if meeting.AudioPartCount > 1 || len(audioKeys) > 1 {
		return "", fmt.Errorf("rediarization does not yet support multi-part audio: %w", ErrInvalidInput)
	}
	if meeting.Status == model.StatusTranscribing || meeting.Status == model.StatusSummarizing {
		return "", fmt.Errorf("meeting is still processing (status: %s): %w", meeting.Status, ErrInvalidInput)
	}
	if len(audioKeys) == 0 || audioKeys[0] == "" {
		return "", fmt.Errorf("no audio available to re-diarize: %w", ErrInvalidInput)
	}
	sourceKey := audioKeys[0]
	if !strings.HasPrefix(sourceKey, "audio/"+userID+"/") {
		return "", ErrForbidden
	}
	return sourceKey, nil
}

// RediarizeMeeting re-runs Whisper speaker diarization on a meeting's
// existing audio with a user-supplied speaker-count hint (e.g. after the
// acoustic pass under-detected speakers). It re-triggers the existing
// S3-event pipeline (audio upload -> EventBridge -> ttobak-transcribe -> Whisper
// ECS) via a same-bucket S3 copy rather than calling ECS RunTask directly, so
// this needs no new IAM grants on the api Lambda — see cmd/transcribe/main.go's
// use of Meeting.DiarizationSpeakerHint. Whisper-only (pyannote diarization,
// ADR-019); AWS Transcribe fallback meetings have no acoustic diarization to
// re-run. Multi-part audio is out of scope for v1 (would need per-part ECS
// retriggers and an AudioPartsReady reset).
func (s *UploadService) RediarizeMeeting(ctx context.Context, userID, meetingID string, speakerCount int) error {
	// Cheap bounds check before the repository round-trip -- a malformed
	// speakerCount shouldn't cost a DynamoDB read.
	if speakerCount < 2 || speakerCount > 20 {
		return fmt.Errorf("speakerCount must be between 2 and 20: %w", ErrInvalidInput)
	}

	meeting, err := s.repo.GetMeeting(ctx, userID, meetingID)
	if err != nil {
		return fmt.Errorf("failed to get meeting: %w", err)
	}

	sourceKey, err := validateRediarizeEligibility(meeting, userID, speakerCount)
	if err != nil {
		return err
	}

	// Old spk_N -> name mappings no longer correspond once diarization
	// re-runs from scratch; clear them and let the user re-map after. This
	// must commit BEFORE the CopyObject below: the copy fires the
	// EventBridge S3 event that wakes ttobak-transcribe, and a race where
	// that Lambda reads the meeting before this write lands would re-diarize
	// with the stale hint/speakerMap/status instead of the new ones.
	// ttobak-transcribe now reads via a strongly-consistent GetItem (see
	// cmd/transcribe/main.go) rather than the GSI3 query, so it can never
	// observe a value older than this write once it runs.
	//
	// Conditioned on status still being what GetMeeting just read (validated
	// non-transcribing/summarizing above): two concurrent RediarizeMeeting
	// calls both pass that eligibility check off the same read, but only one
	// can win this write -- the loser gets ConditionalCheckFailedException
	// and reports "already processing" instead of proceeding, rather than
	// two copy pipelines racing each other over the same meeting.
	if err := s.repo.UpdateMeetingFieldsIfMatch(ctx, userID, meetingID, map[string]interface{}{
		"status": meeting.Status,
	}, map[string]interface{}{
		"diarizationSpeakerHint": speakerCount,
		"speakerMap":             map[string]string{},
		"status":                 model.StatusTranscribing,
	}); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return fmt.Errorf("meeting is already being processed: %w", ErrInvalidInput)
		}
		return fmt.Errorf("failed to update meeting for re-diarization: %w", err)
	}

	// A fresh key under the same meeting prefix, so the existing EventBridge
	// S3 rule + ttobak-transcribe Lambda pick it up exactly as a new upload
	// would. The original audioKey is left untouched.
	retriggerKey := fmt.Sprintf("audio/%s/%s/rediarize_%s_%s", userID, meetingID, uuid.New().String(), path.Base(sourceKey))
	if _, err := s.s3Client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(s.bucketName),
		Key:        aws.String(retriggerKey),
		CopySource: aws.String(fmt.Sprintf("%s/%s", s.bucketName, encodeS3CopySourceKey(sourceKey))),
	}); err != nil {
		// Deliberately not attempting to resolve here whether the copy
		// secretly landed despite this SDK error (a HeadObject immediately
		// after can't prove that either -- S3 could still be completing it
		// asynchronously) or to write the meeting back to any particular
		// status: any inline write here risks racing the retriggered
		// pipeline's own status transitions if the copy did secretly
		// succeed. The status write above already moved this meeting to
		// "transcribing", so a genuinely failed copy leaves it stuck there --
		// which is exactly what the existing 30-minute stuck-transcribing
		// auto-expiry (meeting.go's stuckTranscribingThreshold, surfaced via
		// GetMeeting) is for. Let that reconcile it instead of guessing here.
		return fmt.Errorf("failed to retrigger re-diarization: %w", err)
	}
	return nil
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
