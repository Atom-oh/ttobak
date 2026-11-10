package repository

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

var (
	// ErrMergeJobExists means a start found a merge job that is not
	// restartable (running or done) for the source.
	ErrMergeJobExists = errors.New("account merge job already exists")
	// ErrMergeLeaseHeld means the job is absent, not running, or leased to
	// another worker whose lease has not expired.
	ErrMergeLeaseHeld = errors.New("account merge lease held elsewhere")
)

// mergeJobMaxCounts bounds the persisted per-step counters (nine steps times
// five outcomes fit).
const mergeJobMaxCounts = 64

var mergeErrorCodePattern = regexp.MustCompile(`^[A-Z][A-Z_]{0,63}$`)

func mergeJobKey(sourceID string) map[string]types.AttributeValue {
	return accountHierarchyKey(sourceID, model.SKAccountMergeJob)
}

// GetAccountMergeJob reads the source's merge job with a strongly consistent
// read. It returns nil when no job exists.
func (r *DynamoDBRepository) GetAccountMergeJob(ctx context.Context, sourceID string) (*model.AccountMergeJob, error) {
	if sourceID == "" || strings.Contains(sourceID, "#") {
		return nil, fmt.Errorf("invalid merge job source")
	}
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(r.tableName),
		Key: mergeJobKey(sourceID), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return nil, fmt.Errorf("get merge job: %w", err)
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	var job model.AccountMergeJob
	if err := attributevalue.UnmarshalMap(out.Item, &job); err != nil {
		return nil, fmt.Errorf("unmarshal merge job: %w", err)
	}
	return &job, nil
}

// StartAccountMerge creates (or replaces a failed) merge job and marks the
// source mergingInto the target in one transaction. It re-checks every start
// rule: the source is open, owned by the requester (META and MEMBER role) and
// not itself a merge target; the requester is a direct target member; the
// target is open and its observed ancestor chain is unchanged (the source is
// not in it, so the target is not inside the source subtree). The target's
// incomingMerges is incremented. intoAncestors starts at the target with its
// parent link and ends at the root; at most 64 links give 68 items.
func (r *DynamoDBRepository) StartAccountMerge(ctx context.Context, job *model.AccountMergeJob, intoAncestors []AccountParentLink) error {
	return r.startAccountMerge(ctx, job, intoAncestors, false)
}

// ResumeAccountMerge restarts a failed merge job from its saved step. The
// source must still be mergingInto the same target (a failed job keeps the
// fence), the stored job must be failed for that target, and every
// authorization rule of StartAccountMerge holds again. incomingMerges was
// counted by the original start and is not incremented again.
func (r *DynamoDBRepository) ResumeAccountMerge(ctx context.Context, job *model.AccountMergeJob, intoAncestors []AccountParentLink) error {
	return r.startAccountMerge(ctx, job, intoAncestors, true)
}

func validMergeAccountID(id string) bool { return id != "" && !strings.Contains(id, "#") }

