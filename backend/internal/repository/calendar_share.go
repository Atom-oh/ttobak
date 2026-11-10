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
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
)

var (
	ErrCalendarEventNotFound = errors.New("calendar event not found")
	// ErrCalendarShareGroupTooLarge reports one meetingKey group that did not end within the bounded read.
	ErrCalendarShareGroupTooLarge = errors.New("calendar share group exceeds the bounded read")
	// ErrCalendarShareStateChanged reports that an event's share state changed after a sync
	// batch read it (SetCalendarShareState won); the caller may recompute and retry.
	ErrCalendarShareStateChanged = errors.New("calendar share state changed")
)

const (
	calendarShareStateShared = "shared"
	calendarShareStateVeto   = "veto"
	calendarShareQueryPage   = 100
	calendarShareMaxEmail    = 320
	calendarMaxTransactItems = 100
	// calendarMaxVetoMembers bounds one veto row's set, keeping its exact-set condition
	// within DynamoDB expression limits.
	calendarMaxVetoMembers = 50
)

// ShareKey identifies one account share row (PK ACCOUNT#{AccountID}).
type ShareKey struct {
	AccountID string
	SK        string
}

// CalendarShareRow is one sharer's copy (State "shared") or veto (State "veto", empty Ciphertext)
// of a meeting in the account partition: PK ACCOUNT#{accountId}, SK CALSHARE#{startsAt}#{meetingKey}#{userId}.
type CalendarShareRow struct {
	PK          string    `dynamodbav:"PK"`
	SK          string    `dynamodbav:"SK"`
	AccountID   string    `dynamodbav:"accountId"`
	MeetingKey  string    `dynamodbav:"meetingKey"`
	UserID      string    `dynamodbav:"userId"`
	SourceID    string    `dynamodbav:"sourceId"`
	EventID     string    `dynamodbav:"eventId"`
	StartsAt    time.Time `dynamodbav:"startsAt"`
	EndsAt      time.Time `dynamodbav:"endsAt"`
	State       string    `dynamodbav:"state"`
	Ciphertext  string    `dynamodbav:"ciphertext,omitempty"`
	SharerEmail string    `dynamodbav:"sharerEmail,omitempty"`
	ExpiresAt   int64     `dynamodbav:"pendingShareExpiresAt"`
	// VetoEventIDs holds "{sourceId}#{eventId}" of every event of this user that vetoes the
	// key; the repository maintains it and the row exists while the set is non-empty.
	VetoEventIDs []string `dynamodbav:"vetoEventIds,stringset,omitempty"`
}

// CalendarSharePage groups rows by meetingKey; a page never splits a group.
// GroupCursors[i] resumes the same query right after Groups[i], so a caller can trim a
// page at a group boundary.
type CalendarSharePage struct {
	Groups       [][]CalendarShareRow
	GroupCursors []string
	NextCursor   *string
}

// CalendarOverrideRow is a manual sharing mode: PK USER#{userId}, SK CALOVERRIDE#{sourceId}#{scopeKey}.
type CalendarOverrideRow struct {
	PK        string `dynamodbav:"PK"`
	SK        string `dynamodbav:"SK"`
	UserID    string `dynamodbav:"userId"`
	SourceID  string `dynamodbav:"sourceId"`
	ScopeKey  string `dynamodbav:"scopeKey"`
	Mode      string `dynamodbav:"mode"`
	AccountID string `dynamodbav:"accountId,omitempty"`
}

func accountShareKey(accountID, sortKey string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: model.PrefixAccount + accountID},
		"SK": &types.AttributeValueMemberS{Value: sortKey},
	}
}

func (key ShareKey) id() string { return key.AccountID + "|" + key.SK }

func validCalendarShareKey(key ShareKey, userID string) bool {
	if !calendarStorageID.MatchString(key.AccountID) || !strings.HasPrefix(key.SK, model.PrefixCalendarShare) || !strings.HasSuffix(key.SK, "#"+userID) {
		return false
	}
	const stamp = len("2006-01-02T15:04:05.000000000Z")
	rest := strings.TrimPrefix(key.SK, model.PrefixCalendarShare)
	if len(rest) < stamp+1 {
		return false
	}
	startsAt, err := time.Parse("2006-01-02T15:04:05.000000000Z", rest[:stamp])
	if err != nil {
		return false
	}
	meetingKey := strings.TrimSuffix(strings.TrimPrefix(rest[stamp:], "#"), "#"+userID)
	return calendarStorageID.MatchString(meetingKey) && key.SK == model.CalendarShareSortKey(startsAt, meetingKey, userID)
}

// validCalendarShareInput checks a caller-supplied share row against the event it belongs to.
func validCalendarShareInput(share *CalendarShareRow, userID, sourceID, eventID string, startsAt, endsAt time.Time, expiresAt int64) bool {
	if !calendarStorageID.MatchString(share.AccountID) || !calendarStorageID.MatchString(share.MeetingKey) ||
		share.UserID != userID || share.SourceID != sourceID || share.EventID != eventID ||
		!share.StartsAt.Equal(startsAt) || !share.EndsAt.Equal(endsAt) || share.ExpiresAt != expiresAt ||
		share.PK != "" && share.PK != model.PrefixAccount+share.AccountID ||
		share.SK != "" && share.SK != model.CalendarShareSortKey(share.StartsAt, share.MeetingKey, userID) || len(share.VetoEventIDs) != 0 ||
		len(share.SharerEmail) > calendarShareMaxEmail || !utf8.ValidString(share.SharerEmail) {
		return false
	}
	switch share.State {
	case calendarShareStateShared:
		return share.Ciphertext != "" && len(share.Ciphertext) <= 16*1024
	case calendarShareStateVeto:
		return share.Ciphertext == ""
	}
	return false
}

