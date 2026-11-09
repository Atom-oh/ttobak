package repository

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// Account link write failures. Each also matches ErrConditionFailed.
var (
	// ErrAccountLinkExists means the LINK# row already exists.
	ErrAccountLinkExists = errors.New("account link exists")
	// ErrAccountLinkTarget means the project is no longer linked to the
	// account or the meeting is no longer published to it.
	ErrAccountLinkTarget = errors.New("account link target is not in the account")
	// ErrAccountLinkDenied means a removed auto link blocks re-creation.
	ErrAccountLinkDenied = errors.New("account link was removed")
)

type accountLinkItem struct {
	PK         string `dynamodbav:"PK"`
	SK         string `dynamodbav:"SK"`
	EntityType string `dynamodbav:"entityType"`
	model.AccountLink
}

type accountLinkDenyItem struct {
	PK         string `dynamodbav:"PK"`
	SK         string `dynamodbav:"SK"`
	EntityType string `dynamodbav:"entityType"`
	AccountID  string `dynamodbav:"accountId"`
	FromKind   string `dynamodbav:"fromKind"`
	FromID     string `dynamodbav:"fromId"`
	ToKind     string `dynamodbav:"toKind"`
	ToID       string `dynamodbav:"toId"`
}

// splitLinkMeetingID splits a meeting link ID {ownerUserId}:{meetingId}.
func splitLinkMeetingID(id string) (string, string, bool) {
	owner, meeting, ok := strings.Cut(id, ":")
	return owner, meeting, ok && owner != "" && meeting != "" && !strings.Contains(meeting, ":")
}

// PutAccountLink writes the forward and reverse rows in one transaction:
//
//	0 Put LINK#   attribute_not_exists(PK)
//	1 Put LINKTO#
//	  ConditionCheck PROJECT#{id}/CONFIG contains(accountIds, :a)   (toKind project)
//	  ConditionCheck USER#{owner}/MEETING#{id} published to :a      (toKind meeting)
//	  ConditionCheck LINKDENY# attribute_not_exists(PK)             (origin auto)
//
// Failures map to ErrAccountLinkDenied, ErrAccountLinkTarget or
// ErrAccountLinkExists (in that precedence), each wrapping ErrConditionFailed.
func (r *DynamoDBRepository) PutAccountLink(ctx context.Context, l *model.AccountLink) error {
	if l == nil || l.AccountID == "" || model.ValidateAccountLink(l.Input()) != nil ||
		(l.Origin != model.LinkOriginManual && l.Origin != model.LinkOriginAuto) || l.CreatedAt == "" {
		return fmt.Errorf("invalid account link")
	}
	in := l.Input()
	pk := model.PrefixAccount + l.AccountID
	forward, err := attributevalue.MarshalMap(accountLinkItem{PK: pk, SK: model.AccountLinkSortKey(in), EntityType: "ACCOUNT_LINK", AccountLink: *l})
	if err != nil {
		return fmt.Errorf("marshal account link: %w", err)
	}
	reverse, err := attributevalue.MarshalMap(accountLinkItem{PK: pk, SK: model.AccountLinkToSortKey(in), EntityType: "ACCOUNT_LINK_TO", AccountLink: *l})
	if err != nil {
		return fmt.Errorf("marshal account link reverse row: %w", err)
	}
	absent, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return err
	}
	put := &types.Put{TableName: aws.String(r.tableName), Item: forward}
	put.ConditionExpression, put.ExpressionAttributeNames = absent.Condition(), absent.Names()
	items := []types.TransactWriteItem{{Put: put}, {Put: &types.Put{TableName: aws.String(r.tableName), Item: reverse}}}
	roles := []string{"exists", "reverse"}

	check := func(key map[string]types.AttributeValue, cond expression.ConditionBuilder) error {
		expr, err := expression.NewBuilder().WithCondition(cond).Build()
		if err != nil {
			return err
		}
		cc := &types.ConditionCheck{TableName: aws.String(r.tableName), Key: key, ConditionExpression: expr.Condition(),
			ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()}
		items = append(items, types.TransactWriteItem{ConditionCheck: cc})
		return nil
	}
	switch l.ToKind {
	case model.LinkKindProject:
		cond := expression.Contains(expression.Name("accountIds"), l.AccountID)
		if err := check(accountNewsKey(model.PrefixProject+l.ToID, model.SKProjectConfig), cond); err != nil {
			return err
		}
		roles = append(roles, "target")
	case model.LinkKindMeeting:
		owner, meeting, ok := splitLinkMeetingID(l.ToID)
		if !ok {
			return fmt.Errorf("invalid account link meeting id")
		}
		cond := expression.Name("accountId").Equal(expression.Value(l.AccountID)).
			And(expression.Name("sharedToAccount").Equal(expression.Value(true))).
			And(expression.Name("entityType").Equal(expression.Value("MEETING")))
		if err := check(accountNewsKey(model.PrefixUser+owner, model.PrefixMeeting+meeting), cond); err != nil {
			return err
		}
		roles = append(roles, "target")
	}
	if l.Origin == model.LinkOriginAuto {
		if err := check(accountNewsKey(pk, model.AccountLinkDenySortKey(in)), expression.AttributeNotExists(expression.Name("PK"))); err != nil {
			return err
		}
		roles = append(roles, "deny")
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if err != nil {
		return classifyAccountLinkPut(err, roles)
	}
	return nil
}

