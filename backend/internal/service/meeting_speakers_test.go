package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func TestUpdateSpeakersSharedAccess(t *testing.T) {
	for _, tc := range []struct {
		name       string
		caller     string
		permission string
		origin     string
		member     bool
		published  bool
		allowed    bool
	}{
		{"owner", "owner", "", "", false, false, true},
		{"direct reader", "reader", model.PermissionRead, "", false, false, true},
		{"direct editor", "reader", model.PermissionEdit, "", false, false, true},
		{"team reader", "reader", model.PermissionRead, model.ShareOriginAccount, true, true, true},
		{"new team member without cached share", "reader", "", "", true, true, true},
		{"removed team member with cached share", "reader", model.PermissionRead, model.ShareOriginAccount, false, true, false},
		{"team publication revoked", "reader", model.PermissionRead, model.ShareOriginAccount, true, false, false},
		{"private classification only", "reader", "", "", true, false, false},
		{"direct share revoked", "reader", "", "", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newMockMeetingRepo()
			repo.addMeeting(&model.Meeting{
				MeetingID: "meeting", UserID: "owner", Status: model.StatusDone,
				Title: "Original title", Notes: "Original notes", Content: "spk_0 summary",
				TranscriptA: "[spk_0]\nhello", TranscriptB: "[spk_0]\n안녕하세요",
				TranscriptSegments: `[{"id":"seg-1","speaker":"spk_0","text":"hello"}]`,
				ActionItems:        `[{"id":"task-1","text":"Ask spk_0","assignee":"spk_0","completed":true}]`,
				AccountID:          "account", SharedToAccount: tc.published, UpdatedAt: time.Now().UTC(),
			})
			if tc.permission != "" {
				repo.shares[shareKey(tc.caller, "meeting")] = &model.Share{
					MeetingID: "meeting", OwnerID: "owner", Permission: tc.permission,
					Origin: tc.origin, AccountID: "account",
				}
			}
			if tc.member {
				repo.addMember("account", tc.caller, model.RoleTAM)
			}
			svc := newMeetingServiceWithRepo(repo)
			_, err := svc.UpdateSpeakers(context.Background(), tc.caller, "meeting",
				&model.UpdateSpeakersRequest{SpeakerMap: map[string]string{"spk_0": "김팀장"}})
			stored := repo.meetingsByID["meeting"]
			if !tc.allowed {
				if !errors.Is(err, ErrNotFound) || stored.Content != "spk_0 summary" || len(stored.SpeakerMap) != 0 {
					t.Fatalf("denied rename changed meeting: err=%v meeting=%+v", err, stored)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if stored.Content != "김팀장 summary" || stored.TranscriptA != "[김팀장]\nhello" ||
				stored.TranscriptB != "[김팀장]\n안녕하세요" || !strings.Contains(stored.TranscriptSegments, `"speaker":"김팀장"`) ||
				stored.SpeakerMap["spk_0"] != "김팀장" {
				t.Fatalf("rename was not saved on owner's meeting: %+v", stored)
			}
			if stored.Title != "Original title" || stored.Notes != "Original notes" || stored.SharedToAccount != tc.published {
				t.Fatal("speaker correction changed unrelated meeting fields")
			}
			if tc.caller != "owner" && tc.permission != model.PermissionEdit {
				notes := "Unauthorized notes"
				for _, req := range []*model.UpdateMeetingRequest{
					{Title: "Unauthorized title"}, {Content: "Unauthorized summary"}, {Notes: &notes},
				} {
					if _, err := svc.UpdateMeeting(context.Background(), tc.caller, "meeting", req); !errors.Is(err, ErrForbidden) {
						t.Fatalf("speaker correction granted general editing: %v", err)
					}
				}
			}
		})
	}
}

func TestUpdateSpeakersRejectsArbitraryOrUnboundedReplacement(t *testing.T) {
	many := make(map[string]string)
	for i := 0; i < 101; i++ {
		many[fmt.Sprintf("spk_%d", i)] = "Kim"
	}
	for _, tc := range []struct {
		name    string
		mapping map[string]string
	}{
		{"arbitrary summary text", map[string]string{"summary": "replacement"}},
		{"unknown speaker", map[string]string{"spk_9": "Kim"}},
		{"prefix of another speaker", map[string]string{"spk_1": "Kim"}},
		{"blank name", map[string]string{"spk_0": "  "}},
		{"long name", map[string]string{"spk_0": strings.Repeat("김", 101)}},
		{"control character", map[string]string{"spk_0": "Kim\ninjected text"}},
		{"too many mappings", many},
		{"empty mapping", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newMockMeetingRepo()
			original := &model.Meeting{MeetingID: "meeting", UserID: "owner", Content: "spk_0 summary spk_1000000"}
			repo.addMeeting(original)
			repo.shares[shareKey("reader", "meeting")] = &model.Share{Permission: model.PermissionRead}
			_, err := newMeetingServiceWithRepo(repo).UpdateSpeakers(context.Background(), "reader", "meeting",
				&model.UpdateSpeakersRequest{SpeakerMap: tc.mapping})
			if !errors.Is(err, ErrInvalidInput) || !reflect.DeepEqual(repo.meetingsByID["meeting"], original) {
				t.Fatalf("invalid mapping not rejected without writing: %v", err)
			}
		})
	}
}

