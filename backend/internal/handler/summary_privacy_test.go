package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type privacyActionHandlerRepo struct {
	*mockHandlerMeetingRepo
	statusReads int
}

func (r *privacyActionHandlerRepo) GetActionAnalysis(context.Context, string) (*model.ActionItemsAnalysis, error) {
	r.statusReads++
	return nil, nil
}
func (r *privacyActionHandlerRepo) QueueActionAnalysis(context.Context, string, string, string, *model.ActionItemsAnalysis, *model.ActionItemsAnalysis) error {
	return errors.New("unexpected action analysis write")
}
func (r *privacyActionHandlerRepo) StartActionAnalysis(context.Context, string, string, time.Time, int64) error {
	return errors.New("unexpected action analysis write")
}
func (r *privacyActionHandlerRepo) FailActionAnalysis(context.Context, string, *model.ActionItemsAnalysis, string, time.Time) error {
	return errors.New("unexpected action analysis write")
}
func (r *privacyActionHandlerRepo) CompleteActionAnalysis(context.Context, string, string, string, string, string, string, time.Time) error {
	return errors.New("unexpected action analysis write")
}

func TestSummaryPrivacyHandlersDoNotRestoreGuardedActionItems(t *testing.T) {
	repo := &privacyActionHandlerRepo{mockHandlerMeetingRepo: newMockHandlerMeetingRepo()}
	repo.addMeeting(&model.Meeting{
		UserID: "owner", MeetingID: "meeting", Status: model.StatusDone,
		AccountID: "account", SharedToAccount: true,
		Content: "PRIVATE_PREDECESSOR", ActionItems: `[{"id":"task","text":"PRIVATE_ACTION_MARKER"}]`,
		Notes: "parent user notes",
	})
	repo.members["account|reader"] = &model.AccountMember{
		AccountID: "account", UserID: "reader", SummaryPrivacyVersion: 1,
	}
	meetings := service.NewMeetingServiceForTest(repo)
	actions := service.NewActionItemsAnalysisService(repo, meetings, nil, nil)
	detail := &MeetingHandler{meetingService: meetings, actionItemsService: actions}
	items := NewActionItemsHandler(actions)
	for _, endpoint := range []string{"detail", "action-items"} {
		t.Run(endpoint, func(t *testing.T) {
			req := withChiParam(withUserCtx(httptest.NewRequest(http.MethodGet, "/", nil), "reader"), "meetingId", "meeting")
			w := httptest.NewRecorder()
			expected := http.StatusOK
			if endpoint == "detail" {
				detail.GetMeeting(w, req)
			} else {
				expected = http.StatusConflict
				items.Get(w, req)
			}
			if w.Code != expected || strings.Contains(w.Body.String(), "PRIVATE_ACTION_MARKER") ||
				!strings.Contains(w.Body.String(), "SHARING_REGEN_REQUIRED") {
				t.Fatalf("restricted endpoint=%s status=%d body=%s", endpoint, w.Code, w.Body)
			}
			if endpoint == "detail" && !strings.Contains(w.Body.String(), "parent user notes") {
				t.Fatal("restricted derived read removed the authorized raw notes")
			}
		})
	}
	if repo.statusReads != 0 {
		t.Fatal("guarded action read consulted canonical analysis state")
	}
	for _, user := range []string{"owner", "direct"} {
		repo.shares["direct|meeting"] = &model.Share{
			MeetingID: "meeting", OwnerID: "owner", SharedToID: "direct", Permission: model.PermissionRead,
		}
		w := httptest.NewRecorder()
		items.Get(w, withChiParam(withUserCtx(httptest.NewRequest(http.MethodGet, "/", nil), user), "meetingId", "meeting"))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "PRIVATE_ACTION_MARKER") {
			t.Fatalf("independent %s read lost: status=%d body=%s", user, w.Code, w.Body)
		}
	}
}
