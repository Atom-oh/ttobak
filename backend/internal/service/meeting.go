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
	GetOrCreateUser(ctx context.Context, userID, email, name string) (*model.User, bool, error)
	GetUserByEmail(ctx context.Context, email string) (*model.User, error)
	CreateShare(ctx context.Context, meetingID, ownerID, ownerEmail, sharedToID, email, permission, origin string) (*model.Share, error)
	CreateShareIfMember(ctx context.Context, meetingID, ownerID, ownerEmail, accountID, sharedToID, email, permission string) (*model.Share, error)
	DeleteShare(ctx context.Context, sharedToID, meetingID string) error
	GetMember(ctx context.Context, accountID, userID string) (*model.AccountMember, error)
	ListAccountMembers(ctx context.Context, accountID string) ([]model.AccountMember, error)
	PutMeetingRef(ctx context.Context, ref *model.MeetingRef) error
	PutAccountInsights(ctx context.Context, insights []model.AccountInsight) error
	PutPendingShare(ctx context.Context, share *model.PendingShare) error
	ListPendingShares(ctx context.Context, email string) ([]model.PendingShare, error)
	DeletePendingShare(ctx context.Context, email, sk string) error
	DeletePendingShareIfVersionMatches(ctx context.Context, email string, p *model.PendingShare) error
	MaterializePendingAccountGrant(ctx context.Context, p *model.PendingShare, userID, email string) (bool, error)
	MaterializePendingMeetingGrant(ctx context.Context, p *model.PendingShare, userID, email string) (bool, error)
}

// MeetingService handles meeting business logic
type MeetingService struct {
	repo meetingRepo

	// cognito/cognitoPoolID back InviteUser/SearchUsers. When unset, both
	// methods fall back to building a client inline via
	// config.LoadDefaultConfig (today's behavior) so existing callers that
	// never call SetCognitoAdminAPI keep working unchanged; SetCognitoAdminAPI
	// exists so cmd/api/main.go can inject the cold-start client (avoiding a
	// LoadDefaultConfig call per invite) and so tests can inject a mock.
	cognito       cognitoAdminAPI
	cognitoPoolID string
}

// NewMeetingService creates a new meeting service
func NewMeetingService(repo *repository.DynamoDBRepository) *MeetingService {
	return &MeetingService{repo: repo}
}

// SetCognitoAdminAPI wires a Cognito client for InviteUser/SearchUsers to use
// instead of building one inline per call.
func (s *MeetingService) SetCognitoAdminAPI(client cognitoAdminAPI, poolID string) {
	s.cognito = client
	s.cognitoPoolID = poolID
}

// resolveCognitoPoolID prefers the pool ID set via SetCognitoAdminAPI,
// falling back to the environment variable for callers that never wired one.
func (s *MeetingService) resolveCognitoPoolID() string {
	if s.cognitoPoolID != "" {
		return s.cognitoPoolID
	}
	return os.Getenv("COGNITO_USER_POOL_ID")
}

