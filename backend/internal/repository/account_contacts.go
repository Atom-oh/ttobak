package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/sealing"
)

const contactPrefix = "CONTACT#"

var ErrContactAccess = errors.New("account contact access revoked")

// ErrContactEncryptionUnavailable means no record key is configured. Contact
// rows hold third-party personal data and are never stored or read in plaintext.
var ErrContactEncryptionUnavailable = errors.New("account contact encryption unavailable")

// ErrContactUnreadable means one stored contact row cannot be opened: it is
// malformed, unsealed, bound to another record or fails authentication. It
// is never returned for a missing key or a transport failure, so readers can
// skip the single row.
var ErrContactUnreadable = errors.New("account contact unreadable")

// openingError classifies a sealing.Open failure for one row: a missing key
// stays unavailable, an invalid sealed value is unreadable, anything else
// (a KMS transport failure) is returned as is.
func openingError(unreadable, err error) error {
	if errors.Is(err, sealing.ErrInvalid) {
		return fmt.Errorf("%w: %w", unreadable, err)
	}
	return sealingError(err)
}

// storedContact is the at-rest form: identity and revision stay plaintext for
// keys and conditions; every contact field is sealed together (ADR-021, contact encryption).
type storedContact struct {
	PK        string `dynamodbav:"PK"`
	SK        string `dynamodbav:"SK"`
	ContactID string `dynamodbav:"contactId"`
	Revision  string `dynamodbav:"revision"`
	Sealed    string `dynamodbav:"sealed"`
}

const contactReadConcurrency = 8

func contactBinding(account, id string) map[string]string {
	return map[string]string{"purpose": "ttobak-account-contact", "accountId": account, "contactId": id}
}

func (r *DynamoDBRepository) contactSealer() (sealing.Sealer, error) {
	if r.sealer == nil {
		return nil, ErrContactEncryptionUnavailable
	}
	return r.sealer, nil
}

func sealingError(err error) error {
	if errors.Is(err, sealing.ErrUnavailable) {
		return ErrContactEncryptionUnavailable
	}
	return err
}

// sealContact returns the sealed field payload for one contact row.
func (r *DynamoDBRepository) sealContact(ctx context.Context, account string, c *model.AccountContact) (string, error) {
	s, err := r.contactSealer()
	if err != nil {
		return "", err
	}
	fields := *c
	fields.PK, fields.SK = "", ""
	plain, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	sealed, err := s.Seal(ctx, plain, contactBinding(account, c.ContactID))
	return sealed, sealingError(err)
}

// SealedContactItem is the complete at-rest item for a new contact row.
func (r *DynamoDBRepository) SealedContactItem(ctx context.Context, account string, c *model.AccountContact) (map[string]types.AttributeValue, error) {
	sealed, err := r.sealContact(ctx, account, c)
	if err != nil {
		return nil, err
	}
	return attributevalue.MarshalMap(storedContact{
		PK: model.PrefixAccount + account, SK: contactPrefix + c.ContactID,
		ContactID: c.ContactID, Revision: c.Revision, Sealed: sealed,
	})
}

func (r *DynamoDBRepository) openContact(ctx context.Context, account string, item map[string]types.AttributeValue) (model.AccountContact, error) {
	var row storedContact
	if err := attributevalue.UnmarshalMap(item, &row); err != nil {
		return model.AccountContact{}, fmt.Errorf("%w: %w", ErrContactUnreadable, err)
	}
	s, err := r.contactSealer()
	if err != nil {
		return model.AccountContact{}, err
	}
	if row.Sealed == "" || row.PK != model.PrefixAccount+account || row.SK != contactPrefix+row.ContactID {
		return model.AccountContact{}, fmt.Errorf("%w: contact row is not sealed for this account", ErrContactUnreadable)
	}
	plain, err := s.Open(ctx, row.Sealed, contactBinding(account, row.ContactID))
	if err != nil {
		return model.AccountContact{}, fmt.Errorf("open contact: %w", openingError(ErrContactUnreadable, err))
	}
	var c model.AccountContact
	if err := json.Unmarshal(plain, &c); err != nil || c.ContactID != row.ContactID {
		return model.AccountContact{}, fmt.Errorf("%w: sealed contact does not match its row", ErrContactUnreadable)
	}
	// Revision changes on every update; the plaintext attribute is canonical.
	c.Revision = row.Revision
	return c, nil
}

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
	contact, err := r.openContact(ctx, account, out.Item)
	if err != nil {
		return nil, err
	}
	return &contact, nil
}

