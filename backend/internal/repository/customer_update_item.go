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

func getItemInput(tableName, userID, urlHash string) (*dynamodb.GetItemInput, error) {
	if userID == "" || !urlHashPattern.MatchString(urlHash) {
		return nil, invalidRun("item key")
	}
	return &dynamodb.GetItemInput{
		TableName: aws.String(tableName), Key: updateUserKey(userID, model.UpdateItemSK(urlHash)), ConsistentRead: aws.Bool(true),
	}, nil
}

// itemFromRow returns the stored item, or nil when the row is absent or its
// key does not match its urlHash.
func itemFromRow(userID string, item map[string]types.AttributeValue) (*model.UpdateItem, error) {
	if len(item) == 0 {
		return nil, nil
	}
	var row updateItemRow
	if err := attributevalue.UnmarshalMap(item, &row); err != nil {
		return nil, fmt.Errorf("failed to unmarshal update item: %w", err)
	}
	if row.PK != model.PrefixUser+userID || row.SK != model.UpdateItemSK(row.URLHash) {
		return nil, nil
	}
	return &row.UpdateItem, nil
}

// GetItem returns one of the user's inbox items with a consistent read, or
// nil when absent.
func (r *CustomerUpdateRepository) GetItem(ctx context.Context, userID, urlHash string) (*model.UpdateItem, error) {
	in, err := getItemInput(r.tableName, userID, urlHash)
	if err != nil {
		return nil, err
	}
	res, err := r.client.GetItem(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("failed to get update item: %w", err)
	}
	return itemFromRow(userID, res.Item)
}