// validCalendarShareMutation checks the share fields of one batch mutation.
func validCalendarShareMutation(userID, sourceID string, mutation CalendarWriteMutation) bool {
	if mutation.PreviousShareKey != nil && !validCalendarShareKey(*mutation.PreviousShareKey, userID) {
		return false
	}
	// Share fields are share intent: they require the caller-observed row version.
	if mutation.Expected == nil && (mutation.Share != nil || mutation.ExtraVeto != nil || mutation.PreviousShareKey != nil) {
		return false
	}
	if mutation.Op != "upsert" {
		return mutation.Share == nil && mutation.ExtraVeto == nil
	}
	row := mutation.Event
	if row.SeriesKey != "" && !calendarStorageID.MatchString(row.SeriesKey) {
		return false
	}
	if mutation.ExtraVeto == nil {
		if row.ExtraVetoAccountID != "" || row.ExtraVetoMeetingKey != "" {
			return false
		}
	} else if !validCalendarExtraVeto(mutation.Share, mutation.ExtraVeto, userID, sourceID, mutation.EventID, row.StartsAt, row.EndsAt, row.ExpiresAt) ||
		row.ExtraVetoAccountID != mutation.ExtraVeto.AccountID || row.ExtraVetoMeetingKey != mutation.ExtraVeto.MeetingKey {
		return false
	}
	if mutation.Share == nil {
		return row.ShareState == "" && row.ShareAccountID == "" && row.MeetingKey == ""
	}
	share := mutation.Share
	return validCalendarShareInput(share, userID, sourceID, mutation.EventID, row.StartsAt, row.EndsAt, row.ExpiresAt) &&
		row.ShareAccountID == share.AccountID && row.MeetingKey == share.MeetingKey && row.ShareState == share.State
}

// validCalendarExtraVeto requires a second veto, in another account, next to a veto share.
func validCalendarExtraVeto(share, extra *CalendarShareRow, userID, sourceID, eventID string, startsAt, endsAt time.Time, expiresAt int64) bool {
	return share != nil && share.State == calendarShareStateVeto && extra.State == calendarShareStateVeto &&
		extra.AccountID != share.AccountID && validCalendarShareInput(extra, userID, sourceID, eventID, startsAt, endsAt, expiresAt)
}

// storedCalendarShareKeys returns the share keys an existing event row currently owns
// (its share or veto, and an extra veto).
func storedCalendarShareKeys(old *CalendarEventRow, userID string) ([]ShareKey, error) {
	if old == nil {
		return nil, nil
	}
	var keys []ShareKey
	if old.ShareState != "" || old.ShareAccountID != "" || old.MeetingKey != "" {
		if old.ShareState != calendarShareStateShared && old.ShareState != calendarShareStateVeto ||
			!calendarStorageID.MatchString(old.ShareAccountID) || !calendarStorageID.MatchString(old.MeetingKey) {
			return nil, ErrCalendarCorruptData
		}
		keys = append(keys, ShareKey{AccountID: old.ShareAccountID, SK: model.CalendarShareSortKey(old.StartsAt, old.MeetingKey, userID)})
	}
	if old.ExtraVetoAccountID != "" || old.ExtraVetoMeetingKey != "" {
		if old.ShareState != calendarShareStateVeto || old.ExtraVetoAccountID == old.ShareAccountID ||
			!calendarStorageID.MatchString(old.ExtraVetoAccountID) || !calendarStorageID.MatchString(old.ExtraVetoMeetingKey) {
			return nil, ErrCalendarCorruptData
		}
		keys = append(keys, ShareKey{AccountID: old.ExtraVetoAccountID, SK: model.CalendarShareSortKey(old.StartsAt, old.ExtraVetoMeetingKey, userID)})
	}
	return keys, nil
}

// calendarRowHoldsShare reports whether a stored event row records a share or veto.
func calendarRowHoldsShare(old *CalendarEventRow) bool {
	return old != nil && (old.ShareState != "" || old.ShareAccountID != "" || old.MeetingKey != "" ||
		old.ExtraVetoAccountID != "" || old.ExtraVetoMeetingKey != "")
}

// calendarShareClaim is one share or veto row an event wants to hold.
type calendarShareClaim struct {
	row *CalendarShareRow
	key ShareKey
}

// calendarShareIntent is one event's desired share keys (at most two) and the keys it may vacate.
type calendarShareIntent struct {
	eventID  string
	desired  []calendarShareClaim
	vacating []ShareKey
}

func newCalendarShareIntent(userID, eventID string, old *CalendarEventRow, shares []*CalendarShareRow, previous *ShareKey) (calendarShareIntent, error) {
	intent := calendarShareIntent{eventID: eventID}
	stored, err := storedCalendarShareKeys(old, userID)
	if err != nil {
		return intent, err
	}
	skip := make(map[string]bool)
	for _, share := range shares {
		if share == nil {
			continue
		}
		key := ShareKey{AccountID: share.AccountID, SK: model.CalendarShareSortKey(share.StartsAt, share.MeetingKey, userID)}
		if skip[key.id()] {
			return intent, ErrInvalidCalendarBatch
		}
		skip[key.id()] = true
		intent.desired = append(intent.desired, calendarShareClaim{row: share, key: key})
	}
	if previous != nil {
		stored = append([]ShareKey{*previous}, stored...)
	}
	for _, key := range stored {
		if skip[key.id()] {
			continue
		}
		skip[key.id()] = true
		intent.vacating = append(intent.vacating, key)
	}
	return intent, nil
}

