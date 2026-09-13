package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

var summaryMeetingFields = []string{"meetingId", "userId", "status", "content", "notes",
	"transcriptA", "transcriptB", "selectedTranscript", "transcriptSegments", "accountId", "sharedToAccount"}

var summaryAttachmentFields = []string{"attachmentId", "meetingId", "userId", "originalKey", "processedKey", "type", "status", "fileName", "processedContent", "description"}
var summaryTextFields = []string{"runId", "status", "leaseUntil", "sourceKey", "ownerId", "uploaderId", "sourceETag", "resultKey", "unitCount", "complete", "updatedAt"}

func (r *DynamoDBRepository) summaryRows(ctx context.Context, meetingID, prefix string, limit int) ([]map[string]types.AttributeValue, error) {
	expr, err := expression.NewBuilder().WithKeyCondition(expression.Key("PK").Equal(expression.Value(model.PrefixMeeting + meetingID)).
		And(expression.Key("SK").BeginsWith(prefix))).Build()
	if err != nil {
		return nil, err
	}
	pages := dynamodb.NewQueryPaginator(r.client, &dynamodb.QueryInput{TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true), Limit: aws.Int32(int32(limit + 1)),
		KeyConditionExpression: expr.KeyCondition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()})
	var rows []map[string]types.AttributeValue
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		rows = append(rows, page.Items...)
		if len(rows) > limit {
			return nil, ErrSummaryLimit
		}
	}
	return rows, nil
}

// CaptureResummary performs metadata-only reads and records exact conditions
// for the source plus current edit grant. No transcript S3 hydration occurs.
func (r *DynamoDBRepository) CaptureResummary(ctx context.Context, ownerID, meetingID, requester string) (*model.SummarySnapshot, error) {
	snapshot, err := r.captureMeetingSummaryMetadata(ctx, ownerID, meetingID, summaryMeetingFields)
	if err != nil || snapshot == nil {
		return nil, err
	}
	if requester != ownerID {
		shareRow, err := r.summaryRow(ctx, model.PrefixUser+requester, model.PrefixShare+meetingID)
		if err != nil {
			return nil, err
		}
		var share model.Share
		if err := attributevalue.UnmarshalMap(shareRow, &share); err != nil {
			return nil, err
		}
		if share.Permission != model.PermissionEdit || share.OwnerID != ownerID || share.MeetingID != meetingID {
			return nil, ErrSummaryAccess
		}
		check, err := summaryCheck(model.PrefixUser+requester, model.PrefixShare+meetingID, shareRow, []string{"meetingId", "ownerId", "permission", "origin", "accountId"})
		if err != nil {
			return nil, err
		}
		snapshot.Checks = append(snapshot.Checks, check)
		if share.Origin == model.ShareOriginAccount {
			meeting := snapshot.Meeting
			if !meeting.SharedToAccount || meeting.AccountID == "" || (share.AccountID != "" && share.AccountID != meeting.AccountID) {
				return nil, ErrSummaryAccess
			}
			member, err := r.summaryRow(ctx, model.PrefixAccount+meeting.AccountID, model.PrefixMember+requester)
			if err != nil {
				return nil, err
			}
			if len(member) == 0 {
				return nil, ErrSummaryAccess
			}
			check, err := summaryCheck(model.PrefixAccount+meeting.AccountID, model.PrefixMember+requester, member, []string{"accountId", "userId"})
			if err != nil {
				return nil, err
			}
			snapshot.Checks = append(snapshot.Checks, check)
		}
	}
	attachments, err := r.summaryRows(ctx, meetingID, model.PrefixAttachment, 20)
	if err != nil {
		return nil, err
	}
	states, err := r.summaryRows(ctx, meetingID, model.PrefixAttachmentText, 60)
	if err != nil {
		return nil, err
	}
	stateRows := map[string]map[string]types.AttributeValue{}
	for _, item := range states {
		if sk, ok := item["SK"].(*types.AttributeValueMemberS); ok {
			stateRows[sk.Value] = item
		}
	}
	for _, item := range attachments {
		var att model.Attachment
		if err := attributevalue.UnmarshalMap(item, &att); err != nil {
			return nil, err
		}
		if att.MeetingID != meetingID || att.AttachmentID == "" || att.SK != model.PrefixAttachment+att.AttachmentID {
			return nil, ErrSummaryAccess
		}
		check, err := summaryCheck(model.PrefixMeeting+meetingID, att.SK, item, summaryAttachmentFields)
		if err != nil {
			return nil, err
		}
		snapshot.Checks = append(snapshot.Checks, check)
		snapshot.Attachments = append(snapshot.Attachments, att)
		if att.Type != model.AttachTypeDocument {
			continue
		}
		sk := model.PrefixAttachmentText + att.AttachmentID
		check, err = summaryCheck(model.PrefixMeeting+meetingID, sk, stateRows[sk], summaryTextFields)
		if err != nil {
			return nil, err
		}
		snapshot.Checks = append(snapshot.Checks, check)
		if check.Exists {
			state := &model.AttachmentTextState{}
			if err := attributevalue.UnmarshalMap(stateRows[sk], state); err != nil {
				return nil, err
			}
			snapshot.TextStates[att.AttachmentID] = state
		}
	}
	body, err := json.Marshal(snapshot.Checks)
	if err != nil {
		return nil, err
	}
	if len(body) > 1024*1024 {
		return nil, ErrSummaryLimit
	}
	return snapshot, nil
}