func (r *DynamoDBRepository) ListAccountContacts(ctx context.Context, account string, limit int, after string) ([]model.AccountContact, string, error) {
	items, next, err := r.queryContactPage(ctx, account, limit, after)
	if err != nil {
		return nil, "", err
	}
	contacts, errs := r.openContactPage(ctx, account, items)
	for _, err := range errs {
		if err != nil {
			return nil, "", fmt.Errorf("decode contacts: %w", err)
		}
	}
	return contacts, next, nil
}

// queryContactPage reads one bounded, strongly consistent page of an
// account's contact rows and returns the contact ID to continue after.
func (r *DynamoDBRepository) queryContactPage(ctx context.Context, account string, limit int, after string) ([]map[string]types.AttributeValue, string, error) {
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
	next := ""
	if len(out.LastEvaluatedKey) > 0 {
		pk, pkOK := out.LastEvaluatedKey["PK"].(*types.AttributeValueMemberS)
		sk, skOK := out.LastEvaluatedKey["SK"].(*types.AttributeValueMemberS)
		if !pkOK || !skOK || pk.Value != model.PrefixAccount+account || !strings.HasPrefix(sk.Value, contactPrefix) {
			return nil, "", errors.New("invalid contact page key")
		}
		next = strings.TrimPrefix(sk.Value, contactPrefix)
	}
	return out.Items, next, nil
}

// openContactPage opens each row with bounded concurrency; errs[i] belongs
// to items[i].
func (r *DynamoDBRepository) openContactPage(ctx context.Context, account string, items []map[string]types.AttributeValue) ([]model.AccountContact, []error) {
	contacts := make([]model.AccountContact, len(items))
	errs := make([]error, len(items))
	sem := make(chan struct{}, contactReadConcurrency)
	var wg sync.WaitGroup
	for i, item := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, item map[string]types.AttributeValue) {
			defer wg.Done()
			defer func() { <-sem }()
			contacts[i], errs[i] = r.openContact(ctx, account, item)
		}(i, item)
	}
	wg.Wait()
	return contacts, errs
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

// contactItemErrors names what a failed condition on each item of a plain
// contact write means: [account open, member, contact].
var contactItemErrors = []error{ErrContactAccess, ErrContactAccess, ErrConditionFailed}

// Only complete condition failures are classified. Missing reasons, conflicts,
// and mixed failures remain internal errors rather than false 403/404s.
func classifyContactTransaction(err error) error {
	return classifyContactItems(err, contactItemErrors)
}

// classifyContactItems maps a cancelled contact transaction by item index:
// kinds[i] is the error a failed condition on item i means, and the first
// failed item in order wins (access, then the contact row, then the SFDC
// rows). A nil kind is an item without a condition.
func classifyContactItems(err error, kinds []error) error {
	var cancelled *types.TransactionCanceledException
	if !errors.As(err, &cancelled) || len(cancelled.CancellationReasons) != len(kinds) {
		return err
	}
	var first error
	for i, reason := range cancelled.CancellationReasons {
		switch code := aws.ToString(reason.Code); code {
		case "None":
		case "ConditionalCheckFailed":
			if kinds[i] == nil {
				return err
			}
			if first == nil {
				first = kinds[i]
			}
		default:
			return err
		}
	}
	if first == nil {
		return err
	}
	if errors.Is(first, ErrSfdcAlreadyLinked) || errors.Is(first, ErrSfdcSuppressed) {
		return fmt.Errorf("%w: %w", first, err)
	}
	return first
}

