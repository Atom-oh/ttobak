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

func actionAnalysisKey(meetingID string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
		"SK": &types.AttributeValueMemberS{Value: model.ActionAnalysisSK},
	}
}

func (r *DynamoDBRepository) GetActionAnalysis(ctx context.Context, meetingID string) (*model.ActionItemsAnalysis, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), Key: actionAnalysisKey(meetingID), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, err
	}
	if len(result.Item) == 0 {
		return nil, nil
	}
	var state model.ActionItemsAnalysis
	if err := attributevalue.UnmarshalMap(result.Item, &state); err != nil {
		return nil, fmt.Errorf("decode action analysis: %w", err)
	}
	return &state, nil
}

func analysisUpdate(table string, key map[string]types.AttributeValue, update expression.UpdateBuilder, condition expression.ConditionBuilder) (*types.Update, error) {
	expr, err := expression.NewBuilder().WithUpdate(update).WithCondition(condition).Build()
	if err != nil {
		return nil, err
	}
	return &types.Update{
		TableName: aws.String(table), Key: key, UpdateExpression: expr.Update(),
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	}, nil
}

func analysisWriteError(err error) error {
	var failed *types.ConditionalCheckFailedException
	if errors.As(err, &failed) {
		return ErrConditionFailed
	}
	var cancelled *types.TransactionCanceledException
	if errors.As(err, &cancelled) {
		for _, reason := range cancelled.CancellationReasons {
			if aws.ToString(reason.Code) == "ConditionalCheckFailed" {
				return ErrConditionFailed
			}
		}
	}
	return err
}

func analysisRunCondition(runID, status string) expression.ConditionBuilder {
	return expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("runId").Equal(expression.Value(runID))).
		And(expression.Name("status").Equal(expression.Value(status)))
}

// QueueActionAnalysis atomically verifies the source and claims a new run. The
// prior identity/status/lease protects against concurrent completion or retry.
func (r *DynamoDBRepository) QueueActionAnalysis(ctx context.Context, ownerID, meetingID, source string, prior, next *model.ActionItemsAnalysis) error {
	condition := expression.AttributeNotExists(expression.Name("PK"))
	if prior != nil {
		condition = analysisRunCondition(prior.RunID, prior.Status).
			And(expression.Name("leaseUntil").Equal(expression.Value(prior.LeaseUntil)))
		if prior.Pending() {
			condition = condition.And(expression.Name("leaseUntil").LessThanEqual(expression.Value(next.UpdatedAt.UnixMilli())))
		}
	}
	update := expression.Set(expression.Name("runId"), expression.Value(next.RunID)).
		Set(expression.Name("status"), expression.Value(model.AnalysisQueued)).
		Set(expression.Name("sourceHash"), expression.Value(next.SourceHash)).
		Set(expression.Name("startedAt"), expression.Value(next.StartedAt)).
		Set(expression.Name("updatedAt"), expression.Value(next.UpdatedAt)).
		Set(expression.Name("leaseUntil"), expression.Value(next.LeaseUntil)).
		Set(expression.Name("errorCode"), expression.Value("")).
		Set(expression.Name("entityType"), expression.Value("ACTION_ANALYSIS"))
	stateWrite, err := analysisUpdate(r.tableName, actionAnalysisKey(meetingID), update, condition)
	if err != nil {
		return err
	}
	sourceCheck, err := expression.NewBuilder().WithCondition(
		expression.AttributeExists(expression.Name("PK")).
			And(expression.Name("content").Equal(expression.Value(source))).
			And(expression.Name("status").Equal(expression.Value(model.StatusDone))),
	).Build()
	if err != nil {
		return err
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{ConditionCheck: &types.ConditionCheck{
				TableName: aws.String(r.tableName), Key: meetingKey(ownerID, meetingID),
				ConditionExpression: sourceCheck.Condition(), ExpressionAttributeNames: sourceCheck.Names(),
				ExpressionAttributeValues: sourceCheck.Values(),
			}},
			{Update: stateWrite},
		},
	})
	return analysisWriteError(err)
}

