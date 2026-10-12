package service

import (
	"context"
	"errors"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestAuthorizeEditAllowsOnlyOwnerAndEditShares(t *testing.T) {
	repo := newMockMeetingRepo()
	mtg := &model.Meeting{UserID: "owner", MeetingID: "m1", Title: "t"}
	repo.meetings[meetingKey("owner", "m1")] = mtg
	repo.meetingsByID["m1"] = mtg
	repo.shares["editor|m1"] = &model.Share{MeetingID: "m1", SharedToID: "editor", Permission: model.PermissionEdit}
	repo.shares["reader|m1"] = &model.Share{MeetingID: "m1", SharedToID: "reader", Permission: model.PermissionRead}
	svc := newMeetingServiceWithRepo(repo)

	tests := []struct {
		user string
		want error
	}{
		{"owner", nil},
		{"editor", nil},
		{"reader", ErrForbidden},
		{"stranger", ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			if err := svc.AuthorizeEdit(context.Background(), tt.user, "m1"); !errors.Is(err, tt.want) {
				t.Fatalf("AuthorizeEdit(%s) = %v, want %v", tt.user, err, tt.want)
			}
		})
	}
}
