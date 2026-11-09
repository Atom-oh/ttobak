package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func speakersPublishCase(t *testing.T, published bool, publishErr error) ([]model.PeopleResolveRequested, error) {
	t.Helper()
	repo := newMockMeetingRepo()
	repo.addMeeting(&model.Meeting{MeetingID: "meeting", UserID: "owner", Status: model.StatusDone,
		TranscriptA: "[spk_0]\nhello", AccountID: "account", SharedToAccount: published, UpdatedAt: time.Now().UTC()})
	svc := newMeetingServiceWithRepo(repo)
	var got []model.PeopleResolveRequested
	svc.SetPeopleResolvePublisher(func(_ context.Context, e model.PeopleResolveRequested) error {
		got = append(got, e)
		return publishErr
	})
	_, err := svc.UpdateSpeakers(context.Background(), "owner", "meeting",
		&model.UpdateSpeakersRequest{SpeakerMap: map[string]string{"spk_0": "김팀장"}})
	if err == nil && repo.meetingsByID["meeting"].SpeakerMap["spk_0"] != "김팀장" {
		t.Fatal("speaker map was not saved")
	}
	return got, err
}

func TestUpdateSpeakers_PublishesForPublishedMeeting(t *testing.T) {
	got, err := speakersPublishCase(t, true, nil)
	want := model.PeopleResolveRequested{AccountID: "account", OwnerID: "owner", MeetingID: "meeting", Reason: model.PeopleResolveReasonSpeakers}
	if err != nil || len(got) != 1 || got[0] != want {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestUpdateSpeakers_NoPublishForPrivateMeeting(t *testing.T) {
	if got, err := speakersPublishCase(t, false, nil); err != nil || len(got) != 0 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestUpdateSpeakers_PublishFailureDoesNotFail(t *testing.T) {
	if got, err := speakersPublishCase(t, true, errors.New("eventbridge down")); err != nil || len(got) != 1 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}
