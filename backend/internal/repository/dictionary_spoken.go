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

// ListSpokenForms returns the user's cached generated spoken forms (ADR-008).
func (r *DictionaryRepository) ListSpokenForms(ctx context.Context, userID string) ([]model.SpokenForm, error) {
	var rows []model.SpokenForm
	err := r.queryPrefix(ctx, model.PrefixUser+userID, model.PrefixSpokenForm, func(item map[string]types.AttributeValue) error {
		var row model.SpokenForm
		if err := attributevalue.UnmarshalMap(item, &row); err != nil {
			return err
		}
		rows = append(rows, row)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list spoken forms: %w", err)
	}
	return rows, nil
}

// PutSpokenForms stores generated spoken forms. Each row is a derived,
// regenerable cache entry for one term, so a plain overwrite is safe: two
// writers of the same term store answers to the same question. Rows written
// before a failure stay.
func (r *DictionaryRepository) PutSpokenForms(ctx context.Context, rows []model.SpokenForm) error {
	for _, row := range rows {
		item, err := attributevalue.MarshalMap(row)
		if err != nil {
			return fmt.Errorf("failed to marshal spoken forms: %w", err)
		}
		if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(r.tableName), Item: item}); err != nil {
			return fmt.Errorf("failed to store spoken forms: %w", err)
		}
	}
	return nil
}