// calendarIntentGranted reports whether the claim for share (nil: none) is held.
func calendarIntentGranted(intent calendarShareIntent, granted []bool, share *CalendarShareRow) bool {
	for slot, claim := range intent.desired {
		if claim.row == share {
			return granted[slot]
		}
	}
	return false
}

func calendarVetoToken(sourceID, eventID string) string { return sourceID + "#" + eventID }

func validCalendarVetoSet(tokens []string) bool {
	if len(tokens) == 0 || len(tokens) > calendarMaxVetoMembers {
		return false
	}
	for _, token := range tokens {
		sourceID, eventID, found := strings.Cut(token, "#")
		if !found || uuid.Validate(sourceID) != nil || !calendarStorageID.MatchString(eventID) {
			return false
		}
	}
	return true
}

// calendarShareOwner is a share row as read. For a shared row the owner is sourceID/eventID;
// for a veto row it is every member of vetoes. Invalid rows (valid=false) are never written.
type calendarShareOwner struct {
	sourceID string
	eventID  string
	state    string
	vetoes   []string
	// endsAt and expiresAt are a veto row's window end and TTL: the latest of its members'.
	endsAt    time.Time
	expiresAt int64
	// window holds the stored endsAt and pendingShareExpiresAt values (nil: absent) that
	// every veto row update pins, so a concurrent writer cannot shrink an extension.
	window map[string]types.AttributeValue
	valid  bool
}

func (repo *CalendarRepository) readCalendarShareOwner(ctx context.Context, userID string, key ShareKey) (*calendarShareOwner, error) {
	result, err := repo.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(repo.tableName), Key: accountShareKey(key.AccountID, key.SK), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("read calendar share: %w", err)
	}
	if len(result.Item) == 0 {
		return nil, nil
	}
	var row CalendarShareRow
	if attributevalue.UnmarshalMap(result.Item, &row) != nil || row.PK != model.PrefixAccount+key.AccountID || row.SK != key.SK ||
		row.UserID != userID || uuid.Validate(row.SourceID) != nil || !calendarStorageID.MatchString(row.EventID) ||
		!(row.State == calendarShareStateShared && len(row.VetoEventIDs) == 0 ||
			row.State == calendarShareStateVeto && validCalendarVetoSet(row.VetoEventIDs)) {
		return &calendarShareOwner{}, nil
	}
	return &calendarShareOwner{sourceID: row.SourceID, eventID: row.EventID, state: row.State, vetoes: row.VetoEventIDs,
		endsAt: row.EndsAt, expiresAt: row.ExpiresAt, valid: true, window: map[string]types.AttributeValue{
			"endsAt": result.Item["endsAt"], "pendingShareExpiresAt": result.Item["pendingShareExpiresAt"]}}, nil
}

// calendarSharedOwnerCondition requires a shared row to still belong to the owner as read.
func calendarSharedOwnerCondition(userID string, owner *calendarShareOwner) expression.ConditionBuilder {
	return calendarScopeCondition(userID, owner.sourceID).
		And(expression.Name("eventId").Equal(expression.Value(owner.eventID))).
		And(expression.Name("state").Equal(expression.Value(calendarShareStateShared)))
}

// calendarVetoSetCondition requires a veto row whose set is exactly the members as read.
func calendarVetoSetCondition(userID string, members []string) expression.ConditionBuilder {
	set := expression.Name("vetoEventIds")
	condition := expression.Name("userId").Equal(expression.Value(userID)).
		And(expression.Name("state").Equal(expression.Value(calendarShareStateVeto))).
		And(set.Size().Equal(expression.Value(len(members))))
	for _, member := range members {
		condition = condition.And(expression.Contains(set, member))
	}
	return condition
}

// calendarVetoWindowCondition requires a veto row's window as read.
func calendarVetoWindowCondition(owner *calendarShareOwner) expression.ConditionBuilder {
	var condition expression.ConditionBuilder
	for index, name := range []string{"endsAt", "pendingShareExpiresAt"} {
		part := expression.AttributeNotExists(expression.Name(name))
		if value := owner.window[name]; value != nil {
			part = expression.Name(name).Equal(expression.Value(value))
		}
		if index == 0 {
			condition = part
		} else {
			condition = condition.And(part)
		}
	}
	return condition
}

func calendarStringSet(values []string) expression.ValueBuilder {
	return expression.Value(&types.AttributeValueMemberSS{Value: values})
}