func (r *DynamoDBRepository) startAccountMerge(ctx context.Context, job *model.AccountMergeJob, intoAncestors []AccountParentLink, resume bool) error {
	if job == nil || !validMergeAccountID(job.SourceAccountID) || !validMergeAccountID(job.TargetAccountID) ||
		job.SourceAccountID == job.TargetAccountID || job.RequesterUserID == "" || job.State != model.MergeStateRunning ||
		model.MergeStepIndex(job.Step) < 0 || job.LeaseOwner != "" || job.LeaseUntil != 0 || len(job.Counts) > mergeJobMaxCounts {
		return fmt.Errorf("invalid merge job start")
	}
	if !resume && (job.Step != model.MergeSteps[0] || job.Cursor != "") {
		return fmt.Errorf("a new merge job starts at the first step")
	}
	if len(intoAncestors) == 0 || intoAncestors[0].AccountID != job.TargetAccountID {
		return fmt.Errorf("invalid account hierarchy observations")
	}
	// The source acts as the "child": a chain that contains it is rejected.
	// The first two items are the target membership and META checks; the
	// META check becomes the target update below.
	parentChecks, err := r.hierarchyParentChecks(job.SourceAccountID, job.RequesterUserID, job.TargetAccountID, intoAncestors)
	if err != nil {
		return err
	}
	job.PK, job.SK, job.EntityType = model.PrefixAccount+job.SourceAccountID, model.SKAccountMergeJob, model.EntityTypeAccountMergeJob
	item, err := attributevalue.MarshalMap(job)
	if err != nil {
		return fmt.Errorf("marshal merge job: %w", err)
	}
	failed := expression.Name("state").Equal(expression.Value(model.MergeStateFailed))
	jobCond := expression.AttributeNotExists(expression.Name("PK")).Or(failed)
	if resume {
		jobCond = expression.AttributeExists(expression.Name("PK")).And(failed).
			And(expression.Name("targetAccountId").Equal(expression.Value(job.TargetAccountID)))
	}
	jobExpr, err := expression.NewBuilder().WithCondition(jobCond).Build()
	if err != nil {
		return fmt.Errorf("build merge job condition: %w", err)
	}
	exists := expression.AttributeExists(expression.Name("PK"))
	incoming := expression.Name("incomingMerges")
	sourceCond := exists.And(expression.Name("ownerUserId").Equal(expression.Value(job.RequesterUserID))).
		And(incoming.AttributeNotExists().Or(incoming.Equal(expression.Value(0))))
	if resume {
		sourceCond = sourceCond.And(expression.Name("mergingInto").Equal(expression.Value(job.TargetAccountID))).
			And(expression.AttributeNotExists(expression.Name("mergedInto")))
	} else {
		sourceCond = sourceCond.And(accountOpenCondition())
	}
	targetCond := exists.And(accountParentCondition(intoAncestors[0].ParentAccountID)).And(accountOpenCondition())
	now := time.Now().UTC()

	var source, target types.TransactWriteItem
	if resume {
		if source, err = r.hierarchyCheck(job.SourceAccountID, model.SKAccountMeta, sourceCond); err != nil {
			return err
		}
		if target, err = r.hierarchyCheck(job.TargetAccountID, model.SKAccountMeta, targetCond); err != nil {
			return err
		}
	} else {
		if source, err = r.metaUpdate(job.SourceAccountID, expression.Set(expression.Name("mergingInto"), expression.Value(job.TargetAccountID)).
			Set(expression.Name("updatedAt"), expression.Value(now)), sourceCond); err != nil {
			return err
		}
		if target, err = r.metaUpdate(job.TargetAccountID, expression.Add(incoming, expression.Value(1)), targetCond); err != nil {
			return err
		}
	}
	returnStoredRow(source)
	returnStoredRow(target)
	owner, err := r.hierarchyCheck(job.SourceAccountID, model.PrefixMember+job.RequesterUserID,
		exists.And(expression.Name("role").Equal(expression.Value(model.RoleOwner))))
	if err != nil {
		return err
	}
	items := []types.TransactWriteItem{{Put: &types.Put{
		TableName: aws.String(r.tableName), Item: item, ConditionExpression: jobExpr.Condition(),
		ExpressionAttributeNames: jobExpr.Names(), ExpressionAttributeValues: jobExpr.Values(),
	}}, source, target, owner, parentChecks[0]}
	items = append(items, parentChecks[2:]...)
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
		var cancelled *types.TransactionCanceledException
		if errors.As(err, &cancelled) {
			reasons := cancelled.CancellationReasons
			if len(reasons) > 0 && aws.ToString(reasons[0].Code) == "ConditionalCheckFailed" {
				return fmt.Errorf("start account merge: %w: %w", ErrMergeJobExists, err)
			}
			// A concurrent transaction on an item is a conflict like a
			// failed condition: the caller re-reads and retries.
			for _, reason := range reasons {
				if code := aws.ToString(reason.Code); code == "ConditionalCheckFailed" || code == "TransactionConflict" {
					return fmt.Errorf("start account merge: %w: %w", ErrConditionFailed, err)
				}
			}
		}
		return fmt.Errorf("start account merge: %w", err)
	}
	return nil
}

// metaUpdate is a conditioned Update on an account META row.
func (r *DynamoDBRepository) metaUpdate(accountID string, update expression.UpdateBuilder, cond expression.ConditionBuilder) (types.TransactWriteItem, error) {
	expr, err := expression.NewBuilder().WithUpdate(update).WithCondition(cond).Build()
	if err != nil {
		return types.TransactWriteItem{}, fmt.Errorf("build account META update: %w", err)
	}
	return types.TransactWriteItem{Update: &types.Update{
		TableName: aws.String(r.tableName), Key: accountHierarchyKey(accountID, model.SKAccountMeta),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}}, nil
}

