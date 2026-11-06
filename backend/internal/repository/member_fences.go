package repository

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// memberGrantItems fences a managing member's live grant in accountID: the
// direct member row, or the nearest ancestor member row plus every
// traversed parent link (ADR-016 2026-11-06 amendment).
func (r *DynamoDBRepository) memberGrantItems(ctx context.Context, accountID, actorID string) ([]types.TransactWriteItem, error) {
	checks, err := r.accountTeamMemberChecks(ctx, accountID, actorID)
	if err != nil {
		return nil, err
	}
	items := make([]types.TransactWriteItem, 0, len(checks))
	for _, check := range checks {
		item, err := r.conditionCheckFor(check)
		if err != nil {
			return nil, err
		}
		items = append(items, types.TransactWriteItem{ConditionCheck: item})
	}
	return items, nil
}

func publishedTo(accountID string) expression.ConditionBuilder {
	return expression.Name("accountId").Equal(expression.Value(accountID)).
		And(expression.Name("sharedToAccount").Equal(expression.Value(true)))
}

// UpdateManagedMeetingFieldsIfMatch is UpdateMeetingFieldsIfMatch for a
// managing member: the creator's meeting must also still be published to
// accountID, and the member's live grant is fenced in the same transaction.
func (r *DynamoDBRepository) UpdateManagedMeetingFieldsIfMatch(ctx context.Context, ownerID, actorID, meetingID, accountID string, expected, fields map[string]interface{}) error {
	if accountID == "" || actorID == ownerID {
		return ErrConditionFailed
	}
	extra, err := r.memberGrantItems(ctx, accountID, actorID)
	if err != nil {
		return err
	}
	condition := expression.AttributeExists(expression.Name("PK")).And(publishedTo(accountID))
	for k, v := range expected {
		condition = condition.And(expression.Name(k).Equal(expression.Value(v)))
	}
	return r.updateMeetingFieldsChecked(ctx, ownerID, actorID, meetingID, condition, fields, true, nil, extra)
}

// MeetingProjectUnlinkInAccount unlinks the creator's meeting on behalf of a
// managing member: the meeting must still be published to accountID, the
// project must still be linked to that account, and the member's live grant
// is fenced, all with the unlink itself.
func (r *DynamoDBRepository) MeetingProjectUnlinkInAccount(ctx context.Context, ownerUserID, actorID, meetingID, projectID, accountID, refSK string) error {
	if accountID == "" || actorID == ownerUserID {
		return ErrConditionFailed
	}
	updateExpr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK")).And(publishedTo(accountID))).
		WithUpdate(expression.Delete(expression.Name("projectIds"), expression.Value(&types.AttributeValueMemberSS{Value: []string{projectID}}))).
		Build()
	if err != nil {
		return fmt.Errorf("build member project unlink expression: %w", err)
	}
	projectExpr, err := expression.NewBuilder().WithCondition(expression.AttributeExists(expression.Name("PK")).
		And(expression.Contains(expression.Name("accountIds"), accountID))).Build()
	if err != nil {
		return err
	}
	items := []types.TransactWriteItem{
		{Update: &types.Update{TableName: aws.String(r.tableName),
			Key:                 summaryRowKey(model.PrefixUser+ownerUserID, model.PrefixMeeting+meetingID),
			ConditionExpression: updateExpr.Condition(), UpdateExpression: updateExpr.Update(),
			ExpressionAttributeNames: updateExpr.Names(), ExpressionAttributeValues: updateExpr.Values()}},
		{ConditionCheck: &types.ConditionCheck{TableName: aws.String(r.tableName),
			Key:                 summaryRowKey(model.PrefixProject+projectID, model.SKProjectConfig),
			ConditionExpression: projectExpr.Condition(), ExpressionAttributeNames: projectExpr.Names(),
			ExpressionAttributeValues: projectExpr.Values()}},
	}
	if refSK != "" {
		items = append(items, types.TransactWriteItem{Delete: &types.Delete{TableName: aws.String(r.tableName),
			Key: summaryRowKey(model.PrefixProject+projectID, refSK)}})
	}
	grant, err := r.memberGrantItems(ctx, accountID, actorID)
	if err != nil {
		return err
	}
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: append(items, grant...)}); err != nil {
		return mapProjectTransactionCanceledError(err, meetingID, "meeting", "unlink meeting project")
	}
	return nil
}
