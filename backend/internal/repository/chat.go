package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/model"
)

// ChatRepository provides DynamoDB operations for chat messages
type ChatRepository struct {
	client    *dynamodb.Client
	tableName string
}

// NewChatRepository creates a new ChatRepository
func NewChatRepository(client *dynamodb.Client, tableName string) *ChatRepository {
	return &ChatRepository{
		client:    client,
		tableName: tableName,
	}
}

// SaveMessage saves a chat message to DynamoDB
// PK: RESEARCH#{researchId}, SK: MSG#{createdAt}#{msgId}
func (r *ChatRepository) SaveMessage(ctx context.Context, researchId string, msg *model.ChatMessage) error {
	item := model.ChatMessageItem{
		PK:          model.PrefixResearch + researchId,
		SK:          model.PrefixMsg + msg.CreatedAt + "#" + msg.MsgID,
		EntityType:  "CHAT_MESSAGE",
		ChatMessage: *msg,
	}

	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return fmt.Errorf("failed to marshal chat message: %w", err)
	}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      av,
	})
	if err != nil {
		return fmt.Errorf("failed to save chat message: %w", err)
	}

	return nil
}

// ListMessages lists all chat messages for a research in chronological order
// Query PK=RESEARCH#{researchId}, SK begins_with MSG#, ScanIndexForward=true
func (r *ChatRepository) ListMessages(ctx context.Context, researchId string) ([]model.ChatMessage, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixResearch + researchId)).
		And(expression.Key("SK").BeginsWith(model.PrefixMsg))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		ScanIndexForward:          aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query chat messages: %w", err)
	}

	var items []model.ChatMessageItem
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &items); err != nil {
		return nil, fmt.Errorf("failed to unmarshal chat messages: %w", err)
	}

	messages := make([]model.ChatMessage, 0, len(items))
	for _, item := range items {
		msg := item.ChatMessage
		// Extract msgId from SK if empty (SK format: MSG#{createdAt}#{msgId})
		if msg.MsgID == "" {
			parts := strings.SplitN(item.SK, "#", 3)
			if len(parts) == 3 {
				msg.MsgID = parts[2]
			}
		}
		messages = append(messages, msg)
	}

	return messages, nil
}
