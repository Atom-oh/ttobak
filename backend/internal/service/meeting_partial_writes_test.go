package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// Commit a competing write after the service's read, immediately before its write.
type meetingWriteRaceRepo struct {
	*mockMeetingRepo
	beforeWrite        func(*model.Meeting)
	writeErr           error
	hydratedTranscript string
}

func (r *meetingWriteRaceRepo) GetMeeting(ctx context.Context, owner, id string) (*model.Meeting, error) {
	m, err := r.mockMeetingRepo.GetMeeting(ctx, owner, id)
	if m != nil && r.hydratedTranscript != "" {
		m.TranscriptA = r.hydratedTranscript
	}
	return m, err
}

func (r *meetingWriteRaceRepo) before(owner, id string) error {
	if r.beforeWrite != nil {
		fn := r.beforeWrite
		r.beforeWrite = nil
		fn(r.meetings[meetingKey(owner, id)])
	}
	return r.writeErr
}

func (r *meetingWriteRaceRepo) UpdateMeeting(ctx context.Context, m *model.Meeting) error {
	if err := r.before(m.UserID, m.MeetingID); err != nil {
		return err
	}
	return r.mockMeetingRepo.UpdateMeeting(ctx, m)
}

func (r *meetingWriteRaceRepo) UpdateMeetingFields(ctx context.Context, owner, id string, fields map[string]interface{}) error {
	if err := r.before(owner, id); err != nil {
		return err
	}
	return r.mockMeetingRepo.UpdateMeetingFields(ctx, owner, id, fields)
}

func (r *meetingWriteRaceRepo) UpdateMeetingFieldsIfMatch(ctx context.Context, owner, id string, expected, fields map[string]interface{}) error {
	if err := r.before(owner, id); err != nil {
		return err
	}
	return r.mockMeetingRepo.UpdateMeetingFieldsIfMatch(ctx, owner, id, expected, fields)
}

func newMeetingWriteRace() (*MeetingService, *meetingWriteRaceRepo) {
	r := &meetingWriteRaceRepo{mockMeetingRepo: newMockMeetingRepo()}
	r.addMeeting(&model.Meeting{
		UserID: "owner", MeetingID: "meeting", Status: model.StatusDone,
		Content: "spk_0 summary", TranscriptA: "spk_0 text", TranscriptB: "old B",
		ActionItems: `[{"id":"old","text":"spk_0 task","completed":false}]`,
		UpdatedAt:   time.Now().UTC().Add(-2 * time.Hour),
	})
	r.addMember("account", "owner", model.RoleOwner)
	return newMeetingServiceWithRepo(r), r
}

func TestMeetingPartialWritesPreserveConcurrentActions(t *testing.T) {
	ctx := context.Background()
	for _, op := range []struct {
		name  string
		write func(*MeetingService) error
		check func(*model.Meeting) bool
	}{
		{"select", func(s *MeetingService) error { return s.SelectTranscript(ctx, "owner", "meeting", "B") }, func(m *model.Meeting) bool { return m.SelectedTranscript == "B" }},
		{"status", func(s *MeetingService) error { return s.UpdateMeetingStatus(ctx, "meeting", model.StatusError) }, func(m *model.Meeting) bool { return m.Status == model.StatusError }},
		{"transcript A", func(s *MeetingService) error { return s.UpdateMeetingTranscript(ctx, "meeting", "new A", "") }, func(m *model.Meeting) bool { return m.TranscriptA == "new A" && m.TranscriptB == "old B" }},
		{"transcript B", func(s *MeetingService) error { return s.UpdateMeetingTranscript(ctx, "meeting", "", "new B") }, func(m *model.Meeting) bool { return m.TranscriptA == "spk_0 text" && m.TranscriptB == "new B" }},
		{"content", func(s *MeetingService) error { return s.UpdateMeetingContent(ctx, "meeting", "new summary") }, func(m *model.Meeting) bool { return m.Content == "new summary" && m.Status == model.StatusDone }},
		{"link", func(s *MeetingService) error { return s.LinkMeetingToAccount(ctx, "owner", "meeting", "account") }, func(m *model.Meeting) bool { return m.AccountID == "account" && !m.SharedToAccount }},
		{"share", func(s *MeetingService) error {
			_, err := s.ShareMeetingToAccount(ctx, "owner", "owner@example.com", "meeting", "account")
			return err
		}, func(m *model.Meeting) bool { return m.AccountID == "account" && m.SharedToAccount }},
	} {
		for _, actions := range []string{
			`[{"id":"new","text":"new extracted task","completed":false}]`,
			`[{"id":"old","text":"spk_0 task","completed":true}]`,
		} {
			t.Run(op.name+"/"+actions, func(t *testing.T) {
				s, r := newMeetingWriteRace()
				r.beforeWrite = func(m *model.Meeting) {
					m.ActionItems, m.Notes, m.UpdatedAt = actions, "concurrent note", time.Now().UTC()
				}
				if err := op.write(s); err != nil {
					t.Fatal(err)
				}
				m := r.meetings[meetingKey("owner", "meeting")]
				if m.ActionItems != actions || m.Notes != "concurrent note" || !op.check(m) {
					t.Fatalf("write lost concurrent actions/notes or its own change: %+v", m)
				}
			})
		}
	}
}

