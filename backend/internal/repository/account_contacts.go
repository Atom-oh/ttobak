package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
)

const contactPrefix = "CONTACT#"

var ErrContactAccess = errors.New("account contact access revoked")

func contactKey(account, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: model.PrefixAccount + account}, "SK": &types.AttributeValueMemberS{Value: sk}}
}

func (r *DynamoDBRepository) GetAccountContact(ctx context.Context, account, id string) (*model.AccountContact, error) {
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(r.tableName), Key: contactKey(account, contactPrefix+id), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return nil, err
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	var contact model.AccountContact
	if err := attributevalue.UnmarshalMap(out.Item, &contact); err != nil {
		return nil, err
	}
	return &contact, nil
}

func (r *DynamoDBRepository) ListAccountContacts(ctx context.Context, account string, limit int, after string) ([]model.AccountContact, string, error) {
	key := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + account)).And(expression.Key("SK").BeginsWith(contactPrefix))
	expr, err := expression.NewBuilder().WithKeyCondition(key).Build()
	if err != nil {
		return nil, "", err
	}
	in := &dynamodb.QueryInput{TableName: aws.String(r.tableName), KeyConditionExpression: expr.KeyCondition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(), Limit: aws.Int32(int32(limit)), ConsistentRead: aws.Bool(true)}
	if after != "" {
		in.ExclusiveStartKey = contactKey(account, contactPrefix+after)
	}
	out, err := r.client.Query(ctx, in)
	if err != nil {
		return nil, "", fmt.Errorf("query contacts: %w", err)
	}
	contacts := []model.AccountContact{}
	if err = attributevalue.UnmarshalListOfMaps(out.Items, &contacts); err != nil {
		return nil, "", fmt.Errorf("decode contacts: %w", err)
	}
	next := ""
	if len(out.LastEvaluatedKey) > 0 {
		pk, pkOK := out.LastEvaluatedKey["PK"].(*types.AttributeValueMemberS)
		sk, skOK := out.LastEvaluatedKey["SK"].(*types.AttributeValueMemberS)
		if !pkOK || !skOK || pk.Value != model.PrefixAccount+account || !strings.HasPrefix(sk.Value, contactPrefix) {
			return nil, "", errors.New("invalid contact page key")
		}
		next = strings.TrimPrefix(sk.Value, contactPrefix)
	}
	return contacts, next, nil
}

// Query a bounded page from the canonical partition strongly consistently.
func (r *DynamoDBRepository) ListSpeakerTeamMembers(ctx context.Context, account string, limit int, after string) ([]model.AccountMember, string, error) {
	key := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + account)).And(expression.Key("SK").BeginsWith(model.PrefixMember))
	expr, err := expression.NewBuilder().WithKeyCondition(key).Build()
	if err != nil {
		return nil, "", err
	}
	in := &dynamodb.QueryInput{TableName: aws.String(r.tableName), KeyConditionExpression: expr.KeyCondition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(), ConsistentRead: aws.Bool(true), Limit: aws.Int32(int32(limit))}
	if after != "" {
		in.ExclusiveStartKey = contactKey(account, model.PrefixMember+after)
	}
	out, err := r.client.Query(ctx, in)
	if err != nil {
		return nil, "", err
	}
	members := []model.AccountMember{}
	if err = attributevalue.UnmarshalListOfMaps(out.Items, &members); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out.LastEvaluatedKey) > 0 {
		pk, pkOK := out.LastEvaluatedKey["PK"].(*types.AttributeValueMemberS)
		sk, skOK := out.LastEvaluatedKey["SK"].(*types.AttributeValueMemberS)
		if !pkOK || !skOK || pk.Value != model.PrefixAccount+account || !strings.HasPrefix(sk.Value, model.PrefixMember) {
			return nil, "", errors.New("invalid team page key")
		}
		next = strings.TrimPrefix(sk.Value, model.PrefixMember)
	}
	return members, next, nil
}