// planCalendarShares resolves share keys for one transaction without failing on collisions.
// Every touched key is read once and gets at most one operation:
//   - Absent: veto claimants create a veto row holding all of them; otherwise the first
//     shared claimant creates a shared row.
//   - Shared row: veto claimants convert it to a veto row (owner- and state-scoped). Without
//     a veto claimant, an in-batch owner keeps it if it still wants it, else the first shared
//     claimant takes it, else a vacating owner deletes it; a foreign owner is left alone.
//   - Veto row: veto claimants ADD themselves; members of the set in this batch that vacate
//     the key or now want it shared are DELETEd. When one batch has both, a single SET
//     writes the final set under the set as read. The row is deleted (or, for a shared
//     claimant, replaced) only when its set as read would become empty. A shared claimant
//     never replaces a veto row that keeps members.
//   - Invalid row: a veto claimant returns ErrCalendarCorruptData; otherwise it is left alone.
//   - Every veto write spans the latest endsAt and TTL of the row as read and its veto
//     claimants, so the row outlives every member; an unchanged set whose window grows
//     is extended under the set as read. Veto row updates pin the window as read.
//
// It returns the operations and, per intent, whether its desired share row is held.
func (repo *CalendarRepository) planCalendarShares(ctx context.Context, userID, sourceID string, intents []calendarShareIntent) ([]types.TransactWriteItem, [][]bool, error) {
	type claimRef struct{ intent, slot int }
	granted := make([][]bool, len(intents))
	inBatch := make(map[string]int)
	var order []string
	claims := make(map[string][]claimRef)
	vacated := make(map[string]map[int]bool)
	keys := make(map[string]ShareKey)
	remember := func(key ShareKey) {
		if _, exists := keys[key.id()]; !exists {
			order = append(order, key.id())
			keys[key.id()] = key
			vacated[key.id()] = make(map[int]bool)
		}
	}
	for index, intent := range intents {
		granted[index] = make([]bool, len(intent.desired))
		inBatch[intent.eventID] = index
		for slot, claim := range intent.desired {
			remember(claim.key)
			claims[claim.key.id()] = append(claims[claim.key.id()], claimRef{index, slot})
		}
		for _, key := range intent.vacating {
			remember(key)
			vacated[key.id()][index] = true
		}
	}
	// wants returns the slot of an intent's claim on key id (any state when state is "").
	wants := func(index int, id, state string) (int, bool) {
		for slot, claim := range intents[index].desired {
			if claim.key.id() == id && (state == "" || claim.row.State == state) {
				return slot, true
			}
		}
		return -1, false
	}
	var operations []types.TransactWriteItem
	// vetoWindow is the latest window end and TTL among the veto claimants of a key and,
	// for an existing veto row, its stored ones, so the row outlives every member.
	vetoWindow := func(refs []claimRef, owner *calendarShareOwner) (time.Time, int64) {
		var endsAt time.Time
		var expiresAt int64
		if owner != nil {
			endsAt, expiresAt = owner.endsAt, owner.expiresAt
		}
		for _, ref := range refs {
			row := intents[ref.intent].desired[ref.slot].row
			if row.EndsAt.After(endsAt) {
				endsAt = row.EndsAt
			}
			expiresAt = max(expiresAt, row.ExpiresAt)
		}
		return endsAt.UTC(), expiresAt
	}
	// vetoRefs are the veto claimants of the key being planned; a veto put spans their window.
	var vetoRefs []claimRef
	put := func(key ShareKey, ref claimRef, vetoes []string, condition expression.ConditionBuilder) error {
		row := *intents[ref.intent].desired[ref.slot].row
		row.PK, row.SK = model.PrefixAccount+key.AccountID, key.SK
		row.StartsAt, row.EndsAt = row.StartsAt.UTC(), row.EndsAt.UTC()
		row.VetoEventIDs = vetoes
		if vetoes != nil {
			row.EndsAt, row.ExpiresAt = vetoWindow(vetoRefs, nil)
		}
		item, err := repo.calendarPut(row, condition)
		if err != nil {
			return err
		}
		operations = append(operations, types.TransactWriteItem{Put: item})
		return nil
	}
	conditional := func(key ShareKey, update *expression.UpdateBuilder, condition expression.ConditionBuilder) error {
		builder := expression.NewBuilder().WithCondition(condition)
		if update != nil {
			builder = builder.WithUpdate(*update)
		}
		built, err := builder.Build()
		if err != nil {
			return err
		}
		if update != nil {
			operations = append(operations, types.TransactWriteItem{Update: &types.Update{
				TableName: aws.String(repo.tableName), Key: accountShareKey(key.AccountID, key.SK), UpdateExpression: built.Update(),
				ConditionExpression: built.Condition(), ExpressionAttributeNames: built.Names(), ExpressionAttributeValues: built.Values(),
			}})
			return nil
		}
		operations = append(operations, types.TransactWriteItem{Delete: &types.Delete{
			TableName: aws.String(repo.tableName), Key: accountShareKey(key.AccountID, key.SK),
			ConditionExpression: built.Condition(), ExpressionAttributeNames: built.Names(), ExpressionAttributeValues: built.Values(),
		}})
		return nil
	}
	for _, id := range order {
		key := keys[id]
		owner, err := repo.readCalendarShareOwner(ctx, userID, key)
		if err != nil {
			return nil, nil, err
		}
		var vetoClaimants, sharedClaimants []claimRef
		for _, ref := range claims[id] {
			if intents[ref.intent].desired[ref.slot].row.State == calendarShareStateVeto {
				vetoClaimants = append(vetoClaimants, ref)
			} else {
				sharedClaimants = append(sharedClaimants, ref)
			}
		}
		if len(vetoClaimants) > calendarMaxVetoMembers {
			return nil, nil, ErrInvalidCalendarBatch
		}
		vetoRefs = vetoClaimants
		var vetoTokens []string
		for _, ref := range vetoClaimants {
			vetoTokens = append(vetoTokens, calendarVetoToken(sourceID, intents[ref.intent].eventID))
		}
		grantVetoes := func() {
			for _, ref := range vetoClaimants {
				granted[ref.intent][ref.slot] = true
			}
		}
		switch {
		case owner == nil:
			switch {
			case len(vetoClaimants) > 0:
				err = put(key, vetoClaimants[0], vetoTokens, expression.AttributeNotExists(expression.Name("PK")))
				grantVetoes()
			case len(sharedClaimants) > 0:
				err = put(key, sharedClaimants[0], nil, expression.AttributeNotExists(expression.Name("PK")))
				granted[sharedClaimants[0].intent][sharedClaimants[0].slot] = true
			}
		case !owner.valid:
			if len(vetoClaimants) > 0 {
				return nil, nil, ErrCalendarCorruptData
			}
		case owner.state == calendarShareStateShared:
			ownerIndex, ownerInBatch := -1, false
			if owner.sourceID == sourceID {
				ownerIndex, ownerInBatch = inBatch[owner.eventID]
			}
			condition := calendarSharedOwnerCondition(userID, owner)
			switch {
			case len(vetoClaimants) > 0:
				err = put(key, vetoClaimants[0], vetoTokens, condition)
				grantVetoes()
			case !ownerInBatch:
				// a foreign shared owner keeps its row
			case func() bool { _, ok := wants(ownerIndex, id, ""); return ok }():
				slot, _ := wants(ownerIndex, id, "")
				err = put(key, claimRef{ownerIndex, slot}, nil, condition)
				granted[ownerIndex][slot] = true
			case len(sharedClaimants) > 0:
				err = put(key, sharedClaimants[0], nil, condition)
				granted[sharedClaimants[0].intent][sharedClaimants[0].slot] = true
			case vacated[id][ownerIndex]:
				err = conditional(key, nil, condition)
			}
		default: // veto row
			members := make(map[string]bool)
			for _, member := range owner.vetoes {
				members[member] = true
			}
			var leavers, joiners []string
			for index, intent := range intents {
				token := calendarVetoToken(sourceID, intent.eventID)
				_, wantsShared := wants(index, id, calendarShareStateShared)
				if members[token] && (vacated[id][index] || wantsShared) {
					leavers = append(leavers, token)
				}
			}
			for _, token := range vetoTokens {
				if !members[token] {
					joiners = append(joiners, token)
				}
			}
			grantVetoes()
			if len(owner.vetoes)-len(leavers)+len(joiners) > calendarMaxVetoMembers {
				return nil, nil, ErrInvalidCalendarBatch
			}
			set := expression.Name("vetoEventIds")
			// A claimant whose window ends later than the row's extends it (TTL drift).
			endsAt, expiresAt := vetoWindow(vetoClaimants, owner)
			extend := func(update expression.UpdateBuilder) expression.UpdateBuilder {
				if expiresAt == owner.expiresAt && endsAt.Equal(owner.endsAt) {
					return update
				}
				return update.Set(expression.Name("endsAt"), expression.Value(endsAt)).
					Set(expression.Name("pendingShareExpiresAt"), expression.Value(expiresAt))
			}
			switch {
			case len(leavers) == 0 && len(joiners) == 0:
				if expiresAt != owner.expiresAt || !endsAt.Equal(owner.endsAt) {
					update := extend(expression.UpdateBuilder{})
					err = conditional(key, &update, calendarVetoSetCondition(userID, owner.vetoes).And(calendarVetoWindowCondition(owner)))
				}
			case len(joiners) > 0 && len(leavers) > 0:
				// DynamoDB rejects an UpdateExpression that names one path in two actions,
				// so a join and a leave on the same key SET the final set as read.
				leaving := make(map[string]bool, len(leavers))
				for _, leaver := range leavers {
					leaving[leaver] = true
				}
				var final []string
				for _, member := range owner.vetoes {
					if !leaving[member] {
						final = append(final, member)
					}
				}
				final = append(final, joiners...)
				update := extend(expression.Set(set, calendarStringSet(final)))
				err = conditional(key, &update, calendarVetoSetCondition(userID, owner.vetoes).And(calendarVetoWindowCondition(owner)))
			case len(owner.vetoes)-len(leavers)+len(joiners) > 0:
				condition := expression.Name("userId").Equal(expression.Value(userID)).
					And(expression.Name("state").Equal(expression.Value(calendarShareStateVeto))).And(calendarVetoWindowCondition(owner))
				var update expression.UpdateBuilder
				if len(joiners) > 0 {
					update = extend(expression.Add(set, calendarStringSet(joiners)))
				}
				if len(leavers) > 0 {
					update = extend(expression.Delete(set, calendarStringSet(leavers)))
					for _, leaver := range leavers {
						condition = condition.And(expression.Contains(set, leaver))
					}
					condition = condition.And(set.Size().GreaterThan(expression.Value(len(leavers))))
				}
				err = conditional(key, &update, condition)
			case len(sharedClaimants) > 0:
				err = put(key, sharedClaimants[0], nil, calendarVetoSetCondition(userID, owner.vetoes))
				granted[sharedClaimants[0].intent][sharedClaimants[0].slot] = true
			default:
				err = conditional(key, nil, calendarVetoSetCondition(userID, owner.vetoes))
			}
		}
		if err != nil {
			return nil, nil, err
		}
	}
	return operations, granted, nil
}

