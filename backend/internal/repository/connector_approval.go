package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

type ConnectorApprovalUpdate struct {
	UserID             string
	ClientID           string
	ExpectedGeneration int64
	Capabilities       []string
	ExpiresAt          int64
	Revoke             bool
	Block              bool
}

// UpdateConnectorApproval is user-partition isolated. Client maxima are never
// accepted from the caller; a transaction pins the current enabled policy,
// approval generation and audit receipt before any elevation/revocation succeeds.
func (r *DynamoDBRepository) UpdateConnectorApproval(ctx context.Context, in ConnectorApprovalUpdate) (int64, error) {
	now := time.Now().Unix()
	if !connectorUserID.MatchString(in.UserID) || !connectorClientID.MatchString(in.ClientID) || in.ExpectedGeneration < 0 || in.ExpectedGeneration >= maxConnectorInteger ||
		in.ExpiresAt <= now || in.ExpiresAt > now+86400 || len(in.Capabilities) == 0 ||
		!slices.Contains(in.Capabilities, "read") || !slices.IsSorted(in.Capabilities) {
		return 0, connector.ErrInvalidGrant
	}
	for i, cap := range in.Capabilities {
		if (cap != "read" && cap != "write" && cap != "share" && cap != "delete") ||
			(i > 0 && in.Capabilities[i-1] == cap) {
			return 0, connector.ErrInvalidGrant
		}
	}
	generation := in.ExpectedGeneration + 1
	policyCondition := expression.Name("clientId").Equal(expression.Value(in.ClientID))
	// Removing access must work after administrators disable or tighten a
	// client; it must not revive an old approval if they later re-enable it.
	if !in.Revoke && !in.Block {
		policyCondition = policyCondition.And(expression.Name("enabled").Equal(expression.Value(true)))
		for _, cap := range in.Capabilities {
			policyCondition = policyCondition.And(expression.Contains(expression.Name("maximum"), cap))
		}
	}
	policyExpr, err := expression.NewBuilder().WithCondition(policyCondition).Build()
	if err != nil {
		return 0, err
	}
	approvalCondition := expression.AttributeNotExists(expression.Name("PK"))
	if in.ExpectedGeneration != 0 {
		approvalCondition = expression.Name("userId").Equal(expression.Value(in.UserID)).
			And(expression.Name("clientId").Equal(expression.Value(in.ClientID))).
			And(expression.Name("generation").Equal(expression.Value(in.ExpectedGeneration)))
	}
	approvalExpr, err := expression.NewBuilder().WithCondition(approvalCondition).Build()
	if err != nil {
		return 0, err
	}
	row, err := attributevalue.MarshalMap(map[string]any{
		"PK": "USER#" + in.UserID, "SK": "CONNECTOR_APPROVAL#" + in.ClientID,
		"userId": in.UserID, "clientId": in.ClientID, "generation": generation,
		"expiresAt": in.ExpiresAt, "revoked": in.Revoke, "blocked": in.Block,
		"updatedAt": now, "pendingShareExpiresAt": in.ExpiresAt + 30*86400,
	})
	if err != nil {
		return 0, err
	}
	row["capabilities"] = &types.AttributeValueMemberSS{Value: slices.Clone(in.Capabilities)}
	if in.Block {
		delete(row, "pendingShareExpiresAt")
	} // Revocation tombstone must not silently restore baseline read.
	// Hash IDs in the receipt body; the user partition still authorizes access.
	actor := sha256.Sum256([]byte(in.UserID + "\x00" + in.ClientID))
	audit, err := attributevalue.MarshalMap(map[string]any{
		"PK": "USER#" + in.UserID, "SK": fmt.Sprintf("CONNECTOR_AUDIT#%020d#%s", now, uuid.NewString()),
		"actorHash": hex.EncodeToString(actor[:]), "generation": generation,
		"kind": "approval_updated", "revoked": in.Revoke, "blocked": in.Block, "createdAt": now,
		"pendingShareExpiresAt": now + 30*86400,
	})
	if err != nil {
		return 0, err
	}
	absent, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return 0, connector.ErrInvalidGrant
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{ClientRequestToken: aws.String(uuid.NewString()), TransactItems: []types.TransactWriteItem{
		{ConditionCheck: &types.ConditionCheck{TableName: aws.String(r.tableName),
			Key: map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: "CONNECTOR_CLIENT#" + in.ClientID},
				"SK": &types.AttributeValueMemberS{Value: "POLICY"}},
			ConditionExpression: policyExpr.Condition(), ExpressionAttributeNames: policyExpr.Names(), ExpressionAttributeValues: policyExpr.Values()}},
		{Put: &types.Put{TableName: aws.String(r.tableName), Item: row, ConditionExpression: approvalExpr.Condition(),
			ExpressionAttributeNames: approvalExpr.Names(), ExpressionAttributeValues: approvalExpr.Values()}},
		{Put: &types.Put{TableName: aws.String(r.tableName), Item: audit,
			ConditionExpression: absent.Condition(), ExpressionAttributeNames: absent.Names()}},
	}})
	if err != nil {
		return 0, connectorWriteError(err)
	}
	return generation, nil
}
