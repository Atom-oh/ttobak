package repository

import (
	"context"
	"errors"
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
// deletion sweeps ANALYSIS#terms and the meeting's DICTOBS# rows after the
// meeting item is gone (SweepMeetingTerms, design A8).

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
	return r.meetingConditionCheck(ownerID, meetingID, condition)
}

// meetingConditionCheck is a ConditionCheck of condition on the owner's
// meeting item.
func (r *DynamoDBRepository) meetingConditionCheck(ownerID, meetingID string, condition expression.ConditionBuilder) (types.TransactWriteItem, error) {
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
	return r.termsWriteWithCheck(ctx, meetingID, check, update, condition)
}

// termsWriteWithCheck runs check and the run state update in one transaction.
func (r *DynamoDBRepository) termsWriteWithCheck(ctx context.Context, meetingID string, check types.TransactWriteItem, update expression.UpdateBuilder, condition expression.ConditionBuilder) error {
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
		Set(expression.Name("ownerId"), expression.Value(ownerID)).
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

// ReserveTermsAccepted renews the lease like RenewTermsAnalysisLease and
// stores accepted (the prior list plus the keys this run may observe) before
// any observation is written, so a meeting deleted mid-run still has every
// observation key listed for the deletion sweep. The list only grows.
func (r *DynamoDBRepository) ReserveTermsAccepted(ctx context.Context, ownerID, meetingID, runID string, expectedLease int64, now time.Time, leaseUntil int64, accepted []model.TermsAcceptedTerm) error {
	if err := model.ValidateTermsAccepted(accepted); err != nil {
		return err
	}
	condition := termsLeaseCondition(runID, model.AnalysisRunning, expectedLease).
		And(expression.Name("leaseUntil").GreaterThan(expression.Value(now.UnixMilli())))
	update := setAcceptedTerms(expression.Set(expression.Name("leaseUntil"), expression.Value(leaseUntil)).
		Set(expression.Name("updatedAt"), expression.Value(now)), accepted)
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

// RecordTermsAccount adds accountID to the run state's accountIds and
// termKeys (the keys the Account merge will observe) to accountAcceptedTerms in
// one update before the Account layer writes, so SweepMeetingTerms and
// SweepMeetingAccountTerms clean that partition for every key, including ones
// the personal merge skipped. It needs the observed run and the meeting item
// still linked to accountID (a deleted meeting's sweep may already have run,
// and a relinked meeting must not regain an Account it left), and it bumps
// accountRecords so a concurrent relink sweep keeps the entry.
func (r *DynamoDBRepository) RecordTermsAccount(ctx context.Context, ownerID, meetingID, runID, accountID string, termKeys []string) error {
	if runID == "" || accountID == "" {
		return fmt.Errorf("terms account record requires a run and an account")
	}
	if len(termKeys) > model.MaxTermsAccountAccepted {
		return fmt.Errorf("accountAcceptedTerms has %d entries, limit %d", len(termKeys), model.MaxTermsAccountAccepted)
	}
	seen := make(map[string]bool, len(termKeys))
	keys := make([]string, 0, len(termKeys))
	for _, key := range termKeys {
		if !model.ValidTermKey(key) {
			return fmt.Errorf("accountAcceptedTerms has an invalid key")
		}
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	if ownerID == "" || meetingID == "" {
		return fmt.Errorf("terms analysis write requires owner and meeting")
	}
	check, err := r.meetingConditionCheck(ownerID, meetingID, expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("accountId").Equal(expression.Value(accountID))))
	if err != nil {
		return err
	}
	update := expression.Add(expression.Name("accountIds"), expression.Value(&types.AttributeValueMemberSS{Value: []string{accountID}})).
		Add(expression.Name("accountRecords"), expression.Value(1))
	if len(keys) > 0 {
		update = update.Add(expression.Name("accountAcceptedTerms"), expression.Value(&types.AttributeValueMemberSS{Value: keys}))
	}
	return r.termsWriteWithCheck(ctx, meetingID, check, update, expression.Name("runId").Equal(expression.Value(runID)))
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

// SweepMeetingTerms runs after the meeting item is deleted (design A8): per
// key it deletes the meeting's DICTOBS# row and an unedited auto term left
// without observations (version-guarded), in the owner's personal scope
// (acceptedTerms keys) and in every recorded ACCOUNT# partition (accountIds,
// not the meeting's current link; acceptedTerms plus accountAcceptedTerms
// keys). These Account deletes are system cleanup of the
// meeting's own evidence, so they carry no membership check. The run state goes
// last, only when every step succeeded, so a retry still finds the keys. Only
// the owner recorded on the run state is swept; the meeting service re-runs
// this when the owner retries DELETE on an already-deleted meeting.
func (r *DynamoDBRepository) SweepMeetingTerms(ctx context.Context, ownerID, meetingID string) error {
	state, err := r.GetTermsAnalysis(ctx, meetingID)
	if err != nil || state == nil {
		return err
	}
	if state.OwnerID != ownerID {
		return nil // another owner's run state: a retried sweep never touches it
	}
	personalKeys := make([]string, 0, len(state.AcceptedTerms))
	for _, accepted := range state.AcceptedTerms {
		personalKeys = append(personalKeys, accepted.TermKey)
	}
	type sweepScope struct {
		pk   string
		keys []string
	}
	scopes := []sweepScope{{model.PrefixUser + ownerID, personalKeys}}
	accountKeys := state.AccountSweepKeys()
	for _, accountID := range state.AccountIDs {
		if accountID != "" {
			scopes = append(scopes, sweepScope{model.PrefixAccount + accountID, accountKeys})
		}
	}
	var errs []error
	for _, scope := range scopes {
		for _, key := range scope.keys {
			if !model.ValidTermKey(key) {
				continue
			}
			if _, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
				TableName: aws.String(r.tableName), Key: dictKey(scope.pk, model.DictObsSK(key, meetingID)),
			}); err != nil {
				errs = append(errs, fmt.Errorf("delete term observation: %w", err))
				continue
			}
			if err := r.deleteOrphanAutoTerm(ctx, scope.pk, key); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	if _, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(r.tableName), Key: termsAnalysisKey(meetingID)}); err != nil {
		return fmt.Errorf("delete terms analysis: %w", err)
	}
	return nil
}

// deleteOrphanAutoTerm reads the term version BEFORE counting the remaining
// observations. An observation write (observeAutoTerm) puts DICTOBS# and bumps
// the version in one transaction, so any write that lands after the read fails
// the version-guarded delete; one that lands before it is seen by the count.
func (r *DynamoDBRepository) deleteOrphanAutoTerm(ctx context.Context, pk, termKey string) error {
	term, err := r.readSweptTerm(ctx, pk, termKey)
	if err != nil || term == nil || !autoManagedTerm(term) {
		return err
	}
	observed, err := r.termHasObservations(ctx, pk, termKey)
	if err != nil || observed {
		return err
	}
	// A failed guard means the row was edited, merged or evicted concurrently.
	_, err = r.deleteOrphanAt(ctx, pk, termKey, term.Version)
	return err
}

// readSweptTerm reads a term row consistently; nil means absent.
func (r *DynamoDBRepository) readSweptTerm(ctx context.Context, pk, termKey string) (*model.DictTerm, error) {
	got, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(r.tableName), Key: dictKey(pk, model.DictTermSK(termKey)), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return nil, fmt.Errorf("read swept term: %w", err)
	}
	if len(got.Item) == 0 {
		return nil, nil
	}
	var term model.DictTerm
	if err := attributevalue.UnmarshalMap(got.Item, &term); err != nil {
		return nil, fmt.Errorf("decode swept term: %w", err)
	}
	return &term, nil
}

