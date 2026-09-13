package service

import (
	"context"
	"errors"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestPrivateAccountRelinkRevokesTeamAccessPreservesDirectSharesAndNotes(t *testing.T) {
	for _, target := range []string{"account-a", "account-b"} {
		t.Run(target, func(t *testing.T) {
			ctx := context.Background()
			repo := &meetingWriteRaceRepo{mockMeetingRepo: newMockMeetingRepo()}
			repo.addMeeting(&model.Meeting{MeetingID: "meeting", UserID: "owner", Status: model.StatusDone,
				AccountID: "account-a", SharedToAccount: true, Notes: "saved notes", Content: "saved summary"})
			repo.addMember("account-a", "owner", model.RoleSA)
			repo.addMember("account-b", "owner", model.RoleSA)
			repo.addMember("account-a", "team-a", model.RoleTAM)
			repo.addMember("account-b", "team-b", model.RoleTAM)
			// This cached account-origin grant must become inert without a
			// cleanup write. A direct grant remains independently authorized.
			repo.shares[shareKey("team-a", "meeting")] = &model.Share{MeetingID: "meeting", OwnerID: "owner",
				Permission: model.PermissionRead, Origin: model.ShareOriginAccount, AccountID: "account-a"}
			repo.shares[shareKey("direct", "meeting")] = &model.Share{MeetingID: "meeting", OwnerID: "owner", Permission: model.PermissionEdit}
			svc := newMeetingServiceWithRepo(repo)
			if _, err := svc.GetMeetingDetail(ctx, "team-a", "meeting"); err != nil {
				t.Fatalf("initial team sharing not readable: %v", err)
			}
			if _, err := svc.GetMeetingDetail(ctx, "team-b", "meeting"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("other team already has access: %v", err)
			}

			// A note saved after the link's read must not be overwritten by it.
			repo.beforeWrite = func(m *model.Meeting) { m.Notes = "concurrent notes" }
			if err := svc.LinkMeetingToAccount(ctx, "owner", "meeting", target); err != nil {
				t.Fatal(err)
			}
			owner, err := svc.GetMeetingDetail(ctx, "owner", "meeting")
			if err != nil {
				t.Fatal(err)
			}
			if owner.AccountID != target || owner.SharedToAccount || owner.Notes != "concurrent notes" || owner.Content != "saved summary" {
				t.Errorf("private relink did not preserve content and disable team sharing: %+v", owner)
			}
			for _, user := range []string{"team-a", "team-b"} {
				if detail, err := svc.GetMeetingDetail(ctx, user, "meeting"); !errors.Is(err, ErrNotFound) || detail != nil {
					t.Errorf("team access survived private relink: user=%s err=%v", user, err)
				}
			}
			direct, err := svc.GetMeetingDetail(ctx, "direct", "meeting")
			if err != nil || direct.Permission != model.PermissionEdit || direct.AccountID != target || direct.SharedToAccount {
				t.Fatalf("direct share changed after private relink: detail=%+v err=%v", direct, err)
			}
			notes := "direct editor's note"
			if _, err := svc.UpdateMeeting(ctx, "direct", "meeting", &model.UpdateMeetingRequest{Notes: &notes, ExpectedNotes: &direct.Notes}); err != nil {
				t.Fatalf("direct editor lost notes access: %v", err)
			}
			if repo.meetings[meetingKey("owner", "meeting")].Notes != notes {
				t.Fatal("direct editor's notes were not saved")
			}
		})
	}
}

func TestPrivateAccountRelinkRetainsAuthorizationChecks(t *testing.T) {
	for _, tc := range []struct {
		name, caller string
		member       bool
		lookupFails  bool
	}{
		{"owner missing target membership", "owner", false, false},
		{"direct editor is not owner", "editor", true, false},
		{"membership lookup failure", "owner", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newMockMeetingRepo()
			repo.addMeeting(&model.Meeting{MeetingID: "meeting", UserID: "owner", Status: model.StatusDone,
				AccountID: "account-a", SharedToAccount: true, Notes: "saved notes"})
			if tc.member {
				repo.addMember("account-b", tc.caller, model.RoleSA)
			}
			repo.shares[shareKey("editor", "meeting")] = &model.Share{MeetingID: "meeting", OwnerID: "owner", Permission: model.PermissionEdit}
			if tc.lookupFails {
				repo.getMemberErrCount = 1
			}
			err := newMeetingServiceWithRepo(repo).LinkMeetingToAccount(context.Background(), tc.caller, "meeting", "account-b")
			if err == nil || (!tc.lookupFails && !errors.Is(err, ErrForbidden)) {
				t.Fatalf("unauthorized relink not rejected: %v", err)
			}
			m := repo.meetings[meetingKey("owner", "meeting")]
			if m.AccountID != "account-a" || !m.SharedToAccount || m.Notes != "saved notes" {
				t.Fatal("rejected private link changed the original meeting")
			}
		})
	}
}
