package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// Terms analysis run state follows the action-items CAS: every transition is
// conditioned on the observed run identity, status and lease. Queue, Start,
// RenewLease and Complete also carry a ConditionCheck on the meeting item, so
// those transitions fail once the meeting item is gone. Fail is a single
// conditional update of an existing row with no meeting check and never
// creates one; callers run it with a detached short (3 s) context. Meeting
// deletion does not yet remove ANALYSIS#terms or the meeting's DICTOBS# rows
// (design A8); that cleanup must ship before or with the first writer.

func termsAnalysisKey(meetingID string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
		"SK": &types.AttributeValueMemberS{Value: model.TermsAnalysisSK},
	}
}

// TermsCompletion is what a succeeded run publishes.
type TermsCompletion struct {
	Counters      model.TermsCounters
	AcceptedTerms []model.TermsAcceptedTerm
	ErrorCode     string // "" or TAINTED_SUGGEST_ONLY
}

// GetTermsAnalysis reads the run state consistently; nil means absent.
func (r *DynamoDBRepository) GetTermsAnalysis(ctx context.Context, meetingID string) (*model.TermsAnalysis, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), Key: termsAnalysisKey(meetingID), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, err
	}
	if len(result.Item) == 0 {
		return nil, nil
	}
	var state model.TermsAnalysis
	if err := attributevalue.UnmarshalMap(result.Item, &state); err != nil {
		return nil, fmt.Errorf("decode terms analysis: %w", err)
	}
	return &state, nil
}

// meetingCheck requires the owner's meeting item to exist and, when
// requireDone, to be done.
func (r *DynamoDBRepository) meetingCheck(ownerID, meetingID string, requireDone bool) (types.TransactWriteItem, error) {
	condition := expression.AttributeExists(expression.Name("PK"))
	if requireDone {
		condition = condition.And(expression.Name("status").Equal(expression.Value(model.StatusDone)))
	}
	expr, err := expression.NewBuilder().WithCondition(condition).Build()
	if err != nil {
		return types.TransactWriteItem{}, err
	}
	return types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{
		TableName: aws.String(r.tableName), Key: meetingKey(ownerID, meetingID),
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	}}, nil
}

// checkedTermsWrite runs the meeting ConditionCheck and the state update in
// one transaction.
func (r *DynamoDBRepository) checkedTermsWrite(ctx context.Context, ownerID, meetingID string, requireDone bool, update expression.UpdateBuilder, condition expression.ConditionBuilder) error {
	if ownerID == "" || meetingID == "" {
		return fmt.Errorf("terms analysis write requires owner and meeting")
	}
	check, err := r.meetingCheck(ownerID, meetingID, requireDone)
	if err != nil {
		return err
	}
	write, err := analysisUpdate(r.tableName, termsAnalysisKey(meetingID), update, condition)
	if err != nil {
		return err
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{check, {Update: write}},
	})
	return analysisWriteError(err)
}

func termsLeaseCondition(runID, status string, lease int64) expression.ConditionBuilder {
	return analysisRunCondition(runID, status).
		And(expression.Name("leaseUntil").Equal(expression.Value(lease)))
}

// QueueTermsAnalysis claims a new run for a done meeting. With a prior row it
// must still have the observed run/status/lease, a pending prior must have an
// expired lease, and the new sourceHash must differ from the last succeeded
// run's. Prior acceptedTerms are kept.
func (r *DynamoDBRepository) QueueTermsAnalysis(ctx context.Context, ownerID, meetingID string, prior, next *model.TermsAnalysis) error {
	if next == nil || next.RunID == "" || next.SourceHash == "" {
		return fmt.Errorf("invalid terms analysis queue")
	}
	condition := expression.AttributeNotExists(expression.Name("PK"))
	if prior != nil {
		condition = termsLeaseCondition(prior.RunID, prior.Status, prior.LeaseUntil)
		if prior.Pending() {
			condition = condition.And(expression.Name("leaseUntil").LessThanEqual(expression.Value(next.UpdatedAt.UnixMilli())))
		}
	}
	condition = condition.And(expression.AttributeNotExists(expression.Name("succeededSourceHash")).
		Or(expression.Name("succeededSourceHash").NotEqual(expression.Value(next.SourceHash))))
	update := expression.Set(expression.Name("runId"), expression.Value(next.RunID)).
		Set(expression.Name("status"), expression.Value(model.AnalysisQueued)).
		Set(expression.Name("sourceHash"), expression.Value(next.SourceHash)).
		Set(expression.Name("summaryGeneration"), expression.Value(next.SummaryGeneration)).
		Set(expression.Name("startedAt"), expression.Value(next.StartedAt)).
		Set(expression.Name("updatedAt"), expression.Value(next.UpdatedAt)).
		Set(expression.Name("leaseUntil"), expression.Value(next.LeaseUntil)).
		Set(expression.Name("errorCode"), expression.Value("")).
		Set(expression.Name("counters"), expression.Value(model.TermsCounters{})).
		Set(expression.Name("entityType"), expression.Value(model.EntityTypeTermsAnalysis))
	return r.checkedTermsWrite(ctx, ownerID, meetingID, true, update, condition)
}

