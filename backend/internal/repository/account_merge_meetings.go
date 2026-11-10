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

// validMergeMeetingRef reports whether ref is the source account's reference
// to meetingID.
func validMergeMeetingRef(job *model.AccountMergeJob, meetingID string, ref *model.MeetingRef) bool {
	return ref != nil && ref.MeetingID == meetingID && ref.PK == model.PrefixAccount+job.SourceAccountID &&
		strings.HasPrefix(ref.SK, model.PrefixMeetingRef) && strings.HasSuffix(ref.SK, "#"+meetingID)
}

func validMergeJob(job *model.AccountMergeJob) bool {
	return job != nil && job.SourceAccountID != "" && job.TargetAccountID != "" && job.SourceAccountID != job.TargetAccountID
}

// MoveMeetingForAccountMerge re-points the owner's meeting from the source
// account to the target under the merge fence (ADR-072). The owner, the
// publication flag and the summary are unchanged: the update is conditioned
// on the exact summary privacy snapshot read here (which pins
// sharedToAccount and the approval as observed) and on accountId still
// naming the source, and it never writes sharedToAccount, so a link-only
// meeting stays link-only and an unpublished one never becomes visible. It
// does not apply the generic sharing-safe gate: the meeting keeps the
// audience decision it already had. authorizationRevision is incremented,
// and an approval bound to the source is rebound to the target with every
// other field unchanged; an approval bound elsewhere is left as is. The
// target's MEETINGREF# row (same sort key as ref) is written in the same
// transaction when the meeting is published; a link-only meeting gets none. The source ref is deleted separately by
// DeleteMeetingRefForMerge once the caller has finished the meeting's
// shares and insights, so a resumed run can still find it.
//
// A meeting no longer on the source, or a concurrent change to the
// snapshot, returns ErrConditionFailed. A lost fence returns
// ErrMergeFenceLost; a target that started merging returns ErrAccountMerging.
func (r *DynamoDBRepository) MoveMeetingForAccountMerge(ctx context.Context, job *model.AccountMergeJob, ownerID, meetingID string, ref *model.MeetingRef) error {
	if !validMergeJob(job) || ownerID == "" || meetingID == "" || strings.Contains(ownerID, "#") || strings.Contains(meetingID, "#") ||
		!validMergeMeetingRef(job, meetingID, ref) {
		return fmt.Errorf("invalid merge meeting move")
	}
	fence, err := r.mergeFenceChecks(job)
	if err != nil {
		return err
	}
	snapshot, err := r.CaptureSummaryPrivacy(ctx, ownerID, meetingID)
	if err != nil {
		return err
	}
	if snapshot == nil || snapshot.Meeting.AccountID != job.SourceAccountID {
		return fmt.Errorf("merge meeting: %w", ErrConditionFailed)
	}
	update := expression.Set(expression.Name("accountId"), expression.Value(job.TargetAccountID)).
		Add(expression.Name("authorizationRevision"), expression.Value(1))
	if approval := snapshot.Meeting.SummaryAccountApproval; approval != nil && approval.AccountID == job.SourceAccountID {
		update = update.Set(expression.Name("summaryAccountApproval.accountId"), expression.Value(job.TargetAccountID))
	}
	condition := summaryCondition(snapshot.Checks[0]).
		And(expression.Name("accountId").Equal(expression.Value(job.SourceAccountID)))
	meetingUpdate, err := analysisUpdate(r.tableName, summaryRowKey(snapshot.Checks[0].PK, snapshot.Checks[0].SK), update, condition)
	if err != nil {
		return fmt.Errorf("build merge meeting update: %w", err)
	}
	moved := *ref
	moved.PK, moved.AccountID, moved.OwnerUserID, moved.EntityType = model.PrefixAccount+job.TargetAccountID, job.TargetAccountID, ownerID, model.EntityTypeMeetingRef
	refItem, err := attributevalue.MarshalMap(&moved)
	if err != nil {
		return fmt.Errorf("marshal merge meeting ref: %w", err)
	}
	items := append([]types.TransactWriteItem{{Update: meetingUpdate}}, fence...)
	// A link-only meeting gets no target reference: it is not published to
	// the target, and a reference would only be a stale row to revalidate.
	if snapshot.Meeting.SharedToAccount {
		items = append(items, types.TransactWriteItem{Put: &types.Put{TableName: aws.String(r.tableName), Item: refItem}})
	}
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
		moverFailed, fenceErr := classifyMergeFence(err)
		if fenceErr != nil {
			return fenceErr
		}
		if moverFailed {
			return fmt.Errorf("merge meeting: %w: %w", ErrConditionFailed, err)
		}
		return fmt.Errorf("merge meeting: %w", err)
	}
	return nil
}

