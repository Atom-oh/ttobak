package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	cognitoidp "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	cognitoidptypes "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/speaker"
)

// Auto-expiry thresholds for meetings stuck in an in-progress status with no
// further updates. Transcribing/summarizing are bounded backend processes, so
// 30 minutes is generous. Recording is user-controlled and open-ended (a
// legitimate meeting can run for hours), so it needs a much longer threshold
// to avoid killing an active session — but still short enough to eventually
// reclaim recordings abandoned by a closed tab or crashed browser.
const (
	stuckTranscribingThreshold = 30 * time.Minute
	stuckRecordingThreshold    = 6 * time.Hour
)

// isStuck reports whether a meeting's status has been sitting unchanged past
// its auto-expiry threshold.
func isStuck(status string, updatedAt time.Time) bool {
	switch status {
	case model.StatusTranscribing, model.StatusSummarizing:
		return time.Since(updatedAt) > stuckTranscribingThreshold
	case model.StatusRecording:
		return time.Since(updatedAt) > stuckRecordingThreshold
	default:
		return false
	}
}

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
	UpdateMeetingFields(ctx context.Context, userID, meetingID string, fields map[string]interface{}) error
	DeleteMeeting(ctx context.Context, userID, meetingID string) error
	GetShare(ctx context.Context, sharedToID, meetingID string) (*model.Share, error)
	ListAttachments(ctx context.Context, meetingID string) ([]model.Attachment, error)
	ListSharesForMeeting(ctx context.Context, meetingID string) ([]model.Share, error)
	ListMeetings(ctx context.Context, params repository.ListMeetingsParams) (*repository.ListMeetingsResult, error)
	BatchGetMeetings(ctx context.Context, keys []repository.MeetingKey) ([]*model.Meeting, error)
	GetOrCreateUser(ctx context.Context, userID, email, name string) (*model.User, error)
	GetUserByEmail(ctx context.Context, email string) (*model.User, error)
	CreateShare(ctx context.Context, meetingID, ownerID, ownerEmail, sharedToID, email, permission, origin string) (*model.Share, error)
	CreateShareIfMember(ctx context.Context, meetingID, ownerID, ownerEmail, accountID, sharedToID, email, permission string) (*model.Share, error)
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

// shareAccessRepo is the minimal persistence seam resolveSharedAccess needs --
// satisfied by both meetingRepo (MeetingService) and *repository.DynamoDBRepository
// (KnowledgeService), letting Ask reuse the exact same origin-aware
// membership re-verification as checkAccess instead of duplicating it with
// its own (previously unconditional) GetShare check.
type shareAccessRepo interface {
	GetShare(ctx context.Context, sharedToID, meetingID string) (*model.Share, error)
	GetMeetingByID(ctx context.Context, meetingID string) (*model.Meeting, error)
	GetMember(ctx context.Context, accountID, userID string) (*model.AccountMember, error)
}

// resolveSharedAccess checks non-owner access to meetingID for userID: a
// direct Share, or -- for an account-origin Share -- live account
// membership. Returns (nil, "", nil) if there's no valid access (not an
// error: the caller decides what "no access" means, e.g. checkAccess falls
// through to owner/account-membership-only checks, Ask returns 404).
func resolveSharedAccess(ctx context.Context, repo shareAccessRepo, userID, meetingID string) (*model.Meeting, string, error) {
	share, err := repo.GetShare(ctx, userID, meetingID)
	if err != nil {
		return nil, "", err
	}
	// A direct share (Origin=="") is an independent grant the owner made
	// explicitly -- honor it unconditionally regardless of current account
	// membership.
	if share != nil && share.Origin != model.ShareOriginAccount {
		meeting, err := repo.GetMeetingByID(ctx, meetingID)
		if err != nil {
			return nil, "", err
		}
		if meeting != nil {
			return meeting, share.Permission, nil
		}
	}

	// Reaching here means either there's no Share row, or it's an
	// account-origin row. An account-origin Share is a cache of membership,
	// not an independent grant -- RemoveMember's cleanup that's supposed to
	// delete it is best-effort and can fail (transient DynamoDB error) or
	// race a concurrent ShareMeetingToAccount snapshot, leaving a stale row
	// with no retry path. Re-verifying live membership here (instead of
	// trusting the row, or absence of one, unconditionally) means a removed
	// member loses access immediately even if cleanup never ran, and a
	// member added after the share was written gets access immediately too
	// -- closing the permanent-access gap without needing a reconciliation
	// job. Only SharedToAccount meetings qualify -- a Link-only (AccountID
	// set, not shared) meeting stays private.
	byID, err := repo.GetMeetingByID(ctx, meetingID)
	if err != nil {
		return nil, "", err
	}
	if byID != nil && byID.SharedToAccount && byID.AccountID != "" {
		member, err := repo.GetMember(ctx, byID.AccountID, userID)
		if err != nil {
			return nil, "", err
		}
		if member != nil {
			permission := model.PermissionRead
			if share != nil {
				permission = share.Permission
			}
			return byID, permission, nil
		}
	}

	return nil, "", nil
}

