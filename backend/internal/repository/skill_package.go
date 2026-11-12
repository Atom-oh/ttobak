package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// skillPackageVersionPageLimit bounds the org history query; only the newest
// SkillPackageOrgVersionsKept rows are kept, plus rows a failed prune left.
const skillPackageVersionPageLimit = 50

func skillPackageKey(pk, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: pk},
		"SK": &types.AttributeValueMemberS{Value: sk},
	}
}

// GetSkillPackage returns nil when the row is absent.
func (r *DynamoDBRepository) GetSkillPackage(ctx context.Context, pk, sk string) (*model.SkillPackage, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), Key: skillPackageKey(pk, sk), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("read skill package: %w", err)
	}
	if len(result.Item) == 0 {
		return nil, nil
	}
	var row model.SkillPackage
	if err := attributevalue.UnmarshalMap(result.Item, &row); err != nil || row.PK != pk || row.SK != sk {
		return nil, fmt.Errorf("decode skill package: corrupt row (%v)", err)
	}
	return &row, nil
}

// PutSkillPackage writes the row only when no row exists (previousSHA "") or
// the stored sha256 equals previousSHA.
func (r *DynamoDBRepository) PutSkillPackage(ctx context.Context, row model.SkillPackage, previousSHA string) error {
	item, err := attributevalue.MarshalMap(row)
	if err != nil {
		return fmt.Errorf("encode skill package: %w", err)
	}
	condition := expression.AttributeNotExists(expression.Name("PK"))
	if previousSHA != "" {
		condition = expression.Name("sha256").Equal(expression.Value(previousSHA))
	}
	built, err := expression.NewBuilder().WithCondition(condition).Build()
	if err != nil {
		return err
	}
	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName), Item: item, ConditionExpression: built.Condition(),
		ExpressionAttributeNames: built.Names(), ExpressionAttributeValues: built.Values(),
	})
	if err != nil {
		var rejected *types.ConditionalCheckFailedException
		if errors.As(err, &rejected) {
			return errors.Join(ErrConditionFailed, err)
		}
		return fmt.Errorf("put skill package: %w", err)
	}
	return nil
}

// ListSkillPackageVersions returns org history rows, newest first.
func (r *DynamoDBRepository) ListSkillPackageVersions(ctx context.Context) ([]model.SkillPackage, error) {
	built, err := expression.NewBuilder().WithKeyCondition(
		expression.Key("PK").Equal(expression.Value(model.SkillPackageOrgPK)).
			And(expression.Key("SK").BeginsWith(model.PrefixSkillPackageVersion))).Build()
	if err != nil {
		return nil, err
	}
	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName: aws.String(r.tableName), KeyConditionExpression: built.KeyCondition(),
		ExpressionAttributeNames: built.Names(), ExpressionAttributeValues: built.Values(),
		ScanIndexForward: aws.Bool(false), Limit: aws.Int32(skillPackageVersionPageLimit), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("list skill package versions: %w", err)
	}
	rows := []model.SkillPackage{}
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &rows); err != nil {
		return nil, fmt.Errorf("decode skill package versions: %w", err)
	}
	return rows, nil
}

// DeleteSkillPackage removes one row; a missing row is not an error.
func (r *DynamoDBRepository) DeleteSkillPackage(ctx context.Context, pk, sk string) error {
	if _, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(r.tableName), Key: skillPackageKey(pk, sk)}); err != nil {
		return fmt.Errorf("delete skill package: %w", err)
	}
	return nil
}
