package repository

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

type peopleLinkItem struct {
	PK         string `dynamodbav:"PK"`
	SK         string `dynamodbav:"SK"`
	EntityType string `dynamodbav:"entityType"`
	model.PeopleLink
}

type peopleLinkDenyItem struct {
	PK         string `dynamodbav:"PK"`
	SK         string `dynamodbav:"SK"`
	EntityType string `dynamodbav:"entityType"`
	AccountID  string `dynamodbav:"accountId"`
	MeetingID  string `dynamodbav:"meetingId"`
	ToKind     string `dynamodbav:"toKind"`
	ToID       string `dynamodbav:"toId"`
	Role       string `dynamodbav:"role"`
}

func validPeopleOrigin(origin string) bool {
	return origin == model.LinkOriginAuto || origin == model.LinkOriginManual
}

func validPeopleLinkRow(l *model.PeopleLink) bool {
	return l != nil && l.AccountID != "" && model.ValidatePeopleLinkKey(l.Key()) == nil && validPeopleOrigin(l.Origin) &&
		l.CreatedAt != "" && l.SourceField != "" && utf8.ValidString(l.Mention) && utf8.ValidString(l.Evidence) &&
		utf8.RuneCountInString(l.Mention) <= model.MaxPeopleMentionRunes &&
		utf8.RuneCountInString(l.Evidence) <= model.MaxPeopleEvidenceRunes &&
		len(l.SourceRef) <= model.MaxPeopleSourceRefBytes &&
		// A self-mention would duplicate the META access check item.
		!(l.ToKind == model.LinkKindAccount && l.ToID == l.AccountID)
}

// peopleTargetCheck requires the edge target to exist in (or list) the account.
func peopleTargetCheck(accountID string, k model.PeopleLinkKey) (map[string]types.AttributeValue, expression.ConditionBuilder) {
	exists := expression.AttributeExists(expression.Name("PK"))
	pk := model.PrefixAccount + accountID
	switch k.ToKind {
	case model.LinkKindMember:
		return accountNewsKey(pk, model.PrefixMember+k.ToID), exists
	case model.LinkKindContact:
		return accountNewsKey(pk, contactPrefix+k.ToID), exists
	case model.LinkKindAccount:
		return accountNewsKey(model.PrefixAccount+k.ToID, model.SKAccountMeta), exists
	default:
		return projectInAccountCheck(accountID, k.ToID)
	}
}

// PutPeopleLink writes a people edge and its reverse row in one transaction:
//
//	0 Put LINK#     auto: attribute_not_exists(PK); manual: or origin = auto
//	1 Put LINKTO#
//	2 ConditionCheck USER#{owner}/MEETING#{id} published to the account
//	3 ConditionCheck the target member/contact/account/project
//	  ConditionCheck LINKDENY# attribute_not_exists(PK)         (auto only)
//	  ConditionCheck ACCOUNT#{a}/META, ACCOUNT#{a}/MEMBER#{actor} (actor set)
//
// A manual write replaces an auto row but never another manual row. Manual
// edges require actorID == CreatedBy; the system writer of auto edges passes
// "". Failures map like PutAccountLink.
func (r *DynamoDBRepository) PutPeopleLink(ctx context.Context, l *model.PeopleLink, actorID string) error {
	if !validPeopleLinkRow(l) ||
		(l.Origin == model.LinkOriginManual && (actorID == "" || l.CreatedBy != actorID)) ||
		(l.Origin == model.LinkOriginAuto && actorID != "") {
		return fmt.Errorf("invalid people link")
	}
	k := l.Key()
	pk := model.PrefixAccount + l.AccountID
	forward, err := attributevalue.MarshalMap(peopleLinkItem{PK: pk, SK: model.PeopleLinkSortKey(k), EntityType: "ACCOUNT_PEOPLE_LINK", PeopleLink: *l})
	if err != nil {
		return fmt.Errorf("marshal people link: %w", err)
	}
	reverse, err := attributevalue.MarshalMap(peopleLinkItem{PK: pk, SK: model.PeopleLinkToSortKey(k), EntityType: "ACCOUNT_PEOPLE_LINK_TO", PeopleLink: *l})
	if err != nil {
		return fmt.Errorf("marshal people link reverse row: %w", err)
	}
	cond := expression.AttributeNotExists(expression.Name("PK"))
	if l.Origin == model.LinkOriginManual {
		cond = cond.Or(expression.Name("origin").Equal(expression.Value(model.LinkOriginAuto)))
	}
	putCond, err := expression.NewBuilder().WithCondition(cond).Build()
	if err != nil {
		return err
	}
	items := []types.TransactWriteItem{
		{Put: &types.Put{TableName: aws.String(r.tableName), Item: forward, ConditionExpression: putCond.Condition(),
			ExpressionAttributeNames: putCond.Names(), ExpressionAttributeValues: putCond.Values()}},
		{Put: &types.Put{TableName: aws.String(r.tableName), Item: reverse}},
	}
	roles := []string{"exists", "reverse"}
	add := func(role string, key map[string]types.AttributeValue, c expression.ConditionBuilder) error {
		item, err := r.linkConditionCheck(key, c)
		if err != nil {
			return err
		}
		items, roles = append(items, item), append(roles, role)
		return nil
	}
	meetingKey, meetingCond, ok := meetingPublishedCheck(l.AccountID, k.MeetingID)
	if !ok {
		return fmt.Errorf("invalid people link meeting id")
	}
	if err := add("target", meetingKey, meetingCond); err != nil {
		return err
	}
	targetKey, targetCond := peopleTargetCheck(l.AccountID, k)
	if err := add("target", targetKey, targetCond); err != nil {
		return err
	}
	if l.Origin == model.LinkOriginAuto {
		if err := add("deny", accountNewsKey(pk, model.PeopleLinkDenySortKey(k)), expression.AttributeNotExists(expression.Name("PK"))); err != nil {
			return err
		}
	}
	accessActor := actorID
	if k.ToKind == model.LinkKindMember && k.ToID == actorID {
		// The target check already pins this MEMBER# row; one transaction
		// cannot touch the same item twice.
		accessActor = ""
	}
	access, err := r.accountLinkAccessChecks(l.AccountID, accessActor)
	if err != nil {
		return err
	}
	for _, item := range access {
		items, roles = append(items, item), append(roles, "access")
	}
	if accessActor == "" && actorID != "" {
		// Membership of the actor is still enforced, through the target check.
		roles[3] = "access"
	}
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
		return classifyAccountLinkPut(err, roles)
	}
	return nil
}

