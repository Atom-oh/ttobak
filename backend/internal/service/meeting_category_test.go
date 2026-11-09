package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// SetMeetingCategory mirrors the repository's conditional write: the meeting
// must exist without an account link.
func (m *mockMeetingRepo) SetMeetingCategory(_ context.Context, userID, meetingID, category string) error {
	existing, ok := m.meetings[meetingKey(userID, meetingID)]
	if !ok || existing.AccountID != "" {
		return repository.ErrConditionFailed
	}
	cp := *existing
	cp.MeetingCategory = category
	m.meetings[meetingKey(userID, meetingID)] = &cp
	return nil
}

func TestValidMeetingCategory(t *testing.T) {
	for category, want := range map[string]bool{
		"": true, model.MeetingCategoryTeam: true, model.MeetingCategoryOneOnOne: true,
		model.MeetingCategoryTraining: true, model.MeetingCategoryOther: true,
		"internal": false, "customer": false, "INTERNAL_TEAM": false, "internal_1on1 ": false,
	} {
		if got := model.ValidMeetingCategory(category); got != want {
			t.Errorf("ValidMeetingCategory(%q) = %v, want %v", category, got, want)
		}
	}
}

func TestSetMeetingCategory(t *testing.T) {
	for _, tc := range []struct {
		name, actor, category string
		meeting               *model.Meeting
		share                 bool
		want                  error
		stored                string
	}{
		{name: "owner sets 1:1", actor: "owner", category: model.MeetingCategoryOneOnOne,
			meeting: &model.Meeting{MeetingID: "m", UserID: "owner"}, stored: model.MeetingCategoryOneOnOne},
		{name: "owner clears", actor: "owner", category: "",
			meeting: &model.Meeting{MeetingID: "m", UserID: "owner", MeetingCategory: model.MeetingCategoryTeam}, stored: ""},
		{name: "account-linked meeting rejected", actor: "owner", category: model.MeetingCategoryTeam,
			meeting: &model.Meeting{MeetingID: "m", UserID: "owner", AccountID: "acc"}, want: repository.ErrConditionFailed},
		{name: "invalid value", actor: "owner", category: "internal",
			meeting: &model.Meeting{MeetingID: "m", UserID: "owner"}, want: ErrInvalidInput},
		{name: "shared recipient forbidden", actor: "reader", category: model.MeetingCategoryTeam, share: true,
			meeting: &model.Meeting{MeetingID: "m", UserID: "owner"}, want: ErrForbidden},
		{name: "stranger sees not found", actor: "stranger", category: model.MeetingCategoryTeam,
			meeting: &model.Meeting{MeetingID: "m", UserID: "owner"}, want: ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newMockMeetingRepo()
			repo.addMeeting(tc.meeting)
			if tc.share {
				repo.shares[shareKey(tc.actor, "m")] = &model.Share{MeetingID: "m", OwnerID: "owner", SharedToID: tc.actor, Permission: model.PermissionEdit}
			}
			svc := newMeetingServiceWithRepo(repo)
			err := svc.SetMeetingCategory(context.Background(), tc.actor, "m", tc.category)
			if tc.want != nil {
				if !errors.Is(err, tc.want) {
					t.Fatalf("err = %v, want %v", err, tc.want)
				}
				if got := repo.meetings[meetingKey("owner", "m")].MeetingCategory; got != tc.meeting.MeetingCategory {
					t.Fatalf("rejected write changed category to %q", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := repo.meetings[meetingKey("owner", "m")].MeetingCategory; got != tc.stored {
				t.Fatalf("category = %q, want %q", got, tc.stored)
			}
		})
	}
}

func TestCreateMeetingCategoryValidation(t *testing.T) {
	repo := newMockMeetingRepo()
	repo.addMember("acc", "owner", model.RoleOwner)
	svc := newMeetingServiceWithRepo(repo)
	ctx, now := context.Background(), time.Now().UTC()
	if _, err := svc.CreateMeeting(ctx, "owner", "T", now, nil, "", model.MeetingPreparation{AccountID: "acc", Category: model.MeetingCategoryTeam}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("account plus internal category: %v", err)
	}
	if _, err := svc.CreateMeeting(ctx, "owner", "T", now, nil, "", model.MeetingPreparation{Category: "bogus"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid category: %v", err)
	}
	if _, err := svc.CreateMeeting(ctx, "owner", "T", now, nil, "", model.MeetingPreparation{Category: model.MeetingCategoryOneOnOne}); err != nil {
		t.Fatalf("valid internal create: %v", err)
	}
}
