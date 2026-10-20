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
		"id": "Kim", "text": "Ask Kim", "assignee": name, "completed": true,
		"priority": "high", "dueDate": "2026-10-21", "custom": map[string]interface{}{"value": "Kim"},
	}
	if len(items) != 1 || !reflect.DeepEqual(items[0], want) {
		t.Fatalf("rename changed action metadata: %+v", items)
	}
	stored := repo.meetingsByID["meeting"]
	if stored.Content != "Kim agreed" || stored.TranscriptA != "["+name+"]\nhello" {
		t.Fatalf("custom-name correction changed prose or missed attribution: %+v", stored)
	}
}

func TestSharedSpeakerRenamePreservesRestrictedDerivedContent(t *testing.T) {
	for _, cachedShare := range []bool{false, true} {
		t.Run(fmt.Sprintf("cached-share=%t", cachedShare), func(t *testing.T) {
			repo := newMockMeetingRepo()
			original := &model.Meeting{
				MeetingID: "meeting", UserID: "owner", AccountID: "account", SharedToAccount: true,
				Content: "Private spk_0 summary", ActionItems: `[{"id":"task","text":"Private task","assignee":"spk_0"}]`,
				Insights: "Private insights", Sentiment: "Private sentiment", LiveSummary: "Private live summary",
				Tags: []string{"private"}, LinkedMeetingIDs: []string{"private-link"},
				TranscriptA: "[spk_0]\nhello", TranscriptB: "[spk_0]\nhello",
				TranscriptSegments: `[{"speaker":"spk_0","text":"hello"}]`, UpdatedAt: time.Now().UTC(),
			}
			repo.addMeeting(original)
			repo.addMember("account", "reader", model.RoleTAM)
			repo.members["account|reader"].SummaryPrivacyVersion = model.SummaryPrivacyVersion
			if cachedShare {
				repo.shares[shareKey("reader", "meeting")] = &model.Share{
					Permission: model.PermissionRead, Origin: model.ShareOriginAccount, AccountID: "account",
				}
			}
			svc := newMeetingServiceWithRepo(repo)
			view, _, err := svc.checkAccess(context.Background(), "reader", "meeting")
			if err != nil || !view.SummaryReadRestricted || view.Content != "" || view.ActionItems != "" {
				t.Fatalf("test did not exercise the restricted account projection: view=%+v err=%v", view, err)
			}
			_, err = svc.UpdateSpeakers(context.Background(), "reader", "meeting",
				&model.UpdateSpeakersRequest{SpeakerMap: map[string]string{"spk_0": "Kim"}})
			if err != nil {
				t.Fatal(err)
			}
			stored := repo.meetingsByID["meeting"]
			if stored.Content != original.Content || stored.ActionItems != original.ActionItems ||
				stored.Insights != original.Insights || stored.Sentiment != original.Sentiment ||
				stored.LiveSummary != original.LiveSummary || !reflect.DeepEqual(stored.Tags, original.Tags) ||
				!reflect.DeepEqual(stored.LinkedMeetingIDs, original.LinkedMeetingIDs) {
				t.Fatalf("projected fields were persisted over canonical derived content: %+v", stored)
			}
			if stored.TranscriptA != "[Kim]\nhello" || !strings.Contains(stored.TranscriptSegments, `"speaker":"Kim"`) {
				t.Fatalf("restricted account member could not correct readable speaker attribution: %+v", stored)
			}
		})
	}
}

