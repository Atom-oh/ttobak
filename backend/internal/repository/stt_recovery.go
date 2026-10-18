package repository

import (
	"context"
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
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/ttobak/backend/internal/model"
)

// ErrSttDeletionBlocked means a failed recording has child rows (attachments,
// shares, a simulation, ...) that make it user data; it is only ever marked.
var ErrSttDeletionBlocked = errors.New("failed recording has related data and is not auto-deletable")

// ErrSttNoSpeechSnapshot is a structurally unusable snapshot, not a lost fence.
var ErrSttNoSpeechSnapshot = errors.New("no-speech mark needs exactly one meeting snapshot check")

// ErrSttDeletionPostCommit wraps a failure that happened after the meeting
// rows were already deleted; object cleanup must still run.
var ErrSttDeletionPostCommit = errors.New("failed recording rows deleted; post-commit cleanup failed")

var sttRecoveryEmptyFields = []string{"transcriptA", "transcriptB", "transcriptSegments", "content"}

func emptyOrAbsent(name string) expression.ConditionBuilder {
	return expression.AttributeNotExists(expression.Name(name)).Or(expression.Name(name).Equal(expression.Value("")))
}

func absentOrEmptyCollection(name string) expression.ConditionBuilder {
	return expression.AttributeNotExists(expression.Name(name)).Or(expression.Size(expression.Name(name)).Equal(expression.Value(0)))
}

// ScanSttRecoveryRows lists meeting rows that may need STT recovery. It never
// reads through GetMeetingDetail (which writes) and its projection carries only
// IDs and pipeline state: no title, notes, content, transcript or summary.
func (r *DynamoDBRepository) ScanSttRecoveryRows(ctx context.Context) ([]model.SttRecoveryRow, error) {
	filter := expression.Name("entityType").Equal(expression.Value("MEETING")).
		And(expression.Name("status").In(expression.Value(model.StatusTranscribing),
			expression.Value(model.StatusError), expression.Value(model.StatusRecording)))
	for _, field := range sttRecoveryEmptyFields {
		filter = filter.And(emptyOrAbsent(field))
	}
	names := make([]expression.NameBuilder, 0, len(model.SttRecoveryProjection))
	for _, field := range model.SttRecoveryProjection {
		names = append(names, expression.Name(field))
	}
	projection := expression.NamesList(names[0], names[1:]...)
	expr, err := expression.NewBuilder().WithFilter(filter).WithProjection(projection).Build()
	if err != nil {
		return nil, err
	}
	pages := dynamodb.NewScanPaginator(r.client, &dynamodb.ScanInput{
		TableName: aws.String(r.tableName), FilterExpression: expr.Filter(), ProjectionExpression: expr.Projection(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	})
	var rows []model.SttRecoveryRow
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("scan stt recovery rows: %w", err)
		}
		for _, item := range page.Items {
			row, err := decodeSttRecoveryRow(item)
			if err != nil {
				return nil, err
			}
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func decodeSttRecoveryRow(item map[string]types.AttributeValue) (model.SttRecoveryRow, error) {
	var row model.SttRecoveryRow
	if err := attributevalue.UnmarshalMap(item, &row); err != nil {
		return row, fmt.Errorf("decode stt recovery row: %w", err)
	}
	row.Present = make(map[string]bool, len(item))
	for name := range item {
		row.Present[name] = true
	}
	return row, nil
}

// pinObserved matches an attribute exactly as the scan observed it.
func pinObserved(row model.SttRecoveryRow, name string, value interface{}) expression.ConditionBuilder {
	if !row.Present[name] {
		return expression.AttributeNotExists(expression.Name(name))
	}
	return expression.Name(name).Equal(expression.Value(value))
}

// sttRecoveryBaseCondition fences a claim on the observed row and the full
// eligibility predicate, so a concurrent writer of any kind wins.
func sttRecoveryBaseCondition(row model.SttRecoveryRow, key string) (expression.ConditionBuilder, error) {
	if !row.Present["updatedAt"] || row.UpdatedAt == "" || key == "" || row.AudioKey != key {
		return expression.ConditionBuilder{}, ErrConditionFailed
	}
	if model.IsTerminalSttFailure(row.SttFailureCode) && row.Status != model.StatusTranscribing {
		return expression.ConditionBuilder{}, ErrConditionFailed
	}
	condition := expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("status").Equal(expression.Value(row.Status))).
		And(expression.Name("updatedAt").Equal(expression.Value(row.UpdatedAt))).
		And(expression.Name("audioKey").Equal(expression.Value(key))).
		And(absentOrEmptyCollection("audioKeys")).
		And(expression.AttributeNotExists(expression.Name("audioPartCount")).Or(expression.Name("audioPartCount").LessThanEqual(expression.Value(1)))).
		And(expression.AttributeNotExists(expression.Name("audioCrop"))).
		And(expression.AttributeNotExists(expression.Name("summarizeRetryClaimedAt"))).
		And(expression.AttributeNotExists(expression.Name("summaryRetryPending")).Or(expression.Name("summaryRetryPending").Equal(expression.Value(false)))).
		And(emptyOrAbsent("summaryConflictCode")).
		And(emptyOrAbsent("sttProvider").Or(expression.Name("sttProvider").Equal(expression.Value("whisper")))).
		And(pinObserved(row, "sttFailureCode", row.SttFailureCode)).
		And(pinObserved(row, "sttRecoveryAttempts", row.SttRecoveryAttempts)).
		And(pinObserved(row, "sttRecoveryAudioKey", row.SttRecoveryAudioKey))
	for _, field := range sttRecoveryEmptyFields {
		condition = condition.And(emptyOrAbsent(field))
	}
	return condition, nil
}

func meetingRowKey(ownerID, meetingID string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + ownerID},
		"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
	}
}

