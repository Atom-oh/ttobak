package service

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/ttobak/backend/internal/model"
)

type actionEventClient interface {
	PutEvents(context.Context, *eventbridge.PutEventsInput, ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error)
}

func ActionItemsPublisher(client actionEventClient) func(context.Context, model.ActionItemsRequested) error {
	return func(ctx context.Context, event model.ActionItemsRequested) error {
		detail, err := json.Marshal(event)
		if err != nil {
			return err
		}
		out, err := client.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: []ebtypes.PutEventsRequestEntry{{
			EventBusName: aws.String("default"), Source: aws.String("ttobak.analysis"),
			DetailType: aws.String("ActionItemsRequested"), Detail: aws.String(string(detail)),
		}}})
		if err != nil {
			return err
		}
		// HTTP 200 alone is insufficient; per-entry failures are in the body.
		if out == nil || out.FailedEntryCount != 0 || len(out.Entries) != 1 ||
			out.Entries[0].ErrorCode != nil || aws.ToString(out.Entries[0].EventId) == "" {
			return errors.New("action analysis event was not accepted")
		}
		return nil
	}
}