// Only complete condition failures are classified. Missing reasons, conflicts,
// and mixed failures remain internal errors rather than false 403/404s.
func classifyContactTransaction(err error) error {
	var cancelled *types.TransactionCanceledException
	if !errors.As(err, &cancelled) || len(cancelled.CancellationReasons) != 3 {
		return err
	}
	codes := make([]string, 3)
	failed := false
	for i, reason := range cancelled.CancellationReasons {
		codes[i] = aws.ToString(reason.Code)
		if codes[i] == "ConditionalCheckFailed" {
			failed = true
		} else if codes[i] != "None" {
			return err
		}
	}
	if !failed {
		return err
	}
	if codes[0] == "ConditionalCheckFailed" || codes[1] == "ConditionalCheckFailed" {
		return ErrContactAccess
	}
	if codes[2] == "ConditionalCheckFailed" {
		return ErrConditionFailed
	}
	return err
}

// Each mutation shares one transaction with both canonical access conditions.
// A concurrent revocation/deletion therefore prevents any contact write.
func (r *DynamoDBRepository) WriteAccountContact(ctx context.Context, account, user string, c *model.AccountContact, op string) error {
	exists, err := expression.NewBuilder().WithCondition(expression.AttributeExists(expression.Name("PK"))).Build()
	if err != nil {
		return err
	}
	absent, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return err
	}
	check := func(sk string) types.TransactWriteItem {
		return types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{TableName: aws.String(r.tableName), Key: contactKey(account, sk), ConditionExpression: exists.Condition(), ExpressionAttributeNames: exists.Names()}}
	}
	items := []types.TransactWriteItem{check(model.SKAccountMeta), check(model.PrefixMember + user)}
	nextRevision := ""
	if op == "delete" || op == "update" {
		condition := expression.AttributeExists(expression.Name("PK")).And(expression.Name("revision").Equal(expression.Value(c.Revision)))
		builder := expression.NewBuilder().WithCondition(condition)
		if op == "update" {
			nextRevision = uuid.NewString()
			update := expression.Set(expression.Name("name"), expression.Value(c.Name)).
				Set(expression.Name("company"), expression.Value(c.Company)).
				Set(expression.Name("title"), expression.Value(c.Title)).
				Set(expression.Name("department"), expression.Value(c.Department)).
				Set(expression.Name("email"), expression.Value(c.Email)).
				Set(expression.Name("address"), expression.Value(c.Address)).
				Set(expression.Name("workPhone"), expression.Value(c.WorkPhone)).
				Set(expression.Name("fax"), expression.Value(c.Fax)).
				Set(expression.Name("phone"), expression.Value(c.Phone)).
				Set(expression.Name("cardRegisteredAt"), expression.Value(c.CardRegisteredAt)).
				Set(expression.Name("cardBook"), expression.Value(c.CardBook)).
				Set(expression.Name("group"), expression.Value(c.Group)).
				Set(expression.Name("notes"), expression.Value(c.Notes)).
				Set(expression.Name("revision"), expression.Value(nextRevision))
			builder = builder.WithUpdate(update)
		}
		expr, err := builder.Build()
		if err != nil {
			return err
		}
		if op == "delete" {
			items = append(items, types.TransactWriteItem{Delete: &types.Delete{TableName: aws.String(r.tableName), Key: contactKey(account, contactPrefix+c.ContactID), ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()}})
		} else {
			items = append(items, types.TransactWriteItem{Update: &types.Update{TableName: aws.String(r.tableName), Key: contactKey(account, contactPrefix+c.ContactID), ConditionExpression: expr.Condition(), UpdateExpression: expr.Update(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()}})
		}
	} else if op == "create" {
		row := *c
		row.PK = model.PrefixAccount + account
		row.SK = contactPrefix + c.ContactID
		item, err := attributevalue.MarshalMap(row)
		if err != nil {
			return err
		}
		items = append(items, types.TransactWriteItem{Put: &types.Put{TableName: aws.String(r.tableName), Item: item, ConditionExpression: absent.Condition(), ExpressionAttributeNames: absent.Names()}})
	} else {
		return errors.New("invalid contact operation")
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if err != nil {
		return classifyContactTransaction(err)
	}
	if op == "update" {
		c.Revision = nextRevision
	}
	return nil
}
