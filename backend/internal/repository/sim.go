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

// PutSimRun unconditionally writes the sim run item -- used for the
// extraction step, which has no prior run to race against (a fresh
// extraction always overwrites any earlier draft/result for this meeting).
func (r *DynamoDBRepository) PutSimRun(ctx context.Context, run *model.SimRun) error {
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
	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return fmt.Errorf("failed to put sim run: %w", err)
	}
	return nil
}

// PutSimRunIfNotRunning is the concurrency gate for starting a run: it
// succeeds only if no run exists yet, or the existing run is in a terminal-
// or-draft state (extracted/done/error) -- never while another run for the
// same meeting is queued/running. Two concurrent "실행" clicks off the same
// confirmed form both pass client-side validation, but only one wins this
// write; the loser gets ErrConditionFailed and should report "이미 실행
// 중입니다" instead of starting a second Code Interpreter session and Sonnet
// codegen for the same meeting.
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

// UpdateSimRunFields patches the sim run item (used by ttobak-sim as the run
// progresses through pricing/generating/running/done/error).
func (r *DynamoDBRepository) UpdateSimRunFields(ctx context.Context, meetingID string, fields map[string]interface{}) error {
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
		WithCondition(expression.AttributeExists(expression.Name("PK"))).
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
			return fmt.Errorf("%w: sim run for meeting %s not found", ErrConditionFailed, meetingID)
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