// Each mutation shares one transaction with both canonical access conditions.
// A concurrent revocation/deletion therefore prevents any contact write.
//
// SFDC rows ride in the same transaction (at most 2 extra items):
//   - create/update of a linked contact reserves SFDCCONTACT#{id} for it
//     (create: absent; update: absent or already this contact's) and checks
//     that SFDCSUPPRESS#{id} is absent;
//   - delete removes every index row pointing at the contact and writes its
//     suppression row, so the sync never recreates it.
//
// Changing or clearing an existing link is the caller's responsibility: the
// service carries the stored SFDC fields over on generic updates.
func (r *DynamoDBRepository) WriteAccountContact(ctx context.Context, account, user string, c *model.AccountContact, op string) error {
	if op != "delete" {
		if err := validSfdcLink(c); err != nil {
			return err
		}
	}
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
	open, err := r.accountOpenCheck(account)
	if err != nil {
		return err
	}
	items := []types.TransactWriteItem{open, check(model.PrefixMember + user)}
	kinds := append([]error(nil), contactItemErrors...)
	nextRevision := ""
	if op == "delete" || op == "update" {
		condition := expression.AttributeExists(expression.Name("PK")).And(expression.Name("revision").Equal(expression.Value(c.Revision)))
		builder := expression.NewBuilder().WithCondition(condition)
		if op == "update" {
			nextRevision = uuid.NewString()
			sealed, err := r.sealContact(ctx, account, c)
			if err != nil {
				return err
			}
			update := expression.Set(expression.Name("sealed"), expression.Value(sealed)).
				Set(expression.Name("revision"), expression.Value(nextRevision))
			builder = builder.WithUpdate(update)
		}
		expr, err := builder.Build()
		if err != nil {
			return err
		}
		if op == "delete" {
			items = append(items, types.TransactWriteItem{Delete: &types.Delete{TableName: aws.String(r.tableName), Key: contactKey(account, contactPrefix+c.ContactID), ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()}})
			// The revision condition above pins the contact this index read
			// describes: linking it changes its revision.
			links, err := r.contactSfdcLinks(ctx, account, c.ContactID)
			if err != nil {
				return err
			}
			for _, id := range links {
				del, err := r.sfdcIndexDelete(account, id, c.ContactID)
				if err != nil {
					return err
				}
				put, err := r.sfdcSuppressPut(account, id)
				if err != nil {
					return err
				}
				items = append(items, del, put)
				kinds = append(kinds, ErrConditionFailed, nil)
			}
		} else {
			items = append(items, types.TransactWriteItem{Update: &types.Update{TableName: aws.String(r.tableName), Key: contactKey(account, contactPrefix+c.ContactID), ConditionExpression: expr.Condition(), UpdateExpression: expr.Update(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()}})
		}
	} else if op == "create" {
		item, err := r.SealedContactItem(ctx, account, c)
		if err != nil {
			return err
		}
		items = append(items, types.TransactWriteItem{Put: &types.Put{TableName: aws.String(r.tableName), Item: item, ConditionExpression: absent.Condition(), ExpressionAttributeNames: absent.Names()}})
	} else {
		return errors.New("invalid contact operation")
	}
	if op != "delete" && c.SfdcContactID != "" {
		reserve, err := r.sfdcReserve(account, c, op == "update")
		if err != nil {
			return err
		}
		items = append(items, reserve...)
		kinds = append(kinds, ErrSfdcAlreadyLinked, ErrSfdcSuppressed)
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if err != nil {
		if merging := mergeFenceError(err); merging != nil {
			return merging
		}
		return classifyContactItems(err, kinds)
	}
	if op == "update" {
		c.Revision = nextRevision
	}
	return nil
}