// claimSttRow writes one claim without SDK retries. An ambiguous failure is
// resolved by reading back the unique claim value, never by resending.
func (r *DynamoDBRepository) claimSttRow(ctx context.Context, row model.SttRecoveryRow, condition expression.ConditionBuilder, update expression.UpdateBuilder, claimField, claim string) (string, error) {
	expr, err := expression.NewBuilder().WithCondition(condition).WithUpdate(update).Build()
	if err != nil {
		return "", err
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName), Key: meetingRowKey(row.UserID, row.MeetingID),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}, singleDynamoDBAttempt)
	if err == nil {
		return claim, nil
	}
	var conflict *types.ConditionalCheckFailedException
	if errors.As(err, &conflict) {
		return "", ErrConditionFailed
	}
	current, readErr := r.sttClaimValue(ctx, row.UserID, row.MeetingID, claimField)
	if readErr == nil && current == claim {
		return claim, nil
	}
	return "", errors.Join(err, readErr)
}

func (r *DynamoDBRepository) sttClaimValue(ctx context.Context, ownerID, meetingID, claimField string) (string, error) {
	expr, err := expression.NewBuilder().WithProjection(expression.NamesList(expression.Name(claimField))).Build()
	if err != nil {
		return "", err
	}
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), Key: meetingRowKey(ownerID, meetingID), ConsistentRead: aws.Bool(true),
		ProjectionExpression: expr.Projection(), ExpressionAttributeNames: expr.Names(),
	})
	if err != nil {
		return "", err
	}
	var claim string
	if value, ok := out.Item[claimField]; ok {
		if err := attributevalue.Unmarshal(value, &claim); err != nil {
			return "", err
		}
	}
	return claim, nil
}

// sttClaimUpdate is shared by both claims. Both budgets are bound to
// sttRecoveryAudioKey: claiming a different key resets the other budget.
func sttClaimUpdate(row model.SttRecoveryRow, key, claimField, counterField, otherCounter string, attempt int, claim string) expression.UpdateBuilder {
	update := expression.Set(expression.Name("status"), expression.Value(model.StatusTranscribing)).
		Set(expression.Name("updatedAt"), expression.Value(claim)).
		Set(expression.Name(claimField), expression.Value(claim)).
		Set(expression.Name(counterField), expression.Value(attempt)).
		Set(expression.Name("sttRecoveryAudioKey"), expression.Value(key)).
		Remove(expression.Name("sttFailureCode")).
		Remove(expression.Name("sttFailedAt"))
	if row.SttRecoveryAudioKey != key {
		update = update.Set(expression.Name(otherCounter), expression.Value(0))
	}
	return update
}

