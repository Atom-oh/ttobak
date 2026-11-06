package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func teamCropFixture() (*model.Meeting, *model.Meeting, *model.MeetingRef) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	source := &model.Meeting{UserID: "owner", MeetingID: "source", Status: model.StatusDone, UpdatedAt: now, AccountID: "acc", SharedToAccount: true}
	cropped := &model.Meeting{PK: "USER#owner", SK: "MEETING#copy", UserID: "owner", MeetingID: "copy", Date: now,
		Status: model.StatusTranscribing, AccountID: "acc", SharedToAccount: true, EntityType: "MEETING"}
	ref := &model.MeetingRef{PK: "ACCOUNT#acc", SK: "MEETINGREF#copy", AccountID: "acc", MeetingID: "copy", OwnerUserID: "owner",
		EntityType: model.EntityTypeMeetingRef}
	return source, cropped, ref
}

// A member's crop copy and its team ref are written in one transaction fenced
// on the source publication and the member's live grant.
func TestMemberCropPublishesAtomicallyUnderTheLiveGrant(t *testing.T) {
	t.Setenv(AccountDescendantReadsEnv, "")
	memberRow := `{"PK":{"S":"ACCOUNT#acc"},"SK":{"S":"MEMBER#member"},"accountId":{"S":"acc"},"userId":{"S":"member"}}`
	for _, tc := range []struct {
		name        string
		member      bool
		unpublished bool
		err         error
	}{
		{"success", true, false, nil},
		{"removed member", false, false, ErrSummaryAccess},
		{"source unpublished concurrently", true, true, ErrConditionFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var items []any
			repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
				var body map[string]any
				json.NewDecoder(req.Body).Decode(&body)
				target := req.Header.Get("X-Amz-Target")
				switch {
				case strings.HasSuffix(target, ".GetItem") && tc.member:
					return summaryHTTPResponse(200, `{"Item":`+memberRow+`}`, nil), nil
				case strings.HasSuffix(target, ".TransactWriteItems"):
					items = body["TransactItems"].([]any)
					if tc.unpublished {
						return summaryHTTPResponse(400, `{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException","message":"cancelled","CancellationReasons":[{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"},{"Code":"None"}]}`, nil), nil
					}
				}
				return summaryHTTPResponse(200, `{}`, nil), nil
			})
			source, cropped, ref := teamCropFixture()
			err := repo.CreateAudioCropForTeam(context.Background(), source, cropped, "member", ref)
			if !errors.Is(err, tc.err) {
				t.Fatalf("got %v, want %v", err, tc.err)
			}
			if tc.name == "removed member" {
				if items != nil {
					t.Fatal("a removed member's crop reached a write")
				}
				return
			}
			if len(items) != 4 {
				t.Fatalf("want source check, copy, ref and member check in one transaction, got %d", len(items))
			}
			sourceCheck := items[0].(map[string]any)["ConditionCheck"].(map[string]any)
			names := sourceCheck["ExpressionAttributeNames"].(map[string]any)
			fenced := map[string]bool{}
			for token, name := range names {
				if strings.Contains(sourceCheck["ConditionExpression"].(string), token) {
					fenced[name.(string)] = true
				}
			}
			if !fenced["accountId"] || !fenced["sharedToAccount"] || !fenced["updatedAt"] {
				t.Fatalf("source not fenced on publication: %v", fenced)
			}
			copyItem := items[1].(map[string]any)["Put"].(map[string]any)["Item"].(map[string]any)
			refItem := items[2].(map[string]any)["Put"].(map[string]any)["Item"].(map[string]any)
			if copyItem["sharedToAccount"].(map[string]any)["BOOL"] != true || refItem["SK"].(map[string]any)["S"] != "MEETINGREF#copy" ||
				refItem["ownerUserId"].(map[string]any)["S"] != "owner" {
				t.Fatalf("copy/ref: %v %v", copyItem, refItem)
			}
			member := items[3].(map[string]any)["ConditionCheck"].(map[string]any)
			if member["Key"].(map[string]any)["SK"].(map[string]any)["S"] != "MEMBER#member" {
				t.Fatal("member grant not fenced")
			}
		})
	}
}

func TestMemberCropRejectsMismatchedPublication(t *testing.T) {
	repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
		t.Fatal("a mismatched team crop reached the store")
		return nil, nil
	})
	source, cropped, ref := teamCropFixture()
	ref.OwnerUserID = "member"
	if err := repo.CreateAudioCropForTeam(context.Background(), source, cropped, "member", ref); !errors.Is(err, ErrConditionFailed) {
		t.Fatalf("ref naming the actor: %v", err)
	}
}
