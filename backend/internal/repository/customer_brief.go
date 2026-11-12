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

// ErrCustomerBriefUnreadable means a stored brief cannot be opened for its
// row; it never covers a missing key or a transport failure.
var ErrCustomerBriefUnreadable = errors.New("customer brief unreadable")

func customerBriefBinding(accountID, userID string) map[string]string {
	return map[string]string{"purpose": "ttobak-customer-brief", "accountId": accountID, "userId": userID}
}

func customerBriefKey(accountID, userID string) map[string]types.AttributeValue {
	return contactKey(accountID, model.CustomerBriefSK(userID))
}

// GetCustomerBrief reads the run row without opening the sealed brief.
func (r *DynamoDBRepository) GetCustomerBrief(ctx context.Context, accountID, userID string) (*model.CustomerBriefState, error) {
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(r.tableName), Key: customerBriefKey(accountID, userID), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return nil, fmt.Errorf("get customer brief: %w", err)
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	var state model.CustomerBriefState
	if err := attributevalue.UnmarshalMap(out.Item, &state); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCustomerBriefUnreadable, err)
	}
	if state.PK != model.PrefixAccount+accountID || state.SK != model.CustomerBriefSK(userID) || state.AccountID != accountID || state.UserID != userID {
		return nil, fmt.Errorf("%w: row identity mismatch", ErrCustomerBriefUnreadable)
	}
	return &state, nil
}

// OpenCustomerBrief opens the sealed brief bound to its account and member.
func (r *DynamoDBRepository) OpenCustomerBrief(ctx context.Context, state *model.CustomerBriefState) ([]byte, error) {
	if state == nil || state.Sealed == "" {
		return nil, ErrCustomerBriefUnreadable
	}
	if r.sealer == nil {
		return nil, ErrContactEncryptionUnavailable
	}
	plain, err := r.sealer.Open(ctx, state.Sealed, customerBriefBinding(state.AccountID, state.UserID))
	if err != nil {
		return nil, openingError(ErrCustomerBriefUnreadable, err)
	}
	return plain, nil
}

func customerBriefRunCondition(state *model.CustomerBriefState) expression.ConditionBuilder {
	return analysisRunCondition(state.RunID, state.Status).
		And(expression.Name("leaseUntil").Equal(expression.Value(state.LeaseUntil))).
		And(expression.Name("inputHash").Equal(expression.Value(state.InputHash)))
}

// customerBriefAccessChecks require the account META and the requester's
// direct MEMBER row to exist at commit time.
func (r *DynamoDBRepository) customerBriefAccessChecks(accountID, userID string) ([]types.TransactWriteItem, error) {
	var items []types.TransactWriteItem
	for _, sk := range []string{model.SKAccountMeta, model.PrefixMember + userID} {
		expr, err := expression.NewBuilder().WithCondition(expression.AttributeExists(expression.Name("PK"))).Build()
		if err != nil {
			return nil, err
		}
		items = append(items, types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{
			TableName: aws.String(r.tableName), Key: contactKey(accountID, sk),
			ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
		}})
	}
	return items, nil
}

func (r *DynamoDBRepository) customerBriefWrite(ctx context.Context, accountID, userID string, update expression.UpdateBuilder, condition expression.ConditionBuilder) error {
	op, err := analysisUpdate(r.tableName, customerBriefKey(accountID, userID), update, condition)
	if err != nil {
		return err
	}
	checks, err := r.customerBriefAccessChecks(accountID, userID)
	if err != nil {
		return err
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: append(checks, types.TransactWriteItem{Update: op})})
	return summaryWriteError(err)
}

func validCustomerBriefState(s *model.CustomerBriefState) bool {
	return s != nil && s.AccountID != "" && s.UserID != "" && s.RunID != ""
}

// QueueCustomerBrief claims a new run over prior (nil for an absent row).
// A pending prior is replaceable only after its lease or the row expired.
// clearContent drops a brief past its expiry so it can never be read again;
// otherwise a set next.BriefExpiresAt pins the retained brief's expiry.
func (r *DynamoDBRepository) QueueCustomerBrief(ctx context.Context, prior, next *model.CustomerBriefState, clearContent bool) error {
	if !validCustomerBriefState(next) || prior != nil && (prior.AccountID != next.AccountID || prior.UserID != next.UserID) {
		return ErrConditionFailed
	}
	now := next.UpdatedAt
	condition := expression.AttributeNotExists(expression.Name("PK"))
	if prior != nil {
		condition = customerBriefRunCondition(prior)
		if prior.Pending() {
			ttl := expression.Name(model.PendingShareTTLAttribute)
			condition = condition.And(expression.Name("leaseUntil").LessThanEqual(expression.Value(now.UnixMilli())).
				Or(expression.AttributeExists(ttl).And(ttl.LessThanEqual(expression.Value(now.Unix())))))
		}
	}
	update := expression.Set(expression.Name("accountId"), expression.Value(next.AccountID)).
		Set(expression.Name("userId"), expression.Value(next.UserID)).
		Set(expression.Name("runId"), expression.Value(next.RunID)).
		Set(expression.Name("status"), expression.Value(model.AnalysisQueued)).
		Set(expression.Name("requestedAt"), expression.Value(next.RequestedAt)).
		Set(expression.Name("leaseUntil"), expression.Value(next.LeaseUntil)).
		Set(expression.Name("inputHash"), expression.Value("")).
		Set(expression.Name("errorCode"), expression.Value("")).
		Set(expression.Name("updatedAt"), expression.Value(now)).
		Set(expression.Name(model.PendingShareTTLAttribute), expression.Value(next.ExpiresAt)).
		Set(expression.Name("entityType"), expression.Value(model.EntityTypeCustomerBrief))
	if clearContent {
		update = update.Remove(expression.Name("sealed")).Remove(expression.Name("generatedAt")).
			Remove(expression.Name("resultHash")).Remove(expression.Name("modelId")).Remove(expression.Name("briefExpiresAt"))
	} else if next.BriefExpiresAt != 0 {
		update = update.Set(expression.Name("briefExpiresAt"), expression.Value(next.BriefExpiresAt))
	}
	return r.customerBriefWrite(ctx, next.AccountID, next.UserID, update, condition)
}