// autoManagedTerm reports an unedited automatic active or suggested term, the
// only kind of row a sweep deletes.
func autoManagedTerm(term *model.DictTerm) bool {
	return term.Origin == model.TermOriginAuto && term.UserEditedAt == "" &&
		(term.State == model.TermStateActive || term.State == model.TermStateSuggested)
}

// termHasObservations reports whether the term keeps any DICTOBS# row (any
// source kind).
func (r *DynamoDBRepository) termHasObservations(ctx context.Context, pk, termKey string) (bool, error) {
	keyExpr, err := expression.NewBuilder().WithKeyCondition(expression.Key("PK").Equal(expression.Value(pk)).
		And(expression.Key("SK").BeginsWith(model.PrefixDictObs + termKey + "#"))).Build()
	if err != nil {
		return false, err
	}
	remaining, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true), Limit: aws.Int32(1), Select: types.SelectCount,
		KeyConditionExpression: keyExpr.KeyCondition(), ExpressionAttributeNames: keyExpr.Names(), ExpressionAttributeValues: keyExpr.Values(),
	})
	if err != nil {
		return false, fmt.Errorf("count term observations: %w", err)
	}
	return remaining.Count > 0, nil
}

// deleteOrphanAt deletes an unedited auto term still at version. conflict
// reports a failed guard: the row changed after it was read.
func (r *DynamoDBRepository) deleteOrphanAt(ctx context.Context, pk, termKey string, version int64) (conflict bool, err error) {
	condition := expression.Name("version").Equal(expression.Value(version)).
		And(expression.Name("origin").Equal(expression.Value(model.TermOriginAuto))).
		And(expression.AttributeNotExists(expression.Name("userEditedAt"))).
		And(expression.Name("state").In(expression.Value(model.TermStateActive), expression.Value(model.TermStateSuggested)))
	expr, err := expression.NewBuilder().WithCondition(condition).Build()
	if err != nil {
		return false, err
	}
	_, err = r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName), Key: dictKey(pk, model.DictTermSK(termKey)),
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	})
	var failed *types.ConditionalCheckFailedException
	if errors.As(err, &failed) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("delete orphan auto term: %w", err)
	}
	return false, nil
}