// ClaimSttRecovery moves an eligible stuck meeting back to transcribing and
// records attempt n for the bound key. The returned claim is also updatedAt.
func (r *DynamoDBRepository) ClaimSttRecovery(ctx context.Context, row model.SttRecoveryRow, key string, attempt int, now time.Time) (string, error) {
	condition, err := sttRecoveryBaseCondition(row, key)
	if err != nil {
		return "", err
	}
	claim := now.UTC().Format(time.RFC3339Nano)
	update := sttClaimUpdate(row, key, "sttRecoveryClaimedAt", "sttRecoveryAttempts", "sttSummaryRedriveAttempts", attempt, claim)
	return r.claimSttRow(ctx, row, condition, update, "sttRecoveryClaimedAt", claim)
}

// ClaimSttSummaryRedrive claims a meeting whose transcript JSON exists but was
// never published, recording summary re-drive attempt n. Its claim lives in
// sttSummaryRedriveClaimedAt, so it never reads as an STT restart.
func (r *DynamoDBRepository) ClaimSttSummaryRedrive(ctx context.Context, row model.SttRecoveryRow, attempt int, now time.Time) (string, error) {
	condition, err := sttRecoveryBaseCondition(row, row.AudioKey)
	if err != nil {
		return "", err
	}
	condition = condition.And(pinObserved(row, "sttSummaryRedriveAttempts", row.SttSummaryRedriveAttempts))
	claim := now.UTC().Format(time.RFC3339Nano)
	update := sttClaimUpdate(row, row.AudioKey, "sttSummaryRedriveClaimedAt", "sttSummaryRedriveAttempts", "sttRecoveryAttempts", attempt, claim)
	return r.claimSttRow(ctx, row, condition, update, "sttSummaryRedriveClaimedAt", claim)
}

// markSttFailure writes status=error plus a code, with sttFailedAt and
// updatedAt set to the same value: "no activity since the mark" is exactly
// updatedAt == sttFailedAt.
func (r *DynamoDBRepository) markSttFailure(ctx context.Context, ownerID, meetingID, code string, condition expression.ConditionBuilder) error {
	if code == "" {
		return ErrConditionFailed
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	update := expression.Set(expression.Name("status"), expression.Value(model.StatusError)).
		Set(expression.Name("sttFailureCode"), expression.Value(code)).
		Set(expression.Name("sttFailedAt"), expression.Value(stamp)).
		Set(expression.Name("updatedAt"), expression.Value(stamp))
	expr, err := expression.NewBuilder().WithCondition(condition).WithUpdate(update).Build()
	if err != nil {
		return err
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName), Key: meetingRowKey(ownerID, meetingID),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}, singleDynamoDBAttempt)
	var conflict *types.ConditionalCheckFailedException
	if errors.As(err, &conflict) {
		return ErrConditionFailed
	}
	return err
}

// MarkSttFailureForClaim releases this tick's own STT or summary claim with a
// failure code.
func (r *DynamoDBRepository) MarkSttFailureForClaim(ctx context.Context, ownerID, meetingID, code, claim string) error {
	if claim == "" {
		return ErrConditionFailed
	}
	condition := expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("sttRecoveryClaimedAt").Equal(expression.Value(claim)).
			Or(expression.Name("sttSummaryRedriveClaimedAt").Equal(expression.Value(claim)))).
		And(expression.Name("status").Equal(expression.Value(model.StatusTranscribing))).
		And(expression.Name("updatedAt").Equal(expression.Value(claim)))
	return r.markSttFailure(ctx, ownerID, meetingID, code, condition)
}

// MarkSttFailureObserved marks a scanned row only if it is still unchanged.
func (r *DynamoDBRepository) MarkSttFailureObserved(ctx context.Context, row model.SttRecoveryRow, code string) error {
	if row.UpdatedAt == "" {
		return ErrConditionFailed
	}
	condition := expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("status").Equal(expression.Value(row.Status))).
		And(expression.Name("updatedAt").Equal(expression.Value(row.UpdatedAt))).
		And(expression.AttributeNotExists(expression.Name("audioCrop")))
	return r.markSttFailure(ctx, row.UserID, row.MeetingID, code, condition)
}

