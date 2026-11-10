package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// englishResultFields are the only meeting attributes an English run writes.
var englishResultFields = []string{"contentEn", "contentEnSourceHash", "contentEnGeneratedAt"}

// summaryInlineBudget approximates the 400KB item limit with headroom; it is
// the same budget saved-source and batch publication use.
const summaryInlineBudget = 300 * 1024

// englishItemBudget bounds the marshaled meeting item for an English
// publication. The English path writes only englishResultFields and cannot
// spill transcripts, so applying summaryInlineBudget would reject meetings
// whose unspilled transcript family legitimately sits near
// transcriptInlineBudget even though DynamoDB accepts 400KB items. 380KiB
// leaves headroom for attribute-name and type overhead; DynamoDB's own
// item-size rejection of the transaction remains the backstop.
const englishItemBudget = 380 * 1024

// englishSourceFields are the canonical meeting fields an English run pins:
// the saved-source fence without the Korean note. A Korean resummary or a
// human Korean edit therefore never invalidates an English run.
func englishSourceFields() []string {
	fields := make([]string, 0, len(summaryMeetingFields))
	for _, field := range summaryMeetingFields {
		if field != "content" {
			fields = append(fields, field)
		}
	}
	return fields
}

// englishSummaryChecks derives the English fences from a CaptureResummary
// snapshot. The canonical meeting check keeps only englishSourceFields (each
// must have been captured); the Korean action-analysis fence is dropped
// because a Korean publication legitimately rewrites it. Edit-grant,
// account-membership, attachment and text-state fences are kept.
func englishSummaryChecks(snapshot *model.SummarySnapshot) ([]model.SummaryCheck, error) {
	if !validSummarySnapshot(snapshot) {
		return nil, ErrConditionFailed
	}
	parent := snapshot.Checks[0]
	meeting := model.SummaryCheck{PK: parent.PK, SK: parent.SK, Exists: true, Fields: map[string]model.SummaryValue{}}
	for _, field := range englishSourceFields() {
		value, ok := parent.Fields[field]
		if !ok {
			return nil, ErrConditionFailed
		}
		meeting.Fields[field] = value
	}
	checks := []model.SummaryCheck{meeting}
	for _, check := range snapshot.Checks[1:] {
		if check.PK == model.PrefixMeeting+snapshot.Meeting.MeetingID && check.SK == model.ActionAnalysisSK {
			continue
		}
		checks = append(checks, check)
	}
	return checks, nil
}

// English runs never carry the Korean sharing-safe derivative reset.
func validEnglishSummaryWrite(snapshot *model.SummarySnapshot, state *model.ResummaryState) bool {
	return validResummaryWrite(snapshot, state) && !state.SharingSafe
}

func (r *DynamoDBRepository) GetEnglishSummary(ctx context.Context, meetingID string) (*model.ResummaryState, error) {
	return r.getSummaryState(ctx, meetingID, model.EnglishSummarySK)
}

func (r *DynamoDBRepository) QueueEnglishSummary(ctx context.Context, snapshot *model.SummarySnapshot, prior, next *model.ResummaryState) error {
	if !validEnglishSummaryWrite(snapshot, next) {
		return ErrConditionFailed
	}
	checks, err := englishSummaryChecks(snapshot)
	if err != nil {
		return err
	}
	return r.queueSummaryState(ctx, snapshot.Meeting.MeetingID, model.EnglishSummarySK, checks, prior, next)
}

func (r *DynamoDBRepository) StartEnglishSummary(ctx context.Context, snapshot *model.SummarySnapshot, state *model.ResummaryState, now time.Time, lease int64) error {
	if !validEnglishSummaryWrite(snapshot, state) {
		return ErrConditionFailed
	}
	checks, err := englishSummaryChecks(snapshot)
	if err != nil {
		return err
	}
	return r.startSummaryState(ctx, snapshot.Meeting.MeetingID, model.EnglishSummarySK, checks, state, now, lease)
}

func (r *DynamoDBRepository) FailEnglishSummary(ctx context.Context, meetingID string, state *model.ResummaryState, code string, now time.Time) error {
	return r.failSummaryState(ctx, meetingID, model.EnglishSummarySK, state, code, now)
}

// CompleteEnglishSummary atomically publishes the English note and run
// success. It writes only englishResultFields: Korean content, derivatives,
// provenance, status and transcript spills are untouched, so an item over
// englishItemBudget fails with ErrSummaryLimit instead of spilling.
func (r *DynamoDBRepository) CompleteEnglishSummary(ctx context.Context, snapshot *model.SummarySnapshot, state *model.ResummaryState, content, sourceHash string, now time.Time) error {
	if !validEnglishSummaryWrite(snapshot, state) {
		return ErrConditionFailed
	}
	checks, err := englishSummaryChecks(snapshot)
	if err != nil {
		return err
	}
	values := []interface{}{content, sourceHash, now.UTC().Format(time.RFC3339Nano)}
	stored := make(map[string]interface{}, len(snapshot.Stored)+len(values))
	for key, value := range snapshot.Stored {
		stored[key] = value
	}
	update := expression.Set(expression.Name(englishResultFields[0]), expression.Value(values[0]))
	for i, field := range englishResultFields {
		stored[field] = values[i]
		if i > 0 {
			update = update.Set(expression.Name(field), expression.Value(values[i]))
		}
	}
	body, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	if len(body) > englishItemBudget {
		return ErrSummaryLimit
	}
	meeting, err := analysisUpdate(r.tableName, meetingKey(state.OwnerID, snapshot.Meeting.MeetingID), update, summaryCondition(checks[0]))
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(content))
	run, err := analysisUpdate(r.tableName, englishSummaryKey(snapshot.Meeting.MeetingID),
		expression.Set(expression.Name("status"), expression.Value(model.AnalysisSucceeded)).
			Set(expression.Name("errorCode"), expression.Value("")).
			Set(expression.Name("resultHash"), expression.Value(hex.EncodeToString(digest[:]))).
			Set(expression.Name("leaseUntil"), expression.Value(int64(0))).Set(expression.Name("updatedAt"), expression.Value(now)),
		summaryRunCondition(state).And(expression.Name("leaseUntil").GreaterThan(expression.Value(now.UnixMilli()))))
	if err != nil {
		return err
	}
	fences, err := r.conditionChecks(checks[1:])
	if err != nil {
		return err
	}
	if err := r.CheckResummaryObjects(ctx, snapshot.Meeting.MeetingID, snapshot.Objects); err != nil {
		return errors.Join(ErrConditionFailed, err)
	}
	items := append([]types.TransactWriteItem{{Update: meeting}, {Update: run}}, fences...)
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}, noSummaryRetries)
	return summaryWriteError(err)
}
