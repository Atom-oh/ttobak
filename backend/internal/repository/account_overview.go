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

// GetMeetingActionState is a strong read of a meeting's identity, publication
// and privacy fields plus title, projectIds, date and saved action items. It never
// loads transcripts, notes or summary content.
func (r *DynamoDBRepository) GetMeetingActionState(ctx context.Context, ownerID, meetingID string) (*model.Meeting, error) {
	projection := expression.NamesList(expression.Name("PK"), expression.Name("SK"),
		expression.Name("meetingId"), expression.Name("userId"), expression.Name("entityType"),
		expression.Name("accountId"), expression.Name("sharedToAccount"),
		expression.Name("summaryPrivacy"), expression.Name("summaryAccountApproval"), expression.Name("insightsRevision"),
		expression.Name("projectIds"), expression.Name("date"), expression.Name("actionItems"), expression.Name("title"))
	expr, err := expression.NewBuilder().WithProjection(projection).Build()
	if err != nil {
		return nil, fmt.Errorf("build meeting action projection: %w", err)
	}
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(r.tableName),
		ConsistentRead: aws.Bool(true),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + ownerID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
		},
		ProjectionExpression:     expr.Projection(),
		ExpressionAttributeNames: expr.Names(),
	})
	if err != nil {
		return nil, fmt.Errorf("get meeting action state: %w", err)
	}
	if len(result.Item) == 0 {
		return nil, nil
	}
	var meeting model.Meeting
	if err := attributevalue.UnmarshalMap(result.Item, &meeting); err != nil {
		return nil, fmt.Errorf("unmarshal meeting action state: %w", err)
	}
	return &meeting, nil
}
