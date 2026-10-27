package repository

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

func commonKey(sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: model.PrefixDictCommon},
		"SK": &types.AttributeValueMemberS{Value: sk},
	}
}

// getCommonRow reads one DICT#COMMON row consistently into out; false means
// it does not exist.
func (r *DynamoDBRepository) getCommonRow(ctx context.Context, sk string, out interface{}) (bool, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), Key: commonKey(sk), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return false, err
	}
	if len(result.Item) == 0 {
		return false, nil
	}
	if err := attributevalue.UnmarshalMap(result.Item, out); err != nil {
		return false, fmt.Errorf("decode %s: %w", sk, err)
	}
	return true, nil
}

func (r *DynamoDBRepository) putCommonRow(ctx context.Context, row interface{}) error {
	item, err := attributevalue.MarshalMap(row)
	if err != nil {
		return err
	}
	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(r.tableName), Item: item})
	return err
}

// GetTermsNewsHistory reads a term's news candidate history; nil means the
// term was never a news candidate (or its row expired).
func (r *DynamoDBRepository) GetTermsNewsHistory(ctx context.Context, termKey string) (*model.TermsNewsHistory, error) {
	var h model.TermsNewsHistory
	ok, err := r.getCommonRow(ctx, model.TermsNewsSK(termKey), &h)
	if err != nil || !ok {
		return nil, err
	}
	return &h, nil
}

// PutTermsNewsHistory overwrites a term's news candidate history. The daily
// job is the only writer and its day/document sets are idempotent, so a
// plain Put suffices.
func (r *DynamoDBRepository) PutTermsNewsHistory(ctx context.Context, h *model.TermsNewsHistory) error {
	if h == nil || h.TermKey == "" || len(h.Days) == 0 || h.ExpiresAt <= 0 {
		return fmt.Errorf("%w: news history needs a term, days and an expiry", ErrInvalidInput)
	}
	row := *h
	row.PK, row.SK, row.EntityType = model.PrefixDictCommon, model.TermsNewsSK(h.TermKey), model.EntityTypeTermsNews
	return r.putCommonRow(ctx, row)
}

// GetTermsNewsRun reads a day's news run row; nil means no run succeeded.
func (r *DynamoDBRepository) GetTermsNewsRun(ctx context.Context, day string) (*model.TermsNewsRun, error) {
	var run model.TermsNewsRun
	ok, err := r.getCommonRow(ctx, model.TermsNewsRunSK(day), &run)
	if err != nil || !ok {
		return nil, err
	}
	return &run, nil
}

// PutTermsNewsRun records a succeeded news day.
func (r *DynamoDBRepository) PutTermsNewsRun(ctx context.Context, run *model.TermsNewsRun) error {
	if run == nil || run.Day == "" || run.ContentHash == "" || run.ExpiresAt <= 0 {
		return fmt.Errorf("%w: news run needs a day, a hash and an expiry", ErrInvalidInput)
	}
	row := *run
	row.PK, row.SK, row.EntityType = model.PrefixDictCommon, model.TermsNewsRunSK(run.Day), model.EntityTypeTermsNewsRun
	return r.putCommonRow(ctx, row)
}