// classifyAccountLinkPut maps only pure condition failures; any other reason
// (conflict, throttling) stays a retryable transport error.
func classifyAccountLinkPut(err error, roles []string) error {
	var cancelled *types.TransactionCanceledException
	if !errors.As(err, &cancelled) || len(cancelled.CancellationReasons) != len(roles) {
		return fmt.Errorf("put account link: %w", err)
	}
	failed := map[string]bool{}
	for i, reason := range cancelled.CancellationReasons {
		switch aws.ToString(reason.Code) {
		case "None":
		case "ConditionalCheckFailed":
			failed[roles[i]] = true
		default:
			return fmt.Errorf("put account link: %w", err)
		}
	}
	switch {
	case failed["deny"]:
		return fmt.Errorf("%w: %w", ErrConditionFailed, ErrAccountLinkDenied)
	case failed["target"]:
		return fmt.Errorf("%w: %w", ErrConditionFailed, ErrAccountLinkTarget)
	case failed["exists"]:
		return fmt.Errorf("%w: %w", ErrConditionFailed, ErrAccountLinkExists)
	}
	return fmt.Errorf("put account link: %w", err)
}

func decodeAccountLink(accountID string, row map[string]types.AttributeValue) (*model.AccountLink, bool) {
	var item accountLinkItem
	if err := attributevalue.UnmarshalMap(row, &item); err != nil || item.PK != model.PrefixAccount+accountID {
		return nil, false
	}
	// The key, not the payload, is canonical for the endpoints.
	in, ok := model.ParseAccountLinkSortKey(item.SK)
	if !ok || (item.Origin != model.LinkOriginManual && item.Origin != model.LinkOriginAuto) {
		return nil, false
	}
	link := item.AccountLink
	link.AccountID = accountID
	link.FromKind, link.FromID, link.ToKind, link.ToID = in.FromKind, in.FromID, in.ToKind, in.ToID
	return &link, true
}

// GetAccountLink reads the forward row (strongly consistent), or nil.
func (r *DynamoDBRepository) GetAccountLink(ctx context.Context, accountID string, in model.AccountLinkInput) (*model.AccountLink, error) {
	if model.ValidateAccountLink(in) != nil {
		return nil, fmt.Errorf("invalid account link")
	}
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true),
		Key: accountNewsKey(model.PrefixAccount+accountID, model.AccountLinkSortKey(in))})
	if err != nil {
		return nil, fmt.Errorf("get account link: %w", err)
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	link, ok := decodeAccountLink(accountID, out.Item)
	if !ok {
		return nil, fmt.Errorf("invalid stored account link")
	}
	return link, nil
}