func TestSharedSpeakerRepeatedRenameCannotReplaceOrdinaryProse(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) {
			repo := newMockMeetingRepo()
			meeting := &model.Meeting{
				MeetingID: "meeting", UserID: "owner", Content: "spk_0 agreed on budget",
				TranscriptA: "[spk_0]\nbudget remains", TranscriptB: "[spk_0]\r\nbudget remains\r\n",
				ActionItems: `[{"id":"budget","text":"Ask spk_0 about budget","assignee":"spk_0","completed":true}]`,
			}
			if !legacy {
				meeting.TranscriptSegments = `[{"id":"budget","speaker":"spk_0","text":"budget remains"}]`
			}
			repo.addMeeting(meeting)
			repo.shares[shareKey("reader", "meeting")] = &model.Share{Permission: model.PermissionRead}
			svc := newMeetingServiceWithRepo(repo)
			const name = `Kim "TJ"`
			for _, mapping := range []map[string]string{{"spk_0": "budget"}, {"budget": name}} {
				if _, err := svc.UpdateSpeakers(context.Background(), "reader", "meeting",
					&model.UpdateSpeakersRequest{SpeakerMap: mapping}); err != nil {
					t.Fatal(err)
				}
			}
			stored := repo.meetingsByID["meeting"]
			if stored.Content != "budget agreed on budget" || stored.TranscriptA != "["+name+"]\nbudget remains" ||
				stored.TranscriptB != "["+name+"]\r\nbudget remains\r\n" || stored.SpeakerMap["budget"] != name {
				t.Fatalf("repeated correction rewrote prose or missed attribution: %+v", stored)
			}
			var items []map[string]interface{}
			if err := json.Unmarshal([]byte(stored.ActionItems), &items); err != nil {
				t.Fatal(err)
			}
			if len(items) != 1 || items[0]["id"] != "budget" || items[0]["text"] != "Ask budget about budget" ||
				items[0]["assignee"] != name || items[0]["completed"] != true {
				t.Fatalf("repeated correction rewrote action text or metadata: %+v", items)
			}
			if !legacy {
				var segments []segmentSpeakerField
				if err := json.Unmarshal([]byte(stored.TranscriptSegments), &segments); err != nil ||
					len(segments) != 1 || segments[0].Speaker != name || segments[0].Text != "budget remains" || segments[0].ID != "budget" {
					t.Fatalf("repeated correction rewrote segment text/identity: %+v err=%v", segments, err)
				}
			}
		})
	}
}

func TestSpeakerCustomNameProseReplacementRetainsOwnerEditorBehavior(t *testing.T) {
	for _, caller := range []string{"owner", "editor"} {
		t.Run(caller, func(t *testing.T) {
			repo := newMockMeetingRepo()
			repo.addMeeting(&model.Meeting{
				MeetingID: "meeting", UserID: "owner", Content: "Kim agreed", TranscriptA: "[Kim]\nAsk Kim",
				TranscriptSegments: `[{"speaker":"Kim","text":"hello"}]`,
				ActionItems:        `[{"text":"Ask Kim","assignee":"Kim"}]`,
			})
			repo.shares[shareKey("editor", "meeting")] = &model.Share{Permission: model.PermissionEdit}
			_, err := newMeetingServiceWithRepo(repo).UpdateSpeakers(context.Background(), caller, "meeting",
				&model.UpdateSpeakersRequest{SpeakerMap: map[string]string{"Kim": "Lee"}})
			if err != nil {
				t.Fatal(err)
			}
			stored := repo.meetingsByID["meeting"]
			if stored.Content != "Lee agreed" || stored.TranscriptA != "[Lee]\nAsk Lee" ||
				!strings.Contains(stored.ActionItems, `"text":"Ask Lee"`) {
				t.Fatalf("owner/editor replacement behavior changed: %+v", stored)
			}
		})
	}
}

func TestSharedSpeakerSwapsUseOriginalAttribution(t *testing.T) {
	repo := newMockMeetingRepo()
	repo.addMeeting(&model.Meeting{
		MeetingID: "meeting", UserID: "owner", TranscriptA: "[spk_0]\nfirst\n[spk_1]\nsecond",
		TranscriptSegments: `[{"speaker":"spk_0","text":"first"},{"speaker":"spk_1","text":"second"}]`,
		ActionItems:        `[{"assignee":"spk_0"},{"assignee":"spk_1"}]`,
	})
	repo.shares[shareKey("reader", "meeting")] = &model.Share{Permission: model.PermissionRead}
	_, err := newMeetingServiceWithRepo(repo).UpdateSpeakers(context.Background(), "reader", "meeting",
		&model.UpdateSpeakersRequest{SpeakerMap: map[string]string{"spk_0": "spk_1", "spk_1": "spk_0"}})
	if err != nil {
		t.Fatal(err)
	}
	stored := repo.meetingsByID["meeting"]
	if stored.TranscriptA != "[spk_1]\nfirst\n[spk_0]\nsecond" ||
		stored.ActionItems != `[{"assignee":"spk_1"},{"assignee":"spk_0"}]` {
		t.Fatalf("speaker swap cascaded: %+v", stored)
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