// MarkSttNoSpeech records NO_SPEECH under the same transcript snapshot fence
// that failTranscriptSnapshot uses, without publishing any transcript field.
func (r *DynamoDBRepository) MarkSttNoSpeech(ctx context.Context, snapshot *model.SummarySnapshot) error {
	if !validSummarySnapshot(snapshot) || len(snapshot.Checks) != 1 {
		return ErrSttNoSpeechSnapshot
	}
	return r.markSttFailure(ctx, snapshot.Meeting.UserID, snapshot.Meeting.MeetingID, model.SttFailureNoSpeech,
		summaryCondition(snapshot.Checks[0]))
}

// ScanSttDeletionReferences returns meeting IDs that another meeting links to
// or that have a pending email share. Pending meeting shares are keyed by the
// invitee's email, so only a scan can find them.
func (r *DynamoDBRepository) ScanSttDeletionReferences(ctx context.Context) (map[string]bool, error) {
	filter := expression.Name("entityType").Equal(expression.Value("MEETING")).And(expression.AttributeExists(expression.Name("linkedMeetingIds"))).
		Or(expression.Name("SK").BeginsWith(model.PrefixPendingMeeting))
	expr, err := expression.NewBuilder().WithFilter(filter).
		WithProjection(expression.NamesList(expression.Name("SK"), expression.Name("linkedMeetingIds"))).Build()
	if err != nil {
		return nil, err
	}
	pages := dynamodb.NewScanPaginator(r.client, &dynamodb.ScanInput{
		TableName: aws.String(r.tableName), FilterExpression: expr.Filter(), ProjectionExpression: expr.Projection(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	})
	referenced := map[string]bool{}
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("scan stt deletion references: %w", err)
		}
		for _, item := range page.Items {
			var ref struct {
				SK     string   `dynamodbav:"SK"`
				Linked []string `dynamodbav:"linkedMeetingIds"`
			}
			if err := attributevalue.UnmarshalMap(item, &ref); err != nil {
				return nil, fmt.Errorf("decode stt deletion reference: %w", err)
			}
			if strings.HasPrefix(ref.SK, model.PrefixPendingMeeting) {
				referenced[strings.TrimPrefix(ref.SK, model.PrefixPendingMeeting)] = true
			}
			for _, id := range ref.Linked {
				referenced[id] = true
			}
		}
	}
	return referenced, nil
}

// sttDeletionAllowedChildren are the only MEETING# partition rows a failed
// recording may have and still be auto-deleted.
var sttDeletionAllowedChildren = map[string]bool{model.ActionAnalysisSK: true, model.ResummarySK: true}

