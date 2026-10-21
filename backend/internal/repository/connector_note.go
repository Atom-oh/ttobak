package repository

import (
	"context"
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
	"github.com/ttobak/backend/internal/model"
)

type ConnectorNoteWrite struct {
	Principal        connector.Principal
	Audience, Issuer string
	PolicyGeneration int64
	ArgumentsHash    string
	Document         model.AccountDocument
}

func connectorNoteKey(pk, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: pk}, "SK": &types.AttributeValueMemberS{Value: sk}}
}

func (r *DynamoDBRepository) noteCondition(pk, sk string, condition expression.ConditionBuilder) (types.TransactWriteItem, error) {
	expr, err := expression.NewBuilder().WithCondition(condition).Build()
	if err != nil {
		return types.TransactWriteItem{}, err
	}
	return types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{
		TableName: aws.String(r.tableName), Key: connectorNoteKey(pk, sk),
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}}, nil
}

// Both first creation and replay check current authorization atomically with
// the note/receipt identity. A replay never recreates a deleted document.
func (r *DynamoDBRepository) CreateConnectorNote(ctx context.Context, in ConnectorNoteWrite) (model.ConnectorNoteReceipt, error) {
	empty := model.ConnectorNoteReceipt{}
	p, doc := in.Principal, in.Document
	candidate, candidateErr := uuid.Parse(doc.DocID)
	hash, err := hex.DecodeString(in.ArgumentsHash)
	if err != nil || len(hash) != 32 || hex.EncodeToString(hash) != in.ArgumentsHash ||
		p.Require("api.document.putDocument", "ttobak-api-bridge", time.Now().Unix()) != nil ||
		p.ApprovalGeneration() <= 0 || !validConnectorBinding(in.Audience, in.Issuer) ||
		in.PolicyGeneration <= 0 || in.PolicyGeneration > maxConnectorInteger ||
		doc.PK != model.PrefixUser+p.UserID() || candidateErr != nil || candidate.Version() != 4 || candidate.String() != doc.DocID ||
		doc.SK != model.PrefixDoc+doc.DocID || doc.SourceUserID != p.UserID() || doc.AccountID != "" ||
		doc.DocType != "note" || doc.EntityType != model.EntityTypeUserDoc || doc.FileKey != "" || doc.PublicShareToken != "" {
		return empty, connector.ErrCapabilityDenied
	}
	policy := expression.Name("clientId").Equal(expression.Value(p.ClientID())).
		And(expression.Name("generation").Equal(expression.Value(in.PolicyGeneration))).
		And(expression.Name("audience").Equal(expression.Value(in.Audience))).
		And(expression.Name("issuer").Equal(expression.Value(in.Issuer))).
		And(expression.Name("enabled").Equal(expression.Value(true))).
		And(expression.Contains(expression.Name("maximum"), "read")).
		And(expression.Contains(expression.Name("maximum"), "write"))
	approval := expression.Name("userId").Equal(expression.Value(p.UserID())).
		And(expression.Name("clientId").Equal(expression.Value(p.ClientID()))).
		And(expression.Name("generation").Equal(expression.Value(p.ApprovalGeneration()))).
		And(expression.Name("revoked").Equal(expression.Value(false))).
		And(expression.Name("blocked").Equal(expression.Value(false))).
		And(expression.Name("expiresAt").GreaterThanEqual(expression.Value(p.ExpiresAt()))).
		And(expression.Contains(expression.Name("capabilities"), "read")).
		And(expression.Contains(expression.Name("capabilities"), "write"))
	policyCheck, err := r.noteCondition("CONNECTOR_CLIENT#"+p.ClientID(), "POLICY", policy)
	if err != nil {
		return empty, err
	}
	approvalCheck, err := r.noteCondition(doc.PK, "CONNECTOR_APPROVAL#"+p.ClientID(), approval)
	if err != nil {
		return empty, err
	}
	checks := []types.TransactWriteItem{policyCheck, approvalCheck}
	receiptSK := "CONNECTOR_MUTATION#" + p.ClientID() + "#" + p.RequestID()
	replay := func() (model.ConnectorNoteReceipt, bool, error) {
		row, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
			TableName: aws.String(r.tableName), Key: connectorNoteKey(doc.PK, receiptSK), ConsistentRead: aws.Bool(true),
		})
		if err != nil || row == nil {
			return empty, false, connector.ErrPolicyUnavailable
		}
		if len(row.Item) == 0 {
			return empty, false, nil
		}
		expiry, expiryErr := connectorInteger(row.Item, "pendingShareExpiresAt")
		receiptDocID := connectorText(row.Item, "docId")
		if expiryErr != nil || expiry <= time.Now().Unix() || connectorText(row.Item, "PK") != doc.PK ||
			connectorText(row.Item, "SK") != receiptSK || connectorText(row.Item, "operation") != p.Operation() ||
			connectorText(row.Item, "argumentsHash") != in.ArgumentsHash || !model.ValidConnectorNoteReceiptID(receiptDocID) {
			return empty, true, ErrConditionFailed
		}
		docCheck, err := r.noteCondition(doc.PK, model.PrefixDoc+receiptDocID,
			expression.Name("docId").Equal(expression.Value(receiptDocID)).
				And(expression.Name("sourceUserId").Equal(expression.Value(p.UserID()))).
				And(expression.Name("entityType").Equal(expression.Value(model.EntityTypeUserDoc))))
		if err != nil {
			return empty, true, err
		}
		receiptCheck, err := r.noteCondition(doc.PK, receiptSK,
			expression.Name("argumentsHash").Equal(expression.Value(in.ArgumentsHash)).
				And(expression.Name("docId").Equal(expression.Value(receiptDocID))).
				And(expression.Name("operation").Equal(expression.Value(p.Operation()))).
				And(expression.Name("pendingShareExpiresAt").Equal(expression.Value(expiry))))
		if err != nil {
			return empty, true, err
		}
		if p.Require("api.document.putDocument", "ttobak-api-bridge", time.Now().Unix()) != nil || ctx.Err() != nil {
			return empty, true, connector.ErrCapabilityDenied
		}
		_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
			ClientRequestToken: aws.String(uuid.NewString()),
			TransactItems:      append(slices.Clone(checks), docCheck, receiptCheck),
		}, func(options *dynamodb.Options) { options.RetryMaxAttempts = 1 })
		if err != nil {
			return empty, true, connectorWriteError(err)
		}
		return model.ConnectorNoteReceipt{DocID: receiptDocID, Replayed: true}, true, nil
	}
	if receipt, found, err := replay(); found || err != nil {
		return receipt, err
	}
	item, err := attributevalue.MarshalMap(doc)
	if err != nil {
		return empty, err
	}
	now := time.Now().Unix()
	receipt, err := attributevalue.MarshalMap(map[string]any{
		"PK": doc.PK, "SK": receiptSK, "operation": p.Operation(), "argumentsHash": in.ArgumentsHash,
		"docId": doc.DocID, "generation": p.ApprovalGeneration(), "createdAt": now,
		"pendingShareExpiresAt": now + 30*86400,
	})
	if err != nil {
		return empty, err
	}
	absent, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return empty, err
	}
	put := func(row map[string]types.AttributeValue) types.TransactWriteItem {
		return types.TransactWriteItem{Put: &types.Put{TableName: aws.String(r.tableName), Item: row,
			ConditionExpression: absent.Condition(), ExpressionAttributeNames: absent.Names()}}
	}
	if p.Require("api.document.putDocument", "ttobak-api-bridge", now) != nil || ctx.Err() != nil {
		return empty, connector.ErrCapabilityDenied
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		ClientRequestToken: aws.String(uuid.NewString()), TransactItems: append(slices.Clone(checks), put(item), put(receipt)),
	}, func(options *dynamodb.Options) { options.RetryMaxAttempts = 1 })
	if err != nil {
		// One dispatch only. An ambiguous response or a concurrent winner is
		// reconciled through its receipt; never retry the create blindly.
		if result, found, readErr := replay(); found || readErr != nil {
			return result, readErr
		}
		return empty, connectorWriteError(err)
	}
	return model.ConnectorNoteReceipt{DocID: doc.DocID}, nil
}