func (r *DynamoDBRepository) GetResummary(ctx context.Context, meetingID string) (*model.ResummaryState, error) {
	row, err := r.summaryRow(ctx, model.PrefixMeeting+meetingID, model.ResummarySK)
	if err != nil || len(row) == 0 {
		return nil, err
	}
	var state model.ResummaryState
	if err := attributevalue.UnmarshalMap(row, &state); err != nil {
		return nil, err
	}
	return &state, nil
}
func summaryRunCondition(state *model.ResummaryState) expression.ConditionBuilder {
	return analysisRunCondition(state.RunID, state.Status).
		And(expression.Name("ownerId").Equal(expression.Value(state.OwnerID))).
		And(expression.Name("requestedBy").Equal(expression.Value(state.RequestedBy))).
		And(expression.Name("sourceHash").Equal(expression.Value(state.SourceHash))).
		And(expression.Name("leaseUntil").Equal(expression.Value(state.LeaseUntil)))
}
func (r *DynamoDBRepository) summaryChecks(snapshot *model.SummarySnapshot, start int) ([]types.TransactWriteItem, error) {
	var items []types.TransactWriteItem
	for _, check := range snapshot.Checks[start:] {
		expr, err := expression.NewBuilder().WithCondition(summaryCondition(check)).Build()
		if err != nil {
			return nil, err
		}
		items = append(items, types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{
			TableName: aws.String(r.tableName), Key: summaryRowKey(check.PK, check.SK),
			ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
		}})
	}
	return items, nil
}
func summaryWriteError(err error) error {
	var failed *types.ConditionalCheckFailedException
	if errors.As(err, &failed) {
		return ErrConditionFailed
	}
	var cancelled *types.TransactionCanceledException
	if errors.As(err, &cancelled) {
		conditional := false
		for _, reason := range cancelled.CancellationReasons {
			switch aws.ToString(reason.Code) {
			case "None":
			case "ConditionalCheckFailed":
				conditional = true
			default:
				return err
			}
		}
		if conditional {
			return ErrConditionFailed
		}
	}
	return err
}
func noSummaryRetries(options *dynamodb.Options) {
	options.Retryer = aws.NopRetryer{}
	options.RetryMaxAttempts = 1
}

func validResummaryWrite(snapshot *model.SummarySnapshot, state *model.ResummaryState) bool {
	return validSummarySnapshot(snapshot) && state != nil && snapshot.Meeting.UserID == state.OwnerID
}

func (r *DynamoDBRepository) QueueResummary(ctx context.Context, snapshot *model.SummarySnapshot, prior, next *model.ResummaryState) error {
	if !validResummaryWrite(snapshot, next) {
		return ErrConditionFailed
	}
	condition := expression.AttributeNotExists(expression.Name("PK"))
	if prior != nil {
		condition = summaryRunCondition(prior)
		if prior.Pending() {
			condition = condition.And(expression.Name("leaseUntil").LessThanEqual(expression.Value(next.UpdatedAt.UnixMilli())))
		}
	}
	update := expression.Set(expression.Name("runId"), expression.Value(next.RunID)).
		Set(expression.Name("status"), expression.Value(model.AnalysisQueued)).
		Set(expression.Name("ownerId"), expression.Value(next.OwnerID)).
		Set(expression.Name("requestedBy"), expression.Value(next.RequestedBy)).
		Set(expression.Name("sourceHash"), expression.Value(next.SourceHash)).
		Set(expression.Name("leaseUntil"), expression.Value(next.LeaseUntil)).
		Set(expression.Name("updatedAt"), expression.Value(next.UpdatedAt)).
		Set(expression.Name("errorCode"), expression.Value("")).
		Set(expression.Name("resultHash"), expression.Value("")).
		Set(expression.Name("entityType"), expression.Value("SUMMARY_ANALYSIS"))
	op, err := analysisUpdate(r.tableName, summaryKey(snapshot.Meeting.MeetingID), update, condition)
	if err != nil {
		return err
	}
	checks, err := r.summaryChecks(snapshot, 0)
	if err != nil {
		return err
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: append(checks, types.TransactWriteItem{Update: op})}, noSummaryRetries)
	return summaryWriteError(err)
}
func (r *DynamoDBRepository) StartResummary(ctx context.Context, snapshot *model.SummarySnapshot, state *model.ResummaryState, now time.Time, lease int64) error {
	if !validResummaryWrite(snapshot, state) {
		return ErrConditionFailed
	}
	condition := summaryRunCondition(state).And(expression.Name("leaseUntil").GreaterThan(expression.Value(now.UnixMilli())))
	update := expression.Set(expression.Name("status"), expression.Value(model.AnalysisRunning)).
		Set(expression.Name("leaseUntil"), expression.Value(lease)).Set(expression.Name("updatedAt"), expression.Value(now))
	op, err := analysisUpdate(r.tableName, summaryKey(snapshot.Meeting.MeetingID), update, condition)
	if err != nil {
		return err
	}
	checks, err := r.summaryChecks(snapshot, 0)
	if err != nil {
		return err
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: append(checks, types.TransactWriteItem{Update: op})}, noSummaryRetries)
	return summaryWriteError(err)
}
func (r *DynamoDBRepository) FailResummary(ctx context.Context, meetingID string, state *model.ResummaryState, code string, now time.Time) error {
	if state == nil {
		return ErrConditionFailed
	}
	update := expression.Set(expression.Name("status"), expression.Value(model.AnalysisFailed)).
		Set(expression.Name("errorCode"), expression.Value(code)).
		Set(expression.Name("leaseUntil"), expression.Value(int64(0))).Set(expression.Name("updatedAt"), expression.Value(now))
	op, err := analysisUpdate(r.tableName, summaryKey(meetingID), update, summaryRunCondition(state))
	if err != nil {
		return err
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{TableName: op.TableName, Key: op.Key, UpdateExpression: op.UpdateExpression,
		ConditionExpression: op.ConditionExpression, ExpressionAttributeNames: op.ExpressionAttributeNames, ExpressionAttributeValues: op.ExpressionAttributeValues}, noSummaryRetries)
	return summaryWriteError(err)
}

