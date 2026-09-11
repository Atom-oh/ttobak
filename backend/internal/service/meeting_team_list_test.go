package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func publishedTeamMeeting(t *testing.T) (*mockMeetingRepo, *MeetingService) {
	t.Helper()
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	repo.addMember("acc-a", "owner", model.RoleOwner)
	repo.addMeeting(&model.Meeting{
		MeetingID: "published", UserID: "owner", Title: "Earlier team meeting",
		Status: model.StatusDone, Date: time.Now(),
	})
	if _, err := svc.ShareMeetingToAccount(context.Background(), "owner", "owner@example.com", "published", "acc-a"); err != nil {
		t.Fatal(err)
	}
	return repo, svc
}

func TestListMeetings_LaterTeamMemberAutomaticallySeesPublishedMeetings(t *testing.T) {
	for _, tab := range []string{"all", "shared"} {
		for _, accountID := range []string{"", "acc-a"} {
			t.Run(tab+"/"+accountID, func(t *testing.T) {
				repo, svc := publishedTeamMeeting(t)
				repo.addMember("acc-a", "new-member", model.RoleSA)
				if share, _ := repo.GetShare(context.Background(), "new-member", "published"); share != nil {
					t.Fatal("fixture must represent a member added after share fan-out")
				}
				got, err := svc.ListMeetings(context.Background(), "new-member", tab, "", accountID, 20)
				if err != nil {
					t.Fatal(err)
				}
				if len(got.Meetings) != 1 || got.Meetings[0].MeetingID != "published" ||
					!got.Meetings[0].IsShared || aws.ToString(got.Meetings[0].Permission) != model.PermissionRead {
					t.Fatalf("expected the existing team meeting with read access, got %+v", got.Meetings)
				}
				if _, permission, err := svc.checkAccess(context.Background(), "new-member", "published"); err != nil || permission != model.PermissionRead {
					t.Fatalf("list and detail access disagree: permission=%q error=%v", permission, err)
				}
			})
		}
	}
}

func TestListMeetings_TeamInheritanceRejectsStaleAndPrivateReferences(t *testing.T) {
	for _, state := range []string{"link-only", "moved", "deleted", "membership-removed", "other-filter"} {
		t.Run(state, func(t *testing.T) {
			repo, svc := publishedTeamMeeting(t)
			repo.addMember("acc-a", "new-member", model.RoleSA)
			accountID := ""
			switch state {
			case "link-only":
				repo.meetings[meetingKey("owner", "published")].SharedToAccount = false
			case "moved":
				repo.meetings[meetingKey("owner", "published")].AccountID = "acc-b"
			case "deleted":
				delete(repo.meetings, meetingKey("owner", "published"))
				delete(repo.meetingsByID, "published")
			case "membership-removed":
				repo.staleMemberships = []model.AccountMember{*repo.members["acc-a|new-member"]}
				delete(repo.members, "acc-a|new-member")
			case "other-filter":
				accountID = "acc-b"
			}
			got, err := svc.ListMeetings(context.Background(), "new-member", "shared", "", accountID, 20)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Meetings) != 0 {
				t.Fatalf("unexpected inherited meeting: %+v", got.Meetings)
			}
		})
	}
}

func TestListMeetings_TeamInheritancePreservesDirectShareAndPagination(t *testing.T) {
	repo, svc := publishedTeamMeeting(t)
	repo.addMember("acc-a", "new-member", model.RoleSA)
	repo.meetingRefs["acc-a"] = append(repo.meetingRefs["acc-a"], repo.meetingRefs["acc-a"][0])
	repo.shares[shareKey("new-member", "published")] = &model.Share{
		MeetingID: "published", OwnerID: "owner", OwnerEmail: "owner@example.com",
		SharedToID: "new-member", Permission: model.PermissionEdit,
	}
	// Keep direct grants in the regular cursor stream with their edit
	// permission; the later inherited stream must not duplicate them.
	repo.listMeetingsFn = func(params repository.ListMeetingsParams) (*repository.ListMeetingsResult, error) {
		if params.Cursor == "" {
			return &repository.ListMeetingsResult{
				Shares:     []model.Share{*repo.shares[shareKey("new-member", "published")]},
				NextCursor: aws.String("next-page"),
			}, nil
		}
		return &repository.ListMeetingsResult{}, nil
	}
	got, err := svc.ListMeetings(context.Background(), "new-member", "shared", "", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Meetings) != 1 || aws.ToString(got.Meetings[0].Permission) != model.PermissionEdit ||
		aws.ToString(got.Meetings[0].SharedBy) != "owner@example.com" {
		t.Fatalf("duplicate refs or direct permission lost: %+v", got.Meetings)
	}
	if aws.ToString(got.NextCursor) != "next-page" {
		t.Fatal("inherited meetings must not consume the direct-share cursor")
	}
	next, err := svc.ListMeetings(context.Background(), "new-member", "shared", "next-page", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Meetings) != 0 {
		t.Fatal("inherited stream must not duplicate an individual share")
	}
}

