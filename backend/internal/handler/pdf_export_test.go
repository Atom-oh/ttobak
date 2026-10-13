package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type pdfTestStorage struct {
	calls                   int
	user, meeting, filename string
	data                    []byte
	err                     error
	afterStore              func()
}

func (s *pdfTestStorage) StorePDFExport(_ context.Context, user, meeting, filename string, data []byte) (string, error) {
	s.calls++
	s.user, s.meeting, s.filename, s.data = user, meeting, filename, data
	if s.afterStore != nil {
		s.afterStore()
	}
	return "https://example.invalid/media/synthetic-export.pdf", s.err
}

func TestPDFExportDeliversRealArtifactOnlyToAuthorizedReader(t *testing.T) {
	for _, user := range []string{"owner", "recipient", "stranger"} {
		t.Run(user, func(t *testing.T) {
			meetingHandler, repo := newStubMeetingHandler()
			repo.addMeeting(&model.Meeting{
				MeetingID: "meeting-1", UserID: "owner", Title: "한국어 회의",
				Date: time.Now().UTC(), Status: model.StatusDone,
				Content:     "<p>확정 비용 1,200,000원</p>",
				TranscriptA: "old A", TranscriptB: "선택된 B", SelectedTranscript: "B",
			})
			repo.shares[hKey("recipient", "meeting-1")] = &model.Share{
				MeetingID: "meeting-1", OwnerID: "owner", SharedToID: "recipient", Permission: "read",
			}
			storage := &pdfTestStorage{}
			h := &ExportHandler{meetingService: meetingHandler.meetingService, pdfStorage: storage}
			request := withChiParam(withUserCtx(httptest.NewRequest("POST", "/api/meetings/meeting-1/export",
				strings.NewReader(`{"format":"pdf"}`)), user), "meetingId", "meeting-1")
			response := httptest.NewRecorder()
			h.ExportMeeting(response, request)
			if user == "stranger" {
				if response.Code != http.StatusForbidden && response.Code != http.StatusNotFound {
					t.Fatalf("unauthorized export status=%d", response.Code)
				}
				if storage.calls != 0 {
					t.Fatal("unauthorized request stored a document")
				}
				return
			}
			if response.Code != http.StatusOK {
				t.Fatalf("export failed: %d %s", response.Code, response.Body.String())
			}
			var result model.ExportResponse
			if json.Unmarshal(response.Body.Bytes(), &result) != nil || result.URL == nil ||
				result.Filename == nil || !strings.HasSuffix(*result.Filename, ".pdf") ||
				result.Content != nil || result.Format != "pdf" {
				t.Fatal("PDF export still returned placeholder text")
			}
			if storage.calls != 1 || storage.user != user || storage.meeting != "meeting-1" ||
				!bytes.HasPrefix(storage.data, []byte("%PDF-")) {
				t.Fatal("PDF ownership or delivered bytes were invalid")
			}
		})
	}
}

func TestPDFExportSourceUsesSelectedTranscriptAndConvertedContent(t *testing.T) {
	selected := "B"
	h := &ExportHandler{}
	content := h.generatePDFContent(&model.MeetingDetailResponse{
		Title: "회의", Content: "<h1>확정 요약</h1>", TranscriptA: "stale-A",
		TranscriptB: "current-B", SelectedTranscript: &selected,
		Notes: "사용자가 저장한 <literal> 메모",
	})
	if strings.Contains(content, "stale-A") || strings.Contains(content, "<h1>") ||
		!strings.Contains(content, "current-B") || !strings.Contains(content, "확정 요약") ||
		!strings.Contains(content, "사용자가 저장한 <literal> 메모") {
		t.Fatal("PDF source ignored selection or editor conversion")
	}
}

