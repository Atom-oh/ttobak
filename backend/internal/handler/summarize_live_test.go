package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/service"
)

type fakeLiveSummaryModel struct {
	calls int
	body  []byte
}

func (f *fakeLiveSummaryModel) InvokeModel(_ context.Context, in *bedrockruntime.InvokeModelInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
	f.calls++
	f.body = in.Body
	return &bedrockruntime.InvokeModelOutput{Body: []byte(`{"content":[{"type":"text","text":"## 요약"}],"stop_reason":"end_turn"}`)}, nil
}

type fakeLiveSummaryAccess struct {
	calls   int
	user    string
	meeting string
	err     error
}

func (f *fakeLiveSummaryAccess) AuthorizeEdit(_ context.Context, userID, meetingID string) error {
	f.calls++
	f.user, f.meeting = userID, meetingID
	return f.err
}

func postLiveSummary(h *SummarizeLiveHandler, meetingID, body string) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	router.Post("/api/meetings/{meetingId}/summarize", h.SummarizeLive)
	req := httptest.NewRequest(http.MethodPost, "/api/meetings/"+meetingID+"/summarize", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserIDKey, "alice"))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestSummarizeLiveAuthorizesBeforeInvokingTheModel(t *testing.T) {
	const meeting = "8eeac87a-e4b9-4255-86c3-bddce5c8fcf2"
	body := `{"transcript":"오늘은 배포 일정을 논의했습니다."}`
	tests := []struct {
		name       string
		meetingID  string
		accessErr  error
		body       string
		wantStatus int
		wantAccess int
		wantModel  int
	}{
		{"owner or editor", meeting, nil, body, http.StatusOK, 1, 1},
		{"no access", meeting, service.ErrNotFound, body, http.StatusNotFound, 1, 0},
		{"read-only share", meeting, service.ErrForbidden, body, http.StatusForbidden, 1, 0},
		{"access lookup failure", meeting, errors.New("dynamodb down"), body, http.StatusInternalServerError, 1, 0},
		{"legacy browser id keeps working during the frontend rollout", "meeting_1791767411560", nil, body, http.StatusOK, 0, 1},
		{"legacy-looking id with other text is authorized", "meeting_123abc", service.ErrNotFound, body, http.StatusNotFound, 1, 0},
		{"oversized body", meeting, nil, `{"transcript":"` + strings.Repeat("가", maxLiveSummaryBodyBytes/3+10) + `"}`, http.StatusBadRequest, 1, 0},
		{"empty transcript", meeting, nil, `{"transcript":""}`, http.StatusBadRequest, 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := &fakeLiveSummaryModel{}
			access := &fakeLiveSummaryAccess{err: tt.accessErr}
			rec := postLiveSummary(NewSummarizeLiveHandler(model, access), tt.meetingID, tt.body)
			if rec.Code != tt.wantStatus || access.calls != tt.wantAccess || model.calls != tt.wantModel {
				t.Fatalf("status=%d access=%d model=%d, want %d/%d/%d; body=%s",
					rec.Code, access.calls, model.calls, tt.wantStatus, tt.wantAccess, tt.wantModel, rec.Body.String())
			}
			if access.calls > 0 && (access.user != "alice" || access.meeting != tt.meetingID) {
				t.Fatalf("authorized %q/%q, want alice/%s", access.user, access.meeting, tt.meetingID)
			}
			if tt.wantStatus == http.StatusOK {
				var res map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || res["summary"] != "## 요약" {
					t.Fatalf("summary response = %s (%v)", rec.Body.String(), err)
				}
				if !strings.Contains(string(model.body), `"thinking":{"type":"disabled"}`) {
					t.Fatalf("Haiku request lost its thinking override: %s", model.body)
				}
			}
		})
	}
}