func TestSpeakerRenameRejectsConcurrentAnalysisAndCompletion(t *testing.T) {
	for _, actions := range []string{`[{"id":"new","text":"new task","completed":false}]`, `[{"id":"old","text":"spk_0 task","completed":true}]`} {
		t.Run(actions, func(t *testing.T) {
			s, r := newMeetingWriteRace()
			r.beforeWrite = func(m *model.Meeting) { m.ActionItems, m.UpdatedAt = actions, time.Now().UTC() }
			_, err := s.UpdateSpeakers(context.Background(), "owner", "meeting", &model.UpdateSpeakersRequest{SpeakerMap: map[string]string{"spk_0": "Kim"}})
			m := r.meetings[meetingKey("owner", "meeting")]
			if !errors.Is(err, repository.ErrConditionFailed) || m.ActionItems != actions || m.Content != "spk_0 summary" {
				t.Fatalf("stale rename was not rejected without overwriting: err=%v meeting=%+v", err, m)
			}
		})
	}
}

func TestSpeakerRenameAcceptsHydratedTranscript(t *testing.T) {
	s, r := newMeetingWriteRace()
	r.meetings[meetingKey("owner", "meeting")].TranscriptA = "s3://bucket/transcripts/meeting/transcriptA.txt"
	r.hydratedTranscript = "spk_0 hydrated text"
	_, err := s.UpdateSpeakers(context.Background(), "owner", "meeting", &model.UpdateSpeakersRequest{SpeakerMap: map[string]string{"spk_0": "Kim"}})
	m := r.meetings[meetingKey("owner", "meeting")]
	if err != nil || m.TranscriptA != "Kim hydrated text" || m.Content != "Kim summary" || m.SpeakerMap["spk_0"] != "Kim" {
		t.Fatalf("version CAS must accept hydrated source without comparing it to its S3 ref: err=%v meeting=%+v", err, m)
	}
}

func TestStuckMeetingRecoveryDoesNotOverwriteProgress(t *testing.T) {
	for _, change := range []string{"status", "version", "deleted", "storage error"} {
		t.Run(change, func(t *testing.T) {
			s, r := newMeetingWriteRace()
			r.meetings[meetingKey("owner", "meeting")].Status = model.StatusTranscribing
			r.beforeWrite = func(m *model.Meeting) {
				switch change {
				case "status":
					m.Status = model.StatusDone
				case "version":
					m.UpdatedAt = time.Now().UTC()
				case "deleted":
					delete(r.meetings, meetingKey("owner", "meeting"))
				}
				m.ActionItems = `[{"id":"new","text":"task","completed":true}]`
			}
			if change == "storage error" {
				r.writeErr = errors.New("storage unavailable")
			}
			detail, err := s.GetMeetingDetail(context.Background(), "owner", "meeting")
			if change == "storage error" {
				if !errors.Is(err, r.writeErr) {
					t.Fatalf("recovery write failure was hidden: %v", err)
				}
				return
			}
			if change == "deleted" {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("deleted meeting returned: detail=%+v err=%v", detail, err)
				}
				return
			}
			m := r.meetings[meetingKey("owner", "meeting")]
			if err != nil || detail.Status != m.Status || m.Status == model.StatusError || string(detail.ActionItems) != m.ActionItems {
				t.Fatalf("recovery overwrote progress or returned stale state: detail=%+v meeting=%+v err=%v", detail, m, err)
			}
		})
	}
}
