package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func TestValidatePeopleResolveRequested(t *testing.T) {
	for _, tc := range []struct {
		name string
		e    model.PeopleResolveRequested
		ok   bool
	}{
		{"meeting", model.PeopleResolveRequested{AccountID: "a1", OwnerID: "o1", MeetingID: "m1", Reason: "speakers"}, true},
		{"account", model.PeopleResolveRequested{AccountID: "a1", Reason: "contacts"}, true},
		{"owner without meeting", model.PeopleResolveRequested{AccountID: "a1", OwnerID: "o1", Reason: "speakers"}, false},
		{"meeting without owner", model.PeopleResolveRequested{AccountID: "a1", MeetingID: "m1", Reason: "speakers"}, false},
		{"unknown reason", model.PeopleResolveRequested{AccountID: "a1", Reason: "summary"}, false},
		{"hash in account", model.PeopleResolveRequested{AccountID: "a#1", Reason: "contacts"}, false},
		{"hash in meeting", model.PeopleResolveRequested{AccountID: "a1", OwnerID: "o1", MeetingID: "m#1", Reason: "speakers"}, false},
		{"empty account", model.PeopleResolveRequested{Reason: "contacts"}, false},
	} {
		if err := ValidatePeopleResolveRequested(tc.e); (err == nil) != tc.ok {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
}

type peopleEventClient struct{ in *eventbridge.PutEventsInput }

func (c *peopleEventClient) PutEvents(_ context.Context, in *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	c.in = in
	return &eventbridge.PutEventsOutput{Entries: []ebtypes.PutEventsResultEntry{{EventId: aws.String("e1")}}}, nil
}

func TestPeopleResolvePublisher_Envelope(t *testing.T) {
	client := &peopleEventClient{}
	event := model.PeopleResolveRequested{AccountID: "a1", OwnerID: "o1", MeetingID: "m1", Reason: "speakers"}
	if err := PeopleResolvePublisher(client)(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	entry := client.in.Entries[0]
	if aws.ToString(entry.Source) != "ttobak.analysis" || aws.ToString(entry.DetailType) != "PeopleResolveRequested" || aws.ToString(entry.EventBusName) != "default" {
		t.Fatalf("entry %+v", entry)
	}
	var got model.PeopleResolveRequested
	if err := json.Unmarshal([]byte(aws.ToString(entry.Detail)), &got); err != nil || got != event {
		t.Fatalf("detail %s %v", aws.ToString(entry.Detail), err)
	}
	client.in = nil
	if err := PeopleResolvePublisher(client)(context.Background(), model.PeopleResolveRequested{AccountID: "a1", Reason: "x"}); err == nil || client.in != nil {
		t.Fatal("invalid event published")
	}
}

func TestProcessPeopleResolveRequested_MeetingOutsideAccountSkipped(t *testing.T) {
	f := newFakePeopleRepo()
	res, err := newPeopleTestService(f).ProcessPeopleResolveRequested(context.Background(),
		model.PeopleResolveRequested{AccountID: "other", OwnerID: "owner", MeetingID: "m1", Reason: "speakers"})
	if err != nil || res.Skipped != 1 || f.claims != 0 {
		t.Fatalf("res=%+v err=%v claims=%d", res, err, f.claims)
	}
	res, err = newPeopleTestService(f).ProcessPeopleResolveRequested(context.Background(),
		model.PeopleResolveRequested{AccountID: "acc", OwnerID: "owner", MeetingID: "m1", Reason: "speakers"})
	if err != nil || res.Meetings != 1 || f.claims != 1 {
		t.Fatalf("in account: res=%+v err=%v", res, err)
	}
	f.claimBusy = true
	if res, err := newPeopleTestService(f).ProcessPeopleResolveRequested(context.Background(),
		model.PeopleResolveRequested{AccountID: "acc", Reason: "contacts"}); err != nil || !res.Busy {
		t.Fatalf("busy account: %+v %v", res, err)
	}
}

func TestPeopleRunErrorCode(t *testing.T) {
	for err, want := range map[error]string{
		nil: "", context.DeadlineExceeded: "timeout", ErrInvalidInput: "invalid",
		fmt.Errorf("x: %w", repository.ErrContactEncryptionUnavailable): "contacts_unavailable",
		errors.New("secret text"): "unknown",
	} {
		if got := PeopleRunErrorCode(err); got != want {
			t.Fatalf("%v: %q", err, got)
		}
	}
}

func TestProcessPeopleResolveRequested_PendingRerunIsRetried(t *testing.T) {
	f := newFakePeopleRepo()
	f.reruns = []bool{true, true}
	res, err := newPeopleTestService(f).ProcessPeopleResolveRequested(context.Background(),
		model.PeopleResolveRequested{AccountID: "acc", OwnerID: "owner", MeetingID: "m1", Reason: "speakers"})
	if !errors.Is(err, ErrPeopleRerunPending) || !res.RerunPending || PeopleRunErrorCode(err) != "rerun_pending" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	f = newFakePeopleRepo()
	f.reruns = []bool{true, false}
	if _, err := newPeopleTestService(f).ProcessPeopleResolveRequested(context.Background(),
		model.PeopleResolveRequested{AccountID: "acc", OwnerID: "owner", MeetingID: "m1", Reason: "speakers"}); err != nil {
		t.Fatalf("served rerun: %v", err)
	}
}

func TestResolveMeetingForAccount_SkipsMeetingOfAnotherAccount(t *testing.T) {
	f := newFakePeopleRepo()
	f.meetings["owner:m1"].Participants = []string{"Lee"}
	res, err := newPeopleTestService(f).ResolveMeetingForAccount(context.Background(), "old-account", "owner", "m1", PeopleRunOptions{})
	if err != nil || res.Skipped != 1 || f.claims != 0 || len(f.replaced) != 0 || len(f.puts) != 0 {
		t.Fatalf("res=%+v err=%v claims=%d", res, err, f.claims)
	}
	if res, err := newPeopleTestService(f).ResolveMeetingForAccount(context.Background(), "acc", "owner", "m1", PeopleRunOptions{}); err != nil || res.Meetings != 1 {
		t.Fatalf("own account: %+v %v", res, err)
	}
}