func decodePeopleLinkRow(accountID string, row map[string]types.AttributeValue, reverse bool) (*model.PeopleLink, string, bool) {
	var item peopleLinkItem
	if err := attributevalue.UnmarshalMap(row, &item); err != nil || item.PK != model.PrefixAccount+accountID {
		return nil, item.SK, false
	}
	parse := model.ParsePeopleLinkSortKey
	if reverse {
		parse = model.ParsePeopleLinkToSortKey
	}
	// The key, not the payload, is canonical for the endpoints and role.
	k, ok := parse(item.SK)
	if !ok || !validPeopleOrigin(item.Origin) {
		return nil, item.SK, false
	}
	link := item.PeopleLink
	link.AccountID = accountID
	link.MeetingID, link.ToKind, link.ToID, link.Role = k.MeetingID, k.ToKind, k.ToID, k.Role
	return &link, item.SK, true
}

// GetPeopleLink reads the forward row (strongly consistent), or nil.
func (r *DynamoDBRepository) GetPeopleLink(ctx context.Context, accountID string, k model.PeopleLinkKey) (*model.PeopleLink, error) {
	if accountID == "" || model.ValidatePeopleLinkKey(k) != nil {
		return nil, fmt.Errorf("invalid people link")
	}
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true),
		Key: accountNewsKey(model.PrefixAccount+accountID, model.PeopleLinkSortKey(k))})
	if err != nil {
		return nil, fmt.Errorf("get people link: %w", err)
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	link, _, ok := decodePeopleLinkRow(accountID, out.Item, false)
	if !ok {
		return nil, fmt.Errorf("invalid stored people link")
	}
	return link, nil
}

// DeletePeopleLink deletes the forward and reverse rows in one transaction.
// The forward delete requires the row to exist with expectOrigin. deny also
// Puts LINKDENY#. A member actor must still be a direct member; the system
// cleanup passes actorID "" and may only remove its own auto edge without a
// deny row, which checks only the account META row. Errors map like
// DeleteAccountLink.
func (r *DynamoDBRepository) DeletePeopleLink(ctx context.Context, accountID, actorID string, k model.PeopleLinkKey, expectOrigin string, deny bool) error {
	if accountID == "" || model.ValidatePeopleLinkKey(k) != nil || !validPeopleOrigin(expectOrigin) ||
		(actorID == "" && (expectOrigin != model.LinkOriginAuto || deny)) {
		return fmt.Errorf("invalid people link")
	}
	cond, err := expression.NewBuilder().WithCondition(expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("origin").Equal(expression.Value(expectOrigin)))).Build()
	if err != nil {
		return err
	}
	pk := model.PrefixAccount + accountID
	items := []types.TransactWriteItem{
		{Delete: &types.Delete{TableName: aws.String(r.tableName), Key: accountNewsKey(pk, model.PeopleLinkSortKey(k)),
			ConditionExpression: cond.Condition(), ExpressionAttributeNames: cond.Names(), ExpressionAttributeValues: cond.Values()}},
		{Delete: &types.Delete{TableName: aws.String(r.tableName), Key: accountNewsKey(pk, model.PeopleLinkToSortKey(k))}},
	}
	if deny {
		row, err := attributevalue.MarshalMap(peopleLinkDenyItem{PK: pk, SK: model.PeopleLinkDenySortKey(k), EntityType: "ACCOUNT_PEOPLE_LINK_DENY",
			AccountID: accountID, MeetingID: k.MeetingID, ToKind: k.ToKind, ToID: k.ToID, Role: k.Role})
		if err != nil {
			return fmt.Errorf("marshal people link deny row: %w", err)
		}
		items = append(items, types.TransactWriteItem{Put: &types.Put{TableName: aws.String(r.tableName), Item: row}})
	}
	access, err := r.accountLinkAccessChecks(accountID, actorID)
	if err != nil {
		return err
	}
	firstAccess := len(items)
	items = append(items, access...)
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
		return classifyAccountLinkDelete(err, len(items), firstAccess)
	}
	return nil
}