func TestPDFExportRechecksSourceAndAccessAfterStorage(t *testing.T) {
	for _, changed := range []string{"source", "access"} {
		t.Run(changed, func(t *testing.T) {
			base, repo := newStubMeetingHandler()
			meeting := &model.Meeting{MeetingID: "meeting-1", UserID: "owner", Title: "회의", Status: model.StatusDone, Content: "현재 요약"}
			repo.addMeeting(meeting)
			repo.shares[hKey("reader", "meeting-1")] = &model.Share{MeetingID: "meeting-1", OwnerID: "owner", SharedToID: "reader", Permission: "read"}
			storage := &pdfTestStorage{afterStore: func() {
				if changed == "source" {
					meeting.Content = "저장 중 변경된 요약"
				} else {
					delete(repo.shares, hKey("reader", "meeting-1"))
				}
			}}
			h := &ExportHandler{meetingService: base.meetingService, pdfStorage: storage}
			request := withChiParam(withUserCtx(httptest.NewRequest("POST", "/", strings.NewReader(`{"format":"pdf"}`)), "reader"), "meetingId", "meeting-1")
			response := httptest.NewRecorder()
			h.ExportMeeting(response, request)
			want := http.StatusConflict
			if changed == "access" {
				want = http.StatusNotFound
			}
			if response.Code != want || strings.Contains(response.Body.String(), "synthetic-export.pdf") {
				t.Fatal("source/access change during storage published an export capability")
			}
		})
	}
}

type pdfChangingRepo struct {
	*mockHandlerMeetingRepo
	reads int
}

func (r *pdfChangingRepo) GetMeeting(ctx context.Context, userID, meetingID string) (*model.Meeting, error) {
	r.reads++
	if r.reads == 2 {
		r.meetings[hKey(userID, meetingID)].Notes = "동시 변경"
	}
	return r.mockHandlerMeetingRepo.GetMeeting(ctx, userID, meetingID)
}

func TestPDFExportRechecksSourceBeforeStorageAndBoundsRequest(t *testing.T) {
	repo := &pdfChangingRepo{mockHandlerMeetingRepo: newMockHandlerMeetingRepo()}
	repo.addMeeting(&model.Meeting{UserID: "owner", MeetingID: "meeting-1", Title: "회의", Status: model.StatusDone})
	storage := &pdfTestStorage{}
	h := &ExportHandler{meetingService: service.NewMeetingServiceForTest(repo), pdfStorage: storage}
	for _, body := range []string{`{"format":"pdf"}`, `{"format":"pdf","padding":"` + strings.Repeat("x", 5000) + `"}`} {
		request := withChiParam(withUserCtx(httptest.NewRequest("POST", "/", strings.NewReader(body)), "owner"), "meetingId", "meeting-1")
		response := httptest.NewRecorder()
		h.ExportMeeting(response, request)
		if response.Code != 409 && response.Code != 400 || storage.calls != 0 {
			t.Fatal("changed source or oversized request reached storage")
		}
	}
}

func TestExportFilenameKeepsUTF8BoundariesAndRejectsControlCharacters(t *testing.T) {
	for _, input := range []string{strings.Repeat("회의", 50), "회의\n제목", ""} {
		value := sanitizeFilename(input)
		if !utf8.ValidString(value) || len(value) > 100 || strings.ContainsAny(value, "\r\n") || value == "" {
			t.Fatal("unsafe or malformed export filename")
		}
	}
}

func TestPDFExportStorageFailuresDoNotClaimSuccess(t *testing.T) {
	meetingHandler, repo := newStubMeetingHandler()
	repo.addMeeting(&model.Meeting{MeetingID: "meeting-1", UserID: "owner", Title: "회의", Status: model.StatusDone})
	storage := &pdfTestStorage{err: errors.New("SYNTHETIC_PRIVATE_STORAGE_DETAIL")}
	h := &ExportHandler{meetingService: meetingHandler.meetingService, pdfStorage: storage}
	request := withChiParam(withUserCtx(httptest.NewRequest("POST", "/", strings.NewReader(`{"format":"pdf"}`)), "owner"), "meetingId", "meeting-1")
	response := httptest.NewRecorder()
	h.ExportMeeting(response, request)
	if response.Code != 500 || strings.Contains(response.Body.String(), "SYNTHETIC_PRIVATE") {
		t.Fatal("storage failure reported success or leaked details")
	}
}
