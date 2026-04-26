package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	cognitoidp "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// Sentinel errors for meeting operations
var (
	ErrForbidden      = errors.New("forbidden")
	ErrNotFound       = errors.New("not found")
	ErrStatusMismatch = errors.New("status mismatch")
)

// meetingRepo defines the repository methods used by MeetingService.
type meetingRepo interface {
	CreateMeeting(ctx context.Context, userID, title string, date time.Time, participants []string, sttProvider string) (*model.Meeting, error)
	GetMeeting(ctx context.Context, userID, meetingID string) (*model.Meeting, error)
	GetMeetingByID(ctx context.Context, meetingID string) (*model.Meeting, error)
	UpdateMeeting(ctx context.Context, meeting *model.Meeting) error
	DeleteMeeting(ctx context.Context, userID, meetingID string) error
	GetShare(ctx context.Context, sharedToID, meetingID string) (*model.Share, error)
	ListAttachments(ctx context.Context, meetingID string) ([]model.Attachment, error)
	ListSharesForMeeting(ctx context.Context, meetingID string) ([]model.Share, error)
	ListMeetings(ctx context.Context, params repository.ListMeetingsParams) (*repository.ListMeetingsResult, error)
	BatchGetMeetings(ctx context.Context, keys []repository.MeetingKey) ([]*model.Meeting, error)
	GetOrCreateUser(ctx context.Context, userID, email, name string) (*model.User, error)
	GetUserByEmail(ctx context.Context, email string) (*model.User, error)
	CreateShare(ctx context.Context, meetingID, ownerID, ownerEmail, sharedToID, email, permission string) (*model.Share, error)
	DeleteShare(ctx context.Context, sharedToID, meetingID string) error
}

// MeetingService handles meeting business logic
type MeetingService struct {
	repo meetingRepo
}

// NewMeetingService creates a new meeting service
func NewMeetingService(repo *repository.DynamoDBRepository) *MeetingService {
	return &MeetingService{repo: repo}
}

// newMeetingServiceWithRepo creates a MeetingService with a custom repo (for testing).
func newMeetingServiceWithRepo(repo meetingRepo) *MeetingService {
	return &MeetingService{repo: repo}
}

// NewMeetingServiceForTest creates a MeetingService with a custom MeetingRepo (exported for cross-package tests).
func NewMeetingServiceForTest(repo MeetingRepo) *MeetingService {
	return &MeetingService{repo: repo}
}

// MeetingRepo is the exported version of meetingRepo for cross-package test mocks.
type MeetingRepo = meetingRepo

// CreateMeeting creates a new meeting
func (s *MeetingService) CreateMeeting(ctx context.Context, userID, title string, date time.Time, participants []string, sttProvider string) (*model.Meeting, error) {
	if title == "" {
		return nil, fmt.Errorf("title is required")
	}
	return s.repo.CreateMeeting(ctx, userID, title, date, participants, sttProvider)
}

// checkAccess verifies access and returns meeting, permission, and error
func (s *MeetingService) checkAccess(ctx context.Context, userID, meetingID string) (*model.Meeting, string, error) {
	// Try to get owned meeting
	meeting, err := s.repo.GetMeeting(ctx, userID, meetingID)
	if err != nil {
		return nil, "", err
	}
	if meeting != nil {
		return meeting, "owner", nil
	}

	// Check for shared access
	share, err := s.repo.GetShare(ctx, userID, meetingID)
	if err != nil {
		return nil, "", err
	}
	if share != nil {
		// Get the actual meeting from the owner
		meeting, err = s.repo.GetMeetingByID(ctx, meetingID)
		if err != nil {
			return nil, "", err
		}
		if meeting != nil {
			return meeting, share.Permission, nil
		}
	}

	return nil, "", nil
}