// ClaimMergeLease leases the running job to owner until now+ttl (Unix
// seconds). It succeeds when the job has no lease, its lease has expired, or
// owner already holds it (renewal). A takeover writes the new owner, so the
// previous holder's lease-conditioned writes fail. Otherwise it returns
// ErrMergeLeaseHeld.
func (r *DynamoDBRepository) ClaimMergeLease(ctx context.Context, sourceID, owner string, now time.Time, ttl time.Duration) (*model.AccountMergeJob, error) {
	if !validMergeAccountID(sourceID) || owner == "" || ttl < time.Second {
		return nil, fmt.Errorf("invalid merge lease claim")
	}
	lease := expression.Name("leaseUntil")
	cond := expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("state").Equal(expression.Value(model.MergeStateRunning))).
		And(lease.AttributeNotExists().Or(lease.LessThan(expression.Value(now.Unix()))).
			Or(expression.Name("leaseOwner").Equal(expression.Value(owner))))
	update := expression.Set(expression.Name("leaseOwner"), expression.Value(owner)).
		Set(lease, expression.Value(now.Add(ttl).Unix())).
		Set(expression.Name("updatedAt"), expression.Value(now.UTC()))
	expr, err := expression.NewBuilder().WithUpdate(update).WithCondition(cond).Build()
	if err != nil {
		return nil, fmt.Errorf("build merge lease claim: %w", err)
	}
	out, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{TableName: aws.String(r.tableName), Key: mergeJobKey(sourceID),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(), ReturnValues: types.ReturnValueAllNew})
	if err != nil {
		var ccf *types.ConditionalCheckFailedException
		if errors.As(err, &ccf) {
			return nil, fmt.Errorf("%w: %w", ErrMergeLeaseHeld, err)
		}
		return nil, fmt.Errorf("claim merge lease: %w", err)
	}
	var job model.AccountMergeJob
	if err := attributevalue.UnmarshalMap(out.Attributes, &job); err != nil {
		return nil, fmt.Errorf("unmarshal merge job: %w", err)
	}
	return &job, nil
}

// leasedJobCondition holds while the job is running under job's lease owner.
func leasedJobCondition(job *model.AccountMergeJob) expression.ConditionBuilder {
	return expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("state").Equal(expression.Value(model.MergeStateRunning))).
		And(expression.Name("leaseOwner").Equal(expression.Value(job.LeaseOwner)))
}

func validLeasedJob(job *model.AccountMergeJob) bool {
	return job != nil && validMergeAccountID(job.SourceAccountID) && validMergeAccountID(job.TargetAccountID) &&
		job.RequesterUserID != "" && job.LeaseOwner != ""
}

// updateLeasedJob applies update to the job row while job's lease holds; a
// failed condition returns ErrMergeFenceLost.
func (r *DynamoDBRepository) updateLeasedJob(ctx context.Context, job *model.AccountMergeJob, update expression.UpdateBuilder, cond expression.ConditionBuilder, what string) error {
	expr, err := expression.NewBuilder().WithUpdate(update).WithCondition(cond).Build()
	if err != nil {
		return fmt.Errorf("build %s: %w", what, err)
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{TableName: aws.String(r.tableName), Key: mergeJobKey(job.SourceAccountID),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()})
	if err != nil {
		var ccf *types.ConditionalCheckFailedException
		if errors.As(err, &ccf) {
			return fmt.Errorf("%s: %w: %w", what, ErrMergeFenceLost, err)
		}
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

// RedispatchMergeJob marks a running job for a fresh worker event when no
// worker holds it: the lease is absent or expired at now. It writes only
// updatedAt. A live lease (or a job that is not running) returns
// ErrMergeLeaseHeld, so a crashed or timed-out worker's job can be resumed
// but a working one is never doubled.
func (r *DynamoDBRepository) RedispatchMergeJob(ctx context.Context, sourceID string, now time.Time) (*model.AccountMergeJob, error) {
	if !validMergeAccountID(sourceID) {
		return nil, fmt.Errorf("invalid merge redispatch")
	}
	lease := expression.Name("leaseUntil")
	cond := expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("state").Equal(expression.Value(model.MergeStateRunning))).
		And(lease.AttributeNotExists().Or(lease.LessThan(expression.Value(now.Unix()))))
	expr, err := expression.NewBuilder().WithUpdate(expression.Set(expression.Name("updatedAt"), expression.Value(now.UTC()))).
		WithCondition(cond).Build()
	if err != nil {
		return nil, fmt.Errorf("build merge redispatch: %w", err)
	}
	out, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{TableName: aws.String(r.tableName), Key: mergeJobKey(sourceID),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(), ReturnValues: types.ReturnValueAllNew})
	if err != nil {
		var ccf *types.ConditionalCheckFailedException
		if errors.As(err, &ccf) {
			return nil, fmt.Errorf("%w: %w", ErrMergeLeaseHeld, err)
		}
		return nil, fmt.Errorf("redispatch merge job: %w", err)
	}
	var job model.AccountMergeJob
	if err := attributevalue.UnmarshalMap(out.Attributes, &job); err != nil {
		return nil, fmt.Errorf("unmarshal merge job: %w", err)
	}
	return &job, nil
}

