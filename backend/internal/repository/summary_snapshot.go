package repository

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

var (
	ErrSummaryAccess = errors.New("summary access revoked")
	ErrSummaryLimit  = errors.New("summary snapshot or output exceeds limit")
)

var summaryMeetingFields = []string{"meetingId", "userId", "status", "content", "notes",
	"transcriptA", "transcriptB", "selectedTranscript", "transcriptSegments", "accountId", "sharedToAccount"}

var batchSummaryFields = []string{"meetingId", "userId", "status", "content", "notes", "liveSummary",
	"transcriptA", "transcriptB", "selectedTranscript", "transcriptSegments", "attachmentSummarySources", "summarizeRetryClaimedAt"}

func summaryRowKey(pk, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: pk}, "SK": &types.AttributeValueMemberS{Value: sk}}
}
func (r *DynamoDBRepository) summaryRow(ctx context.Context, pk, sk string) (map[string]types.AttributeValue, error) {
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(r.tableName), Key: summaryRowKey(pk, sk), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return nil, err
	}
	return out.Item, nil
}
func summaryCheck(pk, sk string, item map[string]types.AttributeValue, fields []string) (model.SummaryCheck, error) {
	check := model.SummaryCheck{PK: pk, SK: sk, Exists: len(item) > 0, Fields: map[string]model.SummaryValue{}}
	if !check.Exists {
		return check, nil
	}
	for _, name := range fields {
		value, exists := item[name]
		field := model.SummaryValue{Present: exists}
		if exists {
			if err := attributevalue.Unmarshal(value, &field.Value); err != nil {
				return check, err
			}
		}
		check.Fields[name] = field
	}
	return check, nil
}
func summaryCondition(check model.SummaryCheck) expression.ConditionBuilder {
	if !check.Exists {
		return expression.AttributeNotExists(expression.Name("PK"))
	}
	condition := expression.AttributeExists(expression.Name("PK"))
	names := make([]string, 0, len(check.Fields))
	for name := range check.Fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		field := check.Fields[name]
		if !field.Present {
			condition = condition.And(expression.AttributeNotExists(expression.Name(name)))
		} else {
			condition = condition.And(expression.Name(name).Equal(expression.Value(field.Value)))
		}
	}
	return condition
}
func (r *DynamoDBRepository) captureMeetingSummaryMetadata(ctx context.Context, ownerID, meetingID string, fields []string) (*model.SummarySnapshot, error) {
	row, err := r.summaryRow(ctx, model.PrefixUser+ownerID, model.PrefixMeeting+meetingID)
	if err != nil || len(row) == 0 {
		return nil, err
	}
	snapshot := &model.SummarySnapshot{Meeting: &model.Meeting{}, TextStates: map[string]*model.AttachmentTextState{}, Stored: map[string]interface{}{}}
	if err := attributevalue.UnmarshalMap(row, snapshot.Meeting); err != nil {
		return nil, err
	}
	if snapshot.Meeting.UserID != ownerID || snapshot.Meeting.MeetingID != meetingID {
		return nil, ErrSummaryAccess
	}
	if err := attributevalue.UnmarshalMap(row, &snapshot.Stored); err != nil {
		return nil, err
	}
	parent, err := summaryCheck(model.PrefixUser+ownerID, model.PrefixMeeting+meetingID, row, fields)
	if err != nil {
		return nil, err
	}
	snapshot.Checks = append(snapshot.Checks, parent)
	body, err := json.Marshal(snapshot.Checks)
	if err != nil {
		return nil, err
	}
	if len(body) > 1024*1024 {
		return nil, ErrSummaryLimit
	}
	return snapshot, nil
}

// CaptureMeetingSummary records exact stored attribute presence before hydrating
// the source used by the existing batch summarizer. A missing meeting is nil.
func (r *DynamoDBRepository) CaptureMeetingSummary(ctx context.Context, ownerID, meetingID string) (*model.SummarySnapshot, error) {
	snapshot, err := r.captureMeetingSummaryMetadata(ctx, ownerID, meetingID, batchSummaryFields)
	if err != nil || snapshot == nil {
		return nil, err
	}
	if err := r.resolveTranscripts(ctx, meetingID, snapshot.Meeting); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func validSummarySnapshot(snapshot *model.SummarySnapshot) bool {
	if snapshot == nil || snapshot.Meeting == nil || len(snapshot.Checks) == 0 {
		return false
	}
	parent := snapshot.Checks[0]
	return parent.Exists && parent.PK == model.PrefixUser+snapshot.Meeting.UserID && parent.SK == model.PrefixMeeting+snapshot.Meeting.MeetingID
}

// SaveMeetingSummary preserves absent versus present source attributes and uses
// the immutable-spill writer. Unrelated conditional writers are unchanged.
func (r *DynamoDBRepository) SaveMeetingSummary(ctx context.Context, snapshot *model.SummarySnapshot, content, coverage string) error {
	if !validSummarySnapshot(snapshot) {
		return ErrConditionFailed
	}
	return r.updateMeetingFieldsWithCondition(ctx, snapshot.Meeting.UserID, snapshot.Meeting.MeetingID,
		summaryCondition(snapshot.Checks[0]), map[string]interface{}{"content": content, "status": model.StatusDone, "attachmentSummarySources": coverage,
			"summaryRetryPending": false, "summaryConflictCode": ""}, true)
}

// MarkSummaryConflict discards this attempt without touching human text.
// Only its observed retry claim can be released for a fresh generation.
func (r *DynamoDBRepository) MarkSummaryConflict(ctx context.Context, snapshot *model.SummarySnapshot) error {
	if !validSummarySnapshot(snapshot) {
		return ErrConditionFailed
	}
	condition := expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("status").Equal(expression.Value(model.StatusSummarizing)))
	if claim, present := snapshot.Stored["summarizeRetryClaimedAt"]; present {
		condition = condition.And(expression.Name("summarizeRetryClaimedAt").Equal(expression.Value(claim)))
	} else {
		condition = condition.And(expression.AttributeNotExists(expression.Name("summarizeRetryClaimedAt")))
	}
	update := expression.Set(expression.Name("summaryRetryPending"), expression.Value(true)).
		Set(expression.Name("summaryConflictCode"), expression.Value("SOURCE_CHANGED")).
		Set(expression.Name("updatedAt"), expression.Value(time.Now().UTC())).
		Remove(expression.Name("summarizeRetryClaimedAt"))
	expr, err := expression.NewBuilder().WithCondition(condition).WithUpdate(update).Build()
	if err != nil {
		return err
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{TableName: aws.String(r.tableName),
		Key:                 summaryRowKey(snapshot.Checks[0].PK, snapshot.Checks[0].SK),
		ConditionExpression: expr.Condition(), UpdateExpression: expr.Update(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()})
	return err
}

func summaryKey(meetingID string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
		"SK": &types.AttributeValueMemberS{Value: model.ResummarySK}}
}
