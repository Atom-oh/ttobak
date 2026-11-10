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
	// ErrCalendarShareStateChanged reports that an event's share state changed after a sync
	// batch read it; the caller may recompute and retry.
	ErrCalendarShareStateChanged = errors.New("calendar share state changed")
)

const (
	calendarShareStateShared = "shared"
	calendarShareStateVeto   = "veto"
	calendarShareMaxEmail    = 320
	calendarMaxTransactItems = 100
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
	if mutation.Expected == nil && (mutation.Share != nil || mutation.PreviousShareKey != nil) {
		return false
	}
	if mutation.Op != "upsert" {
		return mutation.Share == nil
	}
	row := mutation.Event
	if row.SeriesKey != "" && !calendarStorageID.MatchString(row.SeriesKey) {
		return false
	}
	if mutation.Share == nil {
		return row.ShareState == "" && row.ShareAccountID == "" && row.MeetingKey == ""
	}
	share := mutation.Share
	return validCalendarShareInput(share, userID, sourceID, mutation.EventID, row.StartsAt, row.EndsAt, row.ExpiresAt) &&
		row.ShareAccountID == share.AccountID && row.MeetingKey == share.MeetingKey && row.ShareState == share.State
}

// storedCalendarShareKeys returns the share key an existing event row currently owns.
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
	return keys, nil
}

// calendarRowHoldsShare reports whether a stored event row records a share or veto.
func calendarRowHoldsShare(old *CalendarEventRow) bool {
	return old != nil && (old.ShareState != "" || old.ShareAccountID != "" || old.MeetingKey != "")
}

// calendarShareClaim is one share or veto row an event wants to hold.
type calendarShareClaim struct {
	row *CalendarShareRow
	key ShareKey
}

// calendarShareIntent is one event's desired share keys and the keys it may vacate.
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
	if len(tokens) == 0 {
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
	valid    bool
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
	return &calendarShareOwner{sourceID: row.SourceID, eventID: row.EventID, state: row.State, vetoes: row.VetoEventIDs, valid: true}, nil
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
	put := func(key ShareKey, ref claimRef, vetoes []string, condition expression.ConditionBuilder) error {
		row := *intents[ref.intent].desired[ref.slot].row
		row.PK, row.SK = model.PrefixAccount+key.AccountID, key.SK
		row.StartsAt, row.EndsAt = row.StartsAt.UTC(), row.EndsAt.UTC()
		row.VetoEventIDs = vetoes
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
			set := expression.Name("vetoEventIds")
			switch {
			case len(leavers) == 0 && len(joiners) == 0:
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
				update := expression.Set(set, calendarStringSet(final))
				err = conditional(key, &update, calendarVetoSetCondition(userID, owner.vetoes))
			case len(owner.vetoes)-len(leavers)+len(joiners) > 0:
				condition := expression.Name("userId").Equal(expression.Value(userID)).
					And(expression.Name("state").Equal(expression.Value(calendarShareStateVeto)))
				var update expression.UpdateBuilder
				if len(joiners) > 0 {
					update = expression.Add(set, calendarStringSet(joiners))
				}
				if len(leavers) > 0 {
					update = expression.Delete(set, calendarStringSet(leavers))
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