func TestListMeetings_TeamInheritanceDoesNotDuplicateOwnedOrSharedMeetings(t *testing.T) {
	repo, svc := publishedTeamMeeting(t)
	repo.addMember("acc-a", "member", model.RoleSA)
	repo.shares[shareKey("member", "published")] = &model.Share{
		MeetingID: "published", OwnerID: "owner", SharedToID: "member", Permission: model.PermissionEdit,
	}
	for _, userID := range []string{"owner", "member"} {
		got, err := svc.ListMeetings(context.Background(), userID, "all", "", "", 20)
		if err != nil || len(got.Meetings) != 1 {
			t.Fatalf("user=%s list=%+v error=%v", userID, got, err)
		}
		if userID == "owner" && got.Meetings[0].IsShared {
			t.Fatal("an owner's meeting must not become a shared item")
		}
	}
}

func TestListMeetings_NewInviteeInheritsAfterVerifiedAccountMaterialization(t *testing.T) {
	for _, verified := range []bool{false, true} {
		t.Run(map[bool]string{false: "unverified", true: "verified"}[verified], func(t *testing.T) {
			repo, svc := publishedTeamMeeting(t)
			repo.pendingShares = append(repo.pendingShares, &model.PendingShare{
				Email: "new@example.com", Kind: model.PendingShareKindAccount,
				AccountID: "acc-a", Role: model.RoleSA, InvitedByUserID: "owner",
				InvitedCognitoSub: "new-member", TTL: time.Now().Add(time.Hour).Unix(),
				SK: model.PrefixPendingAccount + "acc-a",
			})
			joined := svc.EnsureProfileAndMaterializePendingShares(context.Background(), "new-member", "new@example.com", "New Member", verified)
			repo.membershipIndexLag = true
			got, err := svc.ListMeetings(context.Background(), "new-member", "all", "", "", 20, joined...)
			if err != nil {
				t.Fatal(err)
			}
			if (len(got.Meetings) == 1) != verified {
				t.Fatalf("verified=%v meetings=%+v", verified, got.Meetings)
			}
		})
	}
}

func TestListMeetings_TeamHistoryPaginatesWithoutLosingMeetings(t *testing.T) {
	repo, svc := publishedTeamMeeting(t)
	repo.addMember("acc-a", "new-member", model.RoleSA)
	for i := 0; i < 44; i++ {
		id := fmt.Sprintf("history-%02d", i)
		repo.addMeeting(&model.Meeting{MeetingID: id, UserID: "owner", AccountID: "acc-a", SharedToAccount: true})
		repo.meetingRefs["acc-a"] = append(repo.meetingRefs["acc-a"], model.MeetingRef{MeetingID: id, OwnerUserID: "owner", AccountID: "acc-a"})
	}
	seen := make(map[string]bool)
	cursor := ""
	for page := 0; ; page++ {
		if page >= 4 {
			t.Fatal("pagination did not terminate")
		}
		got, err := svc.ListMeetings(context.Background(), "new-member", "shared", cursor, "", 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Meetings) > 20 {
			t.Fatalf("page exceeded limit: %d", len(got.Meetings))
		}
		for _, meeting := range got.Meetings {
			if seen[meeting.MeetingID] {
				t.Fatalf("duplicated %s across pages", meeting.MeetingID)
			}
			seen[meeting.MeetingID] = true
		}
		if got.NextCursor == nil {
			break
		}
		cursor = *got.NextCursor
	}
	if len(seen) != 45 || repo.teamRefPageCalls != 3 {
		t.Fatalf("history lost or over-read: meetings=%d page reads=%d", len(seen), repo.teamRefPageCalls)
	}
}

