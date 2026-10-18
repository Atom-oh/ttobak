package repository

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

func (r *DynamoDBRepository) putCurrentAccountInsights(ctx context.Context, insights []model.AccountInsight) error {
	first := insights[0]
	snapshot, err := r.CaptureSummaryPrivacy(ctx, first.SourceUserID, first.SourceID)
	if err != nil {
		return err
	}
	if snapshot == nil {
		return ErrConditionFailed
	}
	meeting := snapshot.Meeting
	if !meeting.SharedToAccount || meeting.AccountID != first.AccountID ||
		meeting.SummaryGeneration() != first.SummaryGeneration || meeting.InsightsRevision != first.InsightsRevision {
		return ErrConditionFailed
	}
	var canonical []model.MeetingInsight
	if err := json.Unmarshal([]byte(meeting.Insights), &canonical); err != nil || len(canonical) != len(insights) {
		return ErrConditionFailed
	}
	keys := map[string]bool{}
	for i, insight := range insights {
		expected := canonical[i]
		if insight.PK != model.PrefixAccount+first.AccountID || insight.AccountID != first.AccountID ||
			insight.SourceType != "meeting" || insight.SourceID != first.SourceID ||
			insight.SourceUserID != first.SourceUserID || insight.SummaryGeneration != first.SummaryGeneration ||
			insight.InsightsRevision != first.InsightsRevision || insight.Type != expected.Type ||
			insight.Text != expected.Text || insight.Implication != expected.Implication ||
			insight.NextAction != expected.NextAction || insight.TsMarker != expected.TsMarker ||
			!reflect.DeepEqual(insight.Entities, expected.Entities) || insight.Evidence != "" ||
			!insight.OccurredAt.Equal(meeting.Date) || keys[insight.SK] {
			return ErrConditionFailed
		}
		keys[insight.SK] = true
	}
	prefix := first.SK[:strings.LastIndex(first.SK, "#")+1]
	if !strings.HasPrefix(prefix, model.PrefixInsight+meeting.Date.UTC().Format("2006-01-02T15:04:05Z07:00")+"#"+meeting.MeetingID+"#") {
		return ErrConditionFailed
	}
	query, err := expression.NewBuilder().WithKeyCondition(expression.Key("PK").Equal(expression.Value(first.PK)).
		And(expression.Key("SK").BeginsWith(prefix))).WithProjection(
		expression.NamesList(expression.Name("PK"), expression.Name("SK"))).Build()
	if err != nil {
		return err
	}
	pages := dynamodb.NewQueryPaginator(r.client, &dynamodb.QueryInput{
		TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true),
		KeyConditionExpression: query.KeyCondition(), ProjectionExpression: query.Projection(),
		ExpressionAttributeNames: query.Names(), ExpressionAttributeValues: query.Values(),
	})
	checks, err := r.summaryChecks(snapshot, 0)
	if err != nil {
		return err
	}
	items := checks
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, existing := range page.Items {
			sk, ok := existing["SK"].(*types.AttributeValueMemberS)
			if !ok {
				return ErrConditionFailed
			}
			if !keys[sk.Value] {
				items = append(items, types.TransactWriteItem{Delete: &types.Delete{
					TableName: aws.String(r.tableName), Key: existing,
				}})
			}
		}
		if len(items)+len(insights) > 100 {
			return ErrSummaryLimit
		}
	}
	for _, insight := range insights {
		if !strings.HasPrefix(insight.SK, prefix) {
			return ErrConditionFailed
		}
		item, err := attributevalue.MarshalMap(insight)
		if err != nil {
			return err
		}
		items = append(items, types.TransactWriteItem{Put: &types.Put{
			TableName: aws.String(r.tableName), Item: item,
		}})
	}
	if len(items) > 100 {
		return ErrSummaryLimit
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}, noSummaryRetries)
	return summaryWriteError(err)
}

// ClearAccountInsights removes an account's fanned-out rows after a validated
// empty insight result. Each delete batch is conditioned on the captured meeting
// (publication, account, generation, revision and the empty insights bytes), so
// a stale run cannot delete rows that a newer nonempty publication fanned out.
func (r *DynamoDBRepository) ClearAccountInsights(ctx context.Context, ownerID, meetingID, accountID, generation, revision string) error {
	snapshot, err := r.CaptureSummaryPrivacy(ctx, ownerID, meetingID)
	if err != nil {
		return err
	}
	if snapshot == nil {
		return ErrConditionFailed
	}
	meeting := snapshot.Meeting
	if accountID == "" || !meeting.SharedToAccount || meeting.AccountID != accountID ||
		meeting.SummaryGeneration() != generation || meeting.InsightsRevision != revision {
		return ErrConditionFailed
	}
	if raw := strings.TrimSpace(meeting.Insights); raw != "" {
		var canonical []model.MeetingInsight
		if err := json.Unmarshal([]byte(raw), &canonical); err != nil || len(canonical) != 0 {
			return ErrConditionFailed
		}
	}
	prefix := model.PrefixInsight + meeting.Date.UTC().Format("2006-01-02T15:04:05Z07:00") + "#" + meetingID + "#"
	query, err := expression.NewBuilder().WithKeyCondition(expression.Key("PK").Equal(expression.Value(model.PrefixAccount + accountID)).
		And(expression.Key("SK").BeginsWith(prefix))).WithProjection(
		expression.NamesList(expression.Name("PK"), expression.Name("SK"))).Build()
	if err != nil {
		return err
	}
	pages := dynamodb.NewQueryPaginator(r.client, &dynamodb.QueryInput{
		TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true),
		KeyConditionExpression: query.KeyCondition(), ProjectionExpression: query.Projection(),
		ExpressionAttributeNames: query.Names(), ExpressionAttributeValues: query.Values(),
	})
	var deletes []types.TransactWriteItem
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, existing := range page.Items {
			deletes = append(deletes, types.TransactWriteItem{Delete: &types.Delete{
				TableName: aws.String(r.tableName), Key: existing,
			}})
		}
	}
	for len(deletes) > 0 {
		checks, err := r.summaryChecks(snapshot, 0)
		if err != nil {
			return err
		}
		n := min(len(deletes), 100-len(checks))
		if n <= 0 {
			return ErrSummaryLimit
		}
		_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
			TransactItems: append(checks, deletes[:n]...)}, noSummaryRetries)
		if err != nil {
			return summaryWriteError(err)
		}
		deletes = deletes[n:]
	}
	return nil
}
