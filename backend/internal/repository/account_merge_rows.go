package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// ErrMergeFenceLost means a merge writer's own fence no longer holds: the job
// is no longer running under this worker, the source is no longer marked as
// merging into the target, or the requester lost ownership of the source or
// membership of the target. The caller stops without advancing its cursor.
var ErrMergeFenceLost = errors.New("account merge fence lost")

// mergeFenceCheckCount is the number of ConditionChecks mergeFenceChecks adds;
// the target open check is last.
const mergeFenceCheckCount = 5

// mergeFenceChecks returns the operator-path fence every merge mover
// transaction carries. Unlike the open fence on ordinary account writers, it
// holds while the source is mergingInto the target and is conditioned on the
// job instead:
//  1. the job row is running and leased to this worker,
//  2. the source META is mergingInto the target and still owned by the requester,
//  3. the requester is still the Owner member of the source,
//  4. the requester is still a member of the target,
//  5. the target is open (neither merging nor merged); only this item reports
//     the stored row, so a merge marker on it surfaces ErrAccountMerging.
func (r *DynamoDBRepository) mergeFenceChecks(job *model.AccountMergeJob) ([]types.TransactWriteItem, error) {
	if job == nil || job.SourceAccountID == "" || job.TargetAccountID == "" || job.RequesterUserID == "" || job.LeaseOwner == "" ||
		job.PostMergeOperator != "" {
		return nil, fmt.Errorf("invalid merge job fence")
	}
	build := func(c expression.ConditionBuilder) (expression.Expression, error) {
		return expression.NewBuilder().WithCondition(c).Build()
	}
	check := func(key map[string]types.AttributeValue, c expression.ConditionBuilder) (types.TransactWriteItem, error) {
		expr, err := build(c)
		if err != nil {
			return types.TransactWriteItem{}, fmt.Errorf("build merge fence check: %w", err)
		}
		return types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{
			TableName: aws.String(r.tableName), Key: key, ConditionExpression: expr.Condition(),
			ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
		}}, nil
	}
	exists := expression.AttributeExists(expression.Name("PK"))
	jobCheck, err := check(accountHierarchyKey(job.SourceAccountID, model.SKAccountMergeJob),
		exists.And(expression.Name("state").Equal(expression.Value(model.MergeStateRunning))).
			And(expression.Name("leaseOwner").Equal(expression.Value(job.LeaseOwner))))
	if err != nil {
		return nil, err
	}
	sourceCheck, err := check(accountHierarchyKey(job.SourceAccountID, model.SKAccountMeta),
		exists.And(expression.Name("mergingInto").Equal(expression.Value(job.TargetAccountID))).
			And(expression.Name("ownerUserId").Equal(expression.Value(job.RequesterUserID))))
	if err != nil {
		return nil, err
	}
	ownerCheck, err := check(accountHierarchyKey(job.SourceAccountID, model.PrefixMember+job.RequesterUserID),
		exists.And(expression.Name("role").Equal(expression.Value(model.RoleOwner))))
	if err != nil {
		return nil, err
	}
	memberCheck, err := check(accountHierarchyKey(job.TargetAccountID, model.PrefixMember+job.RequesterUserID), exists)
	if err != nil {
		return nil, err
	}
	targetOpen, err := r.accountOpenCheck(job.TargetAccountID)
	if err != nil {
		return nil, err
	}
	return []types.TransactWriteItem{jobCheck, sourceCheck, ownerCheck, memberCheck, targetOpen}, nil
}

// postMergeFenceCheckCount is the number of ConditionChecks
// postMergeFenceChecks adds; the target check is last.
const postMergeFenceCheckCount = 4

// postMergeFenceChecks returns the fence the merge CLI's user-row movers
// carry after the job is done (ADR-072). There is no lease; instead:
//  1. the job row is done for this target (and, when the operator is the
//     job's requester, still names them),
//  2. the source META is the tombstone mergedInto the target (not merging),
//  3. the operator is still a direct member of the target,
//  4. the target is open and, when the operator is not the job's requester,
//     owned by the operator; only this item reports the stored row, so a
//     merge marker on it surfaces ErrAccountMerging.
//
// Each mover still conditions its row on accountId naming the source, and
// nothing writes into the source.
func (r *DynamoDBRepository) postMergeFenceChecks(job *model.AccountMergeJob) ([]types.TransactWriteItem, error) {
	operator := job.PostMergeOperator
	if !validMergeJob(job) || job.RequesterUserID == "" || operator == "" || strings.Contains(operator, "#") || job.State != model.MergeStateDone {
		return nil, fmt.Errorf("invalid post-merge fence")
	}
	check := func(key map[string]types.AttributeValue, c expression.ConditionBuilder) (types.TransactWriteItem, error) {
		expr, err := expression.NewBuilder().WithCondition(c).Build()
		if err != nil {
			return types.TransactWriteItem{}, fmt.Errorf("build post-merge fence check: %w", err)
		}
		return types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{
			TableName: aws.String(r.tableName), Key: key, ConditionExpression: expr.Condition(),
			ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
		}}, nil
	}
	exists := expression.AttributeExists(expression.Name("PK"))
	jobCond := exists.And(expression.Name("state").Equal(expression.Value(model.MergeStateDone))).
		And(expression.Name("targetAccountId").Equal(expression.Value(job.TargetAccountID)))
	targetCond := exists.And(accountOpenCondition())
	if operator == job.RequesterUserID {
		jobCond = jobCond.And(expression.Name("requesterUserId").Equal(expression.Value(operator)))
	} else {
		targetCond = targetCond.And(expression.Name("ownerUserId").Equal(expression.Value(operator)))
	}
	jobCheck, err := check(accountHierarchyKey(job.SourceAccountID, model.SKAccountMergeJob), jobCond)
	if err != nil {
		return nil, err
	}
	sourceCheck, err := check(accountHierarchyKey(job.SourceAccountID, model.SKAccountMeta),
		exists.And(expression.Name("mergedInto").Equal(expression.Value(job.TargetAccountID))).
			And(expression.AttributeNotExists(expression.Name("mergingInto"))))
	if err != nil {
		return nil, err
	}
	memberCheck, err := check(accountHierarchyKey(job.TargetAccountID, model.PrefixMember+operator), exists)
	if err != nil {
		return nil, err
	}
	targetCheck, err := check(accountHierarchyKey(job.TargetAccountID, model.SKAccountMeta), targetCond)
	if err != nil {
		return nil, err
	}
	targetCheck.ConditionCheck.ReturnValuesOnConditionCheckFailure = types.ReturnValuesOnConditionCheckFailureAllOld
	return []types.TransactWriteItem{jobCheck, sourceCheck, memberCheck, targetCheck}, nil
}

