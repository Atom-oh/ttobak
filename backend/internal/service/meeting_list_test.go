package service

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func TestListMeetings_AccountFilterPreservesAccess(t *testing.T) {
	tests := []struct {
		name      string
		tab       string
		accountID string
		want      []string
	}{
		{"all accounts", "all", "", []string{"own-a", "own-b", "own-unlinked", "shared-a", "shared-b"}},
		{"account a", "all", "acc-a", []string{"own-a", "shared-a"}},
		{"account b", "all", "acc-b", []string{"own-b", "shared-b"}},
		{"shared account a", "shared", "acc-a", []string{"shared-a"}},
		{"unknown account", "all", "unknown", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newMockMeetingRepo()
			for _, meeting := range []model.Meeting{
				{MeetingID: "own-a", UserID: "viewer", AccountID: "acc-a"},
				{MeetingID: "own-b", UserID: "viewer", AccountID: "acc-b"},
				{MeetingID: "own-unlinked", UserID: "viewer"},
				{MeetingID: "shared-a", UserID: "owner", AccountID: "acc-a"},
				{MeetingID: "shared-b", UserID: "owner", AccountID: "acc-b"},
				{MeetingID: "private-a", UserID: "owner", AccountID: "acc-a"},
				{MeetingID: "revoked-a", UserID: "owner", AccountID: "acc-a", SharedToAccount: true},
			} {
				repo.addMeeting(&meeting)
			}
			for _, id := range []string{"shared-a", "shared-b", "revoked-a"} {
				repo.shares[shareKey("viewer", id)] = &model.Share{
					MeetingID: id, OwnerID: "owner", SharedToID: "viewer", Permission: model.PermissionRead,
				}
			}
			repo.shares[shareKey("viewer", "revoked-a")].Origin = model.ShareOriginAccount
			svc := newMeetingServiceWithRepo(repo)

			got, err := svc.ListMeetings(context.Background(), "viewer", tt.tab, "", tt.accountID, 20)
			if err != nil {
				t.Fatal(err)
			}
			ids := make([]string, 0, len(got.Meetings))
			for _, meeting := range got.Meetings {
				ids = append(ids, meeting.MeetingID)
			}
			slices.Sort(ids)
			if !slices.Equal(ids, tt.want) {
				t.Fatalf("meetings = %v, want %v", ids, tt.want)
			}
		})
	}
}

func TestListMeetings_AccountFilterAdvancesEmptySharedPages(t *testing.T) {
	repo := newMockMeetingRepo()
	repo.addMeeting(&model.Meeting{MeetingID: "other", UserID: "owner", AccountID: "acc-b"})
	repo.addMeeting(&model.Meeting{MeetingID: "match", UserID: "owner", AccountID: "acc-a"})
	var calls []repository.ListMeetingsParams
	repo.listMeetingsFn = func(params repository.ListMeetingsParams) (*repository.ListMeetingsResult, error) {
		calls = append(calls, params)
		if params.Cursor == "" {
			return &repository.ListMeetingsResult{
				Shares:     []model.Share{{MeetingID: "other", OwnerID: "owner"}},
				NextCursor: aws.String("second-page"),
			}, nil
		}
		return &repository.ListMeetingsResult{
			Shares:     []model.Share{{MeetingID: "match", OwnerID: "owner"}},
			NextCursor: aws.String("third-page"),
		}, nil
	}
	svc := newMeetingServiceWithRepo(repo)
	got, err := svc.ListMeetings(context.Background(), "viewer", "shared", "", "acc-a", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Meetings) != 1 || got.Meetings[0].MeetingID != "match" {
		t.Fatalf("expected the matching meeting from page two, got %+v", got.Meetings)
	}
	if len(calls) != 2 || calls[1].Cursor != "second-page" {
		t.Fatalf("unexpected pagination: %+v", calls)
	}
	for _, call := range calls {
		if call.AccountID != "acc-a" || call.UserID != "viewer" || call.Tab != "shared" {
			t.Fatalf("filter or caller lost during pagination: %+v", call)
		}
	}
	if aws.ToString(got.NextCursor) != "third-page" {
		t.Fatalf("resume cursor = %v, want third-page", got.NextCursor)
	}
}

func TestListMeetings_AccountFilterDoesNotHideSharedReadFailure(t *testing.T) {
	repo := newMockMeetingRepo()
	readErr := errors.New("shared meeting read failed")
	repo.batchMeetingsErr = readErr
	repo.listMeetingsFn = func(repository.ListMeetingsParams) (*repository.ListMeetingsResult, error) {
		return &repository.ListMeetingsResult{
			Shares: []model.Share{{MeetingID: "shared-a", OwnerID: "owner"}},
		}, nil
	}
	svc := newMeetingServiceWithRepo(repo)
	_, err := svc.ListMeetings(context.Background(), "viewer", "shared", "", "acc-a", 20)
	if !errors.Is(err, readErr) {
		t.Fatalf("expected the read failure, got %v", err)
	}
}

func TestListMeetings_AccountFilterBoundsEmptySharedPages(t *testing.T) {
	repo := newMockMeetingRepo()
	calls := 0
	repo.listMeetingsFn = func(params repository.ListMeetingsParams) (*repository.ListMeetingsResult, error) {
		calls++
		if calls > 25 {
			t.Fatal("empty shared pages must not cause unbounded reads")
		}
		return &repository.ListMeetingsResult{NextCursor: aws.String(strconv.Itoa(calls))}, nil
	}
	svc := newMeetingServiceWithRepo(repo)
	got, err := svc.ListMeetings(context.Background(), "viewer", "shared", "", "acc-a", 20)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 25 || aws.ToString(got.NextCursor) != "25" {
		t.Fatalf("work bound must leave a resumable cursor: calls=%d, cursor=%v", calls, got.NextCursor)
	}
}
