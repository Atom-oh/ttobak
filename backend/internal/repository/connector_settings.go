package repository

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/connector"
	"github.com/ttobak/backend/internal/model"
)

// ConnectorSettings reads the caller's configuration even when access is
// disabled or blocked. This management view is never an authorization grant.
func (r *DynamoDBRepository) ConnectorSettings(ctx context.Context, user, client, audience, issuer string) (model.ConnectorSettings, error) {
	view := model.ConnectorSettings{ClientID: client, Maximum: []string{}}
	if !connectorUserID.MatchString(user) || !connectorClientID.MatchString(client) || !validConnectorBinding(audience, issuer) {
		return view, connector.ErrInvalidGrant
	}
	fields := []string{"PK", "SK", "clientId", "userId", "enabled", "maximum", "audience", "issuer", "generation", "capabilities", "expiresAt", "revoked", "blocked"}
	projection := expression.NamesList(expression.Name(fields[0]))
	for _, field := range fields[1:] {
		projection = projection.AddNames(expression.Name(field))
	}
	expr, err := expression.NewBuilder().WithProjection(projection).Build()
	if err != nil {
		return view, err
	}
	get := func(pk, sk string) types.TransactGetItem {
		return types.TransactGetItem{Get: &types.Get{
			TableName:            aws.String(r.tableName),
			Key:                  map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: pk}, "SK": &types.AttributeValueMemberS{Value: sk}},
			ProjectionExpression: expr.Projection(), ExpressionAttributeNames: expr.Names(),
		}}
	}
	result, err := r.client.TransactGetItems(ctx, &dynamodb.TransactGetItemsInput{TransactItems: []types.TransactGetItem{
		get("CONNECTOR_CLIENT#"+client, "POLICY"), get("USER#"+user, "CONNECTOR_APPROVAL#"+client),
	}})
	if err != nil || result == nil || len(result.Responses) != 2 {
		return view, connector.ErrPolicyUnavailable
	}
	policy, approval := result.Responses[0].Item, result.Responses[1].Item
	if len(policy) > 0 {
		enabled, e1 := connectorBool(policy, "enabled")
		maximum, e2 := connectorCaps(policy, "maximum")
		generation, e3 := connectorInteger(policy, "generation")
		if e1 != nil || e2 != nil || e3 != nil || connectorText(policy, "PK") != "CONNECTOR_CLIENT#"+client ||
			connectorText(policy, "SK") != "POLICY" || connectorText(policy, "clientId") != client ||
			connectorText(policy, "audience") != audience || connectorText(policy, "issuer") != issuer {
			return view, connector.ErrInvalidGrant
		}
		view.Enabled, view.Maximum, view.PolicyGeneration = enabled, maximum, generation
	}
	if len(approval) > 0 {
		generation, e1 := connectorInteger(approval, "generation")
		caps, e2 := connectorCaps(approval, "capabilities")
		expiry, e3 := connectorInteger(approval, "expiresAt")
		revoked, e4 := connectorBool(approval, "revoked")
		blocked, e5 := connectorBool(approval, "blocked")
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil ||
			connectorText(approval, "PK") != "USER#"+user || connectorText(approval, "SK") != "CONNECTOR_APPROVAL#"+client ||
			connectorText(approval, "userId") != user || connectorText(approval, "clientId") != client {
			return view, connector.ErrInvalidGrant
		}
		view.Approval = &model.ConnectorApprovalState{Generation: generation, Capabilities: caps, ExpiresAt: expiry, Revoked: revoked, Blocked: blocked}
	}
	return view, nil
}
