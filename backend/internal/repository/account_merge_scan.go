package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// Kinds of user-partition rows the account merge CLI rewrites.
const (
	MergeUserRowSpecReqTask = "specreqTask"
	MergeUserRowCalendar    = "calendarEvent"
	MergeUserRowMeeting     = "meeting"
)

const mergeScanLimit = 500

// mergeUserRowID is the identifier shape of user, meeting, source and event
// IDs in user-partition sort keys.
var mergeUserRowID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// MergeUserRow is the metadata of one user-partition row classified to the
// source account. Calendar ciphertext is carried sealed; no row text is read.
type MergeUserRow struct {
	Kind   string
	PK     string
	SK     string
	UserID string

	// SpecReq task and calendar event fields.
	EventID string
	Version int64

	// Calendar event fields.
	SourceID       string
	Ciphertext     string
	Revision       int64
	ShareAccountID string

	// Meeting fields.
	MeetingID       string
	SharedToAccount bool
	Date            time.Time
	Title           string
}

// MergeUserRowsPage is one page of an operator scan for user-partition rows
// classified to an account. Invalid counts rows the filter matched that fail
// key validation; they are never returned.
type MergeUserRowsPage struct {
	Rows       []MergeUserRow
	Invalid    int
	NextCursor string
}

type mergeUserRowItem struct {
	PK              string    `dynamodbav:"PK"`
	SK              string    `dynamodbav:"SK"`
	UserID          string    `dynamodbav:"userId"`
	AccountID       string    `dynamodbav:"accountId"`
	EventID         string    `dynamodbav:"eventId"`
	SourceID        string    `dynamodbav:"sourceId"`
	Ciphertext      string    `dynamodbav:"ciphertext"`
	Revision        int64     `dynamodbav:"revision"`
	Version         int64     `dynamodbav:"version"`
	ShareAccountID  string    `dynamodbav:"shareAccountId"`
	MeetingID       string    `dynamodbav:"meetingId"`
	SharedToAccount bool      `dynamodbav:"sharedToAccount"`
	Date            time.Time `dynamodbav:"date"`
	Title           string    `dynamodbav:"title"`
}

// ScanMergeUserRowsPage reads one page of the operator scan for SRTASK#,
// CALEVENT# and MEETING# rows in user partitions whose accountId is
// accountID. It is the single-segment form of ScanMergeUserRowsSegmentPage.
func (r *DynamoDBRepository) ScanMergeUserRowsPage(ctx context.Context, accountID, cursor string) (*MergeUserRowsPage, error) {
	return r.ScanMergeUserRowsSegmentPage(ctx, accountID, 0, 1, cursor)
}