func TestUpdateSpeakersCanCorrectExistingNameAndPreservesActionMetadata(t *testing.T) {
	repo := newMockMeetingRepo()
	repo.addMeeting(&model.Meeting{
		MeetingID: "meeting", UserID: "owner",
		Content: "Kim agreed", TranscriptA: "[Kim]\nhello",
		TranscriptSegments: `[{"id":"Kim","speaker":"Kim","text":"hello"}]`,
		ActionItems:        `[{"id":"Kim","text":"Ask Kim","assignee":"Kim","completed":true,"priority":"high","dueDate":"2026-10-21","custom":{"value":"Kim"}}]`,
	})
	repo.shares[shareKey("reader", "meeting")] = &model.Share{Permission: model.PermissionRead}
	const name = `Kim "TJ"`
	_, err := newMeetingServiceWithRepo(repo).UpdateSpeakers(context.Background(), "reader", "meeting",
		&model.UpdateSpeakersRequest{SpeakerMap: map[string]string{"Kim": name}})
	if err != nil {
		t.Fatal(err)
	}
	var items []map[string]interface{}
	if err := json.Unmarshal([]byte(repo.meetingsByID["meeting"].ActionItems), &items); err != nil {
		t.Fatalf("quoted name corrupted action JSON: %v", err)
	}
	want := map[string]interface{}{
		"id": "Kim", "text": "Ask " + name, "assignee": name, "completed": true,
		"priority": "high", "dueDate": "2026-10-21", "custom": map[string]interface{}{"value": "Kim"},
	}
	if len(items) != 1 || !reflect.DeepEqual(items[0], want) {
		t.Fatalf("rename changed action metadata: %+v", items)
	}
}

func TestSharedSpeakerRenameRejectsConcurrentChanges(t *testing.T) {
	svc, repo := newMeetingWriteRace()
	repo.shares[shareKey("reader", "meeting")] = &model.Share{Permission: model.PermissionRead}
	repo.beforeWrite = func(meeting *model.Meeting) {
		meeting.Content = "Concurrent human edit"
		meeting.UpdatedAt = time.Now().UTC()
	}
	_, err := svc.UpdateSpeakers(context.Background(), "reader", "meeting",
		&model.UpdateSpeakersRequest{SpeakerMap: map[string]string{"spk_0": "Kim"}})
	if !errors.Is(err, repository.ErrConditionFailed) || repo.meetingsByID["meeting"].Content != "Concurrent human edit" {
		t.Fatalf("shared rename overwrote concurrent human edit: %v", err)
	}
}