func meetingKey(ownerID, meetingID string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + ownerID},
		"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
	}
}

func (r *DynamoDBRepository) StartActionAnalysis(ctx context.Context, meetingID, runID string, now time.Time, leaseUntil int64) error {
	condition := analysisRunCondition(runID, model.AnalysisQueued).
		And(expression.Name("leaseUntil").GreaterThan(expression.Value(now.UnixMilli())))
	update := expression.Set(expression.Name("status"), expression.Value(model.AnalysisRunning)).
		Set(expression.Name("startedAt"), expression.Value(now)).
		Set(expression.Name("updatedAt"), expression.Value(now)).
		Set(expression.Name("leaseUntil"), expression.Value(leaseUntil))
	return r.writeActionAnalysis(ctx, meetingID, update, condition)
}

func (r *DynamoDBRepository) FailActionAnalysis(ctx context.Context, meetingID string, expected *model.ActionItemsAnalysis, code string, now time.Time) error {
	condition := analysisRunCondition(expected.RunID, expected.Status).
		And(expression.Name("leaseUntil").Equal(expression.Value(expected.LeaseUntil)))
	update := expression.Set(expression.Name("status"), expression.Value(model.AnalysisFailed)).
		Set(expression.Name("errorCode"), expression.Value(code)).
		Set(expression.Name("updatedAt"), expression.Value(now)).
		Set(expression.Name("leaseUntil"), expression.Value(int64(0)))
	return r.writeActionAnalysis(ctx, meetingID, update, condition)
}

func (r *DynamoDBRepository) writeActionAnalysis(ctx context.Context, meetingID string, update expression.UpdateBuilder, condition expression.ConditionBuilder) error {
	op, err := analysisUpdate(r.tableName, actionAnalysisKey(meetingID), update, condition)
	if err != nil {
		return err
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: op.TableName, Key: op.Key, UpdateExpression: op.UpdateExpression,
		ConditionExpression: op.ConditionExpression, ExpressionAttributeNames: op.ExpressionAttributeNames,
		ExpressionAttributeValues: op.ExpressionAttributeValues,
	})
	return analysisWriteError(err)
}

func actionItemsMatch(expected string) expression.ConditionBuilder {
	match := expression.Name("actionItems").Equal(expression.Value(expected))
	if expected == "" {
		match = expression.AttributeNotExists(expression.Name("actionItems")).Or(match)
	}
	return match
}

func (r *DynamoDBRepository) CompleteActionAnalysis(ctx context.Context, ownerID, meetingID, runID, source, previous, result string, now time.Time) error {
	meetingCondition := expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("content").Equal(expression.Value(source))).
		And(actionItemsMatch(previous))
	resultWrite, err := analysisUpdate(r.tableName, meetingKey(ownerID, meetingID),
		expression.Set(expression.Name("actionItems"), expression.Value(result)).
			Set(expression.Name("updatedAt"), expression.Value(now)),
		meetingCondition,
	)
	if err != nil {
		return err
	}
	stateWrite, err := analysisUpdate(r.tableName, actionAnalysisKey(meetingID),
		expression.Set(expression.Name("status"), expression.Value(model.AnalysisSucceeded)).
			Set(expression.Name("errorCode"), expression.Value("")).
			Set(expression.Name("updatedAt"), expression.Value(now)).
			Set(expression.Name("leaseUntil"), expression.Value(int64(0))),
		analysisRunCondition(runID, model.AnalysisRunning).
			And(expression.Name("leaseUntil").GreaterThan(expression.Value(now.UnixMilli()))),
	)
	if err != nil {
		return err
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{{Update: resultWrite}, {Update: stateWrite}},
	})
	return analysisWriteError(err)
}
