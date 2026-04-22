package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

// withUserCtx injects a userID into the request context for testing.
func withUserCtx(r *http.Request, userID string) *http.Request {
	ctx := context.WithValue(r.Context(), middleware.UserIDKey, userID)
	ctx = context.WithValue(ctx, middleware.UserEmailKey, userID+"@test.com")
	return r.WithContext(ctx)
}

// withChiParam injects a chi URL param for testing.
func withChiParam(r *http.Request, key, val string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, val)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// stubMeetingService wraps a real MeetingService with a mock repo for handler tests.
type stubMeetingService struct {
	svc *service.MeetingService
}

func newStubMeetingHandler() (*MeetingHandler, *mockHandlerMeetingRepo) {
	repo := newMockHandlerMeetingRepo()
	svc := service.NewMeetingServiceForTest(repo)
	h := &MeetingHandler{meetingService: svc}
	return h, repo
}

// mockHandlerMeetingRepo implements service.MeetingRepo for handler tests.
type mockHandlerMeetingRepo struct {
	meetings     map[string]*model.Meeting
	meetingsByID map[string]*model.Meeting
	shares       map[string]*model.Share
	attachments  map[string][]model.Attachment
	users        map[string]*model.User
}

func newMockHandlerMeetingRepo() *mockHandlerMeetingRepo {
	return &mockHandlerMeetingRepo{
		meetings:     make(map[string]*model.Meeting),
		meetingsByID: make(map[string]*model.Meeting),
		shares:       make(map[string]*model.Share),
		attachments:  make(map[string][]model.Attachment),
		users:        make(map[string]*model.User),
	}
}

func hKey(userID, meetingID string) string { return userID + "|" + meetingID }

func (m *mockHandlerMeetingRepo) addMeeting(mtg *model.Meeting) {
	m.meetings[hKey(mtg.UserID, mtg.MeetingID)] = mtg
	m.meetingsByID[mtg.MeetingID] = mtg
}

