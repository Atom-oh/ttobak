package service

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/ttobak/backend/internal/model"
)

type actionEventStub struct {
	out *eventbridge.PutEventsOutput
	in  *eventbridge.PutEventsInput
}

func (s *actionEventStub) PutEvents(_ context.Context, in *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	s.in = in
	return s.out, nil
}

func TestActionAnalysisPublisherChecksEntryAndSendsOnlyIdentifiers(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		client := &actionEventStub{out: &eventbridge.PutEventsOutput{FailedEntryCount: 1}}
		if accepted {
			client.out = &eventbridge.PutEventsOutput{Entries: []ebtypes.PutEventsResultEntry{{EventId: aws.String("event")}}}
		}
		err := ActionItemsPublisher(client)(context.Background(), model.ActionItemsRequested{OwnerID: "owner", MeetingID: "meeting", RunID: "run"})
		if (err == nil) != accepted {
			t.Fatalf("accepted=%v err=%v", accepted, err)
		}
		entry := client.in.Entries[0]
		if aws.ToString(entry.Source) != "ttobak.analysis" || aws.ToString(entry.DetailType) != "ActionItemsRequested" ||
			aws.ToString(entry.Detail) != `{"ownerId":"owner","meetingId":"meeting","runId":"run"}` {
			t.Fatalf("wrong event contract: %+v", entry)
		}
	}
}