// ListMeetings lists meetings for a user with pagination
func (s *MeetingService) ListMeetings(ctx context.Context, userID, tab, cursor string, limit int32) (*model.MeetingListResponse, error) {
	result, err := s.repo.ListMeetings(ctx, repository.ListMeetingsParams{
		UserID: userID,
		Tab:    tab,
		Cursor: cursor,
		Limit:  limit,
	})
	if err != nil {
		return nil, err
	}

	response := &model.MeetingListResponse{
		Meetings:   []model.MeetingListItem{},
		NextCursor: result.NextCursor,
	}

	// Add owned meetings
	for _, m := range result.Meetings {
		item := model.ToMeetingListItem(&m, false, nil, nil)
		response.Meetings = append(response.Meetings, item)
	}

	// Add shared meetings (single BatchGetItem call)
	if len(result.Shares) > 0 {
		keys := make([]repository.MeetingKey, len(result.Shares))
		for i, share := range result.Shares {
			keys[i] = repository.MeetingKey{
				OwnerID:   share.OwnerID,
				MeetingID: share.MeetingID,
			}
		}

		meetings, err := s.repo.BatchGetMeetings(ctx, keys)
		if err == nil {
			// Build lookup map
			meetingMap := make(map[string]*model.Meeting, len(meetings))
			for _, m := range meetings {
				meetingMap[m.MeetingID] = m
			}
			for _, share := range result.Shares {
				if meeting, ok := meetingMap[share.MeetingID]; ok {
					perm := share.Permission
					item := model.ToMeetingListItem(meeting, true, &share.OwnerEmail, &perm)
					response.Meetings = append(response.Meetings, item)
				}
			}
		}
	}

	return response, nil
}

// GetMeetingDetail retrieves a meeting with full details
func (s *MeetingService) GetMeetingDetail(ctx context.Context, userID, meetingID string) (*model.MeetingDetailResponse, error) {
	meeting, permission, err := s.checkAccess(ctx, userID, meetingID)
	if err != nil {
		return nil, err
	}
	if meeting == nil {
		return nil, ErrNotFound
	}

	// Get attachments
	attachments, _ := s.repo.ListAttachments(ctx, meetingID)
	var attachmentResponses []model.AttachmentResponse
	for _, att := range attachments {
		attachmentResponses = append(attachmentResponses, model.AttachmentResponse{
			AttachmentID:     att.AttachmentID,
			OriginalKey:      att.OriginalKey,
			ProcessedKey:     att.ProcessedKey,
			Type:             att.Type,
			Status:           att.Status,
			Description:      att.Description,
			ProcessedContent: att.ProcessedContent,
			FileName:         att.FileName,
			FileSize:         att.FileSize,
			MimeType:         att.MimeType,
		})
	}

	// Get shares only for owner
	var shareResponses []model.ShareResponse
	if permission == "owner" {
		shares, _ := s.repo.ListSharesForMeeting(ctx, meetingID)
		for _, share := range shares {
			shareResponses = append(shareResponses, model.ShareResponse{
				UserID:     share.SharedToID,
				Email:      share.Email,
				Permission: share.Permission,
			})
		}
	}

	// Parse transcript segments for speaker diarization
	var transcription json.RawMessage
	if meeting.TranscriptSegments != "" {
		transcription = json.RawMessage(meeting.TranscriptSegments)
	}

	return &model.MeetingDetailResponse{
		MeetingID:          meeting.MeetingID,
		UserID:             meeting.UserID,
		Title:              meeting.Title,
		Date:               meeting.Date.Format(time.RFC3339),
		Status:             meeting.Status,
		Participants:       meeting.Participants,
		Content:            meeting.Content,
		Notes:              meeting.Notes,
		TranscriptA:        meeting.TranscriptA,
		TranscriptB:        meeting.TranscriptB,
		SelectedTranscript: strPtr(meeting.SelectedTranscript),
		AudioKey:           meeting.AudioKey,
		Tags:               meeting.Tags,
		ActionItems:        toRawJSON(meeting.ActionItems),
		SpeakerMap:         meeting.SpeakerMap,
		SttProvider:        meeting.SttProvider,
		Transcription:      transcription,
		Attachments:        attachmentResponses,
		Shares:             shareResponses,
		CreatedAt:          meeting.CreatedAt.Format(time.RFC3339),
		UpdatedAt:          meeting.UpdatedAt.Format(time.RFC3339),
	}, nil
}

