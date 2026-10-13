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

type DocumentCandidate struct {
	SK string `dynamodbav:"SK"`
}

type DocumentCandidatePage struct {
	Rows    []DocumentCandidate
	NextKey string
}

// QueryDocumentCandidates reads at most one bounded DynamoDB page. Partition
// and prefix are constructed from the authenticated user/scope, never a cursor.
func (r *DynamoDBRepository) QueryDocumentCandidates(ctx context.Context, pk, prefix, after string, limit int) (*DocumentCandidatePage, error) {
	if limit < 1 || limit > 50 || (prefix != model.PrefixDoc && prefix != model.PrefixDocShare) {
		return nil, fmt.Errorf("invalid document catalog query")
	}
	key := expression.Key("PK").Equal(expression.Value(pk)).
		And(expression.Key("SK").BeginsWith(prefix))
	expr, err := expression.NewBuilder().WithKeyCondition(key).
		WithProjection(expression.NamesList(expression.Name("SK"))).Build()
	if err != nil {
		return nil, err
	}
	input := &dynamodb.QueryInput{
		TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true), Limit: aws.Int32(int32(limit)),
		ScanIndexForward: aws.Bool(false), KeyConditionExpression: expr.KeyCondition(),
		ProjectionExpression: expr.Projection(), ExpressionAttributeNames: expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	}
	if after != "" {
		input.ExclusiveStartKey = map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: pk}, "SK": &types.AttributeValueMemberS{Value: after},
		}
	}
	result, err := r.client.Query(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("query document catalog: %w", err)
	}
	page := &DocumentCandidatePage{}
	if len(result.LastEvaluatedKey) != 0 {
		partition, ok := result.LastEvaluatedKey["PK"].(*types.AttributeValueMemberS)
		if !ok || partition.Value != pk {
			return nil, fmt.Errorf("document catalog continuation has unexpected partition")
		}
		sortKey, ok := result.LastEvaluatedKey["SK"].(*types.AttributeValueMemberS)
		if !ok {
			return nil, fmt.Errorf("document catalog continuation is missing its key")
		}
		page.NextKey = sortKey.Value
	}
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &page.Rows); err != nil {
		return nil, fmt.Errorf("decode document catalog: %w", err)
	}
	return page, nil
}

// GetDocumentMetadata revalidates a canonical row without hydrating its body or
// returning the public-share capability. Links remain metadata, bounded by the
// underlying DynamoDB item; the service renders only an explicit preview.
func (r *DynamoDBRepository) GetDocumentMetadata(ctx context.Context, pk, docID string) (*model.AccountDocument, error) {
	expr, err := expression.NewBuilder().WithProjection(expression.NamesList(
		expression.Name("PK"), expression.Name("SK"), expression.Name("docId"), expression.Name("accountId"),
		expression.Name("title"), expression.Name("docType"), expression.Name("path"), expression.Name("links"),
		expression.Name("fileKey"), expression.Name("fileName"), expression.Name("mimeType"), expression.Name("fileSize"),
		expression.Name("sourceUserId"), expression.Name("createdAt"), expression.Name("updatedAt"),
		expression.Name("entityType"),
	)).Build()
	if err != nil {
		return nil, err
	}
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: pk},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixDoc + docID},
		},
		ProjectionExpression: expr.Projection(), ExpressionAttributeNames: expr.Names(),
	})
	if err != nil {
		return nil, fmt.Errorf("read document metadata: %w", err)
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	var doc model.AccountDocument
	if err := attributevalue.UnmarshalMap(out.Item, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}
