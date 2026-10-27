package repository

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

func termsSourceKey(ownerID, kind, ref string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + ownerID},
		"SK": &types.AttributeValueMemberS{Value: model.TermsSourceSK(kind, ref)},
	}
}

// GetTermsSourceRun reads a KB/research source's idempotency row
// consistently; nil means the source never succeeded.
func (r *DynamoDBRepository) GetTermsSourceRun(ctx context.Context, ownerID, kind, ref string) (*model.TermsSourceRun, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), Key: termsSourceKey(ownerID, kind, ref), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, err
	}
	if len(result.Item) == 0 {
		return nil, nil
	}
	var run model.TermsSourceRun
	if err := attributevalue.UnmarshalMap(result.Item, &run); err != nil {
		return nil, fmt.Errorf("decode terms source run: %w", err)
	}
	return &run, nil
}

// PutTermsSourceRun records a succeeded source run under the owner's
// partition. It is a plain overwrite: the row only short-circuits a repeat of
// the same content, and merges are idempotent per source on their own.
func (r *DynamoDBRepository) PutTermsSourceRun(ctx context.Context, run *model.TermsSourceRun) error {
	if run == nil || run.OwnerID == "" || run.Kind == "" || run.Ref == "" {
		return fmt.Errorf("%w: terms source run needs owner, kind and ref", ErrInvalidInput)
	}
	row := *run
	row.PK, row.SK, row.EntityType = model.PrefixUser+run.OwnerID, model.TermsSourceSK(run.Kind, run.Ref), model.EntityTypeTermsSource
	item, err := attributevalue.MarshalMap(row)
	if err != nil {
		return err
	}
	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(r.tableName), Item: item})
	return err
}