// ScanMergeUserRowsSegmentPage reads one page of segment (of segments) of the
// same scan. Scan's evaluated-item limit also applies to empty filtered
// pages, so a page may be empty with a continuation.
func (r *DynamoDBRepository) ScanMergeUserRowsSegmentPage(ctx context.Context, accountID string, segment, segments int, cursor string) (*MergeUserRowsPage, error) {
	if !validMergeAccountID(accountID) || segments < 1 || segments > 16 || segment < 0 || segment >= segments {
		return nil, fmt.Errorf("invalid merge user scan")
	}
	var start map[string]types.AttributeValue
	if cursor != "" {
		var key [2]string
		raw, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
		if err != nil || len(cursor) > 4096 || json.Unmarshal(raw, &key) != nil || key[0] == "" || key[1] == "" {
			return nil, fmt.Errorf("invalid merge user scan cursor")
		}
		start = map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: key[0]}, "SK": &types.AttributeValueMemberS{Value: key[1]}}
	}
	sk := expression.Name("SK")
	filter := expression.Name("accountId").Equal(expression.Value(accountID)).
		And(expression.Name("PK").BeginsWith(model.PrefixUser)).
		And(expression.Or(sk.BeginsWith(model.PrefixSpecReqTask), sk.BeginsWith(model.PrefixCalendarEvent), sk.BeginsWith(model.PrefixMeeting)))
	projection := expression.NamesList(expression.Name("PK"), expression.Name("SK"), expression.Name("userId"), expression.Name("accountId"),
		expression.Name("eventId"), expression.Name("sourceId"), expression.Name("ciphertext"), expression.Name("revision"), expression.Name("version"),
		expression.Name("shareAccountId"), expression.Name("meetingId"), expression.Name("sharedToAccount"), expression.Name("date"), expression.Name("title"))
	expr, err := expression.NewBuilder().WithFilter(filter).WithProjection(projection).Build()
	if err != nil {
		return nil, err
	}
	input := &dynamodb.ScanInput{TableName: aws.String(r.tableName), Limit: aws.Int32(mergeScanLimit), ConsistentRead: aws.Bool(true), ExclusiveStartKey: start,
		FilterExpression: expr.Filter(), ProjectionExpression: expr.Projection(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()}
	if segments > 1 {
		input.Segment, input.TotalSegments = aws.Int32(int32(segment)), aws.Int32(int32(segments))
	}
	result, err := r.client.Scan(ctx, input)
	if err != nil {
		return nil, err
	}
	page := &MergeUserRowsPage{Rows: []MergeUserRow{}}
	var items []mergeUserRowItem
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &items); err != nil {
		return nil, err
	}
	for _, item := range items {
		row, ok := classifyMergeUserRow(accountID, item)
		if !ok {
			page.Invalid++
			continue
		}
		page.Rows = append(page.Rows, row)
	}
	if len(result.LastEvaluatedKey) > 0 {
		pk, pkOK := result.LastEvaluatedKey["PK"].(*types.AttributeValueMemberS)
		skv, skOK := result.LastEvaluatedKey["SK"].(*types.AttributeValueMemberS)
		if !pkOK || !skOK || pk.Value == "" || skv.Value == "" || len(result.LastEvaluatedKey) != 2 {
			return nil, fmt.Errorf("invalid merge user scan continuation")
		}
		raw, _ := json.Marshal([2]string{pk.Value, skv.Value})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
		if page.NextCursor == cursor {
			return nil, fmt.Errorf("non-advancing merge user scan continuation")
		}
	}
	return page, nil
}

// classifyMergeUserRow validates a scanned item against its own keys and
// returns the typed row. A row whose keys and identifiers disagree, or whose
// accountId is not accountID, is rejected.
func classifyMergeUserRow(accountID string, item mergeUserRowItem) (MergeUserRow, bool) {
	userID := strings.TrimPrefix(item.PK, model.PrefixUser)
	if item.AccountID != accountID || userID == item.PK || !mergeUserRowID.MatchString(userID) || item.UserID != userID {
		return MergeUserRow{}, false
	}
	row := MergeUserRow{PK: item.PK, SK: item.SK, UserID: userID}
	switch {
	case strings.HasPrefix(item.SK, model.PrefixSpecReqTask):
		row.Kind, row.EventID, row.Version = MergeUserRowSpecReqTask, strings.TrimPrefix(item.SK, model.PrefixSpecReqTask), item.Version
		return row, mergeUserRowID.MatchString(row.EventID) && item.EventID == row.EventID
	case strings.HasPrefix(item.SK, model.PrefixCalendarEvent):
		row.Kind, row.EventID, row.SourceID = MergeUserRowCalendar, item.EventID, item.SourceID
		row.Ciphertext, row.Revision, row.ShareAccountID = item.Ciphertext, item.Revision, item.ShareAccountID
		return row, mergeUserRowID.MatchString(row.EventID) && mergeUserRowID.MatchString(row.SourceID) &&
			item.SK == model.CalendarEventSortKey(row.SourceID, row.EventID) && row.Ciphertext != "" && row.Revision > 0
	case strings.HasPrefix(item.SK, model.PrefixMeeting):
		row.Kind, row.MeetingID = MergeUserRowMeeting, strings.TrimPrefix(item.SK, model.PrefixMeeting)
		row.SharedToAccount, row.Date, row.Title = item.SharedToAccount, item.Date, item.Title
		return row, mergeUserRowID.MatchString(row.MeetingID) && item.MeetingID == row.MeetingID
	}
	return MergeUserRow{}, false
}

