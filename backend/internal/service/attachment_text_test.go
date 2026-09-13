package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type attachmentTestRepo struct {
	*mockMeetingRepo
	attachment *model.Attachment
	state      *model.AttachmentTextState
	queues     int
	failError  error
}

func (r *attachmentTestRepo) GetAttachment(context.Context, string, string) (*model.Attachment, error) {
	if r.attachment == nil {
		return nil, nil
	}
	a := *r.attachment
	return &a, nil
}
func (r *attachmentTestRepo) GetAttachmentText(context.Context, string, string) (*model.AttachmentTextState, error) {
	if r.state == nil {
		return nil, nil
	}
	s := *r.state
	return &s, nil
}
func (r *attachmentTestRepo) QueueAttachmentText(_ context.Context, _ *model.Attachment, prior, next *model.AttachmentTextState) error {
	if (prior == nil) != (r.state == nil) || prior != nil && *prior != *r.state {
		return repository.ErrConditionFailed
	}
	r.queues++
	s := *next
	if prior != nil {
		s.ResultKey = prior.ResultKey
		s.SourceETag = prior.SourceETag
		s.UnitCount = prior.UnitCount
		s.Complete = prior.Complete
	}
	r.state = &s
	return nil
}
func (r *attachmentTestRepo) SaveUnsupportedAttachmentText(_ context.Context, _ *model.Attachment, _, next *model.AttachmentTextState) error {
	s := *next
	r.state = &s
	return nil
}
func (r *attachmentTestRepo) FailAttachmentText(_ context.Context, _, _ string, expected *model.AttachmentTextState, code string, now time.Time) error {
	if r.failError != nil {
		return r.failError
	}
	if r.state == nil || r.state.RunID != expected.RunID || r.state.Status != expected.Status || r.state.LeaseUntil != expected.LeaseUntil {
		return repository.ErrConditionFailed
	}
	r.state.Status = model.AttachmentTextFailed
	r.state.ErrorCode = code
	r.state.LeaseUntil = 0
	r.state.UpdatedAt = now
	return nil
}
func attachmentFixture(t *testing.T) (*AttachmentTextService, *attachmentTestRepo) {
	t.Helper()
	r := &attachmentTestRepo{mockMeetingRepo: newMockMeetingRepo(), attachment: &model.Attachment{AttachmentID: "a", MeetingID: "m", UserID: "owner", OriginalKey: "files/owner/m/노트.md", Type: model.AttachTypeDocument, Status: model.AttachStatusDone}}
	r.meetings[meetingKey("owner", "m")] = &model.Meeting{UserID: "owner", MeetingID: "m", Status: model.StatusDone, Content: "old summary"}
	s := NewAttachmentTextService(r, &MeetingService{repo: r}, nil, "bucket", nil)
	s.now = func() time.Time { return time.Unix(1800000000, 0).UTC() }
	return s, r
}
func TestAttachmentQueuePublishFailurePreservesResult(t *testing.T) {
	s, r := attachmentFixture(t)
	r.state = &model.AttachmentTextState{RunID: "old", Status: model.AttachmentTextSucceeded, OwnerID: "owner", UploaderID: "owner", SourceKey: r.attachment.OriginalKey, ResultKey: "files/owner/m/text/a/old.json", SourceETag: `"etag"`, UnitCount: 2, Complete: true}
	s.publish = func(context.Context, model.DocumentUploadCompleted) error { return errors.New("delivery failed") }
	if _, err := s.Request(context.Background(), "owner", "m", "a"); !errors.Is(err, ErrAttachmentPublish) {
		t.Fatalf("error=%v", err)
	}
	if r.state.Status != "failed" || r.state.ErrorCode != "PUBLISH_FAILED" || r.state.ResultKey != "files/owner/m/text/a/old.json" {
		t.Fatalf("%+v", r.state)
	}
	status, err := s.GetStatus(context.Background(), "owner", "m", "a")
	if err != nil || status.Complete || status.UnitCount != 0 || !status.HasResult || !status.NeedsResummary {
		t.Fatalf("%+v %v", status, err)
	}
}
func TestAttachmentExpiredAndUnsupportedStatus(t *testing.T) {
	s, r := attachmentFixture(t)
	status, err := s.GetStatus(context.Background(), "owner", "m", "a")
	if err != nil || status.Status != "unknown" {
		t.Fatalf("%+v %v", status, err)
	}
	r.state = &model.AttachmentTextState{RunID: "run", Status: "running", LeaseUntil: s.now().Add(-time.Second).UnixMilli(), OwnerID: "owner", UploaderID: "owner", SourceKey: r.attachment.OriginalKey}
	status, err = s.GetStatus(context.Background(), "owner", "m", "a")
	if err != nil || status.Status != "failed" || status.ErrorCode != "INTERRUPTED" {
		t.Fatalf("%+v %v", status, err)
	}
	r.attachment.OriginalKey = "files/owner/m/legacy.ppt"
	r.state = nil
	status, err = s.Request(context.Background(), "owner", "m", "a")
	if !errors.Is(err, ErrUnsupportedAttachment) || r.queues != 0 || status.Status != "failed" {
		t.Fatalf("%+v %v queues=%d", status, err, r.queues)
	}
}
