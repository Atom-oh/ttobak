package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
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

// peopleMentionBatchAttempts bounds BatchWriteItem calls per chunk: the first
// call plus three retries of unprocessed items.
const (
	peopleMentionBatchSize     = 25
	peopleMentionBatchAttempts = 4
)

var peopleMentionBatchBackoff = 25 * time.Millisecond

type mentionIgnoreItem struct {
	PK         string `dynamodbav:"PK"`
	SK         string `dynamodbav:"SK"`
	EntityType string `dynamodbav:"entityType"`
	AccountID  string `dynamodbav:"accountId"`
	Hash       string `dynamodbav:"hash"`
	CreatedBy  string `dynamodbav:"createdBy"`
}

type unresolvedMentionItem struct {
	PK         string `dynamodbav:"PK"`
	SK         string `dynamodbav:"SK"`
	EntityType string `dynamodbav:"entityType"`
	model.UnresolvedMention
}

type peopleRunItem struct {
	PK string `dynamodbav:"PK"`
	SK string `dynamodbav:"SK"`
	model.PeopleRun
}

func validMentionHash(h string) bool {
	if len(h) != 32 {
		return false
	}
	for i := 0; i < len(h); i++ {
		if c := h[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validPeopleRole(role string) bool {
	switch role {
	case model.PeopleRoleParticipant, model.PeopleRoleSpeaker, model.PeopleRoleAssignee, model.PeopleRoleMentions:
		return true
	}
	return false
}

// parseMentionSortKey decodes MENTION#{hash}#{meetingLinkId}#{role}.
func parseMentionSortKey(sk string) (hash, meetingID, role string, ok bool) {
	if !strings.HasPrefix(sk, model.PrefixAccountMention) {
		return "", "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(sk, model.PrefixAccountMention), "#")
	if len(parts) != 3 || !validMentionHash(parts[0]) || !validPeopleRole(parts[2]) {
		return "", "", "", false
	}
	if _, _, ok := splitLinkMeetingID(parts[1]); !ok || !validPeopleListID(parts[1]) {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

func validPeopleRunScope(scope string) bool {
	if scope == model.PeopleRunScopeAccount {
		return true
	}
	_, _, ok := splitLinkMeetingID(scope)
	return ok && validPeopleListID(scope)
}

// PutMentionIgnore marks a normalized mention as "not a person" account-wide.
// The actor must still be a direct member.
func (r *DynamoDBRepository) PutMentionIgnore(ctx context.Context, accountID, actorID, hash string) error {
	if accountID == "" || actorID == "" || !validMentionHash(hash) {
		return fmt.Errorf("invalid mention ignore")
	}
	pk := model.PrefixAccount + accountID
	row, err := attributevalue.MarshalMap(mentionIgnoreItem{PK: pk, SK: model.MentionIgnoreSortKey(hash), EntityType: "ACCOUNT_PEOPLE_MENTION_IGNORE",
		AccountID: accountID, Hash: hash, CreatedBy: actorID})
	if err != nil {
		return fmt.Errorf("marshal mention ignore: %w", err)
	}
	access, err := r.accountLinkAccessChecks(accountID, actorID)
	if err != nil {
		return err
	}
	items := append([]types.TransactWriteItem{{Put: &types.Put{TableName: aws.String(r.tableName), Item: row}}}, access...)
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
		return classifyAccountLinkDelete(err, len(items), 1)
	}
	return nil
}

// GetMentionIgnores reports which hashes have an ignore row, with strongly
// consistent batch reads (100 keys per call, unprocessed keys retried).
func (r *DynamoDBRepository) GetMentionIgnores(ctx context.Context, accountID string, hashes []string) (map[string]bool, error) {
	if accountID == "" {
		return nil, fmt.Errorf("invalid mention ignore lookup")
	}
	pk := model.PrefixAccount + accountID
	keys := make([]batchRowKey, 0, len(hashes))
	for _, h := range hashes {
		if !validMentionHash(h) {
			return nil, fmt.Errorf("invalid mention ignore lookup")
		}
		keys = append(keys, batchRowKey{PK: pk, SK: model.MentionIgnoreSortKey(h)})
	}
	out := map[string]bool{}
	projection, err := expression.NewBuilder().WithProjection(expression.NamesList(expression.Name("PK"), expression.Name("SK"))).Build()
	if err != nil {
		return nil, err
	}
	err = r.batchGetConsistent(ctx, keys, &projection, func(key batchRowKey, _ map[string]types.AttributeValue) error {
		out[strings.TrimPrefix(key.SK, model.PrefixAccountLinkDeny+model.LinkKindMention+"#")] = true
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("get mention ignores: %w", err)
	}
	return out, nil
}

func validUnresolvedMentionRow(accountID, meetingID string, m *model.UnresolvedMention) bool {
	return m.AccountID == accountID && m.MeetingID == meetingID && validMentionHash(m.Hash) && validPeopleRole(m.Role) &&
		m.SourceField != "" && m.Mention != "" && m.CreatedAt != "" && utf8.ValidString(m.Mention) &&
		utf8.RuneCountInString(m.Mention) <= model.MaxPeopleMentionRunes && len(m.SourceRef) <= model.MaxPeopleSourceRefBytes
}

// ReplaceUnresolvedMentions puts every row of one meeting and deletes each
// prior sort key (from the run row) that is not among them. Writes go in
// BatchWriteItem chunks of 25; unprocessed items are retried three times
// before the call fails.
func (r *DynamoDBRepository) ReplaceUnresolvedMentions(ctx context.Context, accountID, meetingID string, rows []model.UnresolvedMention, prior []string) error {
	if _, _, ok := splitLinkMeetingID(meetingID); accountID == "" || !ok || !validPeopleListID(meetingID) {
		return fmt.Errorf("invalid unresolved mentions")
	}
	pk := model.PrefixAccount + accountID
	keep := map[string]bool{}
	var writes []types.WriteRequest
	for i := range rows {
		if !validUnresolvedMentionRow(accountID, meetingID, &rows[i]) {
			return fmt.Errorf("invalid unresolved mention")
		}
		sk := model.MentionSortKey(rows[i].Hash, meetingID, rows[i].Role)
		if keep[sk] {
			continue
		}
		keep[sk] = true
		item, err := attributevalue.MarshalMap(unresolvedMentionItem{PK: pk, SK: sk, EntityType: "ACCOUNT_PEOPLE_MENTION", UnresolvedMention: rows[i]})
		if err != nil {
			return fmt.Errorf("marshal unresolved mention: %w", err)
		}
		writes = append(writes, types.WriteRequest{PutRequest: &types.PutRequest{Item: item}})
	}
	deleted := map[string]bool{}
	for _, sk := range prior {
		if _, m, _, ok := parseMentionSortKey(sk); !ok || m != meetingID {
			return fmt.Errorf("invalid prior unresolved mention")
		}
		if keep[sk] || deleted[sk] {
			continue
		}
		deleted[sk] = true
		writes = append(writes, types.WriteRequest{DeleteRequest: &types.DeleteRequest{Key: accountNewsKey(pk, sk)}})
	}
	for start := 0; start < len(writes); start += peopleMentionBatchSize {
		if err := r.batchWritePeople(ctx, writes[start:min(start+peopleMentionBatchSize, len(writes))]); err != nil {
			return err
		}
	}
	return nil
}

func (r *DynamoDBRepository) batchWritePeople(ctx context.Context, chunk []types.WriteRequest) error {
	for attempt := 1; ; attempt++ {
		out, err := r.client.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{RequestItems: map[string][]types.WriteRequest{r.tableName: chunk}})
		if err != nil {
			return fmt.Errorf("write unresolved mentions: %w", err)
		}
		chunk = out.UnprocessedItems[r.tableName]
		if len(chunk) == 0 {
			return nil
		}
		if attempt >= peopleMentionBatchAttempts {
			return fmt.Errorf("write unresolved mentions: %d unprocessed items remain", len(chunk))
		}
		timer := time.NewTimer(time.Duration(attempt) * peopleMentionBatchBackoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("write unresolved mentions: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func (r *DynamoDBRepository) queryUnresolvedMentions(ctx context.Context, accountID string, key expression.KeyConditionBuilder, skipSK string, maxRows int) ([]model.UnresolvedMention, string, bool, error) {
	expr, err := expression.NewBuilder().WithKeyCondition(key).Build()
	if err != nil {
		return nil, "", false, err
	}
	rows := []model.UnresolvedMention{}
	lastSK := ""
	var start map[string]types.AttributeValue
	// Empty pages with a continuation key count toward a page bound, so a
	// sparse range cannot loop without reading rows.
	maxPages := maxRows/100 + 4
	for read, pages := 0, 0; ; pages++ {
		out, err := r.client.Query(ctx, &dynamodb.QueryInput{TableName: aws.String(r.tableName), KeyConditionExpression: expr.KeyCondition(),
			ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(), ConsistentRead: aws.Bool(true),
			Limit: aws.Int32(int32(min(maxRows-read, 100))), ExclusiveStartKey: start})
		if err != nil {
			return nil, "", false, fmt.Errorf("list unresolved mentions: %w", err)
		}
		read += len(out.Items)
		for _, row := range out.Items {
			var item unresolvedMentionItem
			if err := attributevalue.UnmarshalMap(row, &item); err != nil || item.PK != model.PrefixAccount+accountID {
				continue
			}
			lastSK = item.SK
			// The key, not the payload, is canonical for hash, meeting and role.
			hash, meetingID, role, ok := parseMentionSortKey(item.SK)
			if !ok || item.SK == skipSK || item.Mention == "" {
				continue
			}
			m := item.UnresolvedMention
			m.AccountID, m.Hash, m.MeetingID, m.Role = accountID, hash, meetingID, role
			rows = append(rows, m)
		}
		if len(out.LastEvaluatedKey) == 0 {
			return rows, lastSK, false, nil
		}
		if sk, ok := out.LastEvaluatedKey["SK"].(*types.AttributeValueMemberS); ok && len(out.Items) == 0 {
			lastSK = sk.Value
		}
		if read >= maxRows || pages+1 >= maxPages {
			return rows, lastSK, true, nil
		}
		start = out.LastEvaluatedKey
	}
}

// ListUnresolvedMentions reads at most maxRows MENTION# rows in sort-key
// order, so rows of one hash are contiguous. It resumes strictly after afterSK
// and returns the last sort key read when the bound was hit, or "" once the
// range is exhausted.
func (r *DynamoDBRepository) ListUnresolvedMentions(ctx context.Context, accountID, afterSK string, maxRows int) ([]model.UnresolvedMention, string, error) {
	if accountID == "" || maxRows <= 0 || (afterSK != "" && !strings.HasPrefix(afterSK, model.PrefixAccountMention)) {
		return nil, "", fmt.Errorf("invalid unresolved mention listing")
	}
	low := model.PrefixAccountMention
	if afterSK != "" {
		low = afterSK
	}
	key := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + accountID)).
		And(expression.Key("SK").Between(expression.Value(low), expression.Value("MENTION$")))
	rows, lastSK, more, err := r.queryUnresolvedMentions(ctx, accountID, key, afterSK, maxRows)
	if err != nil {
		return nil, "", err
	}
	if !more {
		lastSK = ""
	}
	return rows, lastSK, nil
}

// ListUnresolvedMentionsByHash reads at most maxRows rows of one normalized
// mention. truncated reports that the bound was hit.
func (r *DynamoDBRepository) ListUnresolvedMentionsByHash(ctx context.Context, accountID, hash string, maxRows int) ([]model.UnresolvedMention, bool, error) {
	if accountID == "" || maxRows <= 0 || !validMentionHash(hash) {
		return nil, false, fmt.Errorf("invalid unresolved mention listing")
	}
	key := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + accountID)).
		And(expression.Key("SK").BeginsWith(model.PrefixAccountMention + hash + "#"))
	rows, _, truncated, err := r.queryUnresolvedMentions(ctx, accountID, key, "", maxRows)
	return rows, truncated, err
}

// DeleteUnresolvedMention removes one unresolved mention row.
func (r *DynamoDBRepository) DeleteUnresolvedMention(ctx context.Context, accountID, hash, meetingID, role string) error {
	sk := model.MentionSortKey(hash, meetingID, role)
	if _, m, _, ok := parseMentionSortKey(sk); accountID == "" || !ok || m != meetingID {
		return fmt.Errorf("invalid unresolved mention")
	}
	if _, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(r.tableName),
		Key: accountNewsKey(model.PrefixAccount+accountID, sk)}); err != nil {
		return fmt.Errorf("delete unresolved mention: %w", err)
	}
	return nil
}

func peopleRunKey(accountID, scope string) map[string]types.AttributeValue {
	return accountNewsKey(model.PrefixAccount+accountID, model.PrefixAccountPeopleRun+scope)
}

// ClaimPeopleRun takes the run lease of one scope when it is free, expired or
// already held by token, and returns the stored row with the prior mentions.
// When another run holds the lease it marks rerun and returns claimed=false.
func (r *DynamoDBRepository) ClaimPeopleRun(ctx context.Context, accountID, scope, token string, now time.Time, lease time.Duration) (*model.PeopleRun, bool, error) {
	if accountID == "" || token == "" || lease <= 0 || !validPeopleRunScope(scope) {
		return nil, false, fmt.Errorf("invalid people run claim")
	}
	cond := expression.AttributeNotExists(expression.Name("leaseUntil")).
		Or(expression.Name("leaseUntil").LessThan(expression.Value(now.Unix()))).
		Or(expression.Name("leaseToken").Equal(expression.Value(token)))
	update := expression.Set(expression.Name("leaseToken"), expression.Value(token)).
		Set(expression.Name("leaseUntil"), expression.Value(now.Add(lease).Unix())).
		Set(expression.Name("rerun"), expression.Value(false)).
		Set(expression.Name("accountId"), expression.Value(accountID)).
		Set(expression.Name("scope"), expression.Value(scope)).
		Set(expression.Name("entityType"), expression.Value("ACCOUNT_PEOPLE_RUN"))
	expr, err := expression.NewBuilder().WithCondition(cond).WithUpdate(update).Build()
	if err != nil {
		return nil, false, err
	}
	out, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{TableName: aws.String(r.tableName), Key: peopleRunKey(accountID, scope),
		ConditionExpression: expr.Condition(), UpdateExpression: expr.Update(), ExpressionAttributeNames: expr.Names(),
		ExpressionAttributeValues: expr.Values(), ReturnValues: types.ReturnValueAllNew})
	var ccfe *types.ConditionalCheckFailedException
	if errors.As(err, &ccfe) {
		rerun, err := expression.NewBuilder().WithUpdate(expression.Set(expression.Name("rerun"), expression.Value(true))).Build()
		if err != nil {
			return nil, false, err
		}
		if _, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{TableName: aws.String(r.tableName), Key: peopleRunKey(accountID, scope),
			UpdateExpression: rerun.Update(), ExpressionAttributeNames: rerun.Names(), ExpressionAttributeValues: rerun.Values()}); err != nil {
			return nil, false, fmt.Errorf("mark people rerun: %w", err)
		}
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("claim people run: %w", err)
	}
	var item peopleRunItem
	if err := attributevalue.UnmarshalMap(out.Attributes, &item); err != nil {
		return nil, false, fmt.Errorf("decode people run: %w", err)
	}
	run := item.PeopleRun
	run.AccountID, run.Scope = accountID, scope
	return &run, true, nil
}

