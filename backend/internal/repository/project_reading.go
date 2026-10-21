package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

type ProjectReadingPage struct {
	Members  []model.ProjectMember
	Meetings []model.ProjectMeetingRef
	Research []model.ProjectResearchRef
	NextKey  string
}

func ProjectReadingPrefix(section string) string {
	switch section {
	case "members":
		return model.PrefixMember
	case "meetings":
		return model.PrefixMeetingRef
	case "insights":
		return model.PrefixProjectMeetingRef
	case "research":
		return model.PrefixProjectResearchRef
	}
	return ""
}

// QueryProjectReading returns one source page, never an entire collection.
// Raw insight Evidence is intentionally excluded, matching the public DTO.
func (r *DynamoDBRepository) QueryProjectReading(ctx context.Context, projectID, section, after string) (*ProjectReadingPage, error) {
	prefix := ProjectReadingPrefix(section)
	if prefix == "" || (after != "" && !strings.HasPrefix(after, prefix)) {
		return nil, fmt.Errorf("invalid project reading selection")
	}
	names := []string{"PK", "SK", "entityType", "projectId"}
	switch section {
	case "members":
		names = append(names, "userId", "email", "addedAt", "GSI1PK", "GSI1SK")
	case "meetings", "insights":
		names = append(names, "meetingId", "ownerUserId", "title", "date", "createdAt")
	case "research":
		names = append(names, "researchId", "ownerUserId", "topic", "createdAt")
	}
	projection := expression.NamesList(expression.Name(names[0]))
	for _, name := range names[1:] {
		projection = projection.AddNames(expression.Name(name))
	}
	pk := model.PrefixProject + projectID
	key := expression.Key("PK").Equal(expression.Value(pk)).
		And(expression.Key("SK").BeginsWith(prefix))
	expr, err := expression.NewBuilder().WithKeyCondition(key).WithProjection(projection).Build()
	if err != nil {
		return nil, err
	}
	input := &dynamodb.QueryInput{
		TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true), Limit: aws.Int32(10),
		ScanIndexForward: aws.Bool(true), KeyConditionExpression: expr.KeyCondition(),
		ProjectionExpression: expr.Projection(), ExpressionAttributeNames: expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	}
	if after != "" {
		input.ExclusiveStartKey = map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: pk}, "SK": &types.AttributeValueMemberS{Value: after},
		}
	}
	out, err := r.client.Query(ctx, input)
	if err != nil {
		return nil, err
	}
	page := &ProjectReadingPage{}
	switch section {
	case "members":
		err = attributevalue.UnmarshalListOfMaps(out.Items, &page.Members)
	case "meetings", "insights":
		err = attributevalue.UnmarshalListOfMaps(out.Items, &page.Meetings)
	case "research":
		err = attributevalue.UnmarshalListOfMaps(out.Items, &page.Research)
	}
	if err != nil {
		return nil, err
	}
	if len(out.LastEvaluatedKey) != 0 {
		key, ok := out.LastEvaluatedKey["SK"].(*types.AttributeValueMemberS)
		partition, valid := out.LastEvaluatedKey["PK"].(*types.AttributeValueMemberS)
		if !ok || !valid || partition.Value != pk || !strings.HasPrefix(key.Value, prefix) || key.Value == after {
			return nil, fmt.Errorf("invalid project reading continuation")
		}
		page.NextKey = key.Value
	}
	return page, nil
}

// GetProjectReadingResearch uses the primary row, never a stale index/batch read.
func (r *DynamoDBRepository) GetProjectReadingResearch(ctx context.Context, id string) (*model.Research, error) {
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true),
		Key: map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: model.PrefixResearch + id}, "SK": &types.AttributeValueMemberS{Value: model.PrefixConfig}}})
	if err != nil {
		return nil, err
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	var item researchItem
	if err := attributevalue.UnmarshalMap(out.Item, &item); err != nil {
		return nil, err
	}
	if item.Research.ResearchID != id {
		return nil, fmt.Errorf("research reading identity mismatch")
	}
	return &item.Research, nil
}