// StartTermsAnalysis moves a queued run with an unexpired lease to running
// while the meeting still exists and is done.
func (r *DynamoDBRepository) StartTermsAnalysis(ctx context.Context, ownerID, meetingID, runID string, now time.Time, leaseUntil int64) error {
	condition := analysisRunCondition(runID, model.AnalysisQueued).
		And(expression.Name("leaseUntil").GreaterThan(expression.Value(now.UnixMilli())))
	update := expression.Set(expression.Name("status"), expression.Value(model.AnalysisRunning)).
		Set(expression.Name("startedAt"), expression.Value(now)).
		Set(expression.Name("updatedAt"), expression.Value(now)).
		Set(expression.Name("leaseUntil"), expression.Value(leaseUntil))
	return r.checkedTermsWrite(ctx, ownerID, meetingID, true, update, condition)
}

// RenewTermsAnalysisLease extends the lease of a running run that still holds
// the observed, unexpired lease.
func (r *DynamoDBRepository) RenewTermsAnalysisLease(ctx context.Context, ownerID, meetingID, runID string, expectedLease int64, now time.Time, leaseUntil int64) error {
	condition := termsLeaseCondition(runID, model.AnalysisRunning, expectedLease).
		And(expression.Name("leaseUntil").GreaterThan(expression.Value(now.UnixMilli())))
	update := expression.Set(expression.Name("leaseUntil"), expression.Value(leaseUntil)).
		Set(expression.Name("updatedAt"), expression.Value(now))
	return r.checkedTermsWrite(ctx, ownerID, meetingID, true, update, condition)
}

// CompleteTermsAnalysis publishes success for the running run holding the
// observed, unexpired lease, records its source as the succeeded one, and
// stores the counters and accumulated acceptedTerms.
func (r *DynamoDBRepository) CompleteTermsAnalysis(ctx context.Context, ownerID, meetingID, runID string, lease int64, result TermsCompletion, now time.Time) error {
	if result.ErrorCode != "" && result.ErrorCode != model.TermsErrorTaintedSuggestOnly {
		return fmt.Errorf("invalid terms completion code")
	}
	if err := model.ValidateTermsAccepted(result.AcceptedTerms); err != nil {
		return err
	}
	condition := termsLeaseCondition(runID, model.AnalysisRunning, lease).
		And(expression.Name("leaseUntil").GreaterThan(expression.Value(now.UnixMilli())))
	update := expression.Set(expression.Name("status"), expression.Value(model.AnalysisSucceeded)).
		Set(expression.Name("succeededSourceHash"), expression.Name("sourceHash")).
		Set(expression.Name("errorCode"), expression.Value(result.ErrorCode)).
		Set(expression.Name("counters"), expression.Value(result.Counters)).
		Set(expression.Name("updatedAt"), expression.Value(now)).
		Set(expression.Name("leaseUntil"), expression.Value(int64(0)))
	update = setAcceptedTerms(update, result.AcceptedTerms)
	return r.checkedTermsWrite(ctx, ownerID, meetingID, false, update, condition)
}

// FailTermsAnalysis ends the observed run with an enum error code. A
// non-empty accepted list replaces acceptedTerms so observations written
// before the failure stay discoverable by deletion cleanup.
func (r *DynamoDBRepository) FailTermsAnalysis(ctx context.Context, meetingID string, expected *model.TermsAnalysis, code string, accepted []model.TermsAcceptedTerm, now time.Time) error {
	if expected == nil || !model.ValidTermsErrorCode(code) || code == model.TermsErrorTaintedSuggestOnly {
		return fmt.Errorf("invalid terms analysis failure")
	}
	if err := model.ValidateTermsAccepted(accepted); err != nil {
		return err
	}
	update := expression.Set(expression.Name("status"), expression.Value(model.AnalysisFailed)).
		Set(expression.Name("errorCode"), expression.Value(code)).
		Set(expression.Name("updatedAt"), expression.Value(now)).
		Set(expression.Name("leaseUntil"), expression.Value(int64(0)))
	update = setAcceptedTerms(update, accepted)
	op, err := analysisUpdate(r.tableName, termsAnalysisKey(meetingID), update,
		termsLeaseCondition(expected.RunID, expected.Status, expected.LeaseUntil))
	if err != nil {
		return err
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: op.TableName, Key: op.Key, UpdateExpression: op.UpdateExpression,
		ConditionExpression: op.ConditionExpression, ExpressionAttributeNames: op.ExpressionAttributeNames,
		ExpressionAttributeValues: op.ExpressionAttributeValues,
	})
	return analysisWriteError(err)
}

// setAcceptedTerms writes a non-empty list. An empty list never clears the
// stored one: the list only grows (callers pass the union with the prior
// list), so cleanup never loses a meeting's observations.
func setAcceptedTerms(update expression.UpdateBuilder, accepted []model.TermsAcceptedTerm) expression.UpdateBuilder {
	if len(accepted) == 0 {
		return update
	}
	return update.Set(expression.Name("acceptedTerms"), expression.Value(accepted))
}