// FinishPeopleRun records the run result and releases the lease, if run still
// holds it. rerun reports that another caller asked for a new pass meanwhile.
func (r *DynamoDBRepository) FinishPeopleRun(ctx context.Context, run *model.PeopleRun) (bool, error) {
	if run == nil || run.AccountID == "" || run.LeaseToken == "" || !validPeopleRunScope(run.Scope) {
		return false, fmt.Errorf("invalid people run")
	}
	update := expression.Set(expression.Name("runAt"), expression.Value(run.RunAt)).
		Set(expression.Name("resolved"), expression.Value(run.Resolved)).
		Set(expression.Name("unresolved"), expression.Value(run.Unresolved)).
		Set(expression.Name("truncated"), expression.Value(run.Truncated)).
		Remove(expression.Name("leaseUntil")).
		Remove(expression.Name("leaseToken"))
	if len(run.Mentions) > 0 {
		update = update.Set(expression.Name("mentions"), expression.Value(run.Mentions))
	} else {
		update = update.Remove(expression.Name("mentions"))
	}
	expr, err := expression.NewBuilder().WithCondition(expression.Name("leaseToken").Equal(expression.Value(run.LeaseToken))).WithUpdate(update).Build()
	if err != nil {
		return false, err
	}
	out, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{TableName: aws.String(r.tableName), Key: peopleRunKey(run.AccountID, run.Scope),
		ConditionExpression: expr.Condition(), UpdateExpression: expr.Update(), ExpressionAttributeNames: expr.Names(),
		ExpressionAttributeValues: expr.Values(), ReturnValues: types.ReturnValueAllOld})
	var ccfe *types.ConditionalCheckFailedException
	if errors.As(err, &ccfe) {
		return false, fmt.Errorf("%w: people run lease lost", ErrConditionFailed)
	}
	if err != nil {
		return false, fmt.Errorf("finish people run: %w", err)
	}
	var old struct {
		Rerun bool `dynamodbav:"rerun"`
	}
	if err := attributevalue.UnmarshalMap(out.Attributes, &old); err != nil {
		return false, fmt.Errorf("decode people run: %w", err)
	}
	return old.Rerun, nil
}

