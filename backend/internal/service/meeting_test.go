package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// mockMeetingRepo is an in-memory implementation of meetingRepo for testing.
type mockMeetingRepo struct {
	meetings    map[string]*model.Meeting    // "userID|meetingID" -> meeting
	shares      map[string]*model.Share      // "sharedToID|meetingID" -> share
	attachments map[string][]model.Attachment // meetingID -> attachments
	meetingsByID map[string]*model.Meeting   // meetingID -> meeting (for GSI3 lookup)
	users       map[string]*model.User       // email -> user
}

func newMockMeetingRepo() *mockMeetingRepo {
	return &mockMeetingRepo{
		meetings:     make(map[string]*model.Meeting),
		shares:       make(map[string]*model.Share),
		attachments:  make(map[string][]model.Attachment),
		meetingsByID: make(map[string]*model.Meeting),
		users:        make(map[string]*model.User),
	}
}

func meetingKey(userID, meetingID string) string {
	return userID + "|" + meetingID
}

func shareKey(sharedToID, meetingID string) string {
	return sharedToID + "|" + meetingID
}

func (m *mockMeetingRepo) addMeeting(mtg *model.Meeting) {
	m.meetings[meetingKey(mtg.UserID, mtg.MeetingID)] = mtg
	m.meetingsByID[mtg.MeetingID] = mtg
}

func (m *mockMeetingRepo) CreateMeeting(_ context.Context, userID, title string, date time.Time, participants []string, sttProvider string) (*model.Meeting, error) {
	mtg := &model.Meeting{
		MeetingID:    "generated-id",
		UserID:       userID,
		Title:        title,
		Date:         date,
		Participants: participants,
		SttProvider:  sttProvider,
		Status:       model.StatusRecording,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	m.addMeeting(mtg)
	return mtg, nil
}

func (m *mockMeetingRepo) GetMeeting(_ context.Context, userID, meetingID string) (*model.Meeting, error) {
	mtg, ok := m.meetings[meetingKey(userID, meetingID)]
	if !ok {
		return nil, nil
	}
	cp := *mtg
	return &cp, nil
}

func (m *mockMeetingRepo) GetMeetingByID(_ context.Context, meetingID string) (*model.Meeting, error) {
	mtg, ok := m.meetingsByID[meetingID]
	if !ok {
		return nil, nil
	}
	cp := *mtg
	return &cp, nil
}

func (m *mockMeetingRepo) UpdateMeeting(_ context.Context, meeting *model.Meeting) error {
	cp := *meeting
	cp.UpdatedAt = time.Now().UTC()
	m.meetings[meetingKey(meeting.UserID, meeting.MeetingID)] = &cp
	m.meetingsByID[meeting.MeetingID] = &cp
	return nil
}

func (m *mockMeetingRepo) DeleteMeeting(_ context.Context, userID, meetingID string) error {
	key := meetingKey(userID, meetingID)
	if _, ok := m.meetings[key]; !ok {
		return nil
	}
	delete(m.meetings, key)
	delete(m.meetingsByID, meetingID)
	return nil
}

func (m *mockMeetingRepo) GetShare(_ context.Context, sharedToID, meetingID string) (*model.Share, error) {
	sh, ok := m.shares[shareKey(sharedToID, meetingID)]
	if !ok {
		return nil, nil
	}
	cp := *sh
	return &cp, nil
}

func (m *mockMeetingRepo) ListAttachments(_ context.Context, meetingID string) ([]model.Attachment, error) {
	return m.attachments[meetingID], nil
}

func (m *mockMeetingRepo) ListSharesForMeeting(_ context.Context, meetingID string) ([]model.Share, error) {
	var result []model.Share
	for _, sh := range m.shares {
		if sh.MeetingID == meetingID {
			result = append(result, *sh)
		}
	}
	return result, nil
}

func (m *mockMeetingRepo) ListMeetings(_ context.Context, params repository.ListMeetingsParams) (*repository.ListMeetingsResult, error) {
	var meetings []model.Meeting
	for _, mtg := range m.meetings {
		if mtg.UserID == params.UserID {
			meetings = append(meetings, *mtg)
		}
	}
	return &repository.ListMeetingsResult{Meetings: meetings}, nil
}

func (m *mockMeetingRepo) BatchGetMeetings(_ context.Context, keys []repository.MeetingKey) ([]*model.Meeting, error) {
	var result []*model.Meeting
	for _, key := range keys {
		if mtg, ok := m.meetings[meetingKey(key.OwnerID, key.MeetingID)]; ok {
			result = append(result, mtg)
		}
	}
	return result, nil
}

func (m *mockMeetingRepo) GetOrCreateUser(_ context.Context, userID, email, name string) (*model.User, error) {
	return &model.User{UserID: userID, Email: email, Name: name}, nil
}

func (m *mockMeetingRepo) GetUserByEmail(_ context.Context, email string) (*model.User, error) {
	u, ok := m.users[email]
	if !ok {
		return nil, nil
	}
	return u, nil
}

func (m *mockMeetingRepo) CreateShare(_ context.Context, meetingID, ownerID, ownerEmail, sharedToID, email, permission string) (*model.Share, error) {
	sh := &model.Share{
		MeetingID:  meetingID,
		OwnerID:    ownerID,
		SharedToID: sharedToID,
		Email:      email,
		Permission: permission,
	}
	m.shares[shareKey(sharedToID, meetingID)] = sh
	return sh, nil
}

func (m *mockMeetingRepo) DeleteShare(_ context.Context, sharedToID, meetingID string) error {
	delete(m.shares, shareKey(sharedToID, meetingID))
	return nil
}

// --- Tests ---

func TestCreateMeeting(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	meeting, err := svc.CreateMeeting(context.Background(), "user-1", "Test Meeting", time.Now(), []string{"Alice"}, "transcribe")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meeting.Title != "Test Meeting" {
		t.Errorf("expected title 'Test Meeting', got %q", meeting.Title)
	}
	if meeting.Status != model.StatusRecording {
		t.Errorf("expected status 'recording', got %q", meeting.Status)
	}
}

func TestCreateMeeting_EmptyTitle(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	_, err := svc.CreateMeeting(context.Background(), "user-1", "", time.Now(), nil, "")
	if err == nil {
		t.Fatal("expected error for empty title, got nil")
	}
}

func TestGetMeetingDetail_Owner(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "user-1", Title: "My Meeting",
		Status: model.StatusDone, Content: "summary text",
		Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})

	detail, err := svc.GetMeetingDetail(context.Background(), "user-1", "m-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.Title != "My Meeting" {
		t.Errorf("expected title 'My Meeting', got %q", detail.Title)
	}
	if detail.Content != "summary text" {
		t.Errorf("expected content 'summary text', got %q", detail.Content)
	}
}

