package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

func simRunKey(meetingID string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
		"SK": &types.AttributeValueMemberS{Value: model.PrefixSimRun},
	}
}

// GetSimRun returns the meeting's singleton simulation run, or nil if none
// has ever been started.
func (r *DynamoDBRepository) GetSimRun(ctx context.Context, meetingID string) (*model.SimRun, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(r.tableName),
		ConsistentRead: aws.Bool(true),
		Key:            simRunKey(meetingID),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get sim run: %w", err)
	}
	if result.Item == nil {
		return nil, nil
	}
	var run model.SimRun
	if err := attributevalue.UnmarshalMap(result.Item, &run); err != nil {
		return nil, fmt.Errorf("failed to unmarshal sim run: %w", err)
	}
	return &run, nil
}

// PutSimRunIfNotRunning is the concurrency gate for both starting a run
// (POST /sim) and re-extracting a draft (POST /sim/extract): it succeeds
// only if no run exists yet, or the existing run is in a terminal-or-draft
// state (extracted/done/error) -- never while another run for the same
// meeting is queued/running. Two concurrent "실행" clicks off the same
// confirmed form both pass client-side validation, but only one wins this
// write; the loser gets ErrConditionFailed and should report "이미 실행
// 중입니다" instead of starting a second Code Interpreter session and Sonnet
// codegen for the same meeting -- and the same guard keeps a re-extraction
// from silently resetting a live run's row out from under it.
func (r *DynamoDBRepository) PutSimRunIfNotRunning(ctx context.Context, run *model.SimRun) error {
	run.PK = model.PrefixMeeting + run.MeetingID
	run.SK = model.PrefixSimRun
	run.EntityType = "SIM_RUN"
	run.UpdatedAt = time.Now().UTC()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = run.UpdatedAt
	}
	item, err := attributevalue.MarshalMap(run)
	if err != nil {
		return fmt.Errorf("failed to marshal sim run: %w", err)
	}

	condition := expression.Or(
		expression.AttributeNotExists(expression.Name("SK")),
		expression.Name("status").Equal(expression.Value(model.SimStatusExtracted)),
		expression.Name("status").Equal(expression.Value(model.SimStatusDone)),
		expression.Name("status").Equal(expression.Value(model.SimStatusError)),
	)
	expr, err := expression.NewBuilder().WithCondition(condition).Build()
	if err != nil {
		return fmt.Errorf("failed to build condition: %w", err)
	}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:                 aws.String(r.tableName),
		Item:                      item,
		ConditionExpression:       expr.Condition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return fmt.Errorf("%w: meeting %s already has a simulation running", ErrConditionFailed, run.MeetingID)
		}
		return fmt.Errorf("failed to put sim run: %w", err)
	}
	return nil
}

// UpdateSimRunFieldsIfMatch patches the sim run item, conditioned on the
// row's simRunId still matching the caller's -- without this, a worker for
// an OLD run (crashed/zombied, then superseded by a fresh claim on the same
// meeting) could write its late pricing/generating/running/done/error update
// straight onto the NEW run's row, corrupting it with stale data the new
// run never produced. The Python ttobak-sim worker (backend/python/sim/
// handler.py) implements the same simRunId-conditioned update independently
// (cross-language, no shared code) -- keep both in sync.
func (r *DynamoDBRepository) UpdateSimRunFieldsIfMatch(ctx context.Context, meetingID, simRunID string, fields map[string]interface{}) error {
	fields["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)

	var update expression.UpdateBuilder
	first := true
	for k, v := range fields {
		if first {
			update = expression.Set(expression.Name(k), expression.Value(v))
			first = false
		} else {
			update = update.Set(expression.Name(k), expression.Value(v))
		}
	}

	expr, err := expression.NewBuilder().
		WithUpdate(update).
		WithCondition(expression.Name("simRunId").Equal(expression.Value(simRunID))).
		Build()
	if err != nil {
		return fmt.Errorf("failed to build update expression: %w", err)
	}

	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(r.tableName),
		Key:                       simRunKey(meetingID),
		UpdateExpression:          expr.Update(),
		ConditionExpression:       expr.Condition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return fmt.Errorf("%w: sim run %s for meeting %s not found or superseded", ErrConditionFailed, simRunID, meetingID)
		}
		return fmt.Errorf("failed to update sim run fields: %w", err)
	}
	return nil
}

// DeleteSimRun removes the meeting's sim run item, if any. Called from
// DeleteMeeting's transaction -- see that method's item-count comment.
func (r *DynamoDBRepository) DeleteSimRun(ctx context.Context, meetingID string) error {
	_, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key:       simRunKey(meetingID),
	})
	if err != nil {
		return fmt.Errorf("failed to delete sim run: %w", err)
	}
	return nil
}
