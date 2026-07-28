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

// DictionaryRepository provides DynamoDB operations for user dictionaries
type DictionaryRepository struct {
	client    *dynamodb.Client
	tableName string
}

// NewDictionaryRepository creates a new dictionary repository
func NewDictionaryRepository(client *dynamodb.Client, tableName string) *DictionaryRepository {
	return &DictionaryRepository{
		client:    client,
		tableName: tableName,
	}
}

// GetDictionary retrieves a user's dictionary from DynamoDB
// Returns nil if the user has no dictionary yet
func (r *DictionaryRepository) GetDictionary(ctx context.Context, userID string) (*model.UserDictionary, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixDictionary},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get dictionary: %w", err)
	}

	if result.Item == nil {
		return nil, nil
	}

	var dict model.UserDictionary
	if err := attributevalue.UnmarshalMap(result.Item, &dict); err != nil {
		return nil, fmt.Errorf("failed to unmarshal dictionary: %w", err)
	}

	return &dict, nil
}

// SaveDictionary saves a user's dictionary to DynamoDB
func (r *DictionaryRepository) SaveDictionary(ctx context.Context, dict *model.UserDictionary) error {
	item, err := attributevalue.MarshalMap(dict)
	if err != nil {
		return fmt.Errorf("failed to marshal dictionary: %w", err)
	}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return fmt.Errorf("failed to save dictionary: %w", err)
	}

	return nil
}
