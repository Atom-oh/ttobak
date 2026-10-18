package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	cognitoidptypes "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

// grantFailureRepo injects the repository outcome of each grant/publication
// write so the handler mapping is exercised end to end through the service.
type grantFailureRepo struct {
	*mockHandlerMeetingRepo
	err error
}

func (r *grantFailureRepo) CreateShare(context.Context, string, string, string, string, string, string, string) (*model.Share, error) {
	return nil, r.err
}
func (r *grantFailureRepo) PutPendingShare(context.Context, *model.PendingShare) error {
	return r.err
}
func (r *grantFailureRepo) UpdateMeetingFields(context.Context, string, string, map[string]interface{}) error {
	return r.err
}

// invitedCognito reports an invited Cognito user with a sub, so sharing with
// an email that has no PROFILE row takes the pending-share branch.
type invitedCognito struct{ *stubCognitoAdminAPI }

func (invitedCognito) AdminGetUser(context.Context, *cognitoidentityprovider.AdminGetUserInput, ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.AdminGetUserOutput, error) {
	return &cognitoidentityprovider.AdminGetUserOutput{UserAttributes: []cognitoidptypes.AttributeType{
		{Name: aws.String("sub"), Value: aws.String("invited-sub")},
	}}, nil
}

func TestShareHandlersMapSummaryPrivacyRejections(t *testing.T) {
	failures := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"sharing unsafe", fmt.Errorf("grant fence: %w", repository.ErrSummarySharingUnsafe), http.StatusConflict, ErrCodeSummarySharingUnsafe},
		{"source changed", repository.ErrConditionFailed, http.StatusConflict, model.ErrCodeConflict},
		{"membership lost", repository.ErrSummaryAccess, http.StatusForbidden, model.ErrCodeForbidden},
		{"storage failure", errors.New("dynamodb unavailable"), http.StatusInternalServerError, model.ErrCodeInternalError},
	}
	paths := []struct {
		name string
		call func(*ShareHandler, *httptest.ResponseRecorder)
	}{
		{"direct share", func(h *ShareHandler, w *httptest.ResponseRecorder) {
			r := httptest.NewRequest(http.MethodPost, "/api/meetings/meeting/share", strings.NewReader(`{"email":"member@example.com"}`))
			h.ShareMeeting(w, withChiParam(withUserCtx(r, "owner"), "meetingId", "meeting"))
		}},
		{"pending share", func(h *ShareHandler, w *httptest.ResponseRecorder) {
			r := httptest.NewRequest(http.MethodPost, "/api/meetings/meeting/share", strings.NewReader(`{"email":"invited@example.com"}`))
			h.ShareMeeting(w, withChiParam(withUserCtx(r, "owner"), "meetingId", "meeting"))
		}},
		{"account publication", func(h *ShareHandler, w *httptest.ResponseRecorder) {
			r := httptest.NewRequest(http.MethodPost, "/api/meetings/meeting/share-account", strings.NewReader(`{"accountId":"account"}`))
			h.ShareToAccount(w, withChiParam(withUserCtx(r, "owner"), "meetingId", "meeting"))
		}},
	}
	for _, path := range paths {
		for _, failure := range failures {
			t.Run(path.name+"/"+failure.name, func(t *testing.T) {
				repo := &grantFailureRepo{mockHandlerMeetingRepo: newMockHandlerMeetingRepo(), err: failure.err}
				repo.addMeeting(&model.Meeting{UserID: "owner", MeetingID: "meeting", Status: model.StatusDone})
				repo.users["member@example.com"] = &model.User{UserID: "member", Email: "member@example.com"}
				repo.members["account|owner"] = &model.AccountMember{AccountID: "account", UserID: "owner", Role: "SA"}
				meetings := service.NewMeetingServiceForTest(repo)
				meetings.SetCognitoAdminAPI(invitedCognito{&stubCognitoAdminAPI{}}, "pool")
				w := httptest.NewRecorder()
				path.call(NewShareHandler(meetings), w)
				var body struct {
					Error struct{ Code, Message string } `json:"error"`
				}
				if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if w.Code != failure.status || body.Error.Code != failure.code || body.Error.Message == "" {
					t.Fatalf("got %d %+v, want %d %s", w.Code, body.Error, failure.status, failure.code)
				}
			})
		}
	}
}
