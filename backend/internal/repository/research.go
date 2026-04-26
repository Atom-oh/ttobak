package repository

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// ResearchRepository provides DynamoDB operations for research entities
type ResearchRepository struct {
	client    *dynamodb.Client
	tableName string
}

// NewResearchRepository creates a new ResearchRepository
func NewResearchRepository(client *dynamodb.Client, tableName string) *ResearchRepository {
	return &ResearchRepository{
		client:    client,
		tableName: tableName,
	}
}

// researchItem wraps a Research with DynamoDB key attributes
type researchItem struct {
	PK         string `dynamodbav:"PK"`
	SK         string `dynamodbav:"SK"`
	EntityType string `dynamodbav:"entityType"`
	model.Research
}

// researchIndexItem is the user index record (PK: USER#{userId}, SK: RESEARCH#{researchId})
type researchIndexItem struct {
	PK         string `dynamodbav:"PK"`
	SK         string `dynamodbav:"SK"`
	EntityType string `dynamodbav:"entityType"`
	ResearchID string `dynamodbav:"researchId"`
}

// CreateResearch writes two items: the main record and the user index record
func (r *ResearchRepository) CreateResearch(ctx context.Context, research *model.Research) error {
	// Main record: PK=RESEARCH#{researchId}, SK=CONFIG
	mainItem := researchItem{
		PK:         model.PrefixResearch + research.ResearchID,
		SK:         model.PrefixConfig,
		EntityType: "RESEARCH",
		Research:   *research,
	}

	mainAV, err := attributevalue.MarshalMap(mainItem)
	if err != nil {
		return fmt.Errorf("failed to marshal research: %w", err)
	}

	// User index: PK=USER#{userId}, SK=RESEARCH#{researchId}
	indexItem := researchIndexItem{
		PK:         model.PrefixUser + research.UserID,
		SK:         model.PrefixResearch + research.ResearchID,
		EntityType: "RESEARCH_INDEX",
		ResearchID: research.ResearchID,
	}

	indexAV, err := attributevalue.MarshalMap(indexItem)
	if err != nil {
		return fmt.Errorf("failed to marshal research index: %w", err)
	}

	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{
				Put: &types.Put{
					TableName: aws.String(r.tableName),
					Item:      mainAV,
				},
			},
			{
				Put: &types.Put{
					TableName: aws.String(r.tableName),
					Item:      indexAV,
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create research: %w", err)
	}

	return nil
}

// GetResearch retrieves a research by researchId
// PK: RESEARCH#{researchId}, SK: CONFIG
func (r *ResearchRepository) GetResearch(ctx context.Context, researchId string) (*model.Research, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixResearch + researchId},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get research: %w", err)
	}

	if result.Item == nil {
		return nil, nil
	}

	var item researchItem
	if err := attributevalue.UnmarshalMap(result.Item, &item); err != nil {
		return nil, fmt.Errorf("failed to unmarshal research: %w", err)
	}

	return &item.Research, nil
}

// UpdateResearchFieldsConditional updates fields only if current status matches expectedStatus.
func (r *ResearchRepository) UpdateResearchFieldsConditional(ctx context.Context, researchId string, fields map[string]interface{}, expectedStatus string) error {
	if len(fields) == 0 {
		return nil
	}

	var updateBuilder expression.UpdateBuilder
	first := true
	for k, v := range fields {
		if first {
			updateBuilder = expression.Set(expression.Name(k), expression.Value(v))
			first = false
		} else {
			updateBuilder = updateBuilder.Set(expression.Name(k), expression.Value(v))
		}
	}

	cond := expression.Name("status").Equal(expression.Value(expectedStatus))
	expr, err := expression.NewBuilder().WithUpdate(updateBuilder).WithCondition(cond).Build()
	if err != nil {
		return fmt.Errorf("failed to build conditional expression: %w", err)
	}

	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixResearch + researchId},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
		},
		UpdateExpression:          expr.Update(),
		ConditionExpression:       expr.Condition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		return fmt.Errorf("conditional update failed: %w", err)
	}
	return nil
}

// UpdateResearchFields performs a partial update on a research record
func (r *ResearchRepository) UpdateResearchFields(ctx context.Context, researchId string, fields map[string]interface{}) error {
	if len(fields) == 0 {
		return nil
	}

	var updateBuilder expression.UpdateBuilder
	first := true
	for k, v := range fields {
		if first {
			updateBuilder = expression.Set(expression.Name(k), expression.Value(v))
			first = false
		} else {
			updateBuilder = updateBuilder.Set(expression.Name(k), expression.Value(v))
		}
	}

	expr, err := expression.NewBuilder().WithUpdate(updateBuilder).Build()
	if err != nil {
		return fmt.Errorf("failed to build update expression: %w", err)
	}

	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixResearch + researchId},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
		},
		UpdateExpression:          expr.Update(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		return fmt.Errorf("failed to update research: %w", err)
	}

	return nil
}

// ListUserResearch lists all research tasks for a user
// Query PK=USER#{userId}, SK begins_with RESEARCH#, then fetch each full record
func (r *ResearchRepository) ListUserResearch(ctx context.Context, userId string) ([]model.Research, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixUser + userId)).
		And(expression.Key("SK").BeginsWith(model.PrefixResearch))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		ScanIndexForward:          aws.Bool(false),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query user research: %w", err)
	}

	var indexItems []researchIndexItem
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &indexItems); err != nil {
		return nil, fmt.Errorf("failed to unmarshal research index items: %w", err)
	}

	researches := make([]model.Research, 0, len(indexItems))
	for _, idx := range indexItems {
		research, err := r.GetResearch(ctx, idx.ResearchID)
		if err != nil {
			return nil, fmt.Errorf("failed to get research %s: %w", idx.ResearchID, err)
		}
		if research != nil {
			researches = append(researches, *research)
		}
	}

	return researches, nil
}

// ListSubPages returns research items that have the given parentId
// Filters the user's research list in-memory (suitable for small volumes)
func (r *ResearchRepository) ListSubPages(ctx context.Context, userId, parentId string) ([]model.Research, error) {
	all, err := r.ListUserResearch(ctx, userId)
	if err != nil {
		return nil, fmt.Errorf("failed to list user research for sub-pages: %w", err)
	}

	subPages := make([]model.Research, 0)
	for _, res := range all {
		if res.ParentID == parentId {
			subPages = append(subPages, res)
		}
	}

	return subPages, nil
}

// DeleteResearch deletes both the main record and the user index record
func (r *ResearchRepository) DeleteResearch(ctx context.Context, researchId, userId string) error {
	_, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{
				Delete: &types.Delete{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: model.PrefixResearch + researchId},
						"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
					},
				},
			},
			{
				Delete: &types.Delete{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userId},
						"SK": &types.AttributeValueMemberS{Value: model.PrefixResearch + researchId},
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete research: %w", err)
	}

	return nil
}