// calendarShareVersionCondition fences an event write on the share version read before it.
func calendarShareVersionCondition(old *CalendarEventRow) expression.ConditionBuilder {
	if old == nil || old.ShareVersion == 0 {
		return expression.AttributeNotExists(expression.Name("shareVersion"))
	}
	return expression.Name("shareVersion").Equal(expression.Value(old.ShareVersion))
}

// calendarFencedConditionError maps a cancelled batch: a source fence failure is
// ErrConditionFailed; otherwise a failure on a fenced event/share operation is
// ErrCalendarShareStateChanged. It returns nil when no condition failed.
func calendarFencedConditionError(err error, fenced map[int]bool) error {
	var cancellation *types.TransactionCanceledException
	if !errors.As(err, &cancellation) {
		return nil
	}
	reasons := cancellation.CancellationReasons
	failed := func(index int) bool {
		return index < len(reasons) && aws.ToString(reasons[index].Code) == "ConditionalCheckFailed"
	}
	if failed(0) {
		return errors.Join(ErrConditionFailed, err)
	}
	for index := range reasons {
		if fenced[index] && failed(index) {
			return errors.Join(ErrCalendarShareStateChanged, err)
		}
	}
	for index := range reasons {
		if failed(index) {
			return errors.Join(ErrConditionFailed, err)
		}
	}
	return nil
}