// StartCustomerBrief moves a live queued run to running and pins inputHash.
func (r *DynamoDBRepository) StartCustomerBrief(ctx context.Context, state *model.CustomerBriefState, inputHash string, now time.Time, lease int64) error {
	if !validCustomerBriefState(state) || state.Status != model.AnalysisQueued || inputHash == "" {
		return ErrConditionFailed
	}
	condition := customerBriefRunCondition(state).And(expression.Name("leaseUntil").GreaterThan(expression.Value(now.UnixMilli())))
	update := expression.Set(expression.Name("status"), expression.Value(model.AnalysisRunning)).
		Set(expression.Name("leaseUntil"), expression.Value(lease)).
		Set(expression.Name("inputHash"), expression.Value(inputHash)).
		Set(expression.Name("updatedAt"), expression.Value(now))
	return r.customerBriefWrite(ctx, state.AccountID, state.UserID, update, condition)
}

// FailCustomerBrief records only the error state; the last good brief stays.
// It needs no membership check, so a revoked member's run can still end.
func (r *DynamoDBRepository) FailCustomerBrief(ctx context.Context, state *model.CustomerBriefState, code string, now time.Time) error {
	if !validCustomerBriefState(state) {
		return ErrConditionFailed
	}
	update := expression.Set(expression.Name("status"), expression.Value(model.AnalysisFailed)).
		Set(expression.Name("errorCode"), expression.Value(code)).
		Set(expression.Name("leaseUntil"), expression.Value(int64(0))).
		Set(expression.Name("updatedAt"), expression.Value(now))
	op, err := analysisUpdate(r.tableName, customerBriefKey(state.AccountID, state.UserID), update, customerBriefRunCondition(state))
	if err != nil {
		return err
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{TableName: op.TableName, Key: op.Key, UpdateExpression: op.UpdateExpression,
		ConditionExpression: op.ConditionExpression, ExpressionAttributeNames: op.ExpressionAttributeNames, ExpressionAttributeValues: op.ExpressionAttributeValues})
	return summaryWriteError(err)
}

// CompleteCustomerBrief seals content and publishes it with success in one
// transaction that also requires the account and the direct membership.
func (r *DynamoDBRepository) CompleteCustomerBrief(ctx context.Context, state *model.CustomerBriefState, content []byte, resultHash, modelID string, now time.Time) error {
	if !validCustomerBriefState(state) || state.Status != model.AnalysisRunning || len(content) == 0 || len(content) > model.CustomerBriefMaxBytes {
		return ErrConditionFailed
	}
	if r.sealer == nil {
		return ErrContactEncryptionUnavailable
	}
	sealed, err := r.sealer.Seal(ctx, content, customerBriefBinding(state.AccountID, state.UserID))
	if err != nil {
		return sealingError(err)
	}
	condition := customerBriefRunCondition(state).And(expression.Name("leaseUntil").GreaterThan(expression.Value(now.UnixMilli())))
	update := expression.Set(expression.Name("status"), expression.Value(model.AnalysisSucceeded)).
		Set(expression.Name("errorCode"), expression.Value("")).
		Set(expression.Name("leaseUntil"), expression.Value(int64(0))).
		Set(expression.Name("updatedAt"), expression.Value(now)).
		Set(expression.Name("sealed"), expression.Value(sealed)).
		Set(expression.Name("generatedAt"), expression.Value(now)).
		Set(expression.Name("resultHash"), expression.Value(resultHash)).
		Set(expression.Name("modelId"), expression.Value(modelID)).
		Set(expression.Name("briefExpiresAt"), expression.Value(now.Add(model.CustomerBriefTTL).Unix())).
		Set(expression.Name(model.PendingShareTTLAttribute), expression.Value(now.Add(model.CustomerBriefTTL).Unix()))
	return r.customerBriefWrite(ctx, state.AccountID, state.UserID, update, condition)
}
