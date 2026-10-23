package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

type MeetingCatalogCandidate struct {
	MeetingID string `dynamodbav:"meetingId"`
	OwnerID   string `dynamodbav:"ownerUserId"`
}
type MeetingCatalogPage struct {
	Rows    []MeetingCatalogCandidate
	NextKey string
}

// Compact string-only provider state; the partition/index is always rebuilt
// from the authenticated user and current phase, never from caller authority.
func meetingCatalogKey(user, phase, account, raw string) (map[string]types.AttributeValue, error) {
	if raw == "" {
		return nil, nil
	}
	bytes, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil {
		return nil, ErrInvalidMeetingCursor
	}
	var parts []string
	if json.Unmarshal(bytes, &parts) != nil || len(parts) < 1 || len(parts) > 3 {
		return nil, ErrInvalidMeetingCursor
	}
	for _, part := range parts {
		if len(part) > 256 || strings.ContainsAny(part, "\x00\r\n") {
			return nil, ErrInvalidMeetingCursor
		}
	}
	key := map[string]string{}
	if phase == "owned" {
		if len(parts) != 3 {
			return nil, ErrInvalidMeetingCursor
		}
		key = map[string]string{"PK": parts[0], "SK": parts[1], "GSI1PK": model.PrefixUser + user, "GSI1SK": parts[2]}
		encoded, _ := json.Marshal(key)
		if ValidateMeetingListCursor(base64.StdEncoding.EncodeToString(encoded), user, "all") != nil {
			return nil, ErrInvalidMeetingCursor
		}
	} else {
		if len(parts) != 1 {
			return nil, ErrInvalidMeetingCursor
		}
		pk, prefix := model.PrefixUser+user, model.PrefixShare
		if phase == "team" {
			pk, prefix = model.PrefixAccount+account, model.PrefixMeetingRef
		} else if phase != "direct" {
			return nil, ErrInvalidMeetingCursor
		}
		if !strings.HasPrefix(parts[0], prefix) || len(parts[0]) == len(prefix) {
			return nil, ErrInvalidMeetingCursor
		}
		key = map[string]string{"PK": pk, "SK": parts[0]}
	}
	out := map[string]types.AttributeValue{}
	for k, v := range key {
		out[k] = &types.AttributeValueMemberS{Value: v}
	}
	return out, nil
}
func (r *DynamoDBRepository) QueryMeetingCatalog(ctx context.Context, user, phase, account, after string, limit int) (*MeetingCatalogPage, error) {
	if limit < 1 || limit > 20 || !navigationID.MatchString(user) || (phase == "team" && !navigationID.MatchString(account)) {
		return nil, fmt.Errorf("invalid meeting catalog selection")
	}
	start, err := meetingCatalogKey(user, phase, account, after)
	if err != nil {
		return nil, err
	}
	pk, pkName, skName, prefix, index := model.PrefixUser+user, "PK", "SK", model.PrefixShare, ""
	if phase == "owned" {
		pkName, skName, index = "GSI1PK", "GSI1SK", "GSI1"
	} else if phase == "team" {
		pk, prefix = model.PrefixAccount+account, model.PrefixMeetingRef
	} else if phase != "direct" {
		return nil, fmt.Errorf("invalid meeting catalog phase")
	}
	key := expression.Key(pkName).Equal(expression.Value(pk))
	if phase != "owned" {
		key = key.And(expression.Key(skName).BeginsWith(prefix))
	}
	builder := expression.NewBuilder().WithKeyCondition(key).WithProjection(expression.NamesList(expression.Name("PK"), expression.Name("SK"), expression.Name("GSI1PK"), expression.Name("GSI1SK"), expression.Name("meetingId"), expression.Name("ownerUserId")))
	if phase == "owned" {
		builder = builder.WithFilter(expression.Name("entityType").Equal(expression.Value("MEETING")))
	}
	expr, err := builder.Build()
	if err != nil {
		return nil, err
	}
	input := &dynamodb.QueryInput{TableName: aws.String(r.tableName), Limit: aws.Int32(int32(limit)), ScanIndexForward: aws.Bool(false), ExclusiveStartKey: start, KeyConditionExpression: expr.KeyCondition(), FilterExpression: expr.Filter(), ProjectionExpression: expr.Projection(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()}
	if index != "" {
		input.IndexName = aws.String(index)
	} else {
		input.ConsistentRead = aws.Bool(true)
	}
	out, err := r.client.Query(ctx, input)
	if err != nil {
		return nil, err
	}
	page := &MeetingCatalogPage{Rows: []MeetingCatalogCandidate{}}
	for _, item := range out.Items {
		var row MeetingCatalogCandidate
		if attributevalue.UnmarshalMap(item, &row) != nil || !navigationID.MatchString(row.MeetingID) {
			return nil, fmt.Errorf("invalid meeting candidate")
		}
		key, ok := item["SK"].(*types.AttributeValueMemberS)
		partition, valid := item["PK"].(*types.AttributeValueMemberS)
		if !ok || !valid {
			return nil, fmt.Errorf("missing meeting candidate scope")
		}
		expectedPK, expectedSK := model.PrefixUser+user, model.PrefixShare+row.MeetingID
		if phase == "owned" {
			expectedSK = model.PrefixMeeting + row.MeetingID
			row.OwnerID = user
		}
		if phase == "team" {
			expectedPK = model.PrefixAccount + account
			expectedSK = key.Value
			if !strings.HasPrefix(key.Value, model.PrefixMeetingRef) || !navigationID.MatchString(row.OwnerID) {
				return nil, fmt.Errorf("invalid meeting reference")
			}
		}
		if partition.Value != expectedPK || key.Value != expectedSK {
			return nil, fmt.Errorf("foreign meeting candidate")
		}
		page.Rows = append(page.Rows, row)
	}
	if len(out.LastEvaluatedKey) > 0 {
		take := func(name string) string {
			v, _ := out.LastEvaluatedKey[name].(*types.AttributeValueMemberS)
			if v == nil {
				return ""
			}
			return v.Value
		}
		parts := []string{take("SK")}
		if phase == "owned" {
			parts = []string{take("PK"), take("SK"), take("GSI1SK")}
		} else if take("PK") != pk {
			return nil, fmt.Errorf("foreign meeting continuation")
		}
		raw, _ := json.Marshal(parts)
		page.NextKey = base64.RawURLEncoding.EncodeToString(raw)
		if _, err := meetingCatalogKey(user, phase, account, page.NextKey); err != nil || page.NextKey == after {
			return nil, fmt.Errorf("invalid meeting continuation")
		}
	}
	return page, nil
}

func (r *DynamoDBRepository) GetMeetingCatalogMetadata(ctx context.Context, owner, id string) (*model.Meeting, error) {
	names := []string{"PK", "SK", "entityType", "userId", "meetingId", "accountId", "sharedToAccount", "title", "date", "status", "content", "participants", "tags", "sentiment", "duration", "createdAt", "updatedAt", "summaryPrivacy"}
	projection := expression.NamesList(expression.Name(names[0]))
	for _, name := range names[1:] {
		projection = projection.AddNames(expression.Name(name))
	}
	expr, err := expression.NewBuilder().WithProjection(projection).Build()
	if err != nil {
		return nil, err
	}
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true), Key: map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + owner}, "SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + id}}, ProjectionExpression: expr.Projection(), ExpressionAttributeNames: expr.Names()})
	if err != nil || len(out.Item) == 0 {
		return nil, err
	}
	var m model.Meeting
	if err := attributevalue.UnmarshalMap(out.Item, &m); err != nil {
		return nil, err
	}
	if m.UserID != owner || m.MeetingID != id {
		return nil, fmt.Errorf("meeting metadata identity mismatch")
	}
	return &m, nil
}