// resolveSharedAccessOrNotFound wraps resolveSharedAccess for callers that
// want "no access" reported as an error rather than a (nil, "", nil) zero
// value the caller must remember to check. resolveSharedAccess itself keeps
// the zero-value contract because checkAccess needs to fall through to
// owner/account-membership-only checks on "no access", not stop -- but a
// single-purpose caller like KnowledgeService.Ask has no fallthrough and
// only wants a request-level Not Found, so folding the nil-check in here
// removes the one call-site step (an easily-diffed-away trailing check) a
// separate manual guard would otherwise depend on.
func resolveSharedAccessOrNotFound(ctx context.Context, repo shareAccessRepo, userID, meetingID string) (*model.Meeting, string, error) {
	meeting, permission, err := resolveSharedAccess(ctx, repo, userID, meetingID)
	if err != nil {
		return nil, "", err
	}
	if meeting == nil {
		return nil, "", ErrNotFound
	}
	return meeting, permission, nil
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

	return resolveSharedAccess(ctx, s.repo, userID, meetingID)
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
			// memberCache avoids a duplicate GetMember call per meeting when
			// the same account appears more than once in this page's shares.
			memberCache := make(map[string]bool)
			for _, share := range result.Shares {
				meeting, ok := meetingMap[share.MeetingID]
				if !ok {
					continue
				}
				// Same read-time re-verification as checkAccess/resolveSharedAccess
				// (meeting.go): an account-origin Share row is a membership cache,
				// not an independent grant, so a removed member's title/summary
				// must not leak into their meeting list via a stale row either.
				// Mirrors resolveSharedAccess's predicate exactly, including the
				// SharedToAccount check -- without it, a meeting whose owner
				// un-shared it from the account (or that was only ever Link-only)
				// but still has a lingering account-origin Share row would leak
				// its title/summary here despite checkAccess correctly blocking
				// the detail view for the same row.
				if share.Origin == model.ShareOriginAccount {
					if !meeting.SharedToAccount || meeting.AccountID == "" {
						continue
					}
					isMember, cached := memberCache[meeting.AccountID]
					if !cached {
						member, err := s.repo.GetMember(ctx, meeting.AccountID, userID)
						if err != nil {
							// Don't cache a transient error as "not a member" --
							// that would suppress every meeting for this account
							// on this page, not just the one call that failed.
							// Skip only this meeting; a later share for the same
							// account on this page gets its own fresh attempt.
							log.Printf("ListMeetings: get member for account %s (meeting %s): %v", meeting.AccountID, meeting.MeetingID, err)
							continue
						}
						isMember = member != nil
						memberCache[meeting.AccountID] = isMember
					}
					if !isMember {
						continue
					}
				}
				perm := share.Permission
				item := model.ToMeetingListItem(meeting, true, &share.OwnerEmail, &perm)
				response.Meetings = append(response.Meetings, item)
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

	if isStuck(meeting.Status, meeting.UpdatedAt) {
		meeting.Status = model.StatusError
		s.repo.UpdateMeeting(ctx, meeting)
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
		LiveSummary:        meeting.LiveSummary,
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

// UpdateMeeting updates a meeting with access check. Uses UpdateMeetingFields
// (DynamoDB UpdateItem/SET) instead of a read-modify-write PutItem -- a
// second concurrent UpdateMeeting call (e.g. a lingering notes autosave PUT
// landing after a status transition) can then only ever touch the fields
// THIS call actually sets, never clobber others it read a stale copy of.
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

	fields := map[string]interface{}{}
	if req.Title != "" {
		fields["title"] = req.Title
	}
	if req.Content != "" {
		fields["content"] = req.Content
	}
	if req.Notes != nil {
		fields["notes"] = *req.Notes
	}
	if req.LiveSummary != nil {
		// Untrusted client input fed into the final-summary prompt (see
		// cmd/summarize/main.go) -- cap at write time so a single PUT can't
		// park hundreds of KB in the item (DynamoDB 400KB) or dominate the
		// prompt. Matches the summarize-side rune cap.
		if len([]rune(*req.LiveSummary)) > model.MaxLiveSummaryRunes {
			return nil, fmt.Errorf("%w: liveSummary exceeds %d characters", ErrInvalidInput, model.MaxLiveSummaryRunes)
		}
		fields["liveSummary"] = *req.LiveSummary
	}
	if req.TranscriptA != "" {
		fields["transcriptA"] = req.TranscriptA
	}
	if req.SelectedTranscript != "" {
		fields["selectedTranscript"] = req.SelectedTranscript
	}
	if req.Participants != nil {
		fields["participants"] = req.Participants
	}
	if req.Status != "" {
		fields["status"] = req.Status
	}

	updatedAt := time.Now().UTC()
	if len(fields) > 0 {
		if err := s.repo.UpdateMeetingFields(ctx, meeting.UserID, meeting.MeetingID, fields); err != nil {
			return nil, err
		}
	} else {
		updatedAt = meeting.UpdatedAt
	}

	return &model.MeetingUpdateResponse{
		MeetingID: meeting.MeetingID,
		UpdatedAt: updatedAt.Format(time.RFC3339),
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

	// Apply replacements to all text fields. Plain-text fields use a
	// word-boundary-aware replace (speaker.ReplaceLabel) so a label like
	// "spk_1" never partially matches a namespaced label like "spk_1000000"
	// (multi-part meetings produce exactly that pair -- see ADR-019).
	// TranscriptSegments is JSON, not prose, so it's updated structurally
	// instead: parsing out the `speaker` field and comparing for exact
	// equality has no substring-collision failure mode at all.
	for label, name := range req.SpeakerMap {
		if name == "" {
			continue
		}
		meeting.Content = speaker.ReplaceLabel(meeting.Content, label, name)
		meeting.TranscriptA = speaker.ReplaceLabel(meeting.TranscriptA, label, name)
		meeting.TranscriptB = speaker.ReplaceLabel(meeting.TranscriptB, label, name)
		meeting.ActionItems = speaker.ReplaceLabel(meeting.ActionItems, label, name)
	}
	meeting.TranscriptSegments = renameSegmentSpeakers(meeting.TranscriptSegments, req.SpeakerMap)

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

// segmentSpeakerField is the on-disk shape of a TranscriptSegments element,
// declared locally (rather than importing cmd/summarize's TranscriptSegmentOut)
// to avoid a service->cmd dependency. Includes "id" -- omitted, it would be
// silently dropped on re-marshal and break the ADR-013 transcript deep-link
// scroll target.
type segmentSpeakerField struct {
	ID        string  `json:"id,omitempty"`
	Speaker   string  `json:"speaker"`
	Text      string  `json:"text"`
	StartTime float64 `json:"startTime"`
	EndTime   float64 `json:"endTime"`
}

// renameSegmentSpeakers rewrites the `speaker` field of each element in a
// TranscriptSegments JSON array via exact match against speakerMap.
// Structural (parse/update/re-marshal) rather than string replacement, so a
// label like "spk_1" can never partially match a longer label like the
// namespaced "spk_1000000" -- there is no substring involved at all. Returns
// raw unchanged if it isn't valid JSON, matching the best-effort spirit of
// the rest of UpdateSpeakers.
func renameSegmentSpeakers(raw string, speakerMap map[string]string) string {
	if raw == "" {
		return raw
	}
	var segments []segmentSpeakerField
	if err := json.Unmarshal([]byte(raw), &segments); err != nil {
		return raw
	}
	for i := range segments {
		if name, ok := speakerMap[segments[i].Speaker]; ok && name != "" {
			segments[i].Speaker = name
		}
	}
	updated, err := json.Marshal(segments)
	if err != nil {
		return raw
	}
	return string(updated)
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

	return s.repo.CreateShare(ctx, meetingID, ownerID, ownerEmail, targetUser.UserID, targetEmail, permission, "")
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
// password and email delivery. Cognito sends the invite email itself (the
// default template: username + temp password, no login link) — no
// SES/templating needed on our side. If admin is true, the new user is also
// added to the "admins" group.
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
		// CreateShareIfMember atomically checks membership and writes the
		// Share in one transaction, closing the TOCTOU window a separate
		// GetMember+CreateShare pair would leave open: if a concurrent
		// RemoveMember completed for this exact member in that gap, the
		// resulting orphaned Share would never be cleaned up by anyone
		// (nothing re-triggers cleanup for an already-fully-removed member).
		_, err := s.repo.CreateShareIfMember(ctx, meetingID, ownerID, ownerEmail, accountID, m.UserID, m.Email, model.PermissionRead)
		if err != nil {
			if errors.Is(err, repository.ErrMemberRemoved) {
				continue // removed concurrently; matches the old member==nil skip
			}
			return nil, err
		}
		// Counted whether the write created a fresh account-origin Share or
		// the clobber-guard preserved an existing direct share for this
		// member -- either way they already have read access to this
		// meeting, so it's correctly reflected in SharedWith.
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