// UpdateMeeting updates a meeting with access check
func (s *MeetingService) UpdateMeeting(ctx context.Context, userID, meetingID string, req *model.UpdateMeetingRequest) (*model.MeetingUpdateResponse, error) {
	meeting, permission, err := s.checkAccess(ctx, userID, meetingID)
	if err != nil {
		return nil, err
	}
	if meeting == nil {
		return nil, ErrNotFound
	}
	if permission != "owner" && permission != model.PermissionEdit {
		return nil, ErrForbidden
	}

	// Apply updates
	if req.Title != "" {
		meeting.Title = req.Title
	}
	if req.Content != "" {
		meeting.Content = req.Content
	}
	if req.Notes != "" {
		meeting.Notes = req.Notes
	}
	if req.TranscriptA != "" {
		meeting.TranscriptA = req.TranscriptA
	}
	if req.SelectedTranscript != "" {
		meeting.SelectedTranscript = req.SelectedTranscript
	}
	if req.Participants != nil {
		meeting.Participants = req.Participants
	}
	if req.Status != "" {
		meeting.Status = req.Status
	}

	if err := s.repo.UpdateMeeting(ctx, meeting); err != nil {
		return nil, err
	}

	return &model.MeetingUpdateResponse{
		MeetingID: meeting.MeetingID,
		UpdatedAt: meeting.UpdatedAt.Format(time.RFC3339),
	}, nil
}

// UpdateSpeakers replaces speaker labels with real names in all text fields
func (s *MeetingService) UpdateSpeakers(ctx context.Context, userID, meetingID string, req *model.UpdateSpeakersRequest) (*model.MeetingUpdateResponse, error) {
	meeting, permission, err := s.checkAccess(ctx, userID, meetingID)
	if err != nil {
		return nil, err
	}
	if meeting == nil {
		return nil, ErrNotFound
	}
	if permission != "owner" && permission != model.PermissionEdit {
		return nil, ErrForbidden
	}

	// Apply replacements to all text fields
	for label, name := range req.SpeakerMap {
		if name == "" {
			continue
		}
		meeting.Content = strings.ReplaceAll(meeting.Content, label, name)
		meeting.TranscriptA = strings.ReplaceAll(meeting.TranscriptA, label, name)
		meeting.TranscriptB = strings.ReplaceAll(meeting.TranscriptB, label, name)
		meeting.TranscriptSegments = strings.ReplaceAll(meeting.TranscriptSegments, label, name)
		meeting.ActionItems = strings.ReplaceAll(meeting.ActionItems, label, name)
	}

	// Store the mapping for reference
	meeting.SpeakerMap = req.SpeakerMap

	if err := s.repo.UpdateMeeting(ctx, meeting); err != nil {
		return nil, err
	}

	return &model.MeetingUpdateResponse{
		MeetingID: meeting.MeetingID,
		UpdatedAt: meeting.UpdatedAt.Format(time.RFC3339),
	}, nil
}

// DeleteMeeting deletes a meeting (owner only)
func (s *MeetingService) DeleteMeeting(ctx context.Context, userID, meetingID string) error {
	// Only owner can delete
	meeting, err := s.repo.GetMeeting(ctx, userID, meetingID)
	if err != nil {
		return err
	}
	if meeting == nil {
		// Check if it exists but user is not owner
		existing, _ := s.repo.GetMeetingByID(ctx, meetingID)
		if existing != nil {
			return ErrForbidden
		}
		return ErrNotFound
	}

	return s.repo.DeleteMeeting(ctx, userID, meetingID)
}

// SelectTranscript selects which transcript to use (A or B)
func (s *MeetingService) SelectTranscript(ctx context.Context, userID, meetingID, selected string) error {
	meeting, permission, err := s.checkAccess(ctx, userID, meetingID)
	if err != nil {
		return err
	}
	if meeting == nil {
		return ErrNotFound
	}
	if permission != "owner" && permission != model.PermissionEdit {
		return ErrForbidden
	}

	meeting.SelectedTranscript = selected
	return s.repo.UpdateMeeting(ctx, meeting)
}

// ShareMeetingByEmail shares a meeting with a user identified by email
func (s *MeetingService) ShareMeetingByEmail(ctx context.Context, ownerID, ownerEmail, meetingID, targetEmail, permission string) (*model.Share, error) {
	// Verify ownership
	meeting, err := s.repo.GetMeeting(ctx, ownerID, meetingID)
	if err != nil {
		return nil, err
	}
	if meeting == nil {
		return nil, ErrNotFound
	}

	// Find user by email
	targetUser, err := s.repo.GetUserByEmail(ctx, targetEmail)
	if err != nil {
		return nil, err
	}
	if targetUser == nil {
		return nil, fmt.Errorf("user not found")
	}

	// Cannot share with self
	if ownerID == targetUser.UserID {
		return nil, fmt.Errorf("cannot share with yourself")
	}

	return s.repo.CreateShare(ctx, meetingID, ownerID, ownerEmail, targetUser.UserID, targetEmail, permission)
}

