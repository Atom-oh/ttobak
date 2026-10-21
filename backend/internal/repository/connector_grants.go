package repository

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/connector"
)

const maxConnectorInteger int64 = 9007199254740991

var connectorUserID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var connectorClientID = regexp.MustCompile(`^[A-Za-z0-9]{1,128}$`)

func validConnectorCapabilities(caps []string) bool {
	if len(caps) < 1 || len(caps) > 4 || !slices.IsSorted(caps) || !slices.Contains(caps, "read") {
		return false
	}
	for i, c := range caps {
		if !slices.Contains([]string{"delete", "read", "share", "write"}, c) || (i > 0 && caps[i-1] == c) {
			return false
		}
	}
	return true
}
func validConnectorBinding(audience, issuer string) bool {
	a, ea := url.Parse(audience)
	i, ei := url.Parse(issuer)
	if ea != nil || ei != nil {
		return false
	}
	for _, u := range []*url.URL{a, i} {
		if u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
			return false
		}
	}
	return a.Path == "/api/mcp" && strings.HasPrefix(i.Host, "cognito-idp.") && strings.HasSuffix(i.Host, ".amazonaws.com") &&
		connectorUserID.MatchString(strings.TrimPrefix(i.Path, "/"))
}
func connectorText(row map[string]types.AttributeValue, key string) string {
	if value, ok := row[key].(*types.AttributeValueMemberS); ok {
		return value.Value
	}
	return ""
}
func connectorInteger(row map[string]types.AttributeValue, key string) (int64, error) {
	value, ok := row[key].(*types.AttributeValueMemberN)
	if !ok {
		return 0, connector.ErrInvalidGrant
	}
	result, err := strconv.ParseInt(value.Value, 10, 64)
	if err != nil || result <= 0 || result > maxConnectorInteger || strconv.FormatInt(result, 10) != value.Value {
		return 0, connector.ErrInvalidGrant
	}
	return result, nil
}
func connectorBool(row map[string]types.AttributeValue, key string) (bool, error) {
	if value, ok := row[key].(*types.AttributeValueMemberBOOL); ok {
		return value.Value, nil
	}
	return false, connector.ErrInvalidGrant
}
func connectorCaps(row map[string]types.AttributeValue, key string) ([]string, error) {
	raw, ok := row[key].(*types.AttributeValueMemberSS)
	if !ok {
		return nil, connector.ErrInvalidGrant
	}
	caps := slices.Clone(raw.Value)
	slices.Sort(caps)
	if !validConnectorCapabilities(caps) {
		return nil, connector.ErrInvalidGrant
	}
	return caps, nil
}

// ConnectorGrantResolver reads client policy and user approval in one atomic
// metadata-only snapshot. Caller-supplied or cached rows never grant access.
func (r *DynamoDBRepository) ConnectorGrantResolver(audience, issuer string) connector.GrantResolver {
	return func(ctx context.Context, userID, clientID string) (connector.Grants, error) {
		if !validConnectorBinding(audience, issuer) || !connectorUserID.MatchString(userID) || !connectorClientID.MatchString(clientID) {
			return connector.Grants{}, connector.ErrInvalidGrant
		}
		fields := []string{"PK", "SK", "userId", "clientId", "enabled", "maximum", "audience", "issuer", "generation", "capabilities", "expiresAt", "revoked", "blocked"}
		projection := expression.NamesList(expression.Name(fields[0]))
		for _, field := range fields[1:] {
			projection = projection.AddNames(expression.Name(field))
		}
		expr, err := expression.NewBuilder().WithProjection(projection).Build()
		if err != nil {
			return connector.Grants{}, connector.ErrPolicyUnavailable
		}
		get := func(pk, sk string) types.TransactGetItem {
			return types.TransactGetItem{Get: &types.Get{
				TableName: aws.String(r.tableName), Key: map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: pk}, "SK": &types.AttributeValueMemberS{Value: sk}},
				ProjectionExpression: expr.Projection(), ExpressionAttributeNames: expr.Names(),
			}}
		}
		result, err := r.client.TransactGetItems(ctx, &dynamodb.TransactGetItemsInput{TransactItems: []types.TransactGetItem{
			get("CONNECTOR_CLIENT#"+clientID, "POLICY"), get("USER#"+userID, "CONNECTOR_APPROVAL#"+clientID),
		}})
		if err != nil || result == nil || len(result.Responses) != 2 {
			return connector.Grants{}, connector.ErrPolicyUnavailable
		}
		policy, approval := result.Responses[0].Item, result.Responses[1].Item
		enabled, e1 := connectorBool(policy, "enabled")
		maximum, e2 := connectorCaps(policy, "maximum")
		_, e3 := connectorInteger(policy, "generation")
		if e1 != nil || e2 != nil || e3 != nil || !enabled || connectorText(policy, "PK") != "CONNECTOR_CLIENT#"+clientID || connectorText(policy, "SK") != "POLICY" || connectorText(policy, "clientId") != clientID ||
			connectorText(policy, "audience") != audience || connectorText(policy, "issuer") != issuer {
			return connector.Grants{}, connector.ErrInvalidGrant
		}
		grants := connector.Grants{UserID: userID, ClientID: clientID, ClientEnabled: true, ClientMaximum: maximum}
		if len(approval) > 0 {
			revoked, e1 := connectorBool(approval, "revoked")
			blocked, e2 := connectorBool(approval, "blocked")
			generation, e3 := connectorInteger(approval, "generation")
			expiry, e4 := connectorInteger(approval, "expiresAt")
			caps, e5 := connectorCaps(approval, "capabilities")
			if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || connectorText(approval, "PK") != "USER#"+userID || connectorText(approval, "SK") != "CONNECTOR_APPROVAL#"+clientID || connectorText(approval, "userId") != userID || connectorText(approval, "clientId") != clientID {
				return connector.Grants{}, connector.ErrInvalidGrant
			}
			grants.ClientEnabled = !blocked
			grants.Approval = &connector.Approval{UserID: userID, ClientID: clientID, Generation: generation, Capabilities: caps, ExpiresAt: expiry, Revoked: revoked}
		}
		return grants, nil
	}
}

func connectorWriteError(err error) error {
	if err == nil {
		return nil
	}
	var cancelled *types.TransactionCanceledException
	if errors.As(err, &cancelled) {
		for _, reason := range cancelled.CancellationReasons {
			if aws.ToString(reason.Code) == "ConditionalCheckFailed" {
				return ErrConditionFailed
			}
		}
	}
	// Transport failure can mean an unknown outcome; callers must inspect the
	// generation before submitting a fresh request. Never log SDK body/cause.
	return connector.ErrPolicyUnavailable
}
