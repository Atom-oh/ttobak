package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

func TestMeetingAssetDownloadsRejectForeignKeysAndPreserveSharing(t *testing.T) {
	handler, repo := newStubMeetingHandler()
	client := s3.New(s3.Options{
		Region: "ap-northeast-2", Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider("AKID", "SECRET", "")),
		HTTPClient: readingHTTP(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodHead || !strings.HasPrefix(r.URL.Path, "/audio/owner/meeting/") {
				t.Fatalf("unexpected object lookup: %s %s", r.Method, r.URL.Path)
			}
			return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}),
	})
	handler.uploadService = service.NewUploadService(client, nil, "test-bucket")
	now := time.Now().UTC()
	meeting := &model.Meeting{MeetingID: "meeting", UserID: "owner", Status: model.StatusDone, Date: now, CreatedAt: now, UpdatedAt: now}
	repo.addMeeting(meeting)
	repo.shares["reader|meeting"] = &model.Share{MeetingID: "meeting", OwnerID: "owner", SharedToID: "reader", Permission: model.PermissionRead}
	repo.attachments["meeting"] = []model.Attachment{
		{AttachmentID: "good", MeetingID: "meeting", UserID: "owner", OriginalKey: "images/owner/meeting/사진.png", Type: model.AttachTypePhoto},
		{AttachmentID: "file", MeetingID: "meeting", UserID: "owner", OriginalKey: "files/owner/meeting/자료.pdf", Type: model.AttachTypeDocument},
		{AttachmentID: "bad", MeetingID: "meeting", UserID: "owner", OriginalKey: "docs/victim/private.pdf", Type: model.AttachTypePhoto},
	}
	for _, user := range []string{"owner", "reader"} {
		t.Run(user+" attachments", func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.GetMeeting(w, withChiParam(withUserCtx(httptest.NewRequest(http.MethodGet, "/", nil), user), "meetingId", "meeting"))
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			var result model.MeetingDetailResponse
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Attachments) != 3 || result.Attachments[0].URL == "" || result.Attachments[1].URL == "" || result.Attachments[2].URL != "" {
				t.Fatalf("unexpected attachment access: %+v", result.Attachments)
			}
		})
	}
	for _, tc := range []struct {
		name      string
		audioKey  string
		audioKeys []string
		status    int
	}{
		{"single valid", "audio/owner/meeting/녹음.webm", nil, http.StatusOK},
		{"single foreign", "docs/victim/private.pdf", nil, http.StatusNotFound},
		{"foreign owner", "audio/victim/meeting/recording.webm", nil, http.StatusNotFound},
		{"foreign meeting", "audio/owner/other/recording.webm", nil, http.StatusNotFound},
		{"multipart valid", "", []string{"audio/owner/meeting/part_000.webm", "audio/owner/meeting/part_001.webm"}, http.StatusOK},
		{"multipart mixed", "", []string{"audio/owner/meeting/part_000.webm", "docs/victim/private.pdf"}, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meeting.AudioKey, meeting.AudioKeys = tc.audioKey, tc.audioKeys
			w := httptest.NewRecorder()
			handler.GetAudioURL(w, withChiParam(withUserCtx(httptest.NewRequest(http.MethodGet, "/", nil), "reader"), "meetingId", "meeting"))
			if w.Code != tc.status || strings.Contains(w.Body.String(), "victim") {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
	delete(repo.shares, "reader|meeting")
	w := httptest.NewRecorder()
	handler.GetAudioURL(w, withChiParam(withUserCtx(httptest.NewRequest(http.MethodGet, "/", nil), "reader"), "meetingId", "meeting"))
	if w.Code != http.StatusNotFound && w.Code != http.StatusForbidden {
		t.Fatalf("revoked reader retained access: status=%d body=%s", w.Code, w.Body)
	}
}
