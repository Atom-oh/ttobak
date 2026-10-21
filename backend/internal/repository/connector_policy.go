package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/connector"
)

func (r *DynamoDBRepository) SetConnectorReadPolicy(ctx context.Context, actor, client, audience, issuer string,
	enabled bool, expectedGeneration int64) (int64, error) {
	return r.SetConnectorPolicy(ctx, actor, client, audience, issuer, enabled, expectedGeneration, []string{"read"})
}

func (r *DynamoDBRepository) SetConnectorPolicy(ctx context.Context, actor, client, audience, issuer string,
	enabled bool, expectedGeneration int64, maximum []string) (int64, error) {
	if !connectorUserID.MatchString(actor) || !connectorClientID.MatchString(client) ||
		!validConnectorBinding(audience, issuer) || expectedGeneration < 0 || expectedGeneration >= maxConnectorInteger ||
		!(slices.Equal(maximum, []string{"read"}) || slices.Equal(maximum, []string{"read", "write"})) {
		return 0, connector.ErrInvalidGrant
	}
	condition := expression.AttributeNotExists(expression.Name("PK"))
	if expectedGeneration != 0 {
		condition = expression.Name("generation").Equal(expression.Value(expectedGeneration)).
			And(expression.Name("clientId").Equal(expression.Value(client)))
	}
	expr, err := expression.NewBuilder().WithCondition(condition).Build()
	if err != nil {
		return 0, err
	}
	generation, now := expectedGeneration+1, time.Now().Unix()
	row, err := attributevalue.MarshalMap(map[string]any{
		"PK": "CONNECTOR_CLIENT#" + client, "SK": "POLICY", "clientId": client,
		"enabled": enabled, "audience": audience, "issuer": issuer, "generation": generation, "updatedAt": now,
	})
	if err != nil {
		return 0, err
	}
	row["maximum"] = &types.AttributeValueMemberSS{Value: slices.Clone(maximum)}
	hash := sha256.Sum256([]byte(actor + "\x00" + client))
	audit, err := attributevalue.MarshalMap(map[string]any{
		"PK": "USER#" + actor, "SK": "CONNECTOR_AUDIT#" + uuid.NewString(),
		"actorHash": hex.EncodeToString(hash[:]), "kind": "client_policy_updated", "generation": generation,
		"enabled": enabled, "createdAt": now, "pendingShareExpiresAt": now + 30*86400,
	})
	if err != nil {
		return 0, err
	}
	absent, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return 0, connector.ErrInvalidGrant
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{ClientRequestToken: aws.String(uuid.NewString()), TransactItems: []types.TransactWriteItem{
		{Put: &types.Put{TableName: aws.String(r.tableName), Item: row, ConditionExpression: expr.Condition(),
			ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()}},
		{Put: &types.Put{TableName: aws.String(r.tableName), Item: audit, ConditionExpression: absent.Condition(), ExpressionAttributeNames: absent.Names()}},
	}})
	if err != nil {
		return 0, connectorWriteError(err)
	}
	return generation, nil
}