// DeleteAccountLink deletes the forward and reverse rows in one transaction.
// The forward delete requires the row to exist with the origin the caller
// observed: auto when deny is true (which also Puts LINKDENY#), manual
// otherwise. ErrConditionFailed means the row is gone or its origin changed.
func (r *DynamoDBRepository) DeleteAccountLink(ctx context.Context, accountID string, in model.AccountLinkInput, deny bool) error {
	if accountID == "" || model.ValidateAccountLink(in) != nil {
		return fmt.Errorf("invalid account link")
	}
	origin := model.LinkOriginManual
	if deny {
		origin = model.LinkOriginAuto
	}
	cond, err := expression.NewBuilder().WithCondition(expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("origin").Equal(expression.Value(origin)))).Build()
	if err != nil {
		return err
	}
	pk := model.PrefixAccount + accountID
	del := &types.Delete{TableName: aws.String(r.tableName), Key: accountNewsKey(pk, model.AccountLinkSortKey(in)),
		ConditionExpression: cond.Condition(), ExpressionAttributeNames: cond.Names(), ExpressionAttributeValues: cond.Values()}
	items := []types.TransactWriteItem{{Delete: del}, {Delete: &types.Delete{TableName: aws.String(r.tableName), Key: accountNewsKey(pk, model.AccountLinkToSortKey(in))}}}
	if deny {
		row, err := attributevalue.MarshalMap(accountLinkDenyItem{PK: pk, SK: model.AccountLinkDenySortKey(in), EntityType: "ACCOUNT_LINK_DENY",
			AccountID: accountID, FromKind: in.FromKind, FromID: in.FromID, ToKind: in.ToKind, ToID: in.ToID})
		if err != nil {
			return fmt.Errorf("marshal account link deny row: %w", err)
		}
		items = append(items, types.TransactWriteItem{Put: &types.Put{TableName: aws.String(r.tableName), Item: row}})
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if err == nil {
		return nil
	}
	var cancelled *types.TransactionCanceledException
	if errors.As(err, &cancelled) && len(cancelled.CancellationReasons) == len(items) {
		failed := false
		for _, reason := range cancelled.CancellationReasons {
			switch aws.ToString(reason.Code) {
			case "None":
			case "ConditionalCheckFailed":
				failed = true
			default:
				return fmt.Errorf("delete account link: %w", err)
			}
		}
		if failed {
			return fmt.Errorf("%w: account link", ErrConditionFailed)
		}
	}
	return fmt.Errorf("delete account link: %w", err)
}

func encodeAccountLinkCursor(key map[string]types.AttributeValue, accountID string) (*string, error) {
	pk, okPK := key["PK"].(*types.AttributeValueMemberS)
	sk, okSK := key["SK"].(*types.AttributeValueMemberS)
	if !okPK || !okSK || pk.Value != model.PrefixAccount+accountID || !strings.HasPrefix(sk.Value, model.PrefixAccountLink) {
		return nil, fmt.Errorf("unexpected account link continuation key")
	}
	c := base64.RawURLEncoding.EncodeToString([]byte(sk.Value))
	return &c, nil
}

// ErrInvalidAccountLinkCursor reports a cursor that is not a link sort key.
var ErrInvalidAccountLinkCursor = errors.New("invalid account link cursor")

func decodeAccountLinkCursor(cursor string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(raw) > 1024 {
		return "", ErrInvalidAccountLinkCursor
	}
	sk := string(raw)
	if _, ok := model.ParseAccountLinkSortKey(sk); !ok {
		return "", ErrInvalidAccountLinkCursor
	}
	return sk, nil
}

