package service

import (
	"context"
	"fmt"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// MeetingService handles meeting business logic
type MeetingService struct {
	repo *repository.DynamoDBRepository
}

// NewMeetingService creates a new meeting service
func NewMeetingService(repo *repository.DynamoDBRepository) *MeetingService {
	return &MeetingService{repo: repo}
}

// CreateMeeting creates a new meeting
func (s *MeetingService) CreateMeeting(ctx context.Context, userID, title string, date time.Time, participants []string) (*model.Meeting, error) {
	if title == "" {
		return nil, fmt.Errorf("title is required")
	}
	return s.repo.CreateMeeting(ctx, userID, title, date, participants)
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

	// Add shared meetings
	for _, share := range result.Shares {
		meeting, err := s.repo.GetMeetingByID(ctx, share.MeetingID)
		if err != nil || meeting == nil {
			continue
		}
		perm := share.Permission
		item := model.ToMeetingListItem(meeting, true, &share.OwnerEmail, &perm)
		response.Meetings = append(response.Meetings, item)
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
		return nil, fmt.Errorf("not found")
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

	return &model.MeetingDetailResponse{
		MeetingID:          meeting.MeetingID,
		UserID:             meeting.UserID,
		Title:              meeting.Title,
		Date:               meeting.Date.Format(time.RFC3339),
		Status:             meeting.Status,
		Participants:       meeting.Participants,
		Content:            meeting.Content,
		TranscriptA:        meeting.TranscriptA,
		TranscriptB:        meeting.TranscriptB,
		SelectedTranscript: strPtr(meeting.SelectedTranscript),
		AudioKey:           meeting.AudioKey,
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
		return nil, fmt.Errorf("not found")
	}
	if permission != "owner" && permission != model.PermissionEdit {
		return nil, fmt.Errorf("forbidden")
	}

	// Apply updates
	if req.Title != "" {
		meeting.Title = req.Title
	}
	if req.Content != "" {
		meeting.Content = req.Content
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
			return fmt.Errorf("forbidden")
		}
		return fmt.Errorf("not found")
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
		return fmt.Errorf("not found")
	}
	if permission != "owner" && permission != model.PermissionEdit {
		return fmt.Errorf("forbidden")
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
		return nil, fmt.Errorf("not found")
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
			return fmt.Errorf("forbidden")
		}
		return fmt.Errorf("not found")
	}

	return s.repo.DeleteShare(ctx, sharedToID, meetingID)
}

// SearchUsers searches users by email
func (s *MeetingService) SearchUsers(ctx context.Context, query string) ([]model.UserSearchResponse, error) {
	users, err := s.repo.SearchUsersByEmail(ctx, query)
	if err != nil {
		return nil, err
	}

	var responses []model.UserSearchResponse
	for _, u := range users {
		responses = append(responses, model.UserSearchResponse{
			UserID: u.UserID,
			Email:  u.Email,
			Name:   u.Name,
		})
	}

	if responses == nil {
		responses = []model.UserSearchResponse{}
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
