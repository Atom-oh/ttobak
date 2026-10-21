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

type AccountReadingPage struct {
	Members  []model.AccountMember
	Meetings []model.MeetingRef
	Insights []model.AccountInsight
	NextKey  string
}

func AccountReadingPrefix(section string) string {
	switch section {
	case "members":
		return model.PrefixMember
	case "meetings":
		return model.PrefixMeetingRef
	case "insights":
		return model.PrefixInsight
	}
	return ""
}

// QueryAccountReading returns one source page, never an entire collection.
// Raw insight Evidence is intentionally excluded, matching the public DTO.
func (r *DynamoDBRepository) QueryAccountReading(ctx context.Context, accountID, section, after string) (*AccountReadingPage, error) {
	prefix := AccountReadingPrefix(section)
	if prefix == "" || (after != "" && !strings.HasPrefix(after, prefix)) {
		return nil, fmt.Errorf("invalid account reading selection")
	}
	names := []string{"PK", "SK", "entityType", "accountId"}
	switch section {
	case "members":
		names = append(names, "userId", "email", "role", "addedAt", "GSI1PK", "GSI1SK")
	case "meetings":
		names = append(names, "meetingId", "ownerUserId", "title", "date", "createdAt")
	case "insights":
		names = append(names, "type", "text", "implication", "nextAction", "sourceType",
			"sourceId", "sourceUserId", "summaryGeneration", "insightsRevision",
			"occurredAt", "tsMarker", "entities")
	}
	projection := expression.NamesList(expression.Name(names[0]))
	for _, name := range names[1:] {
		projection = projection.AddNames(expression.Name(name))
	}
	pk := model.PrefixAccount + accountID
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
	page := &AccountReadingPage{}
	switch section {
	case "members":
		err = attributevalue.UnmarshalListOfMaps(out.Items, &page.Members)
	case "meetings":
		err = attributevalue.UnmarshalListOfMaps(out.Items, &page.Meetings)
	case "insights":
		err = attributevalue.UnmarshalListOfMaps(out.Items, &page.Insights)
	}
	if err != nil {
		return nil, err
	}
	if len(out.LastEvaluatedKey) != 0 {
		key, ok := out.LastEvaluatedKey["SK"].(*types.AttributeValueMemberS)
		partition, valid := out.LastEvaluatedKey["PK"].(*types.AttributeValueMemberS)
		if !ok || !valid || partition.Value != pk || !strings.HasPrefix(key.Value, prefix) || key.Value <= after {
			return nil, fmt.Errorf("invalid account reading continuation")
		}
		page.NextKey = key.Value
	}
	return page, nil
}