// ReleaseMergeLease gives up job's lease without changing its progress, so
// the next AccountMergeRequested delivery can claim it at once. A lease that
// already moved returns ErrMergeFenceLost.
func (r *DynamoDBRepository) ReleaseMergeLease(ctx context.Context, job *model.AccountMergeJob) error {
	if !validLeasedJob(job) {
		return fmt.Errorf("invalid merge lease release")
	}
	now := time.Now().UTC()
	update := expression.Set(expression.Name("updatedAt"), expression.Value(now)).
		Remove(expression.Name("leaseOwner")).Remove(expression.Name("leaseUntil"))
	if err := r.updateLeasedJob(ctx, job, update, leasedJobCondition(job), "release merge lease"); err != nil {
		return err
	}
	job.LeaseOwner, job.LeaseUntil, job.UpdatedAt = "", 0, now
	return nil
}

// SaveMergeProgress records the step, cursor and counts under the lease. The
// step may only stay or advance, and the cursor must be empty whenever the
// step changes, so a new step never inherits the previous step's cursor. The
// update also requires the stored step to equal job.Step, so a stale job
// copy cannot rewind progress. On success job is updated in place.
func (r *DynamoDBRepository) SaveMergeProgress(ctx context.Context, job *model.AccountMergeJob, step, cursor string, counts map[string]int) error {
	if !validLeasedJob(job) {
		return fmt.Errorf("invalid merge progress job")
	}
	from, to := model.MergeStepIndex(job.Step), model.MergeStepIndex(step)
	if from < 0 || to < from {
		return fmt.Errorf("invalid merge progress step")
	}
	if step != job.Step && cursor != "" {
		return fmt.Errorf("merge cursor must reset when the step changes")
	}
	if len(counts) > mergeJobMaxCounts {
		return fmt.Errorf("too many merge counts")
	}
	for k, v := range counts {
		if k == "" || len(k) > 64 || v < 0 {
			return fmt.Errorf("invalid merge count")
		}
	}
	now := time.Now().UTC()
	update := expression.Set(expression.Name("step"), expression.Value(step)).Set(expression.Name("updatedAt"), expression.Value(now))
	if cursor == "" {
		update = update.Remove(expression.Name("cursor"))
	} else {
		update = update.Set(expression.Name("cursor"), expression.Value(cursor))
	}
	if len(counts) == 0 {
		update = update.Remove(expression.Name("counts"))
	} else {
		update = update.Set(expression.Name("counts"), expression.Value(counts))
	}
	cond := leasedJobCondition(job).And(expression.Name("step").Equal(expression.Value(job.Step)))
	if err := r.updateLeasedJob(ctx, job, update, cond, "save merge progress"); err != nil {
		return err
	}
	saved := make(map[string]int, len(counts))
	for k, v := range counts {
		saved[k] = v
	}
	job.Step, job.Cursor, job.Counts, job.UpdatedAt = step, cursor, saved, now
	return nil
}

// FailMergeJob marks the leased job failed with code and releases the lease.
// The source keeps mergingInto (it stays fenced) and the target keeps its
// incomingMerges count until the owner restarts the merge, which resumes from
// the saved step; nothing is rolled back.
func (r *DynamoDBRepository) FailMergeJob(ctx context.Context, job *model.AccountMergeJob, code string) error {
	if !validLeasedJob(job) || !mergeErrorCodePattern.MatchString(code) {
		return fmt.Errorf("invalid merge job failure")
	}
	now := time.Now().UTC()
	update := expression.Set(expression.Name("state"), expression.Value(model.MergeStateFailed)).
		Set(expression.Name("errorCode"), expression.Value(code)).
		Set(expression.Name("updatedAt"), expression.Value(now)).
		Remove(expression.Name("leaseOwner")).Remove(expression.Name("leaseUntil"))
	if err := r.updateLeasedJob(ctx, job, update, leasedJobCondition(job), "fail merge job"); err != nil {
		return err
	}
	job.State, job.ErrorCode, job.LeaseOwner, job.LeaseUntil, job.UpdatedAt = model.MergeStateFailed, code, "", 0, now
	return nil
}

