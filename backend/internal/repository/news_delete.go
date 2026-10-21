package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// MarkNewsDeleted revokes canonical publication before object cleanup. A retained
// tombstone keeps immutable cleanup keys available for retries without deleting a
// concurrently published replacement. Source ownership is checked in the write.
func (r *CrawlerRepository) MarkNewsDeleted(ctx context.Context, key NewsReadKey, observed map[string]types.AttributeValue, userID string, isAdmin bool) error {
	if userID == "" {
		return ErrConditionFailed
	}
	owner := expression.Name("PK").AttributeExists().
		And(expression.Name("sourceId").Equal(expression.Value(key.SourceID)))
	if !isAdmin {
		owner = owner.And(expression.Name("ownerId").Equal(expression.Value(userID)))
	}
	source, err := expression.NewBuilder().WithCondition(owner).Build()
	if err != nil {
		return err
	}
	condition := expression.Name("PK").AttributeExists()
	for _, field := range []string{
		"schemaVersion", "qualityStatus", "sourceId", "docHash", "attemptToken",
		"sourceRevision", "s3Key", "artifactKey", "markdownSha256", "artifactSha256", "type",
	} {
		if observed[field] == nil {
			return ErrConditionFailed
		}
		condition = condition.And(expression.Name(field).Equal(expression.Value(observed[field])))
	}
	update := expression.Set(expression.Name("qualityStatus"), expression.Value("DELETED")).
		Set(expression.Name("inKB"), expression.Value(false))
	document, err := expression.NewBuilder().WithCondition(condition).WithUpdate(update).Build()
	if err != nil {
		return err
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{ConditionCheck: &types.ConditionCheck{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: "CRAWLER#" + key.SourceID},
					"SK": &types.AttributeValueMemberS{Value: "CONFIG"},
				},
				ConditionExpression: source.Condition(), ExpressionAttributeNames: source.Names(),
				ExpressionAttributeValues: source.Values(),
			}},
			{Update: &types.Update{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: "CRAWLER#" + key.SourceID},
					"SK": &types.AttributeValueMemberS{Value: key.SortKey},
				},
				ConditionExpression: document.Condition(), UpdateExpression: document.Update(),
				ExpressionAttributeNames: document.Names(), ExpressionAttributeValues: document.Values(),
			}},
		},
	})
	if err != nil {
		var canceled *types.TransactionCanceledException
		if errors.As(err, &canceled) {
			for _, reason := range canceled.CancellationReasons {
				if aws.ToString(reason.Code) == "ConditionalCheckFailed" {
					return fmt.Errorf("%w: publication or source owner changed", ErrConditionFailed)
				}
			}
		}
		return fmt.Errorf("unpublish news document: %w", err)
	}
	return nil
}