// userRowFenceChecks returns the fence for the CLI's user-row movers: the
// post-merge fence for a done job copy carrying PostMergeOperator, else the
// in-job lease fence.
func (r *DynamoDBRepository) userRowFenceChecks(job *model.AccountMergeJob) ([]types.TransactWriteItem, error) {
	if job != nil && job.PostMergeOperator != "" {
		return r.postMergeFenceChecks(job)
	}
	return r.mergeFenceChecks(job)
}

// classifyMergeFence maps a cancelled merge-mover transaction. The mover item
// sits at index 0 and mergeFenceChecks follow it. It reports whether the
// mover's own condition failed and any fence error; a fence failure wins over
// the mover's own condition.
func classifyMergeFence(err error) (moverFailed bool, fence error) {
	return classifyFence(err, mergeFenceCheckCount)
}

// classifyFence is classifyMergeFence for a fence of n checks whose last
// item reports the stored target row.
func classifyFence(err error, n int) (moverFailed bool, fence error) {
	var cancelled *types.TransactionCanceledException
	if !errors.As(err, &cancelled) {
		return false, nil
	}
	failed := func(i int) bool {
		return i < len(cancelled.CancellationReasons) && aws.ToString(cancelled.CancellationReasons[i].Code) == "ConditionalCheckFailed"
	}
	for i := 1; i <= n; i++ {
		if !failed(i) {
			continue
		}
		if i == n {
			if merging := mergeFenceError(err); merging != nil {
				return failed(0), merging
			}
		}
		return failed(0), fmt.Errorf("%w: %w", ErrMergeFenceLost, err)
	}
	return failed(0), nil
}

// PutMergedMember copies a source member into the target account under the
// merge fence. The row is create-only: when the user is already a target
// member the condition failure is treated as success and the existing role
// stays. A lost fence returns ErrMergeFenceLost; a target that started
// merging returns ErrAccountMerging.
func (r *DynamoDBRepository) PutMergedMember(ctx context.Context, job *model.AccountMergeJob, member *model.AccountMember) error {
	if job == nil || member == nil || member.UserID == "" || member.AccountID == "" || member.AccountID != job.TargetAccountID {
		return fmt.Errorf("invalid merged member")
	}
	member = stampSummaryMembership(member)
	member.PK, member.SK = model.PrefixAccount+member.AccountID, model.PrefixMember+member.UserID
	member.GSI1PK, member.GSI1SK = model.PrefixUser+member.UserID, model.PrefixAccount+member.AccountID
	member.EntityType = "ACCOUNT_MEMBER"
	item, err := attributevalue.MarshalMap(member)
	if err != nil {
		return fmt.Errorf("marshal merged member: %w", err)
	}
	expr, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return err
	}
	checks, err := r.mergeFenceChecks(job)
	if err != nil {
		return err
	}
	put := types.TransactWriteItem{Put: &types.Put{
		TableName: aws.String(r.tableName), Item: item,
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(),
	}}
	// The copied user must still be a source member when the grant commits, so
	// a stale enumeration can never grant a removed user target membership.
	source, err := r.noteCondition(model.PrefixAccount+job.SourceAccountID, model.PrefixMember+member.UserID, expression.AttributeExists(expression.Name("PK")))
	if err != nil {
		return err
	}
	sourceIndex := 1 + len(checks)
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: append(append([]types.TransactWriteItem{put}, checks...), source)}); err != nil {
		moverFailed, fence := classifyMergeFence(err)
		if fence != nil {
			return fence
		}
		if moverFailed {
			return nil
		}
		var cancelled *types.TransactionCanceledException
		if errors.As(err, &cancelled) && sourceIndex < len(cancelled.CancellationReasons) && aws.ToString(cancelled.CancellationReasons[sourceIndex].Code) == "ConditionalCheckFailed" {
			return fmt.Errorf("source member %s is gone: %w", member.UserID, ErrConditionFailed)
		}
		return fmt.Errorf("put merged member: %w", err)
	}
	return nil
}
