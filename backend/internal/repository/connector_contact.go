package repository

import (
	"context"
	"encoding/hex"
	"errors"
	"slices"
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

type ConnectorContactWrite struct {
	Principal              connector.Principal
	Audience, Issuer       string
	PolicyGeneration       int64
	Account, ArgumentsHash string
	Contact                model.AccountContact
	ReplayOnly             bool
}

var ErrConnectorReceiptMissing = errors.New("connector receipt missing")

type ConnectorContactReceipt struct {
	ContactID string `json:"contactId"`
	Replayed  bool   `json:"replayed,omitempty"`
}

// Creation and replay pin current policy, approval, account, membership and
// receipt in the same transaction. A replay never recreates an edited or deleted contact.
func (r *DynamoDBRepository) CreateConnectorContact(ctx context.Context, in ConnectorContactWrite) (ConnectorContactReceipt, error) {
	empty := ConnectorContactReceipt{}
	p, c := in.Principal, in.Contact
	hash, err := hex.DecodeString(in.ArgumentsHash)
	id, idErr := uuid.Parse(c.ContactID)
	revision, revErr := uuid.Parse(c.Revision)
	if err != nil || len(hash) != 32 || hex.EncodeToString(hash) != in.ArgumentsHash || p.Require("api.accountContacts.create", "ttobak-api-bridge", time.Now().Unix()) != nil || p.ApprovalGeneration() <= 0 || !validConnectorBinding(in.Audience, in.Issuer) || in.PolicyGeneration <= 0 || in.PolicyGeneration > maxConnectorInteger || idErr != nil || id.Version() != 4 || id.String() != c.ContactID || revErr != nil || revision.Version() != 4 || revision.String() != c.Revision || c.PK != "" || c.SK != "" || in.Account == "" {
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
	replay := func() (ConnectorContactReceipt, bool, error) {
		row, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(r.tableName), Key: connectorNoteKey(userPK, receiptSK), ConsistentRead: aws.Bool(true)})
		if err != nil || row == nil {
			return empty, false, connector.ErrPolicyUnavailable
		}
		if len(row.Item) == 0 {
			return empty, false, nil
		}
		created, createdErr := connectorInteger(row.Item, "createdAt")
		contactID := connectorText(row.Item, "contactId")
		rev := connectorText(row.Item, "revision")
		parsed, e1 := uuid.Parse(contactID)
		parsedRev, e2 := uuid.Parse(rev)
		if createdErr != nil || created <= 0 || connectorText(row.Item, "PK") != userPK || connectorText(row.Item, "SK") != receiptSK || connectorText(row.Item, "operation") != p.Operation() || connectorText(row.Item, "argumentsHash") != in.ArgumentsHash || connectorText(row.Item, "accountId") != in.Account || e1 != nil || parsed.Version() != 4 || parsed.String() != contactID || e2 != nil || parsedRev.Version() != 4 || parsedRev.String() != rev {
			return empty, true, ErrConditionFailed
		}
		cc, err := r.noteCondition(accountPK, "CONTACT#"+contactID, expression.Name("contactId").Equal(expression.Value(contactID)).And(expression.Name("revision").Equal(expression.Value(rev))))
		if err != nil {
			return empty, true, err
		}
		rc, err := r.noteCondition(userPK, receiptSK, expression.Name("argumentsHash").Equal(expression.Value(in.ArgumentsHash)).And(expression.Name("accountId").Equal(expression.Value(in.Account))).And(expression.Name("contactId").Equal(expression.Value(contactID))).And(expression.Name("revision").Equal(expression.Value(rev))).And(expression.Name("operation").Equal(expression.Value(p.Operation()))).And(expression.Name("createdAt").Equal(expression.Value(created))))
		if err != nil {
			return empty, true, err
		}
		if p.Require("api.accountContacts.create", "ttobak-api-bridge", time.Now().Unix()) != nil || ctx.Err() != nil {
			return empty, true, connector.ErrCapabilityDenied
		}
		_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{ClientRequestToken: aws.String(uuid.NewString()), TransactItems: append(slices.Clone(checks), cc, rc)}, func(o *dynamodb.Options) { o.RetryMaxAttempts = 1 })
		if err != nil {
			return empty, true, connectorWriteError(err)
		}
		return ConnectorContactReceipt{ContactID: contactID, Replayed: true}, true, nil
	}
	if receipt, found, err := replay(); found || err != nil {
		return receipt, err
	}
	if in.ReplayOnly {
		missing, err := r.noteCondition(userPK, receiptSK, expression.AttributeNotExists(expression.Name("PK")))
		if err != nil {
			return empty, err
		}
		if p.Require("api.accountContacts.create", "ttobak-api-bridge", time.Now().Unix()) != nil || ctx.Err() != nil {
			return empty, connector.ErrCapabilityDenied
		}
		_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
			ClientRequestToken: aws.String(uuid.NewString()), TransactItems: append(slices.Clone(checks), missing),
		}, func(o *dynamodb.Options) { o.RetryMaxAttempts = 1 })
		if err != nil {
			return empty, connectorWriteError(err)
		}
		return empty, ErrConnectorReceiptMissing
	}
	// The row is sealed before any write; without a key nothing is written.
	item, err := r.SealedContactItem(ctx, in.Account, &c)
	if err != nil {
		return empty, err
	}
	now := time.Now().Unix()
	receipt, err := attributevalue.MarshalMap(map[string]any{"PK": userPK, "SK": receiptSK, "operation": p.Operation(), "argumentsHash": in.ArgumentsHash, "accountId": in.Account, "contactId": c.ContactID, "revision": c.Revision, "generation": p.ApprovalGeneration(), "createdAt": now})
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
	if p.Require("api.accountContacts.create", "ttobak-api-bridge", now) != nil || ctx.Err() != nil {
		return empty, connector.ErrCapabilityDenied
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{ClientRequestToken: aws.String(uuid.NewString()), TransactItems: append(slices.Clone(checks), put(item), put(receipt))}, func(o *dynamodb.Options) { o.RetryMaxAttempts = 1 })
	if err != nil {
		if result, found, readErr := replay(); found || readErr != nil {
			return result, readErr
		}
		return empty, connectorWriteError(err)
	}
	return ConnectorContactReceipt{ContactID: c.ContactID}, nil
}
