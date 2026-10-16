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
	statusReads     int
	statusErr       error
	memberErr       error
	afterStatusRead func()
}

func (r *privacyActionHandlerRepo) GetActionAnalysis(context.Context, string) (*model.ActionItemsAnalysis, error) {
	r.statusReads++
	if r.afterStatusRead != nil {
		change := r.afterStatusRead
		r.afterStatusRead = nil
		change()
	}
	return nil, r.statusErr
}
func (r *privacyActionHandlerRepo) GetMember(ctx context.Context, account, user string) (*model.AccountMember, error) {
	if r.memberErr != nil {
		return nil, r.memberErr
	}
	return r.mockHandlerMeetingRepo.GetMember(ctx, account, user)
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

func TestSummaryPrivacyDetailRechecksLateRestrictionAndRevocation(t *testing.T) {
	t.Setenv(service.SummaryPrivacyReaderEnv, "0")
	for _, change := range []string{"membership", "publication", "revocation", "forbidden"} {
		t.Run(change, func(t *testing.T) {
			repo := &privacyActionHandlerRepo{mockHandlerMeetingRepo: newMockHandlerMeetingRepo()}
			meeting := &model.Meeting{UserID: "owner", MeetingID: "meeting", Status: model.StatusDone,
				AccountID: "account", SharedToAccount: true, Content: "PRIVATE_CONTENT_MARKER",
				ActionItems: `[{"id":"task","text":"PRIVATE_ACTION_MARKER"}]`, Notes: "authorized raw notes"}
			member := &model.AccountMember{AccountID: "account", UserID: "reader"}
			if change == "publication" {
				member.SummaryPrivacyVersion = 1
				meeting.SummaryPrivacy = &model.SummaryPrivacy{Version: 1, State: model.SummaryPrivacySafe,
					GenerationID: "generation", ContentSHA256: model.SummaryPrivacyHash(meeting.Content)}
			}
			repo.addMeeting(meeting)
			repo.members["account|reader"] = member
			repo.afterStatusRead = func() {
				switch change {
				case "membership":
					member.SummaryPrivacyVersion = 1
				case "publication":
					meeting.SummaryPrivacy.State = model.SummaryPrivacyPrivate
				case "revocation":
					delete(repo.members, "account|reader")
				case "forbidden":
					repo.memberErr = service.ErrForbidden
				}
			}
			meetings := service.NewMeetingServiceForTest(repo)
			handler := &MeetingHandler{meetingService: meetings,
				actionItemsService: service.NewActionItemsAnalysisService(repo, meetings, nil, nil)}
			w := httptest.NewRecorder()
			handler.GetMeeting(w, withChiParam(withUserCtx(httptest.NewRequest(http.MethodGet, "/", nil), "reader"), "meetingId", "meeting"))
			expected := http.StatusOK
			if change == "revocation" {
				expected = http.StatusNotFound
			} else if change == "forbidden" {
				expected = http.StatusForbidden
			}
			body := w.Body.String()
			if repo.statusReads == 0 || w.Code != expected || strings.Contains(body, "PRIVATE_CONTENT_MARKER") ||
				strings.Contains(body, "PRIVATE_ACTION_MARKER") {
				t.Fatalf("late %s: status=%d body=%s", change, w.Code, body)
			}
			if expected == http.StatusOK && (!strings.Contains(body, "authorized raw notes") ||
				!strings.Contains(body, `"derivedReadRestricted":true`) || !strings.Contains(body, "SHARING_REGEN_REQUIRED")) {
				t.Fatalf("late %s lost current raw projection: %s", change, body)
			}
			if meeting.Content != "PRIVATE_CONTENT_MARKER" {
				t.Fatal("read projection changed canonical content")
			}
		})
	}
}

func TestSummaryPrivacyDetailPreservesAuthorizedStatusFailure(t *testing.T) {
	for _, user := range []string{"owner", "direct"} {
		t.Run(user, func(t *testing.T) {
			repo := &privacyActionHandlerRepo{mockHandlerMeetingRepo: newMockHandlerMeetingRepo(), statusErr: errors.New("synthetic status failure")}
			repo.addMeeting(&model.Meeting{UserID: "owner", MeetingID: "meeting", Status: model.StatusDone,
				Content: "AUTHORIZED_CONTENT", ActionItems: `[{"id":"task","text":"AUTHORIZED_ACTION"}]`, Notes: "authorized raw notes"})
			repo.shares["direct|meeting"] = &model.Share{MeetingID: "meeting", OwnerID: "owner", SharedToID: "direct", Permission: model.PermissionRead}
			meetings := service.NewMeetingServiceForTest(repo)
			handler := &MeetingHandler{meetingService: meetings,
				actionItemsService: service.NewActionItemsAnalysisService(repo, meetings, nil, nil)}
			w := httptest.NewRecorder()
			handler.GetMeeting(w, withChiParam(withUserCtx(httptest.NewRequest(http.MethodGet, "/", nil), user), "meetingId", "meeting"))
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "AUTHORIZED_CONTENT") ||
				!strings.Contains(w.Body.String(), "AUTHORIZED_ACTION") || !strings.Contains(w.Body.String(), "STATUS_UNAVAILABLE") {
				t.Fatalf("%s status failure changed authorized detail: status=%d body=%s", user, w.Code, w.Body)
			}
		})
	}
}