// meetingSingletonDeleteKeys mirrors DeleteMeeting's leading singleton deletes
// (meeting row, action analysis, sim run, saved summary state). A drift test
// keeps both paths aligned.
func meetingSingletonDeleteKeys(ownerID, meetingID string) []map[string]types.AttributeValue {
	return []map[string]types.AttributeValue{
		meetingRowKey(ownerID, meetingID),
		actionAnalysisKey(meetingID),
		{"PK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID}, "SK": &types.AttributeValueMemberS{Value: model.PrefixSimRun}},
		summaryKey(meetingID),
	}
}

// sttFailedMeetingDeleteCondition requires the exact current mark, the
// single-file shape (one corrupt part never deletes a multipart recording) and
// the absence of every user-authored or relation field.
func sttFailedMeetingDeleteCondition(mark model.SttFailureMark) expression.ConditionBuilder {
	condition := expression.AttributeExists(expression.Name("PK")).
		And(expression.AttributeNotExists(expression.Name("audioPartCount")).Or(expression.Name("audioPartCount").LessThanEqual(expression.Value(1)))).
		And(expression.Name("status").Equal(expression.Value(model.StatusError))).
		And(expression.Name("sttFailureCode").Equal(expression.Value(mark.Code))).
		And(expression.Name("sttFailedAt").Equal(expression.Value(mark.FailedAt))).
		And(expression.Name("updatedAt").Equal(expression.Value(mark.FailedAt))).
		And(expression.AttributeNotExists(expression.Name("projectIds"))).
		And(expression.AttributeNotExists(expression.Name("sharedToAccount")).Or(expression.Name("sharedToAccount").Equal(expression.Value(false)))).
		And(expression.AttributeNotExists(expression.Name("diarizationSpeakerHint")).Or(expression.Name("diarizationSpeakerHint").Equal(expression.Value(0)))).
		And(expression.AttributeNotExists(expression.Name("audioCrop"))).
		And(expression.AttributeNotExists(expression.Name("summarizeRetryClaimedAt")))
	for _, field := range []string{"notes", "content", "transcriptA", "transcriptB", "transcriptSegments", "liveSummary", "actionItems", "notionPageId", "accountId"} {
		condition = condition.And(emptyOrAbsent(field))
	}
	for _, field := range []string{"audioKeys", "linkedMeetingIds", "participants", "speakerMap"} {
		condition = condition.And(absentOrEmptyCollection(field))
	}
	return condition
}

// DeleteSttFailedMeeting deletes a marked failed recording's rows. It refuses
// any meeting with attachments, shares, a simulation or unknown child rows,
// and the meeting row delete is conditioned on the mark still being current.
func (r *DynamoDBRepository) DeleteSttFailedMeeting(ctx context.Context, ownerID, meetingID string, mark model.SttFailureMark) error {
	if !model.IsDeletableSttFailure(mark.Code) || mark.FailedAt == "" || ownerID == "" || meetingID == "" {
		return ErrConditionFailed
	}
	keyExpr, err := expression.NewBuilder().
		WithKeyCondition(expression.Key("PK").Equal(expression.Value(model.PrefixMeeting + meetingID))).
		WithProjection(expression.NamesList(expression.Name("SK"))).Build()
	if err != nil {
		return err
	}
	pages := dynamodb.NewQueryPaginator(r.client, &dynamodb.QueryInput{
		TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true),
		KeyConditionExpression: keyExpr.KeyCondition(), ProjectionExpression: keyExpr.Projection(),
		ExpressionAttributeNames: keyExpr.Names(), ExpressionAttributeValues: keyExpr.Values(),
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list failed recording children: %w", err)
		}
		for _, item := range page.Items {
			sk, _ := item["SK"].(*types.AttributeValueMemberS)
			if sk == nil || !sttDeletionAllowedChildren[sk.Value] {
				return ErrSttDeletionBlocked
			}
		}
	}
	// A server-side checkpoint is the owner's recovery source (ADR-048): its
	// presence makes the meeting user data, never auto-deleted.
	if r.s3Client != nil && safeSttPathSegment.MatchString(ownerID) && safeSttPathSegment.MatchString(meetingID) {
		keys, err := r.listSttObjectKeys(ctx, "audio/"+ownerID+"/"+meetingID+"/")
		if err != nil {
			return err
		}
		for _, key := range keys {
			if sttCheckpointName.MatchString(key[strings.LastIndex(key, "/")+1:]) {
				return ErrSttDeletionBlocked
			}
		}
	} else {
		return ErrSummaryObject
	}
	condExpr, err := expression.NewBuilder().WithCondition(sttFailedMeetingDeleteCondition(mark)).Build()
	if err != nil {
		return err
	}
	keys := meetingSingletonDeleteKeys(ownerID, meetingID)
	items := make([]types.TransactWriteItem, 0, len(keys))
	for i, key := range keys {
		del := &types.Delete{TableName: aws.String(r.tableName), Key: key}
		if i == 0 {
			del.ConditionExpression = condExpr.Condition()
			del.ExpressionAttributeNames = condExpr.Names()
			del.ExpressionAttributeValues = condExpr.Values()
		}
		items = append(items, types.TransactWriteItem{Delete: del})
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}, singleDynamoDBAttempt)
	var canceled *types.TransactionCanceledException
	if errors.As(err, &canceled) {
		for _, reason := range canceled.CancellationReasons {
			if aws.ToString(reason.Code) == "ConditionalCheckFailed" {
				return ErrConditionFailed
			}
		}
	}
	if err != nil {
		// A lost response may hide a committed transaction. Resolve it with a
		// consistent read of the meeting row instead of resending: absence
		// means the delete committed, so the caller must run object cleanup and
		// count it. An unreadable outcome stays a plain error, and object
		// cleanup is never attempted while the row might still exist.
		exists, readErr := r.sttMeetingRowExists(ctx, ownerID, meetingID)
		if readErr != nil || exists {
			return fmt.Errorf("delete failed recording: %w", errors.Join(err, readErr))
		}
		if cleanupErr := r.deleteAttachmentTextStates(ctx, meetingID); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
		return errors.Join(ErrSttDeletionPostCommit, err)
	}
	if err := r.deleteAttachmentTextStates(ctx, meetingID); err != nil {
		return errors.Join(ErrSttDeletionPostCommit, err)
	}
	return nil
}

