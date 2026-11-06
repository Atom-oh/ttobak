package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

// Account-team access on a published meeting is creator-equivalent edit access
// (ADR-016 2026-11-06 amendment); delete stays owner-only.

func TestResolveSharedAccessAccountTeamPermission(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	readShare := &model.Share{OwnerID: "owner", SharedToID: "reader", MeetingID: "meeting", Permission: model.PermissionRead}

	t.Run("direct read share with live team membership yields edit", func(t *testing.T) {
		r := newDescendantAccessRepo()
		r.members = map[string]*model.AccountMember{}
		r.member("child", "reader", model.RoleSA)
		r.share = readShare
		meeting, permission, err := resolveSharedAccess(context.Background(), r, "reader", "meeting")
		if err != nil || meeting == nil || permission != model.PermissionEdit {
			t.Fatalf("got %v/%q/%v; want edit", meeting != nil, permission, err)
		}
	})
	t.Run("direct read share without membership stays read", func(t *testing.T) {
		r := newDescendantAccessRepo()
		r.members = map[string]*model.AccountMember{}
		r.share = readShare
		meeting, permission, err := resolveSharedAccess(context.Background(), r, "reader", "meeting")
		if err != nil || meeting == nil || permission != model.PermissionRead {
			t.Fatalf("got %v/%q/%v; want read", meeting != nil, permission, err)
		}
	})
	t.Run("direct read share on an unpublished meeting stays read", func(t *testing.T) {
		r := newDescendantAccessRepo()
		r.member("child", "reader", model.RoleSA)
		r.meeting.SharedToAccount = false
		r.share = readShare
		_, permission, err := resolveSharedAccess(context.Background(), r, "reader", "meeting")
		if err != nil || permission != model.PermissionRead {
			t.Fatalf("got %q/%v; want read", permission, err)
		}
	})
	t.Run("removed member loses access", func(t *testing.T) {
		r := newDescendantAccessRepo()
		r.members = map[string]*model.AccountMember{}
		r.share = &model.Share{Origin: model.ShareOriginAccount, Permission: model.PermissionRead}
		meeting, _, err := resolveSharedAccess(context.Background(), r, "reader", "meeting")
		if err != nil || meeting != nil {
			t.Fatalf("removed member retained access: %v/%v", meeting != nil, err)
		}
	})
	t.Run("inherited ancestor member yields edit", func(t *testing.T) {
		r := newDescendantAccessRepo()
		meeting, permission, err := resolveSharedAccess(context.Background(), r, "reader", "meeting")
		if err != nil || meeting == nil || permission != model.PermissionEdit {
			t.Fatalf("got %v/%q/%v; want edit", meeting != nil, permission, err)
		}
	})
}

func accountPermissionRepo() *mockMeetingRepo {
	repo := newMockMeetingRepo()
	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Title: "Meeting", AccountID: "acc", SharedToAccount: true,
		Status: model.StatusDone, Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	return repo
}

func TestUpdateMeetingAccountMemberEditsLikeCreator(t *testing.T) {
	repo := accountPermissionRepo()
	repo.addMember("acc", "member-1", model.RoleSA)
	svc := newMeetingServiceWithRepo(repo)
	if _, err := svc.UpdateMeeting(context.Background(), "member-1", "m-1", &model.UpdateMeetingRequest{Title: "Edited"}); err != nil {
		t.Fatalf("account member edit: %v", err)
	}
	if repo.meetingsByID["m-1"].Title != "Edited" {
		t.Fatalf("title not saved on owner's meeting: %q", repo.meetingsByID["m-1"].Title)
	}
}

func TestUpdateMeetingInheritedMemberEditsLikeCreator(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	r := &inheritedSpeakerRepo{mockMeetingRepo: newMockMeetingRepo(), hierarchy: newDescendantAccessRepo()}
	meeting := *r.hierarchy.meeting
	r.addMeeting(&meeting)
	if _, err := newMeetingServiceWithRepo(r).UpdateMeeting(context.Background(), "reader", "meeting",
		&model.UpdateMeetingRequest{Title: "Edited"}); err != nil {
		t.Fatalf("inherited member edit: %v", err)
	}
	if r.meetingsByID["meeting"].Title != "Edited" {
		t.Fatalf("title not saved: %q", r.meetingsByID["meeting"].Title)
	}
}

func TestUpdateMeetingDirectReadShareNonMemberForbidden(t *testing.T) {
	repo := accountPermissionRepo()
	repo.shares[shareKey("reader-1", "m-1")] = &model.Share{
		MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "reader-1", Permission: model.PermissionRead,
	}
	_, err := newMeetingServiceWithRepo(repo).UpdateMeeting(context.Background(), "reader-1", "m-1", &model.UpdateMeetingRequest{Title: "Hacked"})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestDeleteMeetingStillRejectsAccountMembers(t *testing.T) {
	repo := accountPermissionRepo()
	repo.addMember("acc", "member-1", model.RoleSA)
	err := newMeetingServiceWithRepo(repo).DeleteMeeting(context.Background(), "member-1", "m-1")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("account member delete: %v", err)
	}
	if repo.meetingsByID["m-1"] == nil {
		t.Fatal("meeting deleted by a non-owner")
	}
}

func TestUpdateMeetingRejectsDerivedWritesFromRestrictedProjection(t *testing.T) {
	// Members no longer receive restricted projections (ADR-049 amendment);
	// simulate one so the backstop stays covered if a guard returns.
	repo := accountPermissionRepo()
	repo.meetingsByID["m-1"].Content = "PRIVATE_SUMMARY"
	repo.meetingsByID["m-1"].SummaryReadRestricted = true
	repo.addMember("acc", "member-1", model.RoleSA)
	svc := newMeetingServiceWithRepo(repo)
	_, err := svc.UpdateMeeting(context.Background(), "member-1", "m-1", &model.UpdateMeetingRequest{Content: "overwrite"})
	if !errors.Is(err, ErrSummaryReadRestricted) {
		t.Fatalf("restricted projection overwrote derived content: %v", err)
	}
	if repo.meetingsByID["m-1"].Content != "PRIVATE_SUMMARY" {
		t.Fatal("canonical summary changed")
	}
	if _, err := svc.UpdateMeeting(context.Background(), "member-1", "m-1", &model.UpdateMeetingRequest{Title: "Edited"}); err != nil {
		t.Fatalf("non-derived edit blocked: %v", err)
	}
}