// MeetingRefForMergeRow builds the source account's reference to a scanned
// meeting row, in the shape the meeting mover requires.
func MeetingRefForMergeRow(accountID string, row MergeUserRow) *model.MeetingRef {
	return &model.MeetingRef{PK: model.PrefixAccount + accountID,
		SK:        model.PrefixMeetingRef + row.Date.UTC().Format(time.RFC3339) + "#" + row.MeetingID,
		AccountID: accountID, MeetingID: row.MeetingID, OwnerUserID: row.UserID, Title: row.Title, Date: row.Date,
		EntityType: model.EntityTypeMeetingRef}
}

// MoveSpecReqTaskRowForMerge re-points a scanned SpecReq task from the source
// account to the target. The update is conditioned on accountId still naming
// the source and on the observed version, which it increments so a task
// runner holding an older copy cannot write it back. A row changed or moved
// since the scan returns ErrConditionFailed.
func (r *DynamoDBRepository) MoveSpecReqTaskRowForMerge(ctx context.Context, fromID, toID string, row MergeUserRow) error {
	if !validMergeAccountID(fromID) || !validMergeAccountID(toID) || fromID == toID || row.Kind != MergeUserRowSpecReqTask ||
		row.PK != model.PrefixUser+row.UserID || row.SK != model.SpecReqTaskSortKey(row.EventID) {
		return fmt.Errorf("invalid merge task move")
	}
	update := expression.Set(expression.Name("accountId"), expression.Value(toID)).Add(expression.Name("version"), expression.Value(1))
	cond := expression.AttributeExists(expression.Name("PK")).And(expression.Name("accountId").Equal(expression.Value(fromID))).
		And(expression.Name("version").Equal(expression.Value(row.Version)))
	return r.updateMergeUserRow(ctx, row, update, cond)
}

// MoveCalendarEventRowForMerge re-points a scanned calendar event from the
// source account to the target and replaces its sealed payload with
// ciphertext, which the caller built from the payload with the same account
// replacement. shareAccountId moves only when it names the source. The update
// is conditioned on accountId, revision, the observed ciphertext and the
// observed share account, so a concurrent calendar sync or share change
// returns ErrConditionFailed and nothing is written.
func (r *DynamoDBRepository) MoveCalendarEventRowForMerge(ctx context.Context, fromID, toID string, row MergeUserRow, ciphertext string) error {
	if !validMergeAccountID(fromID) || !validMergeAccountID(toID) || fromID == toID || row.Kind != MergeUserRowCalendar || ciphertext == "" ||
		row.PK != model.PrefixUser+row.UserID || row.SK != model.CalendarEventSortKey(row.SourceID, row.EventID) {
		return fmt.Errorf("invalid merge calendar move")
	}
	update := expression.Set(expression.Name("accountId"), expression.Value(toID)).Set(expression.Name("ciphertext"), expression.Value(ciphertext))
	cond := expression.AttributeExists(expression.Name("PK")).And(expression.Name("accountId").Equal(expression.Value(fromID))).
		And(expression.Name("revision").Equal(expression.Value(row.Revision))).
		And(expression.Name("ciphertext").Equal(expression.Value(row.Ciphertext)))
	if row.ShareAccountID == "" {
		cond = cond.And(expression.AttributeNotExists(expression.Name("shareAccountId")))
	} else {
		cond = cond.And(expression.Name("shareAccountId").Equal(expression.Value(row.ShareAccountID)))
		if row.ShareAccountID == fromID {
			update = update.Set(expression.Name("shareAccountId"), expression.Value(toID))
		}
	}
	return r.updateMergeUserRow(ctx, row, update, cond)
}

func (r *DynamoDBRepository) updateMergeUserRow(ctx context.Context, row MergeUserRow, update expression.UpdateBuilder, cond expression.ConditionBuilder) error {
	expr, err := expression.NewBuilder().WithUpdate(update).WithCondition(cond).Build()
	if err != nil {
		return fmt.Errorf("build merge user row update: %w", err)
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{TableName: aws.String(r.tableName),
		Key:              map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: row.PK}, "SK": &types.AttributeValueMemberS{Value: row.SK}},
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()})
	var failed *types.ConditionalCheckFailedException
	if errors.As(err, &failed) {
		return fmt.Errorf("merge user row: %w", ErrConditionFailed)
	}
	if err != nil {
		return fmt.Errorf("merge user row: %w", err)
	}
	return nil
}