func (r *DynamoDBRepository) CompleteResummary(ctx context.Context, snapshot *model.SummarySnapshot, state *model.ResummaryState, content, coverage, resultHash string, now time.Time) (resultErr error) {
	if !validResummaryWrite(snapshot, state) {
		return ErrConditionFailed
	}
	fields := map[string]interface{}{"content": content, "attachmentSummarySources": coverage, "status": model.StatusDone, "updatedAt": now.Format(time.RFC3339Nano),
		"summaryRetryPending": false, "summaryConflictCode": ""}
	stored := map[string]interface{}{}
	for key, value := range snapshot.Stored {
		stored[key] = value
	}
	for key, value := range fields {
		stored[key] = value
	}
	var uploaded []string
	safeCleanup := true
	defer func() {
		if resultErr != nil && safeCleanup {
			resultErr = errors.Join(resultErr, r.deleteUncommittedTranscriptSpills(ctx, uploaded))
		}
	}()
	// All rewritten family fields remain guarded by their exact original values.
	// Existing blobs are never overwritten or removed.
	for {
		body, err := json.Marshal(stored)
		if err != nil {
			return err
		}
		if len(body) <= 300*1024 {
			break
		}
		pick, text := "", ""
		for _, field := range transcriptFamilyFields {
			value, _ := stored[field].(string)
			if !strings.HasPrefix(value, "s3://") && len(value) > len(text) {
				pick, text = field, value
			}
		}
		if pick == "" || r.s3Client == nil {
			return ErrSummaryLimit
		}
		ref, err := r.storeConditionalTranscript(ctx, snapshot.Meeting.MeetingID, pick, text, &uploaded)
		if err != nil {
			return err
		}
		stored[pick], fields[pick] = ref, ref
	}
	update := expression.Set(expression.Name("content"), expression.Value(content))
	for key, value := range fields {
		if key != "content" {
			update = update.Set(expression.Name(key), expression.Value(value))
		}
	}
	meeting, err := analysisUpdate(r.tableName, meetingKey(state.OwnerID, snapshot.Meeting.MeetingID), update, summaryCondition(snapshot.Checks[0]))
	if err != nil {
		return err
	}
	run, err := analysisUpdate(r.tableName, summaryKey(snapshot.Meeting.MeetingID),
		expression.Set(expression.Name("status"), expression.Value(model.AnalysisSucceeded)).
			Set(expression.Name("errorCode"), expression.Value("")).
			Set(expression.Name("resultHash"), expression.Value(resultHash)).
			Set(expression.Name("leaseUntil"), expression.Value(int64(0))).Set(expression.Name("updatedAt"), expression.Value(now)),
		summaryRunCondition(state).And(expression.Name("leaseUntil").GreaterThan(expression.Value(now.UnixMilli()))))
	if err != nil {
		return err
	}
	checks, err := r.summaryChecks(snapshot, 1)
	if err != nil {
		return err
	}
	items := append([]types.TransactWriteItem{{Update: meeting}, {Update: run}}, checks...)
	if err := r.CheckResummaryObjects(ctx, snapshot.Meeting.MeetingID, snapshot.Objects); err != nil {
		return errors.Join(ErrConditionFailed, err)
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}, noSummaryRetries)
	resultErr = summaryWriteError(err)
	if resultErr != nil && !errors.Is(resultErr, ErrConditionFailed) {
		safeCleanup = false
	}
	return resultErr
}

func summaryKey(meetingID string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
		"SK": &types.AttributeValueMemberS{Value: model.ResummarySK}}
}
