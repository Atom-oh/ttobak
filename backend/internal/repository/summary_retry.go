package repository

import (
	"context"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/model"
)

const MaxSummaryRetries = 2

var ErrSummaryRetryBusy = errors.New("summary retry claim is active")

func (r *DynamoDBRepository) summaryRetryUpdate(ctx context.Context, owner, id string, update expression.UpdateBuilder, condition expression.ConditionBuilder) error {
	op, err := analysisUpdate(r.tableName, meetingKey(owner, id), update, condition)
	if err != nil {
		return err
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{TableName: op.TableName, Key: op.Key, UpdateExpression: op.UpdateExpression,
		ConditionExpression: op.ConditionExpression, ExpressionAttributeNames: op.ExpressionAttributeNames, ExpressionAttributeValues: op.ExpressionAttributeValues},
		func(o *dynamodb.Options) { o.Retryer = aws.NopRetryer{}; o.RetryMaxAttempts = 1 })
	return analysisWriteError(err)
}

// ClaimSummaryRetry returns its exact claim so failed reads can release only it.
func (r *DynamoDBRepository) ClaimSummaryRetry(ctx context.Context, owner, id string) (string, error) {
	now := time.Now().UTC()
	claim := now.Format(time.RFC3339Nano)
	condition := expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("status").Equal(expression.Value(model.StatusSummarizing))).
		And(expression.Name("summaryRetryPending").Equal(expression.Value(true))).
		And(expression.AttributeNotExists(expression.Name("summaryRetryAttempts")).Or(expression.Name("summaryRetryAttempts").LessThan(expression.Value(MaxSummaryRetries)))).
		And(expression.AttributeNotExists(expression.Name("summarizeRetryClaimedAt")).Or(expression.Name("summarizeRetryClaimedAt").LessThan(expression.Value(now.Add(-SummarizeRetryClaimTTL).Format(time.RFC3339Nano)))))
	update := expression.Set(expression.Name("summarizeRetryClaimedAt"), expression.Value(claim)).
		Set(expression.Name("updatedAt"), expression.Value(claim)).Add(expression.Name("summaryRetryAttempts"), expression.Value(1))
	err := r.summaryRetryUpdate(ctx, owner, id, update, condition)
	if errors.Is(err, ErrConditionFailed) {
		if err := r.ExpireSummaryRetry(ctx, owner, id); err != nil {
			return "", err
		}
		current, err := r.MetadataView().GetMeeting(ctx, owner, id)
		if err == nil && current != nil && current.Status == model.StatusSummarizing && current.SummaryRetryPending {
			return "", ErrSummaryRetryBusy
		}
		return "", err
	}
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return "", errors.Join(err, r.ReleaseSummaryRetryClaim(cleanup, owner, id, claim))
	}
	return claim, nil
}

// ExpireSummaryRetry ends an exhausted marker only after its claim is absent or stale.
func (r *DynamoDBRepository) ExpireSummaryRetry(ctx context.Context, owner, id string) error {
	condition := expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("status").Equal(expression.Value(model.StatusSummarizing))).
		And(expression.Name("summaryRetryPending").Equal(expression.Value(true))).
		And(expression.Name("summaryRetryAttempts").GreaterThanEqual(expression.Value(MaxSummaryRetries))).
		And(expression.AttributeNotExists(expression.Name("summarizeRetryClaimedAt")).Or(expression.Name("summarizeRetryClaimedAt").LessThan(expression.Value(time.Now().UTC().Add(-SummarizeRetryClaimTTL).Format(time.RFC3339Nano)))))
	update := expression.Set(expression.Name("status"), expression.Value(model.StatusError)).
		Set(expression.Name("summaryRetryPending"), expression.Value(false)).
		Set(expression.Name("summaryConflictCode"), expression.Value("RETRY_EXHAUSTED")).
		Remove(expression.Name("summarizeRetryClaimedAt"))
	err := r.summaryRetryUpdate(ctx, owner, id, update, condition)
	if errors.Is(err, ErrConditionFailed) {
		return nil
	}
	return err
}

// ReleaseSummaryRetryClaim also terminates the final failed attempt visibly.
func (r *DynamoDBRepository) ReleaseSummaryRetryClaim(ctx context.Context, owner, id, claim string) error {
	owned := expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("summarizeRetryClaimedAt").Equal(expression.Value(claim)))
	for _, terminal := range []bool{true, false} {
		condition := owned.And(expression.Name("status").Equal(expression.Value(model.StatusSummarizing)))
		code := "SOURCE_CHANGED"
		update := expression.Remove(expression.Name("summarizeRetryClaimedAt")).
			Set(expression.Name("summaryRetryPending"), expression.Value(!terminal)).
			Set(expression.Name("updatedAt"), expression.Value(time.Now().UTC()))
		if terminal {
			condition = condition.And(expression.Name("summaryRetryAttempts").GreaterThanEqual(expression.Value(MaxSummaryRetries)))
			update = update.Set(expression.Name("status"), expression.Value(model.StatusError))
			code = "RETRY_EXHAUSTED"
		} else {
			condition = condition.And(expression.Name("summaryRetryAttempts").LessThan(expression.Value(MaxSummaryRetries)))
		}
		update = update.Set(expression.Name("summaryConflictCode"), expression.Value(code))
		err := r.summaryRetryUpdate(ctx, owner, id, update, condition)
		if !errors.Is(err, ErrConditionFailed) {
			return err
		}
	}
	// A different lifecycle state may retain our claim; leave that state intact.
	condition := owned.And(expression.AttributeNotExists(expression.Name("status")).Or(expression.Name("status").NotEqual(expression.Value(model.StatusSummarizing))))
	update := expression.Remove(expression.Name("summarizeRetryClaimedAt")).
		Set(expression.Name("summaryRetryPending"), expression.Value(false))
	err := r.summaryRetryUpdate(ctx, owner, id, update, condition)
	if errors.Is(err, ErrConditionFailed) {
		return nil // a different claim or deletion owns the row
	}
	return err
}