func (m *mockHandlerMeetingRepo) CreateMeeting(_ context.Context, userID, title string, date time.Time, participants []string, sttProvider string) (*model.Meeting, error) {
	mtg := &model.Meeting{
		MeetingID: "new-meeting-id", UserID: userID, Title: title, Date: date,
		Participants: participants, SttProvider: sttProvider,
		Status: model.StatusRecording, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	m.addMeeting(mtg)
	return mtg, nil
}
func (m *mockHandlerMeetingRepo) GetMeeting(_ context.Context, userID, meetingID string) (*model.Meeting, error) {
	mtg, ok := m.meetings[hKey(userID, meetingID)]
	if !ok {
		return nil, nil
	}
	cp := *mtg
	return &cp, nil
}
func (m *mockHandlerMeetingRepo) GetMeetingByID(_ context.Context, meetingID string) (*model.Meeting, error) {
	mtg, ok := m.meetingsByID[meetingID]
	if !ok {
		return nil, nil
	}
	cp := *mtg
	return &cp, nil
}
func (m *mockHandlerMeetingRepo) UpdateMeeting(_ context.Context, meeting *model.Meeting) error {
	cp := *meeting
	cp.UpdatedAt = time.Now().UTC()
	m.meetings[hKey(meeting.UserID, meeting.MeetingID)] = &cp
	m.meetingsByID[meeting.MeetingID] = &cp
	return nil
}
func (m *mockHandlerMeetingRepo) DeleteMeeting(_ context.Context, userID, meetingID string) error {
	delete(m.meetings, hKey(userID, meetingID))
	delete(m.meetingsByID, meetingID)
	return nil
}
func (m *mockHandlerMeetingRepo) GetShare(_ context.Context, sharedToID, meetingID string) (*model.Share, error) {
	sh, ok := m.shares[sharedToID+"|"+meetingID]
	if !ok {
		return nil, nil
	}
	cp := *sh
	return &cp, nil
}
func (m *mockHandlerMeetingRepo) ListAttachments(_ context.Context, meetingID string) ([]model.Attachment, error) {
	return m.attachments[meetingID], nil
}
func (m *mockHandlerMeetingRepo) ListSharesForMeeting(_ context.Context, meetingID string) ([]model.Share, error) {
	return nil, nil
}
func (m *mockHandlerMeetingRepo) ListMeetings(_ context.Context, _ interface{}) (interface{}, error) {
	return nil, nil
}
func (m *mockHandlerMeetingRepo) BatchGetMeetings(_ context.Context, _ interface{}) (interface{}, error) {
	return nil, nil
}
func (m *mockHandlerMeetingRepo) GetOrCreateUser(_ context.Context, userID, email, name string) (*model.User, error) {
	return &model.User{UserID: userID, Email: email}, nil
}
func (m *mockHandlerMeetingRepo) GetUserByEmail(_ context.Context, email string) (*model.User, error) {
	u, ok := m.users[email]
	if !ok {
		return nil, nil
	}
	return u, nil
}
func (m *mockHandlerMeetingRepo) CreateShare(_ context.Context, meetingID, ownerID, ownerEmail, sharedToID, email, permission string) (*model.Share, error) {
	return &model.Share{MeetingID: meetingID, SharedToID: sharedToID, Permission: permission}, nil
}
func (m *mockHandlerMeetingRepo) DeleteShare(_ context.Context, sharedToID, meetingID string) error {
	return nil
}

// --- Tests ---

func TestCreateMeetingHandler(t *testing.T) {
	h, _ := newStubMeetingHandler()

	body := `{"title": "Test Meeting", "participants": ["Alice"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/meetings", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = withUserCtx(req, "user-1")

	rr := httptest.NewRecorder()
	h.CreateMeeting(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["title"] != "Test Meeting" {
		t.Errorf("expected title 'Test Meeting', got %v", resp["title"])
	}
}

func TestCreateMeetingHandler_EmptyTitle(t *testing.T) {
	h, _ := newStubMeetingHandler()

	body := `{"title": ""}`
	req := httptest.NewRequest(http.MethodPost, "/api/meetings", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = withUserCtx(req, "user-1")

	rr := httptest.NewRecorder()
	h.CreateMeeting(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetMeetingHandler_Owner(t *testing.T) {
	h, repo := newStubMeetingHandler()
	now := time.Now().UTC()

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "user-1", Title: "My Meeting",
		Status: model.StatusDone, Content: "summary",
		Date: now, CreatedAt: now, UpdatedAt: now,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/meetings/m-1", nil)
	req = withUserCtx(req, "user-1")
	req = withChiParam(req, "meetingId", "m-1")

	rr := httptest.NewRecorder()
	h.GetMeeting(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp model.MeetingDetailResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Title != "My Meeting" {
		t.Errorf("expected title 'My Meeting', got %q", resp.Title)
	}
}

func TestGetMeetingHandler_NotFound(t *testing.T) {
	h, _ := newStubMeetingHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/meetings/nonexistent", nil)
	req = withUserCtx(req, "user-1")
	req = withChiParam(req, "meetingId", "nonexistent")

	rr := httptest.NewRecorder()
	h.GetMeeting(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestDeleteMeetingHandler_Owner(t *testing.T) {
	h, repo := newStubMeetingHandler()
	now := time.Now().UTC()

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "user-1", Title: "Meeting",
		Status: model.StatusDone, Date: now, CreatedAt: now, UpdatedAt: now,
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/meetings/m-1", nil)
	req = withUserCtx(req, "user-1")
	req = withChiParam(req, "meetingId", "m-1")

	rr := httptest.NewRecorder()
	h.DeleteMeeting(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestDeleteMeetingHandler_Forbidden(t *testing.T) {
	h, repo := newStubMeetingHandler()
	now := time.Now().UTC()

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Title: "Meeting",
		Status: model.StatusDone, Date: now, CreatedAt: now, UpdatedAt: now,
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/meetings/m-1", nil)
	req = withUserCtx(req, "other-user")
	req = withChiParam(req, "meetingId", "m-1")

	rr := httptest.NewRecorder()
	h.DeleteMeeting(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestUpdateMeetingHandler(t *testing.T) {
	h, repo := newStubMeetingHandler()
	now := time.Now().UTC()

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "user-1", Title: "Old",
		Status: model.StatusDone, Date: now, CreatedAt: now, UpdatedAt: now,
	})

	body := `{"title": "New Title"}`
	req := httptest.NewRequest(http.MethodPut, "/api/meetings/m-1", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = withUserCtx(req, "user-1")
	req = withChiParam(req, "meetingId", "m-1")

	rr := httptest.NewRecorder()
	h.UpdateMeeting(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSelectTranscriptHandler_InvalidSelection(t *testing.T) {
	h, repo := newStubMeetingHandler()
	now := time.Now().UTC()

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "user-1", Title: "Meeting",
		Status: model.StatusDone, Date: now, CreatedAt: now, UpdatedAt: now,
	})

	body := `{"selected": "C"}`
	req := httptest.NewRequest(http.MethodPut, "/api/meetings/m-1/transcript", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = withUserCtx(req, "user-1")
	req = withChiParam(req, "meetingId", "m-1")

	rr := httptest.NewRecorder()
	h.SelectTranscript(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid selection, got %d: %s", rr.Code, rr.Body.String())
	}

	var errResp model.ErrorResponse
	json.Unmarshal(rr.Body.Bytes(), &errResp)
	if errResp.Error.Code != model.ErrCodeBadRequest {
		t.Errorf("expected error code BAD_REQUEST, got %q", errResp.Error.Code)
	}
}

// Verify error response structure matches API spec
func TestErrorResponseFormat(t *testing.T) {
	h, _ := newStubMeetingHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/meetings/x", nil)
	req = withUserCtx(req, "user-1")
	req = withChiParam(req, "meetingId", "x")

	rr := httptest.NewRecorder()
	h.GetMeeting(rr, req)

	var errResp model.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to parse error response: %v", err)
	}
	if errResp.Error.Code == "" {
		t.Error("error response missing code field")
	}
	if errResp.Error.Message == "" {
		t.Error("error response missing message field")
	}
}
