package repository

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// actorGrantChecks fences a managing member's grant on the creator's meeting
// in the same transaction (ADR-016 2026-11-06 amendment): the meeting stays
// published to its account, and the actor's direct membership or, with
// descendant reads enabled, the nearest ancestor membership plus every
// traversed parent link is unchanged. When the summary privacy fence already
// updates the meeting row, its snapshot pins accountId/sharedToAccount and no
// second operation on that item is added.
func (r *DynamoDBRepository) actorGrantChecks(ctx context.Context, ownerID, meetingID, actorID string, snapshot *model.SummarySnapshot) ([]types.TransactWriteItem, error) {
	var meeting *model.Meeting
	pinned := snapshot != nil && snapshot.Meeting != nil
	if pinned {
		meeting = snapshot.Meeting
	} else {
		row, err := r.summaryRow(ctx, model.PrefixUser+ownerID, model.PrefixMeeting+meetingID)
		if err != nil {
			return nil, err
		}
		if len(row) == 0 {
			return nil, ErrSummaryAccess
		}
		meeting = &model.Meeting{}
		if err := attributevalue.UnmarshalMap(row, meeting); err != nil {
			return nil, err
		}
	}
	if !meeting.SharedToAccount || meeting.AccountID == "" || meeting.UserID != ownerID {
		return nil, ErrSummaryAccess
	}
	var items []types.TransactWriteItem
	if !pinned {
		expr, err := expression.NewBuilder().WithCondition(expression.AttributeExists(expression.Name("PK")).
			And(expression.Name("accountId").Equal(expression.Value(meeting.AccountID))).
			And(expression.Name("sharedToAccount").Equal(expression.Value(true)))).Build()
		if err != nil {
			return nil, err
		}
		items = append(items, types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{
			TableName: aws.String(r.tableName), Key: summaryRowKey(model.PrefixUser+ownerID, model.PrefixMeeting+meetingID),
			ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
		}})
	}
	checks, err := r.accountTeamMemberChecks(ctx, meeting.AccountID, actorID)
	if err != nil {
		return nil, err
	}
	for _, check := range checks {
		item, err := r.conditionCheckFor(check)
		if err != nil {
			return nil, err
		}
		items = append(items, types.TransactWriteItem{ConditionCheck: item})
	}
	return items, nil
}