func validCalendarShareRow(row CalendarShareRow, accountID string, now int64) bool {
	if row.PK != model.PrefixAccount+accountID || row.AccountID != accountID || !calendarStorageID.MatchString(row.MeetingKey) ||
		!calendarStorageID.MatchString(row.UserID) || uuid.Validate(row.SourceID) != nil || !calendarStorageID.MatchString(row.EventID) ||
		row.SK != model.CalendarShareSortKey(row.StartsAt, row.MeetingKey, row.UserID) || !validCalendarWindow(row.StartsAt, row.EndsAt) ||
		row.ExpiresAt != row.EndsAt.Add(90*24*time.Hour).Unix() || row.ExpiresAt <= now {
		return false
	}
	switch row.State {
	case calendarShareStateShared:
		return row.Ciphertext != ""
	case calendarShareStateVeto:
		return row.Ciphertext == "" && validCalendarVetoSet(row.VetoEventIDs)
	}
	return false
}

// ListCalendarShareRows reads share rows overlapping the window, grouped by meetingKey.
// Groups are contiguous in the sort key; a page always ends after a complete group, so a
// veto row is never separated from the shared rows it hides, and a page resumed from a cursor
// skips the rest of the cursor's group. A single group larger than the limit is returned
// whole; a group that does not end within five queries is an error.
func (repo *CalendarRepository) ListCalendarShareRows(ctx context.Context, accountID string, query model.CalendarQuery) (*CalendarSharePage, error) {
	limit, err := calendarQueryLimit(accountID, query.Limit)
	if err != nil || !validCalendarWindow(query.From, query.To) || query.AccountID != "" && query.AccountID != accountID {
		return nil, ErrInvalidCalendarQuery
	}
	scope := calendarCursor{Version: 1, Kind: "shares", From: query.From.UTC().Format(time.RFC3339Nano),
		To: query.To.UTC().Format(time.RFC3339Nano), AccountID: accountID, Limit: limit}
	lower := model.PrefixCalendarShare + query.From.Add(-calendarMaxWindow).UTC().Format("2006-01-02T15:04:05.000000000Z")
	upper := model.PrefixCalendarShare + query.To.UTC().Format("2006-01-02T15:04:05.000000000Z")
	startKey, err := decodeCalendarCursor(query.Cursor, scope, lower, upper)
	if err != nil {
		return nil, err
	}
	// A page never begins with a partial group: rows after the cursor that share its
	// meetingKey prefix were added after the previous page returned that group, or the
	// cursor points into a group, so they are skipped rather than returned without their veto.
	var skipPrefix string
	if cursorSK, ok := startKey["SK"].(*types.AttributeValueMemberS); ok {
		skipPrefix = cursorSK.Value[:strings.LastIndex(cursorSK.Value, "#")+1]
	}
	keyCondition := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + accountID)).
		And(expression.Key("SK").Between(expression.Value(lower), expression.Value(upper)))
	built, err := expression.NewBuilder().WithKeyCondition(keyCondition).Build()
	if err != nil {
		return nil, err
	}
	page := &CalendarSharePage{Groups: make([][]CalendarShareRow, 0)}
	now := repo.now().Unix()
	kept := 0
	var groupPrefix, groupLastSK, completeSK string
	var group []CalendarShareRow
	var groupSKs []string
	complete := func() {
		if groupPrefix == "" {
			return
		}
		for _, row := range group {
			if row.StartsAt.Before(query.To) && row.EndsAt.After(query.From) {
				page.Groups = append(page.Groups, group)
				groupSKs = append(groupSKs, groupLastSK)
				kept += len(group)
				break
			}
		}
		completeSK, groupPrefix, group = groupLastSK, "", nil
	}
	finish := func() error {
		for _, sortKey := range groupSKs {
			cursor, err := encodeCalendarCursor(scope, accountShareKey(accountID, sortKey), lower, upper)
			if err != nil || cursor == nil {
				return ErrInvalidCalendarCursor
			}
			page.GroupCursors = append(page.GroupCursors, *cursor)
		}
		return nil
	}
	cursorAt := func(sortKey string) error {
		page.NextCursor, err = encodeCalendarCursor(scope, accountShareKey(accountID, sortKey), lower, upper)
		if err != nil {
			return err
		}
		return finish()
	}
	for pageNumber := 0; pageNumber < 5; pageNumber++ {
		result, err := repo.client.Query(ctx, &dynamodb.QueryInput{
			TableName: aws.String(repo.tableName), KeyConditionExpression: built.KeyCondition(),
			ExpressionAttributeNames: built.Names(), ExpressionAttributeValues: built.Values(),
			ConsistentRead: aws.Bool(true), ScanIndexForward: aws.Bool(true), Limit: aws.Int32(calendarShareQueryPage), ExclusiveStartKey: startKey,
		})
		if err != nil {
			return nil, fmt.Errorf("query calendar shares: %w", err)
		}
		for _, item := range result.Items {
			sortKey, ok := item["SK"].(*types.AttributeValueMemberS)
			if !ok || sortKey.Value < lower || sortKey.Value > upper {
				continue
			}
			prefix := sortKey.Value[:strings.LastIndex(sortKey.Value, "#")+1]
			if skipPrefix != "" {
				if prefix == skipPrefix {
					continue
				}
				skipPrefix = ""
			}
			if groupPrefix != "" && prefix != groupPrefix {
				complete()
				if kept >= int(limit) {
					return calendarSharePageOrError(page, cursorAt(completeSK))
				}
			}
			groupPrefix, groupLastSK = prefix, sortKey.Value
			var row CalendarShareRow
			if attributevalue.UnmarshalMap(item, &row) == nil && validCalendarShareRow(row, accountID, now) {
				group = append(group, row)
			}
		}
		startKey = result.LastEvaluatedKey
		if len(startKey) == 0 {
			complete()
			return calendarSharePageOrError(page, finish())
		}
	}
	switch {
	case completeSK != "":
		return calendarSharePageOrError(page, cursorAt(completeSK))
	case groupPrefix != "":
		return nil, ErrCalendarShareGroupTooLarge
	}
	page.NextCursor, err = encodeCalendarCursor(scope, startKey, lower, upper)
	if err != nil {
		return nil, err
	}
	return calendarSharePageOrError(page, finish())
}