// DeleteMeetingRefForMerge deletes the source account's MEETINGREF# row for a
// meeting the merge has finished under the merge fence. Deleting an absent
// row succeeds.
func (r *DynamoDBRepository) DeleteMeetingRefForMerge(ctx context.Context, job *model.AccountMergeJob, ref *model.MeetingRef) error {
	if !validMergeJob(job) || ref == nil || !validMergeMeetingRef(job, ref.MeetingID, ref) || strings.Contains(ref.MeetingID, "#") {
		return fmt.Errorf("invalid merge meeting ref delete")
	}
	fence, err := r.mergeFenceChecks(job)
	if err != nil {
		return err
	}
	del := types.TransactWriteItem{Delete: &types.Delete{TableName: aws.String(r.tableName), Key: summaryRowKey(ref.PK, ref.SK)}}
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: append([]types.TransactWriteItem{del}, fence...)}); err != nil {
		if _, fenceErr := classifyMergeFence(err); fenceErr != nil {
			return fenceErr
		}
		return fmt.Errorf("delete merge meeting ref: %w", err)
	}
	return nil
}

// RetagAccountShareForMerge moves an account-origin share of meetingID to
// sharedToID from the source account to the target under the merge fence.
// Both copies (USER#{sharedToID}/SHARED#{meetingID} and
// MEETING#{meetingID}/SHARE_TO#{sharedToID}) are conditioned on
// origin=account and accountId naming the source and change together. A
// share that is direct, granted by another account, already retagged or
// gone fails that condition and is left untouched (nil). A lost fence
// returns ErrMergeFenceLost.
func (r *DynamoDBRepository) RetagAccountShareForMerge(ctx context.Context, job *model.AccountMergeJob, meetingID, sharedToID string) error {
	if !validMergeJob(job) || meetingID == "" || sharedToID == "" || strings.Contains(meetingID, "#") || strings.Contains(sharedToID, "#") {
		return fmt.Errorf("invalid merge share retag")
	}
	fence, err := r.mergeFenceChecks(job)
	if err != nil {
		return err
	}
	expr, err := expression.NewBuilder().
		WithUpdate(expression.Set(expression.Name("accountId"), expression.Value(job.TargetAccountID))).
		WithCondition(expression.Name("origin").Equal(expression.Value(model.ShareOriginAccount)).
			And(expression.Name("accountId").Equal(expression.Value(job.SourceAccountID)))).Build()
	if err != nil {
		return fmt.Errorf("build merge share retag: %w", err)
	}
	retag := func(pk, sk string) types.TransactWriteItem {
		return types.TransactWriteItem{Update: &types.Update{TableName: aws.String(r.tableName), Key: summaryRowKey(pk, sk),
			UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
			ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()}}
	}
	items := append([]types.TransactWriteItem{retag(model.PrefixUser+sharedToID, model.PrefixShare+meetingID)}, fence...)
	items = append(items, retag(model.PrefixMeeting+meetingID, model.PrefixShareTo+sharedToID))
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
		if _, fenceErr := classifyMergeFence(err); fenceErr != nil {
			return fenceErr
		}
		// Only the share rows' own conditions remain; a transient failure
		// on any item is still an error.
		if isConditionalCheckFailedTransaction(err) {
			return nil
		}
		return fmt.Errorf("retag merge share: %w", err)
	}
	return nil
}