// resolveCognitoClient returns the injected client if SetCognitoAdminAPI was
// called, otherwise builds one inline (today's behavior, preserved for any
// caller that constructs MeetingService without wiring a client).
func (s *MeetingService) resolveCognitoClient(ctx context.Context) (cognitoAdminAPI, error) {
	if s.cognito != nil {
		return s.cognito, nil
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	return cognitoidp.NewFromConfig(cfg), nil
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

// ShareMeetingByEmail shares a meeting with a user identified by email. The
// returned bool is true when targetEmail has no PROFILE row yet but does
// have an invited Cognito account -- the grant was queued as a PendingShare
// (see its doc comment) rather than a real Share, and the returned *Share is
// nil.
func (s *MeetingService) ShareMeetingByEmail(ctx context.Context, ownerID, ownerEmail, meetingID, targetEmail, permission string) (*model.Share, bool, error) {
	// Verify ownership
	meeting, err := s.repo.GetMeeting(ctx, ownerID, meetingID)
	if err != nil {
		return nil, false, err
	}
	if meeting == nil {
		return nil, false, ErrNotFound
	}

	// Find user by email
	targetUser, err := s.repo.GetUserByEmail(ctx, targetEmail)
	if err != nil {
		return nil, false, err
	}
	if targetUser == nil {
		invited, sub, err := emailHasPendingInvite(ctx, s.cognito, s.resolveCognitoPoolID(), targetEmail)
		if err != nil {
			return nil, false, err
		}
		if !invited || sub == "" {
			// A fail-closed guard: without a sub, materializeOne would have
			// no identity to bind the grant to. AdminGetUserOutput.Username
			// is a required response field, so this should never actually
			// be empty in practice -- treat it the same as "not invited"
			// rather than queuing an unclaimable grant.
			return nil, false, ErrUserNotFound
		}
		if err := s.repo.PutPendingShare(ctx, &model.PendingShare{
			Email:             targetEmail,
			Kind:              model.PendingShareKindMeeting,
			MeetingID:         meetingID,
			Permission:        permission,
			InvitedByUserID:   ownerID,
			InvitedByEmail:    ownerEmail,
			InvitedCognitoSub: sub,
		}); err != nil {
			return nil, false, err
		}
		return nil, true, nil
	}

	// Cannot share with self
	if ownerID == targetUser.UserID {
		return nil, false, ErrSelfShare
	}

	share, err := s.repo.CreateShare(ctx, meetingID, ownerID, ownerEmail, targetUser.UserID, targetEmail, permission, "")
	return share, false, err
}

// RevokePendingShare cancels a queued PendingShare meeting invite before
// the target has ever logged in -- there's no Share row yet (that's the
// whole point of ShareMeetingByEmail's pending branch), so this can't go
// through RevokeShare. Exists for the same reason as AccountService.
// RevokePendingMember: AGENTS.md's "PII in DynamoDB requires KMS + TTL"
// mandate expects standing access grants to be cancellable, not just
// eventually expiring, and a PendingShare is otherwise invisible and
// un-revocable for up to PendingShareTTL once queued.
//
// The pending row's SK already encodes meetingID
// (PENDING_MEETING#{meetingId}), so this can only ever address this exact
// meeting's queued invite for email, never another meeting's; ownerID
// just needs to currently own meetingID. A DeleteItem on an already-gone/
// never-existed row is a silent no-op, matching RevokeShare's own
// idempotent-delete behavior.
func (s *MeetingService) RevokePendingShare(ctx context.Context, ownerID, meetingID, email string) error {
	meeting, err := s.repo.GetMeeting(ctx, ownerID, meetingID)
	if err != nil {
		return err
	}
	if meeting == nil {
		existing, _ := s.repo.GetMeetingByID(ctx, meetingID)
		if existing != nil {
			return ErrForbidden
		}
		return ErrNotFound
	}
	return s.repo.DeletePendingShare(ctx, email, model.PrefixPendingMeeting+meetingID)
}

// emailHasPendingInvite reports whether email belongs to a Cognito user who
// has been invited but hasn't completed a first login yet -- exactly the
// gap PendingShare exists to bridge. Deliberately requires an explicitly
// wired client (unlike resolveCognitoClient's other callers, e.g. InviteUser)
// rather than falling back to building one inline: a service constructed
// without SetCognitoAdminAPI (unit tests, or any future caller that never
// wires one) should treat every unresolvable email as plainly unknown, not
// make a live AWS call just to decide that.
//
// The returned sub (AdminGetUserOutput.Username -- == the Cognito `sub`
// for this pool specifically because auth-stack.ts's User Pool has no
// username alias, only signInAliases:{email:true}, so Cognito assigns a
// generated-UUID username == sub for every user) is stored on the queued
// PendingShare as InvitedCognitoSub, so materializeOne can later bind the
// grant to that exact identity rather than to a bare email string.
func emailHasPendingInvite(ctx context.Context, client cognitoAdminAPI, poolID, email string) (bool, string, error) {
	if client == nil || poolID == "" {
		return false, "", nil
	}
	out, err := client.AdminGetUser(ctx, &cognitoidp.AdminGetUserInput{
		UserPoolId: aws.String(poolID),
		Username:   aws.String(email),
	})
	if err != nil {
		var notFound *cognitoidptypes.UserNotFoundException
		if errors.As(err, &notFound) {
			return false, "", nil
		}
		return false, "", fmt.Errorf("failed to check invite status for %s: %w", email, err)
	}
	return true, aws.ToString(out.Username), nil
}

// MaterializePendingShares turns every PendingShare queued for email into a
// real AccountMember or Share row for the now-known userID, then deletes
// the queued row. Called from handler/meeting.go on every ListMeetings/
// CreateMeeting request (not gated on GetOrCreateUser's created flag --
// this is a cheap, idempotent Query that's almost always empty, and gating
// on "first PROFILE-creating call only" would make a materialization
// failure permanent: by the time it could be retried, created is already
// false forever). Business logic (grant issuance policy: which identity
// checks must pass before attempting a grant) lives here in the service
// layer; the transactional re-verify-and-grant primitives it calls
// (MaterializePendingAccountGrant/MaterializePendingMeetingGrant) are
// themselves plain data primitives that happen to need a transaction, not
// policy themselves.
//
// emailVerified is the CURRENT login's email_verified claim (see
// middleware.GetEmailVerified) -- required, not just the pending grant's own
// Cognito state at queue time, because a PendingShare is otherwise bound to
// nothing but a plain email string until this call. A false value here
// means "don't materialize yet," not "drop": the grant stays queued for a
// later login where the claim is true.
//
// A single item's materialization failure is logged and skipped (continue,
// not abort) so it can't block every other queued grant for the same
// email -- matching docs/superpowers/specs/2026-08-04-pending-email-
// invites-design.md's explicit design intent ("logged and skipped, not
// fatal"). It stays queued and is retried on the next call.
func (s *MeetingService) MaterializePendingShares(ctx context.Context, userID, email string, emailVerified bool) {
	pending, err := s.repo.ListPendingShares(ctx, email)
	if err != nil {
		log.Printf("MaterializePendingShares: failed to list pending shares for %s: %v", email, err)
		return
	}
	now := time.Now().Unix()
	for i := range pending {
		p := &pending[i]
		// Fail-closed identity checks -- these gate whether an attempt is
		// even made, before any write. TTL<=0 (missing, not just expired)
		// is treated as invalid rather than "no expiry": every row this
		// service ever queues sets it (see PutPendingShare), so a zero
		// value only means corruption or a future caller that forgot to,
		// and either way it must not be read as "claim never expires."
		// Likewise InvitedCognitoSub=="" is treated as unbound rather than
		// "skip the identity check" -- an authorization gate must fail
		// closed on missing data, not open.
		if p.TTL <= 0 || p.TTL <= now || p.InvitedCognitoSub == "" || p.InvitedCognitoSub != userID || !emailVerified {
			if p.TTL <= 0 || p.TTL <= now {
				// Version-conditioned (not a plain delete): this cleanup
				// runs off a stale read from ListPendingShares above, and an
				// unconditioned delete here could otherwise wipe a fresh
				// re-invite (PutPendingShare upserts the same key) that
				// landed in the gap -- a real, shipped bug in an earlier
				// round of this PR.
				if err := s.repo.DeletePendingShareIfVersionMatches(ctx, email, p); err != nil {
					log.Printf("MaterializePendingShares: failed to clear expired/invalid pending share %s for %s: %v", p.SK, email, err)
				}
			}
			// Otherwise (sub mismatch, or email not verified this login):
			// leave queued -- not this call's identity to resolve, but
			// possibly a later one's.
			continue
		}
		granted, err := s.materializeOne(ctx, userID, email, p)
		if err != nil {
			log.Printf("MaterializePendingShares: skipping pending share %s for %s: %v", p.SK, email, err)
			continue
		}
		if !granted {
			// Condition failed inside the transaction (inviter lost
			// authority, target gone, or already granted via another
			// path) -- not an error, but nothing was materialized OR
			// cleared (the Delete was part of the same failed
			// transaction). Left queued; PendingShareTTL is what
			// eventually reclaims a permanently-dead one, since this
			// service can't cheaply tell "permanently invalid" apart from
			// "transient" from a bare ConditionalCheckFailed.
			continue
		}
		// Nothing left to do here: materializeOne's transactional
		// primitives already deleted the pending row as part of their own
		// successful transaction (or, for the default/unknown-kind branch,
		// materializeOne cleared it itself via the same versioned delete).
		// A further unconditioned delete call here was a real, shipped bug
		// in an earlier round -- it could silently wipe a fresh re-invite
		// that landed in the gap between that transaction committing and
		// this call, exactly the race the versioned delete exists to
		// prevent.
	}
}

// materializeOne atomically re-verifies one queued grant's inviter (still
// owns the account as RoleOwner / still owns the meeting) and the absence
// of an existing row for userID, then writes the real grant and clears the
// queued one -- all in a single transaction (MaterializePendingAccountGrant/
// MaterializePendingMeetingGrant), closing the check-then-write race a
// separate read-then-write pair would have (AGENTS.md's conditional-write
// rule) and the non-atomicity between "grant written" and "pending row
// cleared" a separate Delete call afterward would have. The TTL/sub/
// emailVerified identity checks happen one level up, in
// MaterializePendingShares's loop -- this only handles the grant-issuance
// transaction itself.
//
// Returns (granted, err). err means "leave this one queued, retry later"
// (a transient failure, e.g. a DynamoDB error unrelated to the condition).
// granted=false, err=nil means the transaction's condition failed --
// MaterializePendingShares treats that as "leave queued, bounded by TTL"
// rather than assuming it's safe to drop, since a ConditionalCheckFailed
// alone doesn't distinguish a permanently dead grant from a transient race.
func (s *MeetingService) materializeOne(ctx context.Context, userID, email string, p *model.PendingShare) (bool, error) {
	switch p.Kind {
	case model.PendingShareKindAccount:
		return s.repo.MaterializePendingAccountGrant(ctx, p, userID, email)
	case model.PendingShareKindMeeting:
		return s.repo.MaterializePendingMeetingGrant(ctx, p, userID, email)
	default:
		// Not a transaction (there's no grant to issue), but still must be
		// a versioned delete: an unconditioned one could wipe a fresh
		// re-invite that upserted the same key after this row was read.
		log.Printf("materializeOne: unknown PendingShare kind %q for %s -- dropping", p.Kind, email)
		if err := s.repo.DeletePendingShareIfVersionMatches(ctx, email, p); err != nil {
			return false, fmt.Errorf("drop unknown-kind pending share: %w", err)
		}
		return false, nil
	}
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
	poolID := s.resolveCognitoPoolID()
	if poolID == "" {
		return fmt.Errorf("server misconfiguration: COGNITO_USER_POOL_ID is not set")
	}
	client, err := s.resolveCognitoClient(ctx)
	if err != nil {
		return err
	}

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
	poolID := s.resolveCognitoPoolID()
	if poolID == "" {
		return []model.UserSearchResponse{}, nil
	}

	client, err := s.resolveCognitoClient(ctx)
	if err != nil {
		return nil, err
	}

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
			Type: p.Type,
			Text: p.Text,
			// Evidence (near-verbatim meeting quotes) is deliberately NOT
			// fanned out here -- account members without access to the
			// source meeting would otherwise be able to read direct quotes
			// from it via this derived view. Implication/nextAction are
			// LLM-generated summaries, not quotes, so they're safe to carry.
			Implication:  p.Implication,
			NextAction:   p.NextAction,
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
