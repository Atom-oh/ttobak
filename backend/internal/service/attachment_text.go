package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

var (
	ErrUnsupportedAttachment   = errors.New("unsupported attachment format")
	ErrAttachmentPublish       = errors.New("attachment extraction event delivery failed")
	ErrAttachmentUnavailable   = errors.New("attachment text unavailable")
	ErrAttachmentSourceChanged = errors.New("attachment source or result changed")
	ErrAttachmentCursor        = errors.New("invalid or stale attachment cursor")
	attachmentIdentifier       = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

type attachmentTextRepo interface {
	GetAttachment(context.Context, string, string) (*model.Attachment, error)
	ListAttachments(context.Context, string) ([]model.Attachment, error)
	GetAttachmentText(context.Context, string, string) (*model.AttachmentTextState, error)
	QueueAttachmentText(context.Context, *model.Attachment, *model.AttachmentTextState, *model.AttachmentTextState) error
	SaveUnsupportedAttachmentText(context.Context, *model.Attachment, *model.AttachmentTextState, *model.AttachmentTextState) error
	FailAttachmentText(context.Context, string, string, *model.AttachmentTextState, string, time.Time) error
}
type attachmentTextS3 interface {
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}
type AttachmentTextService struct {
	repo     attachmentTextRepo
	meetings *MeetingService
	s3       attachmentTextS3
	bucket   string
	publish  func(context.Context, model.DocumentUploadCompleted) error
	now      func() time.Time
}

func NewAttachmentTextService(repo attachmentTextRepo, meetings *MeetingService, client attachmentTextS3, bucket string, publish func(context.Context, model.DocumentUploadCompleted) error) *AttachmentTextService {
	return &AttachmentTextService{repo: repo, meetings: meetings, s3: client, bucket: bucket, publish: publish, now: time.Now}
}
func AttachmentTextPublisher(client *eventbridge.Client) func(context.Context, model.DocumentUploadCompleted) error {
	return func(ctx context.Context, event model.DocumentUploadCompleted) error {
		if client == nil {
			return ErrAttachmentPublish
		}
		body, err := json.Marshal(event)
		if err != nil {
			return ErrAttachmentPublish
		}
		out, err := client.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: []ebtypes.PutEventsRequestEntry{{
			Source: aws.String("ttobak.upload"), DetailType: aws.String("DocumentUploadCompleted"), Detail: aws.String(string(body)),
		}}})
		if err != nil || out == nil || out.FailedEntryCount != 0 || len(out.Entries) != 1 || aws.ToString(out.Entries[0].ErrorCode) != "" || aws.ToString(out.Entries[0].EventId) == "" {
			return ErrAttachmentPublish
		}
		return nil
	}
}
func attachmentFormat(key string) string {
	switch ext := strings.ToLower(path.Ext(key)); ext {
	case ".pdf", ".pptx", ".docx", ".md":
		return ext[1:]
	}
	return ""
}
func validAttachmentSource(att *model.Attachment) bool {
	if att == nil || !attachmentIdentifier.MatchString(att.MeetingID) || !attachmentIdentifier.MatchString(att.UserID) || !attachmentIdentifier.MatchString(att.AttachmentID) || !utf8.ValidString(att.OriginalKey) || len(att.OriginalKey) > 1024 {
		return false
	}
	parts := strings.Split(att.OriginalKey, "/")
	if len(parts) != 4 || parts[0] != "files" || parts[1] != att.UserID || parts[2] != att.MeetingID || parts[3] == "" || parts[3] == "." || parts[3] == ".." {
		return false
	}
	return !strings.ContainsFunc(att.OriginalKey, func(r rune) bool { return r == '\\' || r < 32 || r == 127 })
}
func (s *AttachmentTextService) authorized(ctx context.Context, userID, meetingID, attachmentID string, edit bool) (*model.Meeting, *model.Attachment, error) {
	if !attachmentIdentifier.MatchString(meetingID) || !attachmentIdentifier.MatchString(attachmentID) {
		return nil, nil, ErrInvalidInput
	}
	meeting, permission, err := s.meetings.checkAccess(ctx, userID, meetingID)
	if err != nil {
		return nil, nil, err
	}
	if meeting == nil {
		return nil, nil, ErrNotFound
	}
	if edit && permission != "owner" && permission != model.PermissionEdit {
		return nil, nil, ErrForbidden
	}
	att, err := s.repo.GetAttachment(ctx, meetingID, attachmentID)
	if err != nil {
		return nil, nil, err
	}
	if att == nil {
		return nil, nil, ErrNotFound
	}
	if att.MeetingID != meetingID || att.AttachmentID != attachmentID || !validAttachmentSource(att) {
		return nil, nil, ErrInvalidInput
	}
	return meeting, att, nil
}
func stateMatchesAttachment(state *model.AttachmentTextState, meeting *model.Meeting, att *model.Attachment) bool {
	return state != nil && state.OwnerID == meeting.UserID && state.UploaderID == att.UserID && state.SourceKey == att.OriginalKey
}
func (s *AttachmentTextService) describe(ctx context.Context, meeting *model.Meeting, att *model.Attachment) (*model.AttachmentTextState, error) {
	for attempt := 0; attempt < 3; attempt++ {
		state, err := s.repo.GetAttachmentText(ctx, att.MeetingID, att.AttachmentID)
		if err != nil {
			return nil, err
		}
		if state == nil {
			return &model.AttachmentTextState{Status: model.AttachmentTextUnknown}, nil
		}
		if state.Pending() && state.LeaseUntil <= s.now().UnixMilli() {
			err = s.repo.FailAttachmentText(ctx, att.MeetingID, att.AttachmentID, state, "INTERRUPTED", s.now().UTC())
			if err != nil && !errors.Is(err, repository.ErrConditionFailed) {
				return nil, err
			}
			continue
		}
		copy := *state
		if !stateMatchesAttachment(state, meeting, att) {
			copy.Status = model.AttachmentTextFailed
			copy.ErrorCode = "SOURCE_CHANGED"
			copy.ResultKey = ""
		}
		return &copy, nil
	}
	return nil, repository.ErrConditionFailed
}
func attachmentRevision(state *model.AttachmentTextState) string {
	if state == nil {
		return ""
	}
	body, _ := json.Marshal([]string{state.RunID, state.Status, state.SourceKey, state.SourceETag, state.ResultKey})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

type attachmentSummarySources struct {
	ContentHash string            `json:"contentHash"`
	Documents   map[string]string `json:"documents"`
	Excerpted   map[string]bool   `json:"excerpted,omitempty"`
}

func attachmentNeedsResummary(meeting *model.Meeting, att *model.Attachment, state *model.AttachmentTextState) bool {
	if strings.TrimSpace(meeting.Content) == "" {
		return false
	}
	var sources attachmentSummarySources
	if json.Unmarshal([]byte(meeting.AttachmentSummarySources), &sources) != nil || sources.ContentHash != actionSourceHash(meeting.Content) {
		return true
	}
	return sources.Documents[att.AttachmentID] == "" || sources.Documents[att.AttachmentID] != attachmentRevision(state)
}
func attachmentStatus(meeting *model.Meeting, att *model.Attachment, state *model.AttachmentTextState) *model.AttachmentTextStatus {
	out := &model.AttachmentTextStatus{Status: state.Status, RunID: state.RunID, ErrorCode: state.ErrorCode, LeaseUntil: state.LeaseUntil,
		HasResult: state.ResultKey != "", NeedsResummary: attachmentNeedsResummary(meeting, att, state)}
	if !state.UpdatedAt.IsZero() {
		updated := state.UpdatedAt
		out.UpdatedAt = &updated
	}
	if state.Status == model.AttachmentTextSucceeded || state.Status == model.AttachmentTextPartial {
		out.UnitCount = state.UnitCount
		out.Complete = state.Complete
	}
	if attachmentFormat(att.OriginalKey) == "" {
		out.Status = model.AttachmentTextFailed
		out.ErrorCode = "UNSUPPORTED_FORMAT"
		out.UnitCount = 0
		out.Complete = false
	}
	if !out.NeedsResummary {
		var sources attachmentSummarySources
		if json.Unmarshal([]byte(meeting.AttachmentSummarySources), &sources) == nil {
			out.SummaryExcerpted = sources.Excerpted[att.AttachmentID]
		}
	}
	return out
}
func (s *AttachmentTextService) GetStatus(ctx context.Context, userID, meetingID, attachmentID string) (*model.AttachmentTextStatus, error) {
	meeting, att, err := s.authorized(ctx, userID, meetingID, attachmentID, false)
	if err != nil {
		return nil, err
	}
	state, err := s.describe(ctx, meeting, att)
	if err != nil {
		return nil, err
	}
	return attachmentStatus(meeting, att, state), nil
}
func (s *AttachmentTextService) Request(ctx context.Context, userID, meetingID, attachmentID string) (*model.AttachmentTextStatus, error) {
	meeting, att, err := s.authorized(ctx, userID, meetingID, attachmentID, true)
	if err != nil {
		return nil, err
	}
	prior, err := s.repo.GetAttachmentText(ctx, meetingID, attachmentID)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	if prior.Pending() && prior.LeaseUntil > now.UnixMilli() && stateMatchesAttachment(prior, meeting, att) {
		return attachmentStatus(meeting, att, prior), nil
	}
	next := &model.AttachmentTextState{RunID: uuid.NewString(), Status: model.AttachmentTextQueued, SourceKey: att.OriginalKey, OwnerID: meeting.UserID, UploaderID: att.UserID, LeaseUntil: now.Add(5 * time.Minute).UnixMilli(), UpdatedAt: now}
	if attachmentFormat(att.OriginalKey) == "" {
		next.Status = model.AttachmentTextFailed
		next.ErrorCode = "UNSUPPORTED_FORMAT"
		next.LeaseUntil = 0
		if err := s.repo.SaveUnsupportedAttachmentText(ctx, att, prior, next); err != nil {
			return nil, err
		}
		return attachmentStatus(meeting, att, next), ErrUnsupportedAttachment
	}
	if err := s.repo.QueueAttachmentText(ctx, att, prior, next); err != nil {
		return nil, err
	}
	event := model.DocumentUploadCompleted{Bucket: s.bucket, Key: att.OriginalKey, MeetingID: meetingID, OwnerID: meeting.UserID, UserID: att.UserID, AttachmentID: attachmentID, RunID: next.RunID}
	err = ErrAttachmentPublish
	if s.publish != nil {
		err = s.publish(ctx, event)
	}
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		failErr := s.repo.FailAttachmentText(cleanup, meetingID, attachmentID, next, "PUBLISH_FAILED", s.now().UTC())
		if errors.Is(failErr, repository.ErrConditionFailed) {
			failErr = nil
		}
		return nil, errors.Join(ErrAttachmentPublish, failErr)
	}
	state, err := s.describe(ctx, meeting, att)
	if err != nil {
		return nil, err
	}
	return attachmentStatus(meeting, att, state), nil
}