func TestListMeetings_TeamCursorNeverGrantsMembership(t *testing.T) {
	repo, svc := publishedTeamMeeting(t)
	forged, err := encodeTeamMeetingCursor(teamMeetingCursor{
		UserID: "outsider", Tab: "shared", Accounts: []string{"acc-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.ListMeetings(context.Background(), "outsider", "shared", *forged, "", 20)
	if err != nil || len(got.Meetings) != 0 {
		t.Fatalf("cursor granted access: result=%+v error=%v", got, err)
	}
	if repo.teamRefPageCalls != 0 {
		t.Fatal("must reject membership before reading references")
	}
	for _, c := range []struct{ cursor, user, tab, account string }{
		{"team:invalid", "outsider", "shared", ""},
		{*forged, "someone-else", "shared", ""},
		{*forged, "outsider", "all", ""},
		{*forged, "outsider", "shared", "acc-b"},
	} {
		if _, err := svc.ListMeetings(context.Background(), c.user, c.tab, c.cursor, c.account, 20); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("expected invalid cursor error, got %v", err)
		}
	}
}

func TestListMeetings_TeamHistoryContinuesAfterFullRegularPage(t *testing.T) {
	repo, svc := publishedTeamMeeting(t)
	repo.addMember("acc-a", "new-member", model.RoleSA)
	repo.addMeeting(&model.Meeting{MeetingID: "personal", UserID: "new-member"})
	first, err := svc.ListMeetings(context.Background(), "new-member", "all", "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Meetings) != 1 || first.Meetings[0].MeetingID != "personal" || first.NextCursor == nil {
		t.Fatalf("regular page or team continuation lost: %+v", first)
	}
	if repo.teamRefPageCalls != 0 {
		t.Fatal("must not read team history when this page is already full")
	}
	next, err := svc.ListMeetings(context.Background(), "new-member", "all", *first.NextCursor, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Meetings) != 1 || next.Meetings[0].MeetingID != "published" || next.NextCursor != nil {
		t.Fatalf("unexpected inherited continuation: %+v", next)
	}
}

func TestListMeetings_TeamReferenceWorkBoundKeepsCursor(t *testing.T) {
	repo, svc := publishedTeamMeeting(t)
	repo.addMember("acc-a", "new-member", model.RoleSA)
	published := repo.meetingRefs["acc-a"][0]
	repo.meetingRefs["acc-a"] = nil
	for i := 0; i < 25; i++ {
		repo.meetingRefs["acc-a"] = append(repo.meetingRefs["acc-a"], model.MeetingRef{
			MeetingID: fmt.Sprintf("deleted-%d", i), OwnerUserID: "owner",
		})
	}
	repo.meetingRefs["acc-a"] = append(repo.meetingRefs["acc-a"], published)
	first, err := svc.ListMeetings(context.Background(), "new-member", "shared", "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Meetings) != 0 || first.NextCursor == nil || repo.teamRefPageCalls != 25 {
		t.Fatalf("expected a bounded empty page with continuation: result=%+v reads=%d", first, repo.teamRefPageCalls)
	}
	next, err := svc.ListMeetings(context.Background(), "new-member", "shared", *first.NextCursor, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Meetings) != 1 || next.Meetings[0].MeetingID != "published" || next.NextCursor != nil {
		t.Fatalf("matching meeting after stale refs was lost: %+v", next)
	}
}

func TestListMeetings_NewInviteHintsSurviveRegularPagination(t *testing.T) {
	repo, svc := publishedTeamMeeting(t)
	repo.pendingShares = append(repo.pendingShares, &model.PendingShare{
		Email: "new@example.com", Kind: model.PendingShareKindAccount,
		AccountID: "acc-a", Role: model.RoleSA, InvitedByUserID: "owner",
		InvitedCognitoSub: "new-member", TTL: time.Now().Add(time.Hour).Unix(),
		SK: model.PrefixPendingAccount + "acc-a",
	})
	joined := svc.EnsureProfileAndMaterializePendingShares(context.Background(), "new-member", "new@example.com", "New Member", true)
	repo.membershipIndexLag = true
	repo.listMeetingsFn = func(params repository.ListMeetingsParams) (*repository.ListMeetingsResult, error) {
		switch params.Cursor {
		case "":
			return &repository.ListMeetingsResult{
				Meetings:   []model.Meeting{{MeetingID: "personal-1", UserID: "new-member"}},
				NextCursor: aws.String("page-2"),
			}, nil
		case "page-2":
			return &repository.ListMeetingsResult{
				Meetings:   []model.Meeting{{MeetingID: "personal-2", UserID: "new-member"}},
				NextCursor: aws.String("page-3"),
			}, nil
		case "page-3":
			return &repository.ListMeetingsResult{}, nil
		default:
			t.Fatalf("unexpected repository cursor: %q", params.Cursor)
			return nil, nil
		}
	}
	first, err := svc.ListMeetings(context.Background(), "new-member", "all", "", "", 1, joined...)
	if err != nil || first.NextCursor == nil {
		t.Fatalf("first page: %+v error=%v", first, err)
	}
	second, err := svc.ListMeetings(context.Background(), "new-member", "all", *first.NextCursor, "", 1)
	if err != nil || second.NextCursor == nil {
		t.Fatalf("second page: %+v error=%v", second, err)
	}
	third, err := svc.ListMeetings(context.Background(), "new-member", "all", *second.NextCursor, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Meetings) != 1 || third.Meetings[0].MeetingID != "published" || third.NextCursor != nil {
		t.Fatalf("joined membership was lost across regular pages: %+v", third)
	}
}

func TestListMeetings_TeamInheritanceSurfacesReadFailures(t *testing.T) {
	for _, stage := range []string{"memberships", "references", "meetings"} {
		t.Run(stage, func(t *testing.T) {
			repo, svc := publishedTeamMeeting(t)
			repo.addMember("acc-a", "new-member", model.RoleSA)
			want := errors.New("temporary read failure")
			switch stage {
			case "memberships":
				repo.listAccountMembershipsErr = want
			case "references":
				repo.listAccountRefsErr = want
			case "meetings":
				repo.batchMeetingsErr = want
			}
			_, err := svc.ListMeetings(context.Background(), "new-member", "all", "", "", 20)
			if !errors.Is(err, want) {
				t.Fatalf("read failure was hidden: %v", err)
			}
		})
	}
}