func calendarSharePageOrError(page *CalendarSharePage, err error) (*CalendarSharePage, error) {
	if err != nil {
		return nil, err
	}
	return page, nil
}

func validCalendarOverrideScope(userID, sourceID, scopeKey string) bool {
	return calendarStorageID.MatchString(userID) && uuid.Validate(sourceID) == nil && calendarStorageID.MatchString(scopeKey)
}

func validCalendarOverrideMode(mode, accountID string) bool {
	switch mode {
	case "account":
		return calendarStorageID.MatchString(accountID)
	case "hidden":
		return accountID == ""
	}
	return false
}

func (repo *CalendarRepository) GetCalendarOverride(ctx context.Context, userID, sourceID, scopeKey string) (*CalendarOverrideRow, error) {
	if !validCalendarOverrideScope(userID, sourceID, scopeKey) {
		return nil, ErrInvalidCalendarQuery
	}
	sortKey := model.CalendarOverrideSortKey(sourceID, scopeKey)
	item, err := repo.calendarGet(ctx, userID, sortKey)
	if err != nil || len(item) == 0 {
		return nil, err
	}
	var row CalendarOverrideRow
	if attributevalue.UnmarshalMap(item, &row) != nil || row.PK != model.PrefixUser+userID || row.SK != sortKey ||
		row.UserID != userID || row.SourceID != sourceID || row.ScopeKey != scopeKey || !validCalendarOverrideMode(row.Mode, row.AccountID) {
		return nil, ErrCalendarCorruptData
	}
	return &row, nil
}

func (repo *CalendarRepository) PutCalendarOverride(ctx context.Context, userID, sourceID, scopeKey string, override CalendarOverrideRow) error {
	if !validCalendarOverrideScope(userID, sourceID, scopeKey) || !validCalendarOverrideMode(override.Mode, override.AccountID) {
		return ErrInvalidCalendarBatch
	}
	row := CalendarOverrideRow{PK: model.PrefixUser + userID, SK: model.CalendarOverrideSortKey(sourceID, scopeKey),
		UserID: userID, SourceID: sourceID, ScopeKey: scopeKey, Mode: override.Mode, AccountID: override.AccountID}
	put, err := repo.calendarPut(row, expression.AttributeNotExists(expression.Name("PK")).Or(calendarScopeCondition(userID, sourceID)))
	if err != nil {
		return err
	}
	return repo.calendarTransact(ctx, []types.TransactWriteItem{{Put: put}})
}

func (repo *CalendarRepository) DeleteCalendarOverride(ctx context.Context, userID, sourceID, scopeKey string) error {
	if !validCalendarOverrideScope(userID, sourceID, scopeKey) {
		return ErrInvalidCalendarBatch
	}
	condition := expression.AttributeNotExists(expression.Name("PK")).Or(calendarScopeCondition(userID, sourceID))
	built, err := expression.NewBuilder().WithCondition(condition).Build()
	if err != nil {
		return err
	}
	return repo.calendarTransact(ctx, []types.TransactWriteItem{{Delete: &types.Delete{
		TableName: aws.String(repo.tableName), Key: calendarKey(userID, model.CalendarOverrideSortKey(sourceID, scopeKey)),
		ConditionExpression: built.Condition(), ExpressionAttributeNames: built.Names(), ExpressionAttributeValues: built.Values(),
	}}})
}

// CalendarShareChange is one occurrence's requested share state for SetCalendarShareState.
type CalendarShareChange struct {
	// Expected is the event row version the caller planned from; a different stored
	// version returns ErrCalendarShareStateChanged before any write.
	Expected CalendarRowVersion
	// Share is the share or veto row; nil clears sharing.
	Share *CalendarShareRow
	// ExtraVeto is a second veto in another account; it requires Share to be a veto.
	ExtraVeto *CalendarShareRow
	// Previous is a share key the caller saw granted; it is deleted unless still desired.
	Previous *ShareKey
}

