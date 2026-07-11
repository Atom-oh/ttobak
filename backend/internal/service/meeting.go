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
	ErrUserNotFound   = errors.New("user not found")
	ErrSelfShare      = errors.New("cannot share with yourself")
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
	GetMember(ctx context.Context, accountID, userID string) (*model.AccountMember, error)
	ListAccountMembers(ctx context.Context, accountID string) ([]model.AccountMember, error)
	PutMeetingRef(ctx context.Context, ref *model.MeetingRef) error
	PutAccountInsights(ctx context.Context, insights []model.AccountInsight) error
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

	// Account-membership grants LIVE read access to meetings shared to an account:
	// a teammate added AFTER the meeting was shared can still read it, and a removed
	// member loses access automatically (no stale per-user Share snapshot). Only
	// SharedToAccount meetings qualify — a Link-only (AccountID set, not shared)
	// meeting stays private.
	byID, err := s.repo.GetMeetingByID(ctx, meetingID)
	if err != nil {
		return nil, "", err
	}
	if byID != nil && byID.SharedToAccount && byID.AccountID != "" {
		member, err := s.repo.GetMember(ctx, byID.AccountID, userID)
		if err != nil {
			return nil, "", err
		}
		if member != nil {
			return byID, model.PermissionRead, nil
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

	// The owner's exported Notion page is private to their own export — a
	// shared viewer has no use for it and no business seeing which external
	// page the owner's summary lives on (same reasoning as Shares above).
	notionPageID := ""
	if permission == "owner" {
		notionPageID = meeting.NotionPageID
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
		AudioKeys:          meeting.AudioKeys,
		AudioPartCount:     meeting.AudioPartCount,
		AudioPartsReady:    meeting.AudioPartsReady,
		Tags:               meeting.Tags,
		ActionItems:        toRawJSON(meeting.ActionItems),
		SpeakerMap:         meeting.SpeakerMap,
		SttProvider:        meeting.SttProvider,
		LinkedMeetingIDs:   meeting.LinkedMeetingIDs,
		NotionPageID:       notionPageID,
		Permission:         permission,
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

// ErrUserAlreadyExists is returned when inviting an email that is already registered
var ErrUserAlreadyExists = errors.New("user already exists")

// ErrAdminGroupAddFailed is returned when the user was created and invited
// successfully but adding them to the "admins" group failed. Callers should
// treat this as a partial success, not a failure of the whole request.
var ErrAdminGroupAddFailed = errors.New("user invited but failed to add to admins group")

// InviteUser creates a new Cognito user with a system-generated temporary
// password and email delivery. Cognito sends the invite email itself
// (sign-in URL + temp password) — no SES/templating needed on our side.
// If admin is true, the new user is also added to the "admins" group.
func (s *MeetingService) InviteUser(ctx context.Context, email, name string, admin bool) error {
	poolID := os.Getenv("COGNITO_USER_POOL_ID")
	if poolID == "" {
		return fmt.Errorf("server misconfiguration: COGNITO_USER_POOL_ID is not set")
	}

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}
	client := cognitoidp.NewFromConfig(cfg)

	attrs := []cognitoidptypes.AttributeType{
		{Name: aws.String("email"), Value: aws.String(email)},
		{Name: aws.String("email_verified"), Value: aws.String("true")},
	}
	if name != "" {
		attrs = append(attrs, cognitoidptypes.AttributeType{Name: aws.String("name"), Value: aws.String(name)})
	}

	_, err = client.AdminCreateUser(ctx, &cognitoidp.AdminCreateUserInput{
		UserPoolId:             aws.String(poolID),
		Username:               aws.String(email),
		UserAttributes:         attrs,
		DesiredDeliveryMediums: []cognitoidptypes.DeliveryMediumType{cognitoidptypes.DeliveryMediumTypeEmail},
	})
	if err != nil {
		var exists *cognitoidptypes.UsernameExistsException
		if errors.As(err, &exists) {
			return ErrUserAlreadyExists
		}
		return fmt.Errorf("failed to create user: %w", err)
	}

	if admin {
		_, err = client.AdminAddUserToGroup(ctx, &cognitoidp.AdminAddUserToGroupInput{
			UserPoolId: aws.String(poolID),
			Username:   aws.String(email),
			GroupName:  aws.String("admins"),
		})
		if err != nil {
			// The account was already created and the invite email sent —
			// surface this as a partial failure rather than losing the user.
			return fmt.Errorf("%w: %v", ErrAdminGroupAddFailed, err)
		}
	}

	return nil
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

// LinkMeetingToAccount classifies a meeting under an account (no sharing).
// Only the meeting owner who is a member of the account may do this.
func (s *MeetingService) LinkMeetingToAccount(ctx context.Context, ownerID, meetingID, accountID string) error {
	meeting, err := s.repo.GetMeeting(ctx, ownerID, meetingID)
	if err != nil {
		return err
	}
	if meeting == nil {
		if existing, _ := s.repo.GetMeetingByID(ctx, meetingID); existing != nil {
			return ErrForbidden
		}
		return ErrNotFound
	}
	member, err := s.repo.GetMember(ctx, accountID, ownerID)
	if err != nil {
		return err
	}
	if member == nil {
		return ErrForbidden
	}
	meeting.AccountID = accountID
	return s.repo.UpdateMeeting(ctx, meeting)
}

// ShareMeetingToAccount publishes a meeting to an account team: sets
// accountId+sharedToAccount, grants read Share to every account member
// (except the owner), and writes a MeetingRef into the account partition.
func (s *MeetingService) ShareMeetingToAccount(ctx context.Context, ownerID, ownerEmail, meetingID, accountID string) (*model.ShareToAccountResult, error) {
	meeting, err := s.repo.GetMeeting(ctx, ownerID, meetingID)
	if err != nil {
		return nil, err
	}
	if meeting == nil {
		if existing, _ := s.repo.GetMeetingByID(ctx, meetingID); existing != nil {
			return nil, ErrForbidden
		}
		return nil, ErrNotFound
	}
	owner, err := s.repo.GetMember(ctx, accountID, ownerID)
	if err != nil {
		return nil, err
	}
	if owner == nil {
		return nil, ErrForbidden
	}

	// This sequence is non-transactional, but every write is idempotent
	// (UpdateMeeting and the MeetingRef PutItem target fixed keys; CreateShare
	// keys on the recipient), so a client retry converges. Single-item DynamoDB
	// writes rarely fail; full atomicity via TransactWriteItems was considered
	// but rejected because its 100-item limit would cap account team size.
	// Order matters: write the MeetingRef BEFORE the per-member share loop so a
	// list-visible record always exists even if a later CreateShare fails —
	// otherwise the meeting would be flagged sharedToAccount=true with no ref,
	// leaving ListAccountMeetings permanently unable to surface it.
	meeting.AccountID = accountID
	meeting.SharedToAccount = true
	if err := s.repo.UpdateMeeting(ctx, meeting); err != nil {
		return nil, err
	}

	ref := &model.MeetingRef{
		PK:          model.PrefixAccount + accountID,
		SK:          model.PrefixMeetingRef + meeting.Date.UTC().Format(time.RFC3339) + "#" + meetingID,
		AccountID:   accountID,
		MeetingID:   meetingID,
		OwnerUserID: ownerID,
		Title:       meeting.Title,
		Date:        meeting.Date,
		EntityType:  model.EntityTypeMeetingRef,
	}
	if err := s.repo.PutMeetingRef(ctx, ref); err != nil {
		return nil, err
	}

	if items, berr := BuildAccountInsights(accountID, meeting); berr == nil && len(items) > 0 {
		if err := s.repo.PutAccountInsights(ctx, items); err != nil {
			return nil, err
		}
	}

	members, err := s.repo.ListAccountMembers(ctx, accountID)
	if err != nil {
		return nil, err
	}
	shared := 0
	for _, m := range members {
		if m.UserID == ownerID {
			continue
		}
		if _, err := s.repo.CreateShare(ctx, meetingID, ownerID, ownerEmail, m.UserID, m.Email, model.PermissionRead); err != nil {
			return nil, err
		}
		shared++
	}

	return &model.ShareToAccountResult{AccountID: accountID, SharedWith: shared}, nil
}

// BuildAccountInsights parses a meeting's stored insights JSON and builds
// account-partition AccountInsight items. SK = INSIGHT#{occurredAt}#{meetingId}#{index}
// is deterministic per (meeting,index), so re-running overwrites the same items
// (idempotent). Returns nil if the meeting has no insights yet.
func BuildAccountInsights(accountID string, meeting *model.Meeting) ([]model.AccountInsight, error) {
	if meeting == nil || strings.TrimSpace(meeting.Insights) == "" {
		return nil, nil
	}
	var parsed []model.MeetingInsight
	if err := json.Unmarshal([]byte(meeting.Insights), &parsed); err != nil {
		return nil, err
	}
	occurred := meeting.Date.UTC().Format(time.RFC3339)
	now := time.Now().UTC()
	out := make([]model.AccountInsight, 0, len(parsed))
	for i, p := range parsed {
		out = append(out, model.AccountInsight{
			PK:           model.PrefixAccount + accountID,
			SK:           fmt.Sprintf("%s%s#%s#%d", model.PrefixInsight, occurred, meeting.MeetingID, i),
			AccountID:    accountID,
			InsightID:    fmt.Sprintf("%s_%d", meeting.MeetingID, i),
			Type:         p.Type,
			Text:         p.Text,
			SourceType:   "meeting",
			SourceID:     meeting.MeetingID,
			SourceUserID: meeting.UserID,
			OccurredAt:   meeting.Date,
			TsMarker:     p.TsMarker,
			Entities:     p.Entities,
			CreatedAt:    now,
			EntityType:   model.EntityTypeInsight,
		})
	}
	return out, nil
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