var safeSttPathSegment = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// sttCheckpointName mirrors service.isCheckpointFileName's exact allowlist.
var sttCheckpointName = regexp.MustCompile(`^recording_progress\.(webm|m4a|ogg)$`)

// SttFailedMeetingObjectsResult counts current-version deletions.
type SttFailedMeetingObjectsResult struct {
	Deleted int
	Failed  int
}

// DeleteSttFailedMeetingObjects removes the current versions of a deleted
// failed recording's owned objects. The bucket is versioned, so the noncurrent
// versions remain as an operator-recoverable safety net. Failures surface.
func (r *DynamoDBRepository) DeleteSttFailedMeetingObjects(ctx context.Context, ownerID, meetingID string) (SttFailedMeetingObjectsResult, error) {
	var result SttFailedMeetingObjectsResult
	if r.s3Client == nil || !safeSttPathSegment.MatchString(ownerID) || !safeSttPathSegment.MatchString(meetingID) {
		return result, ErrSummaryObject
	}
	var keys []string
	for _, category := range []string{"audio", "images", "files"} {
		prefix := category + "/" + ownerID + "/" + meetingID + "/"
		listed, err := r.listSttObjectKeys(ctx, prefix)
		if err != nil {
			return result, err
		}
		keys = append(keys, listed...)
	}
	transcripts, err := r.listSttObjectKeys(ctx, "transcripts/"+meetingID)
	if err != nil {
		return result, err
	}
	for _, key := range transcripts {
		if IsSttMeetingTranscriptKey(meetingID, key) {
			keys = append(keys, key)
		}
	}
	for start := 0; start < len(keys); start += 1000 {
		batch := keys[start:min(start+1000, len(keys))]
		objects := make([]s3types.ObjectIdentifier, 0, len(batch))
		for _, key := range batch {
			objects = append(objects, s3types.ObjectIdentifier{Key: aws.String(key)})
		}
		out, err := r.s3Client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(r.bucketName), Delete: &s3types.Delete{Objects: objects, Quiet: aws.Bool(true)},
		})
		if err != nil {
			result.Failed += len(batch)
			continue
		}
		result.Failed += len(out.Errors)
		result.Deleted += len(batch) - len(out.Errors)
	}
	if result.Failed > 0 {
		return result, fmt.Errorf("failed recording object cleanup left %d objects", result.Failed)
	}
	return result, nil
}

var sttPartTranscriptSuffix = regexp.MustCompile(`^_part_[0-9]{3}\.json$`)

// IsSttMeetingTranscriptKey reports exact STT outputs and spill files of one meeting.
func IsSttMeetingTranscriptKey(meetingID, key string) bool {
	base := "transcripts/" + meetingID
	rest, ok := strings.CutPrefix(key, base)
	if !ok {
		return false
	}
	switch {
	case rest == ".json", rest == "-nova.json":
		return true
	case sttPartTranscriptSuffix.MatchString(rest):
		return true
	case strings.HasPrefix(rest, "/") && len(rest) > 1 && !strings.Contains(rest, ".."):
		return true
	}
	return false
}

func (r *DynamoDBRepository) listSttObjectKeys(ctx context.Context, prefix string) ([]string, error) {
	pages := s3.NewListObjectsV2Paginator(r.s3Client, &s3.ListObjectsV2Input{Bucket: aws.String(r.bucketName), Prefix: aws.String(prefix)})
	var keys []string
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list failed recording objects: %w", err)
		}
		for _, object := range page.Contents {
			if key := aws.ToString(object.Key); strings.HasPrefix(key, prefix) {
				keys = append(keys, key)
			}
		}
	}
	return keys, nil
}

// sttMeetingRowExists is a strongly consistent existence check used to resolve
// an ambiguous grace-delete transaction.
func (r *DynamoDBRepository) sttMeetingRowExists(ctx context.Context, ownerID, meetingID string) (bool, error) {
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), Key: meetingRowKey(ownerID, meetingID),
		ConsistentRead: aws.Bool(true), ProjectionExpression: aws.String("PK"),
	})
	if err != nil {
		return false, fmt.Errorf("read failed recording row: %w", err)
	}
	return len(out.Item) > 0, nil
}
