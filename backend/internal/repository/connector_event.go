package repository

import (
	"context"
	"encoding/hex"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/connector"
	"github.com/ttobak/backend/internal/model"
)

type ConnectorEventWrite struct {
	Principal              connector.Principal
	Audience, Issuer       string
	PolicyGeneration       int64
	Account, ArgumentsHash string
	Event                  model.AccountEvent
	ReplayOnly             bool
}

type ConnectorEventReceipt struct {
	EventID  string `json:"eventId"`
	Replayed bool   `json:"replayed,omitempty"`
}

const connectorEventOperation = "api.accountEvents.create"

func validUUIDv4(v string) bool {
	id, err := uuid.Parse(v)
	return err == nil && id.Version() == 4 && id.String() == v
}

// CreateConnectorEvent mirrors CreateConnectorContact: creation and replay pin
// current policy, approval, account, membership and receipt in one
// transaction, and a replay never recreates a moved, edited or deleted event.
func (r *DynamoDBRepository) CreateConnectorEvent(ctx context.Context, in ConnectorEventWrite) (ConnectorEventReceipt, error) {
	empty := ConnectorEventReceipt{}
	p, e := in.Principal, in.Event
	hash, err := hex.DecodeString(in.ArgumentsHash)
	if err != nil || len(hash) != 32 || hex.EncodeToString(hash) != in.ArgumentsHash || p.Require(connectorEventOperation, "ttobak-api-bridge", time.Now().Unix()) != nil || p.ApprovalGeneration() <= 0 ||
		!validConnectorBinding(in.Audience, in.Issuer) || in.PolicyGeneration <= 0 || in.PolicyGeneration > maxConnectorInteger ||
		!validUUIDv4(e.EventID) || !validUUIDv4(e.Revision) || e.StartAt == "" || in.Account == "" {
		return empty, connector.ErrCapabilityDenied
	}
	policy := expression.Name("clientId").Equal(expression.Value(p.ClientID())).And(expression.Name("generation").Equal(expression.Value(in.PolicyGeneration))).And(expression.Name("audience").Equal(expression.Value(in.Audience))).And(expression.Name("issuer").Equal(expression.Value(in.Issuer))).And(expression.Name("enabled").Equal(expression.Value(true))).And(expression.Contains(expression.Name("maximum"), "read")).And(expression.Contains(expression.Name("maximum"), "write"))
	approval := expression.Name("userId").Equal(expression.Value(p.UserID())).And(expression.Name("clientId").Equal(expression.Value(p.ClientID()))).And(expression.Name("generation").Equal(expression.Value(p.ApprovalGeneration()))).And(expression.Name("revoked").Equal(expression.Value(false))).And(expression.Name("blocked").Equal(expression.Value(false))).And(expression.Name("expiresAt").GreaterThanEqual(expression.Value(p.ExpiresAt()))).And(expression.Contains(expression.Name("capabilities"), "read")).And(expression.Contains(expression.Name("capabilities"), "write"))
	userPK := model.PrefixUser + p.UserID()
	accountPK := model.PrefixAccount + in.Account
	pc, err := r.noteCondition("CONNECTOR_CLIENT#"+p.ClientID(), "POLICY", policy)
	if err != nil {
		return empty, err
	}
	ac, err := r.noteCondition(userPK, "CONNECTOR_APPROVAL#"+p.ClientID(), approval)
	if err != nil {
		return empty, err
	}
	accountCheck, err := r.noteCondition(accountPK, model.SKAccountMeta, expression.AttributeExists(expression.Name("PK")))
	if err != nil {
		return empty, err
	}
	memberCheck, err := r.noteCondition(accountPK, model.PrefixMember+p.UserID(), expression.AttributeExists(expression.Name("PK")))
	if err != nil {
		return empty, err
	}
	checks := []types.TransactWriteItem{pc, ac, accountCheck, memberCheck}
	receiptSK := "CONNECTOR_MUTATION#" + p.ClientID() + "#" + p.RequestID()
	once := func(o *dynamodb.Options) { o.RetryMaxAttempts = 1 }
	replay := func() (ConnectorEventReceipt, bool, error) {
		row, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(r.tableName), Key: connectorNoteKey(userPK, receiptSK), ConsistentRead: aws.Bool(true)})
		if err != nil || row == nil {
			return empty, false, connector.ErrPolicyUnavailable
		}
		if len(row.Item) == 0 {
			return empty, false, nil
		}
		created, createdErr := connectorInteger(row.Item, "createdAt")
		eventID, rev, eventSK := connectorText(row.Item, "eventId"), connectorText(row.Item, "revision"), connectorText(row.Item, "eventSK")
		if createdErr != nil || created <= 0 || connectorText(row.Item, "PK") != userPK || connectorText(row.Item, "SK") != receiptSK || connectorText(row.Item, "operation") != p.Operation() ||
			connectorText(row.Item, "argumentsHash") != in.ArgumentsHash || connectorText(row.Item, "accountId") != in.Account || !validUUIDv4(eventID) || !validUUIDv4(rev) ||
			!strings.HasPrefix(eventSK, eventPrefix) || !strings.HasSuffix(eventSK, "#"+eventID) {
			return empty, true, ErrConditionFailed
		}
		ec, err := r.noteCondition(accountPK, eventSK, expression.Name("eventId").Equal(expression.Value(eventID)).And(expression.Name("revision").Equal(expression.Value(rev))))
		if err != nil {
			return empty, true, err
		}
		rc, err := r.noteCondition(userPK, receiptSK, expression.Name("argumentsHash").Equal(expression.Value(in.ArgumentsHash)).And(expression.Name("accountId").Equal(expression.Value(in.Account))).And(expression.Name("eventId").Equal(expression.Value(eventID))).And(expression.Name("revision").Equal(expression.Value(rev))).And(expression.Name("eventSK").Equal(expression.Value(eventSK))).And(expression.Name("operation").Equal(expression.Value(p.Operation()))).And(expression.Name("createdAt").Equal(expression.Value(created))))
		if err != nil {
			return empty, true, err
		}
		if p.Require(connectorEventOperation, "ttobak-api-bridge", time.Now().Unix()) != nil || ctx.Err() != nil {
			return empty, true, connector.ErrCapabilityDenied
		}
		_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{ClientRequestToken: aws.String(uuid.NewString()), TransactItems: append(slices.Clone(checks), ec, rc)}, once)
		if err != nil {
			return empty, true, connectorWriteError(err)
		}
		return ConnectorEventReceipt{EventID: eventID, Replayed: true}, true, nil
	}
	if receipt, found, err := replay(); found || err != nil {
		return receipt, err
	}
	if in.ReplayOnly {
		missing, err := r.noteCondition(userPK, receiptSK, expression.AttributeNotExists(expression.Name("PK")))
		if err != nil {
			return empty, err
		}
		if p.Require(connectorEventOperation, "ttobak-api-bridge", time.Now().Unix()) != nil || ctx.Err() != nil {
			return empty, connector.ErrCapabilityDenied
		}
		if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{ClientRequestToken: aws.String(uuid.NewString()), TransactItems: append(slices.Clone(checks), missing)}, once); err != nil {
			return empty, connectorWriteError(err)
		}
		return empty, ErrConnectorReceiptMissing
	}
	eventSK := EventSortKey(e.StartAt, e.EventID)
	// The event row is sealed before any write; without a key nothing is written.
	item, err := r.sealedEventItem(ctx, in.Account, eventSK, &e)
	if err != nil {
		return empty, err
	}
	ref, err := attributevalue.MarshalMap(storedEventRef{PK: accountPK, SK: eventRefPrefix + e.EventID, EventID: e.EventID, EventSK: eventSK})
	if err != nil {
		return empty, err
	}
	now := time.Now().Unix()
	receipt, err := attributevalue.MarshalMap(map[string]any{"PK": userPK, "SK": receiptSK, "operation": p.Operation(), "argumentsHash": in.ArgumentsHash, "accountId": in.Account, "eventId": e.EventID, "eventSK": eventSK, "revision": e.Revision, "generation": p.ApprovalGeneration(), "createdAt": now})
	if err != nil {
		return empty, err
	}
	absent, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return empty, err
	}
	put := func(row map[string]types.AttributeValue) types.TransactWriteItem {
		return types.TransactWriteItem{Put: &types.Put{TableName: aws.String(r.tableName), Item: row, ConditionExpression: absent.Condition(), ExpressionAttributeNames: absent.Names()}}
	}
	if p.Require(connectorEventOperation, "ttobak-api-bridge", now) != nil || ctx.Err() != nil {
		return empty, connector.ErrCapabilityDenied
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{ClientRequestToken: aws.String(uuid.NewString()), TransactItems: append(slices.Clone(checks), put(item), put(ref), put(receipt))}, once)
	if err != nil {
		if result, found, readErr := replay(); found || readErr != nil {
			return result, readErr
		}
		return empty, connectorWriteError(err)
	}
	return ConnectorEventReceipt{EventID: e.EventID}, nil
}