// FinishAccountMerge completes a job at its finish step in one transaction:
// the job becomes done and releases its lease, the source META replaces
// mergingInto with mergedInto and mergedAt (the tombstone), and the target's
// incomingMerges is decremented. The requester's source ownership and target
// membership are checked again. A lost lease or authority returns
// ErrMergeFenceLost; a target without a positive incomingMerges returns
// ErrConditionFailed.
func (r *DynamoDBRepository) FinishAccountMerge(ctx context.Context, job *model.AccountMergeJob) error {
	if !validLeasedJob(job) || job.Step != model.MergeSteps[len(model.MergeSteps)-1] {
		return fmt.Errorf("invalid merge job finish")
	}
	now := time.Now().UTC()
	jobUpdate := expression.Set(expression.Name("state"), expression.Value(model.MergeStateDone)).
		Set(expression.Name("finishedAt"), expression.Value(now)).
		Set(expression.Name("updatedAt"), expression.Value(now)).
		Remove(expression.Name("leaseOwner")).Remove(expression.Name("leaseUntil")).Remove(expression.Name("cursor"))
	jobExpr, err := expression.NewBuilder().WithUpdate(jobUpdate).
		WithCondition(leasedJobCondition(job).And(expression.Name("step").Equal(expression.Value(job.Step)))).Build()
	if err != nil {
		return fmt.Errorf("build merge job finish: %w", err)
	}
	exists := expression.AttributeExists(expression.Name("PK"))
	source, err := r.metaUpdate(job.SourceAccountID,
		expression.Remove(expression.Name("mergingInto")).
			Set(expression.Name("mergedInto"), expression.Value(job.TargetAccountID)).
			Set(expression.Name("mergedAt"), expression.Value(now)).
			Set(expression.Name("updatedAt"), expression.Value(now)),
		exists.And(expression.Name("mergingInto").Equal(expression.Value(job.TargetAccountID))).
			And(expression.Name("ownerUserId").Equal(expression.Value(job.RequesterUserID))))
	if err != nil {
		return err
	}
	owner, err := r.hierarchyCheck(job.SourceAccountID, model.PrefixMember+job.RequesterUserID,
		exists.And(expression.Name("role").Equal(expression.Value(model.RoleOwner))))
	if err != nil {
		return err
	}
	member, err := r.hierarchyCheck(job.TargetAccountID, model.PrefixMember+job.RequesterUserID, exists)
	if err != nil {
		return err
	}
	target, err := r.metaUpdate(job.TargetAccountID, expression.Add(expression.Name("incomingMerges"), expression.Value(-1)),
		exists.And(expression.Name("incomingMerges").GreaterThan(expression.Value(0))))
	if err != nil {
		return err
	}
	items := []types.TransactWriteItem{{Update: &types.Update{
		TableName: aws.String(r.tableName), Key: mergeJobKey(job.SourceAccountID),
		UpdateExpression: jobExpr.Update(), ConditionExpression: jobExpr.Condition(),
		ExpressionAttributeNames: jobExpr.Names(), ExpressionAttributeValues: jobExpr.Values(),
	}}, source, owner, member, target}
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
		var cancelled *types.TransactionCanceledException
		if errors.As(err, &cancelled) {
			for i, reason := range cancelled.CancellationReasons {
				if aws.ToString(reason.Code) != "ConditionalCheckFailed" {
					continue
				}
				if i == len(items)-1 {
					return fmt.Errorf("finish account merge: %w: %w", ErrConditionFailed, err)
				}
				return fmt.Errorf("finish account merge: %w: %w", ErrMergeFenceLost, err)
			}
			for _, reason := range cancelled.CancellationReasons {
				if aws.ToString(reason.Code) == "TransactionConflict" {
					return fmt.Errorf("finish account merge: %w: %w", ErrConditionFailed, err)
				}
			}
		}
		return fmt.Errorf("finish account merge: %w", err)
	}
	job.State, job.FinishedAt, job.UpdatedAt, job.LeaseOwner, job.LeaseUntil, job.Cursor = model.MergeStateDone, &now, now, "", 0, ""
	return nil
}