// PrunePeopleLink deletes a people edge of either origin, with its reverse
// row, for an operator prune of a meeting whose row no longer exists. The
// transaction requires the meeting row to be absent, so a live meeting's
// edges, including unpublished ones, are never removed. A failed condition
// maps to ErrConditionFailed.
func (r *DynamoDBRepository) PrunePeopleLink(ctx context.Context, accountID string, k model.PeopleLinkKey) error {
	owner, meeting, ok := splitLinkMeetingID(k.MeetingID)
	if accountID == "" || !ok || model.ValidatePeopleLinkKey(k) != nil {
		return fmt.Errorf("invalid people link")
	}
	exists, err := expression.NewBuilder().WithCondition(expression.AttributeExists(expression.Name("PK"))).Build()
	if err != nil {
		return err
	}
	pk := model.PrefixAccount + accountID
	gone, err := r.linkConditionCheck(accountNewsKey(model.PrefixUser+owner, model.PrefixMeeting+meeting), expression.AttributeNotExists(expression.Name("PK")))
	if err != nil {
		return err
	}
	items := []types.TransactWriteItem{
		{Delete: &types.Delete{TableName: aws.String(r.tableName), Key: accountNewsKey(pk, model.PeopleLinkSortKey(k)),
			ConditionExpression: exists.Condition(), ExpressionAttributeNames: exists.Names()}},
		{Delete: &types.Delete{TableName: aws.String(r.tableName), Key: accountNewsKey(pk, model.PeopleLinkToSortKey(k))}},
		gone,
	}
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
		var cancelled *types.TransactionCanceledException
		if errors.As(err, &cancelled) {
			return fmt.Errorf("%w: prune people link", ErrConditionFailed)
		}
		return fmt.Errorf("prune people link: %w", err)
	}
	return nil
}
