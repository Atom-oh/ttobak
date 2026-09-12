package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

func (r *DynamoDBRepository) publishMeetingSummary(ctx context.Context, snapshot *model.SummarySnapshot, content, coverage string) (resultErr error) {
	if !validSummarySnapshot(snapshot) {
		return ErrConditionFailed
	}
	if len(snapshot.Checks) > 100 {
		return ErrSummaryLimit
	}
	fields := map[string]interface{}{"content": content, "status": model.StatusDone, "attachmentSummarySources": coverage,
		"summaryRetryPending": false, "summaryConflictCode": "", "summaryRetryAttempts": 0, "updatedAt": time.Now().UTC().Format(time.RFC3339Nano)}
	stored := map[string]interface{}{}
	for key, value := range snapshot.Stored {
		stored[key] = value
	}
	for key, value := range fields {
		stored[key] = value
	}
	parent := snapshot.Checks[0]
	parent.Fields = map[string]model.SummaryValue{}
	for key, value := range snapshot.Checks[0].Fields {
		parent.Fields[key] = value
	}
	var uploaded []string
	safeCleanup := true
	defer func() {
		if resultErr != nil && safeCleanup {
			resultErr = errors.Join(resultErr, r.deleteUncommittedTranscriptSpills(ctx, uploaded))
		}
	}()
	for {
		body, err := json.Marshal(stored)
		if err != nil {
			return err
		}
		if len(body) <= 300*1024 {
			break
		}
		pick, text := "", ""
		for _, name := range transcriptFamilyFields {
			value, _ := stored[name].(string)
			if !strings.HasPrefix(value, "s3://") && len(value) > len(text) {
				pick, text = name, value
			}
		}
		if pick == "" || r.s3Client == nil {
			return ErrSummaryLimit
		}
		original, present := snapshot.Stored[pick]
		parent.Fields[pick] = model.SummaryValue{Present: present, Value: original}
		ref, err := r.storeConditionalTranscript(ctx, snapshot.Meeting.MeetingID, pick, text, &uploaded)
		if err != nil {
			return err
		}
		stored[pick], fields[pick] = ref, ref
	}
	update := expression.Set(expression.Name("content"), expression.Value(content))
	for key, value := range fields {
		if key != "content" {
			update = update.Set(expression.Name(key), expression.Value(value))
		}
	}
	update = update.Remove(expression.Name("summarizeRetryClaimedAt"))
	op, err := analysisUpdate(r.tableName, summaryRowKey(parent.PK, parent.SK), update, summaryCondition(parent))
	if err != nil {
		return err
	}
	items := []types.TransactWriteItem{{Update: op}}
	for _, check := range snapshot.Checks[1:] {
		expr, err := expression.NewBuilder().WithCondition(summaryCondition(check)).Build()
		if err != nil {
			return err
		}
		items = append(items, types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{TableName: aws.String(r.tableName),
			Key: summaryRowKey(check.PK, check.SK), ConditionExpression: expr.Condition(),
			ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()}})
	}
	// No source bytes or row conditions are refreshed to authorize old output.
	if err := r.CheckResummaryObjects(ctx, snapshot.Meeting.MeetingID, snapshot.Objects); err != nil {
		return errors.Join(ErrConditionFailed, err)
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items},
		func(o *dynamodb.Options) { o.Retryer = aws.NopRetryer{}; o.RetryMaxAttempts = 1 })
	if err == nil {
		return nil
	}
	if isConditionalCheckFailedTransaction(err) {
		return ErrConditionFailed
	}
	safeCleanup = false // Unknown commit outcome: preserve every potentially referenced blob.
	return err
}