// SetCalendarShareState changes one occurrence's share in a single transaction: the event row's
// share fields (fenced on its current revision and share version, which it increments) plus the
// share puts and stale share deletes. A key owned by another event of the caller is not taken by
// a share (the event then records no share); a veto converts a shared row or joins a veto row's
// set (ErrCalendarShareStateChanged if it cannot be held). A changed event row returns
// ErrConditionFailed; a changed share row or a version other than change.Expected returns
// ErrCalendarShareStateChanged.
func (repo *CalendarRepository) SetCalendarShareState(ctx context.Context, userID, sourceID, eventID string, change CalendarShareChange) error {
	share, extra, previous := change.Share, change.ExtraVeto, change.Previous
	if !calendarStorageID.MatchString(userID) || uuid.Validate(sourceID) != nil || !calendarStorageID.MatchString(eventID) ||
		previous != nil && !validCalendarShareKey(*previous, userID) {
		return ErrInvalidCalendarBatch
	}
	old, err := repo.getCalendarEventRow(ctx, userID, sourceID, eventID)
	if err != nil {
		return err
	}
	if old == nil {
		return ErrCalendarEventNotFound
	}
	if !validCalendarEventIdentity(old, userID, sourceID, eventID) {
		return ErrCalendarCorruptData
	}
	if change.Expected != CalendarRowVersionOf(old) {
		return ErrCalendarShareStateChanged
	}
	if share != nil && !validCalendarShareInput(share, userID, sourceID, eventID, old.StartsAt, old.EndsAt, old.ExpiresAt) ||
		extra != nil && !validCalendarExtraVeto(share, extra, userID, sourceID, eventID, old.StartsAt, old.EndsAt, old.ExpiresAt) {
		return ErrInvalidCalendarBatch
	}
	intent, err := newCalendarShareIntent(userID, eventID, old, []*CalendarShareRow{share, extra}, previous)
	if err != nil {
		return err
	}
	shareOperations, granted, err := repo.planCalendarShares(ctx, userID, sourceID, []calendarShareIntent{intent})
	if err != nil {
		return err
	}
	shareHeld := calendarIntentGranted(intent, granted[0], share)
	extraHeld := calendarIntentGranted(intent, granted[0], extra)
	if share != nil && share.State == calendarShareStateVeto && !shareHeld || extra != nil && !extraHeld {
		return ErrCalendarShareStateChanged // a requested veto could not be written
	}
	update := expression.Set(expression.Name("shareVersion"), expression.Value(old.ShareVersion+1))
	if share != nil && shareHeld {
		update = update.Set(expression.Name("shareAccountId"), expression.Value(share.AccountID)).
			Set(expression.Name("meetingKey"), expression.Value(share.MeetingKey)).
			Set(expression.Name("shareState"), expression.Value(share.State))
	} else {
		update = update.Remove(expression.Name("shareAccountId")).Remove(expression.Name("meetingKey")).Remove(expression.Name("shareState"))
	}
	if extra != nil {
		update = update.Set(expression.Name("extraVetoAccountId"), expression.Value(extra.AccountID)).
			Set(expression.Name("extraVetoMeetingKey"), expression.Value(extra.MeetingKey))
	} else {
		update = update.Remove(expression.Name("extraVetoAccountId")).Remove(expression.Name("extraVetoMeetingKey"))
	}
	condition := expression.AttributeExists(expression.Name("PK")).
		And(calendarScopeCondition(userID, sourceID)).
		And(expression.Name("eventId").Equal(expression.Value(eventID))).
		And(expression.Name("revision").Equal(expression.Value(old.Revision))).
		And(calendarShareVersionCondition(old))
	built, err := expression.NewBuilder().WithUpdate(update).WithCondition(condition).Build()
	if err != nil {
		return err
	}
	operations := []types.TransactWriteItem{{Update: &types.Update{
		TableName: aws.String(repo.tableName), Key: calendarKey(userID, model.CalendarEventSortKey(sourceID, eventID)),
		UpdateExpression: built.Update(), ConditionExpression: built.Condition(),
		ExpressionAttributeNames: built.Names(), ExpressionAttributeValues: built.Values(),
	}}}
	fenced := make(map[int]bool)
	for index := range shareOperations {
		fenced[len(operations)+index] = true
	}
	return repo.calendarTransactFenced(ctx, append(operations, shareOperations...), fenced)
}

// calendarTransactFenced is calendarTransact with share-fence error mapping: a failed first
// operation is ErrConditionFailed, a failed fenced operation ErrCalendarShareStateChanged.
func (repo *CalendarRepository) calendarTransactFenced(ctx context.Context, operations []types.TransactWriteItem, fenced map[int]bool) error {
	if len(operations) > calendarMaxTransactItems {
		return ErrInvalidCalendarBatch
	}
	_, err := repo.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: operations},
		func(options *dynamodb.Options) { options.Retryer = aws.NopRetryer{} })
	if err == nil {
		return nil
	}
	if mapped := calendarFencedConditionError(err, fenced); mapped != nil {
		return mapped
	}
	return fmt.Errorf("calendar transaction: %w", err)
}

// calendarTransact sends one non-retried transaction and maps condition failures.
func (repo *CalendarRepository) calendarTransact(ctx context.Context, operations []types.TransactWriteItem) error {
	if len(operations) > calendarMaxTransactItems {
		return ErrInvalidCalendarBatch
	}
	_, err := repo.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: operations},
		func(options *dynamodb.Options) { options.Retryer = aws.NopRetryer{} })
	if err == nil {
		return nil
	}
	if calendarConditionFailed(err) {
		return errors.Join(ErrConditionFailed, err)
	}
	return fmt.Errorf("calendar transaction: %w", err)
}

func calendarConditionFailed(err error) bool {
	var cancellation *types.TransactionCanceledException
	if errors.As(err, &cancellation) {
		for _, reason := range cancellation.CancellationReasons {
			if aws.ToString(reason.Code) == "ConditionalCheckFailed" {
				return true
			}
		}
	}
	return false
}