// RevokeShare revokes a share (owner only)
func (s *MeetingService) RevokeShare(ctx context.Context, ownerID, meetingID, sharedToID string) error {
	// Verify ownership
	meeting, err := s.repo.GetMeeting(ctx, ownerID, meetingID)
	if err != nil {
		return err
	}
	if meeting == nil {
		// Check if meeting exists
		existing, _ := s.repo.GetMeetingByID(ctx, meetingID)
		if existing != nil {
			return ErrForbidden
		}
		return ErrNotFound
	}

	return s.repo.DeleteShare(ctx, sharedToID, meetingID)
}

// SearchUsers searches users by email using Cognito ListUsers API
func (s *MeetingService) SearchUsers(ctx context.Context, query string) ([]model.UserSearchResponse, error) {
	poolID := os.Getenv("COGNITO_USER_POOL_ID")
	if poolID == "" {
		return []model.UserSearchResponse{}, nil
	}

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	client := cognitoidp.NewFromConfig(cfg)

	result, err := client.ListUsers(ctx, &cognitoidp.ListUsersInput{
		UserPoolId: aws.String(poolID),
		Filter:     aws.String(fmt.Sprintf("email ^= \"%s\"", strings.ReplaceAll(query, "\"", ""))),
		Limit:      aws.Int32(10),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to search users: %w", err)
	}

	responses := make([]model.UserSearchResponse, 0, len(result.Users))
	for _, u := range result.Users {
		var email, name, userID string
		for _, attr := range u.Attributes {
			switch aws.ToString(attr.Name) {
			case "sub":
				userID = aws.ToString(attr.Value)
			case "email":
				email = aws.ToString(attr.Value)
			case "name":
				name = aws.ToString(attr.Value)
			}
		}
		if userID != "" && email != "" {
			responses = append(responses, model.UserSearchResponse{
				UserID: userID,
				Email:  email,
				Name:   name,
			})
		}
	}

	return responses, nil
}

// UpdateMeetingStatus updates the status of a meeting (internal use)
func (s *MeetingService) UpdateMeetingStatus(ctx context.Context, meetingID, status string) error {
	meeting, err := s.repo.GetMeetingByID(ctx, meetingID)
	if err != nil {
		return err
	}
	if meeting == nil {
		return fmt.Errorf("meeting not found")
	}

	meeting.Status = status
	return s.repo.UpdateMeeting(ctx, meeting)
}

// UpdateMeetingTranscript updates transcript fields (internal use)
func (s *MeetingService) UpdateMeetingTranscript(ctx context.Context, meetingID string, transcriptA, transcriptB string) error {
	meeting, err := s.repo.GetMeetingByID(ctx, meetingID)
	if err != nil {
		return err
	}
	if meeting == nil {
		return fmt.Errorf("meeting not found")
	}

	if transcriptA != "" {
		meeting.TranscriptA = transcriptA
	}
	if transcriptB != "" {
		meeting.TranscriptB = transcriptB
	}

	return s.repo.UpdateMeeting(ctx, meeting)
}

// UpdateMeetingContent updates the content/summary field (internal use)
func (s *MeetingService) UpdateMeetingContent(ctx context.Context, meetingID, content string) error {
	meeting, err := s.repo.GetMeetingByID(ctx, meetingID)
	if err != nil {
		return err
	}
	if meeting == nil {
		return fmt.Errorf("meeting not found")
	}

	meeting.Content = content
	meeting.Status = model.StatusDone
	return s.repo.UpdateMeeting(ctx, meeting)
}

// strPtr returns a pointer to string, or nil if empty
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// toRawJSON converts a JSON string to json.RawMessage, or nil if empty/invalid
func toRawJSON(s string) json.RawMessage {
	if s == "" || s == "[]" {
		return nil
	}
	return json.RawMessage(s)
}