// ListAccountLinks returns one page of the account's forward LINK# rows in
// sort-key order. It is a raw listing: callers must revalidate endpoints.
// The cursor is the opaque last sort key; it only positions within the given
// account's partition.
func (r *DynamoDBRepository) ListAccountLinks(ctx context.Context, accountID, cursor string, limit int32) ([]model.AccountLink, *string, error) {
	if accountID == "" || limit <= 0 {
		return nil, nil, fmt.Errorf("invalid account link listing")
	}
	key := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + accountID)).
		And(expression.Key("SK").BeginsWith(model.PrefixAccountLink))
	expr, err := expression.NewBuilder().WithKeyCondition(key).Build()
	if err != nil {
		return nil, nil, err
	}
	in := &dynamodb.QueryInput{TableName: aws.String(r.tableName), KeyConditionExpression: expr.KeyCondition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(), ConsistentRead: aws.Bool(true), Limit: aws.Int32(limit)}
	if cursor != "" {
		sk, err := decodeAccountLinkCursor(cursor)
		if err != nil {
			return nil, nil, err
		}
		in.ExclusiveStartKey = accountNewsKey(model.PrefixAccount+accountID, sk)
	}
	out, err := r.client.Query(ctx, in)
	if err != nil {
		return nil, nil, fmt.Errorf("list account links: %w", err)
	}
	links := make([]model.AccountLink, 0, len(out.Items))
	for _, row := range out.Items {
		if link, ok := decodeAccountLink(accountID, row); ok {
			links = append(links, *link)
		}
	}
	if len(out.LastEvaluatedKey) == 0 {
		return links, nil, nil
	}
	next, err := encodeAccountLinkCursor(out.LastEvaluatedKey, accountID)
	if err != nil {
		return nil, nil, err
	}
	return links, next, nil
}

// ListAccountLinksByPrefix reads at most maxRows link rows of the account
// whose sort key starts with prefix, a LINK# (forward) or LINKTO# (reverse)
// key prefix such as LINK#news#{newsId}# or LINKTO#project#{projectId}#.
// Endpoints come from the sort key. truncated reports that the bound was hit
// while more rows may remain; the rows read so far are still returned.
func (r *DynamoDBRepository) ListAccountLinksByPrefix(ctx context.Context, accountID, prefix string, maxRows int) ([]model.AccountLink, bool, error) {
	reverse := strings.HasPrefix(prefix, model.PrefixAccountLinkTo)
	if accountID == "" || maxRows <= 0 || (!reverse && !strings.HasPrefix(prefix, model.PrefixAccountLink)) || !strings.HasSuffix(prefix, "#") {
		return nil, false, fmt.Errorf("invalid account link prefix query")
	}
	key := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + accountID)).
		And(expression.Key("SK").BeginsWith(prefix))
	expr, err := expression.NewBuilder().WithKeyCondition(key).Build()
	if err != nil {
		return nil, false, err
	}
	links := []model.AccountLink{}
	var start map[string]types.AttributeValue
	for read := 0; ; {
		limit := int32(min(maxRows-read, 100))
		out, err := r.client.Query(ctx, &dynamodb.QueryInput{TableName: aws.String(r.tableName), KeyConditionExpression: expr.KeyCondition(),
			ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(), ConsistentRead: aws.Bool(true),
			Limit: aws.Int32(limit), ExclusiveStartKey: start})
		if err != nil {
			return nil, false, fmt.Errorf("list account links by prefix: %w", err)
		}
		read += len(out.Items)
		for _, row := range out.Items {
			if link, ok := decodeAccountLinkRow(accountID, row, reverse); ok && strings.HasPrefix(linkRowSortKey(link, reverse), prefix) {
				links = append(links, *link)
			}
		}
		if len(out.LastEvaluatedKey) == 0 {
			return links, false, nil
		}
		if read >= maxRows {
			return links, true, nil
		}
		start = out.LastEvaluatedKey
	}
}

func linkRowSortKey(l *model.AccountLink, reverse bool) string {
	if reverse {
		return model.AccountLinkToSortKey(l.Input())
	}
	return model.AccountLinkSortKey(l.Input())
}

// decodeAccountLinkRow decodes a forward or reverse row from its sort key.
func decodeAccountLinkRow(accountID string, row map[string]types.AttributeValue, reverse bool) (*model.AccountLink, bool) {
	if !reverse {
		return decodeAccountLink(accountID, row)
	}
	var item accountLinkItem
	if err := attributevalue.UnmarshalMap(row, &item); err != nil || item.PK != model.PrefixAccount+accountID {
		return nil, false
	}
	in, ok := model.ParseAccountLinkToSortKey(item.SK)
	if !ok || (item.Origin != model.LinkOriginManual && item.Origin != model.LinkOriginAuto) {
		return nil, false
	}
	link := item.AccountLink
	link.AccountID = accountID
	link.FromKind, link.FromID, link.ToKind, link.ToID = in.FromKind, in.FromID, in.ToKind, in.ToID
	return &link, true
}