// queryPeopleLinks reads at most maxRows rows matching key and decodes people
// rows, skipping a row whose sort key equals skipSK. It returns the decoded
// links, the last sort key read and whether the bound was hit with more rows
// possibly remaining.
func (r *DynamoDBRepository) queryPeopleLinks(ctx context.Context, accountID string, key expression.KeyConditionBuilder, reverse bool, skipSK string, maxRows int) ([]model.PeopleLink, string, bool, error) {
	expr, err := expression.NewBuilder().WithKeyCondition(key).Build()
	if err != nil {
		return nil, "", false, err
	}
	links := []model.PeopleLink{}
	lastSK := ""
	var start map[string]types.AttributeValue
	for read := 0; ; {
		out, err := r.client.Query(ctx, &dynamodb.QueryInput{TableName: aws.String(r.tableName), KeyConditionExpression: expr.KeyCondition(),
			ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(), ConsistentRead: aws.Bool(true),
			Limit: aws.Int32(int32(min(maxRows-read, 100))), ExclusiveStartKey: start})
		if err != nil {
			return nil, "", false, fmt.Errorf("list people links: %w", err)
		}
		read += len(out.Items)
		for _, row := range out.Items {
			link, sk, ok := decodePeopleLinkRow(accountID, row, reverse)
			if sk != "" {
				lastSK = sk
			}
			if ok && sk != skipSK {
				links = append(links, *link)
			}
		}
		if len(out.LastEvaluatedKey) == 0 {
			return links, lastSK, false, nil
		}
		if read >= maxRows {
			return links, lastSK, true, nil
		}
		start = out.LastEvaluatedKey
	}
}

func validPeopleListID(id string) bool {
	return id != "" && !strings.Contains(id, "#") && utf8.ValidString(id)
}

// ListPeopleLinksForMeeting reads at most maxRows forward people rows of one
// meeting (LINK#meeting#{m}#). truncated reports that the bound was hit.
func (r *DynamoDBRepository) ListPeopleLinksForMeeting(ctx context.Context, accountID, meetingID string, maxRows int) ([]model.PeopleLink, bool, error) {
	if _, _, ok := splitLinkMeetingID(meetingID); accountID == "" || maxRows <= 0 || !ok || !validPeopleListID(meetingID) {
		return nil, false, fmt.Errorf("invalid people link listing")
	}
	key := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + accountID)).
		And(expression.Key("SK").BeginsWith(model.PrefixAccountLink + model.LinkKindMeeting + "#" + meetingID + "#"))
	links, _, truncated, err := r.queryPeopleLinks(ctx, accountID, key, false, "", maxRows)
	return links, truncated, err
}

// ListPeopleLinksByTarget reads at most maxRows reverse rows of one target kind
// (LINKTO#{toKind}#), resuming strictly after afterSK when it is set. It
// returns the last sort key read when the bound was hit, or "" once the range
// is exhausted. Project 1 reverse rows in the range are read but not returned.
func (r *DynamoDBRepository) ListPeopleLinksByTarget(ctx context.Context, accountID, toKind, afterSK string, maxRows int) ([]model.PeopleLink, string, error) {
	prefix := model.PrefixAccountLinkTo + toKind + "#"
	if accountID == "" || maxRows <= 0 || !validPeopleListID(toKind) || (afterSK != "" && !strings.HasPrefix(afterSK, prefix)) {
		return nil, "", fmt.Errorf("invalid people link listing")
	}
	low := prefix
	if afterSK != "" {
		low = afterSK
	}
	key := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + accountID)).
		And(expression.Key("SK").Between(expression.Value(low), expression.Value(model.PrefixAccountLinkTo+toKind+"$")))
	links, lastSK, more, err := r.queryPeopleLinks(ctx, accountID, key, true, afterSK, maxRows)
	if err != nil {
		return nil, "", err
	}
	if !more {
		lastSK = ""
	}
	return links, lastSK, nil
}

// ListPeopleLinksToTarget reads at most maxRows reverse people rows pointing to
// one target (LINKTO#{toKind}#{toId}#meeting#). truncated reports that the
// bound was hit.
func (r *DynamoDBRepository) ListPeopleLinksToTarget(ctx context.Context, accountID, toKind, toID string, maxRows int) ([]model.PeopleLink, bool, error) {
	if accountID == "" || maxRows <= 0 || !validPeopleListID(toKind) || !validPeopleListID(toID) {
		return nil, false, fmt.Errorf("invalid people link listing")
	}
	key := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + accountID)).
		And(expression.Key("SK").BeginsWith(model.PrefixAccountLinkTo + toKind + "#" + toID + "#" + model.LinkKindMeeting + "#"))
	links, _, truncated, err := r.queryPeopleLinks(ctx, accountID, key, true, "", maxRows)
	return links, truncated, err
}
