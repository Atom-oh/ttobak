package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

var ErrIndexStatusUnavailable = errors.New("index status unavailable")

type indexStatusRepository interface {
	GetIndexSource(context.Context, model.IndexResource) (*model.IndexRecord, error)
	GetDocShare(context.Context, string, string) (*model.Share, error)
	GetMember(context.Context, string, string) (*model.AccountMember, error)
}
type indexStatusReader interface {
	Status(context.Context, model.IndexResource) (*model.IndexJob, error)
}

// IndexStatusResponse intentionally excludes job keys, grants and provider IDs.
type IndexStatusResponse struct {
	State     string `json:"state"`
	ErrorCode string `json:"errorCode,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

type IndexStatusService struct {
	repo          indexStatusRepository
	reader        indexStatusReader
	meetingAccess func(context.Context, string, string) (*model.Meeting, error)
}

func NewIndexStatusService(repo *repository.DynamoDBRepository, engine *IndexingService) *IndexStatusService {
	view := repo.MetadataView()
	meetings := NewMeetingService(view)
	s := &IndexStatusService{repo: view, meetingAccess: func(ctx context.Context, userID, meetingID string) (*model.Meeting, error) {
		meeting, _, err := meetings.checkAccess(ctx, userID, meetingID)
		return meeting, err
	}}
	if engine != nil {
		s.reader = engine
	}
	return s
}

func (s *IndexStatusService) Meeting(ctx context.Context, userID, meetingID string) (*IndexStatusResponse, error) {
	if userID == "" {
		return nil, ErrForbidden
	}
	if _, ok := model.CanonicalIndexResource("USER#"+userID, "MEETING#"+meetingID); !ok {
		return nil, ErrInvalidInput
	}
	if s.meetingAccess == nil {
		return nil, ErrIndexStatusUnavailable
	}
	meeting, err := s.meetingAccess(ctx, userID, meetingID)
	if err != nil {
		return nil, err
	}
	if meeting == nil || meeting.MeetingID != meetingID {
		return nil, ErrNotFound
	}
	key, ok := model.CanonicalIndexResource("USER#"+meeting.UserID, "MEETING#"+meetingID)
	if !ok {
		return nil, ErrNotFound
	}
	return s.result(ctx, key)
}

func validStatusDocument(record *model.IndexRecord, key model.IndexResource) bool {
	if record == nil || record.Fields["docId"] != key.ID {
		return false
	}
	_, scopeID, _ := strings.Cut(key.PK, "#")
	if key.Kind == "accountDocument" {
		return record.Fields["accountId"] == scopeID
	}
	return record.Fields["sourceUserId"] == scopeID
}

func (s *IndexStatusService) PersonalDocument(ctx context.Context, userID, docID string) (*IndexStatusResponse, error) {
	if userID == "" {
		return nil, ErrForbidden
	}
	key, ok := model.CanonicalIndexResource("USER#"+userID, "DOC#"+docID)
	if !ok {
		return nil, ErrInvalidInput
	}
	// Own-partition reads are already authorized. A foreign partition is never
	// read until its current direct grant has been validated.
	record, err := s.repo.GetIndexSource(ctx, key)
	if err != nil {
		return nil, err
	}
	if record == nil {
		share, err := s.repo.GetDocShare(ctx, userID, docID)
		if err != nil {
			return nil, err
		}
		if share == nil || share.PK != "USER#"+userID || share.SK != model.PrefixDocShare+docID ||
			share.EntityType != model.EntityTypeDocShare || share.MeetingID != docID ||
			share.SharedToID != userID || share.Permission != model.PermissionRead {
			return nil, ErrNotFound
		}
		key, ok = model.CanonicalIndexResource("USER#"+share.OwnerID, "DOC#"+docID)
		if !ok {
			return nil, ErrNotFound
		}
		record, err = s.repo.GetIndexSource(ctx, key)
		if err != nil {
			return nil, err
		}
	}
	if !validStatusDocument(record, key) {
		return nil, ErrNotFound
	}
	return s.result(ctx, key)
}

func (s *IndexStatusService) AccountDocument(ctx context.Context, userID, accountID, docID string) (*IndexStatusResponse, error) {
	if userID == "" {
		return nil, ErrForbidden
	}
	key, ok := model.CanonicalIndexResource("ACCOUNT#"+accountID, "DOC#"+docID)
	if !ok {
		return nil, ErrInvalidInput
	}
	member, err := s.repo.GetMember(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	if member == nil || member.AccountID != accountID || member.UserID != userID {
		return nil, ErrForbidden
	}
	record, err := s.repo.GetIndexSource(ctx, key)
	if err != nil {
		return nil, err
	}
	if !validStatusDocument(record, key) {
		return nil, ErrNotFound
	}
	return s.result(ctx, key)
}

func (s *IndexStatusService) result(ctx context.Context, key model.IndexResource) (*IndexStatusResponse, error) {
	if s.reader == nil {
		return nil, ErrIndexStatusUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	job, err := s.reader.Status(ctx, key)
	if err != nil {
		return nil, errors.Join(ErrIndexStatusUnavailable, err)
	}
	if job == nil {
		return &IndexStatusResponse{State: "UNTRACKED"}, nil
	}
	switch job.State {
	case model.IndexPending, model.IndexPreparing, model.IndexWaitingSync, model.IndexWaitingSource,
		model.IndexIndexed, model.IndexDeleted, model.IndexFailed:
	default:
		return nil, ErrIndexStatusUnavailable
	}
	result := &IndexStatusResponse{State: job.State}
	if job.State == model.IndexFailed || job.State == model.IndexWaitingSource {
		result.ErrorCode = job.ErrorCode
	}
	if job.UpdatedAt > 0 {
		result.UpdatedAt = time.UnixMilli(job.UpdatedAt).UTC().Format(time.RFC3339Nano)
	}
	return result, nil
}