func TestGetMeetingDetail_SharedReadAccess(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Title: "Shared Meeting",
		Status: model.StatusDone, Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	repo.shares[shareKey("reader-1", "m-1")] = &model.Share{
		MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "reader-1",
		Permission: model.PermissionRead,
	}

	detail, err := svc.GetMeetingDetail(context.Background(), "reader-1", "m-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.Title != "Shared Meeting" {
		t.Errorf("expected title 'Shared Meeting', got %q", detail.Title)
	}
	// Shares should not be visible to non-owners
	if len(detail.Shares) > 0 {
		t.Error("expected no shares visible to non-owner")
	}
}

func TestGetMeetingDetail_NotFound(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	_, err := svc.GetMeetingDetail(context.Background(), "user-1", "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdateMeeting_OwnerCanUpdate(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "user-1", Title: "Old Title",
		Status: model.StatusDone, Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})

	result, err := svc.UpdateMeeting(context.Background(), "user-1", "m-1", &model.UpdateMeetingRequest{Title: "New Title"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.MeetingID != "m-1" {
		t.Errorf("expected meetingId 'm-1', got %q", result.MeetingID)
	}

	// Verify the update was persisted
	updated := repo.meetingsByID["m-1"]
	if updated.Title != "New Title" {
		t.Errorf("expected title 'New Title', got %q", updated.Title)
	}
}

func TestUpdateMeeting_ReadOnlyShareForbidden(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Title: "Meeting",
		Status: model.StatusDone, Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	repo.shares[shareKey("reader-1", "m-1")] = &model.Share{
		MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "reader-1",
		Permission: model.PermissionRead,
	}

	_, err := svc.UpdateMeeting(context.Background(), "reader-1", "m-1", &model.UpdateMeetingRequest{Title: "Hacked"})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestUpdateMeeting_EditShareAllowed(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Title: "Meeting",
		Status: model.StatusDone, Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	repo.shares[shareKey("editor-1", "m-1")] = &model.Share{
		MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "editor-1",
		Permission: model.PermissionEdit,
	}

	_, err := svc.UpdateMeeting(context.Background(), "editor-1", "m-1", &model.UpdateMeetingRequest{Title: "Updated"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeleteMeeting_OwnerOnly(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Title: "Meeting",
		Status: model.StatusDone, Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})

	// Non-owner should be forbidden
	err := svc.DeleteMeeting(context.Background(), "other-user", "m-1")
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden for non-owner, got %v", err)
	}

	// Owner should succeed
	err = svc.DeleteMeeting(context.Background(), "owner-1", "m-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify deleted
	if _, ok := repo.meetingsByID["m-1"]; ok {
		t.Error("expected meeting to be deleted")
	}
}

func TestDeleteMeeting_NotFound(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	err := svc.DeleteMeeting(context.Background(), "user-1", "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdateSpeakers_ReplacesInAllFields(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "user-1", Title: "Meeting",
		Status:             model.StatusDone,
		Content:            "spk_0 said hello",
		TranscriptA:        "spk_0: hello",
		TranscriptSegments: `[{"speaker":"spk_0","text":"hello"}]`,
		ActionItems:        `[{"text":"spk_0 will do it"}]`,
		Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})

	_, err := svc.UpdateSpeakers(context.Background(), "user-1", "m-1", &model.UpdateSpeakersRequest{
		SpeakerMap: map[string]string{"spk_0": "Kim"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := repo.meetingsByID["m-1"]
	if updated.Content != "Kim said hello" {
		t.Errorf("expected content 'Kim said hello', got %q", updated.Content)
	}
	if updated.TranscriptA != "Kim: hello" {
		t.Errorf("expected transcriptA 'Kim: hello', got %q", updated.TranscriptA)
	}
}

func TestShareMeeting_CannotShareWithSelf(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "user-1", Title: "Meeting",
		Status: model.StatusDone, Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	repo.users["user@test.com"] = &model.User{UserID: "user-1", Email: "user@test.com"}

	_, err := svc.ShareMeetingByEmail(context.Background(), "user-1", "user@test.com", "m-1", "user@test.com", "read")
	if err == nil {
		t.Fatal("expected error for self-share, got nil")
	}
}

func TestSelectTranscript_ReadOnlyForbidden(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Title: "Meeting",
		Status: model.StatusDone, Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	repo.shares[shareKey("reader-1", "m-1")] = &model.Share{
		MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "reader-1",
		Permission: model.PermissionRead,
	}

	err := svc.SelectTranscript(context.Background(), "reader-1", "m-1", "B")
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}
