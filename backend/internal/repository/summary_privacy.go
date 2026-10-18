package repository

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
)

var ErrSummarySharingUnsafe = errors.New("sharing-safe summary regeneration required")

// Readers are installed before these writers are enabled. This switch never
// disables protection for an already-stamped membership.
func SummaryPrivacyWritesEnabled() bool {
	value := os.Getenv("SUMMARY_PRIVACY_WRITER_ENABLED")
	return value == "1"
}

func summaryGenerationCondition(generation string) expression.ConditionBuilder {
	if generation == "" {
		return expression.AttributeNotExists(expression.Name("summaryPrivacy"))
	}
	return expression.Name("summaryPrivacy.generationId").Equal(expression.Value(generation))
}

func analysisGenerationCondition(generation string) expression.ConditionBuilder {
	match := expression.Name("summaryGeneration").Equal(expression.Value(generation))
	if generation == "" {
		return expression.AttributeNotExists(expression.Name("summaryGeneration")).Or(match)
	}
	return match
}

func stampSummaryMembership(member *model.AccountMember) *model.AccountMember {
	copy := *member
	if SummaryPrivacyWritesEnabled() {
		copy.AddedAt = time.Now().UTC()
		copy.SummaryPrivacyVersion = model.SummaryPrivacyVersion
	}
	return &copy
}

var summaryPrivacyFields = []string{
	"authorizationRevision", "summaryPrivacy", "linkedMeetingIds", "accountId", "sharedToAccount",
	"actionItems", "tags", "sentiment", "insights", "insightsRevision", "actionItemContinuity",
}

func (r *DynamoDBRepository) captureMeetingSummaryFromRow(row map[string]types.AttributeValue, ownerID, meetingID string) (*model.Meeting, error) {
	var meeting model.Meeting
	if err := attributevalue.UnmarshalMap(row, &meeting); err != nil {
		return nil, err
	}
	if meeting.UserID != ownerID || meeting.MeetingID != meetingID {
		return nil, ErrSummaryAccess
	}
	return &meeting, nil
}

func withSummaryPrivacyFields(fields []string) []string {
	result := append([]string(nil), fields...)
	seen := map[string]bool{}
	for _, field := range result {
		seen[field] = true
	}
	for _, field := range summaryPrivacyFields {
		if !seen[field] {
			result = append(result, field)
			seen[field] = true
		}
	}
	return result
}

// Legacy completion only invalidates clearance; unrelated derivative edits
// remain outside its source fence. Fresh provenance publishers bind the full
// privacy snapshot separately.
func withSummaryClearanceFields(fields []string) []string {
	return append(append([]string(nil), fields...), "authorizationRevision", "summaryPrivacy", "insightsRevision")
}

func (r *DynamoDBRepository) CaptureSummaryPrivacy(ctx context.Context, ownerID, meetingID string) (*model.SummarySnapshot, error) {
	return r.captureMeetingSummaryMetadata(ctx, ownerID, meetingID, withSummaryPrivacyFields([]string{
		"meetingId", "userId", "content", "title", "status", "date", "notes", "liveSummary",
		"transcriptA", "transcriptB", "selectedTranscript", "transcriptSegments",
	}))
}

// Called before any model invocation. Account publication itself is sufficient
// to skip private predecessors, even before any member/share cache row exists.
// Account-origin caches on an unshared meeting do not confer read access.
func (r *DynamoDBRepository) BindLinkedSummarySources(ctx context.Context, snapshot *model.SummarySnapshot) error {
	if !validSummarySnapshot(snapshot) {
		return ErrConditionFailed
	}
	for _, field := range summaryPrivacyFields {
		if _, ok := snapshot.Checks[0].Fields[field]; !ok {
			return ErrConditionFailed
		}
	}
	meeting := snapshot.Meeting
	if snapshot.SharingSafe || meeting.SharedToAccount || len(meeting.LinkedMeetingIDs) == 0 {
		return nil
	}
	if len(meeting.LinkedMeetingIDs) > 3 {
		return ErrSummaryLimit
	}
	shares, err := r.ListSharesForMeetingConsistent(ctx, meeting.MeetingID)
	if err != nil {
		return err
	}
	for _, share := range shares {
		if share.SharedToID != meeting.UserID && share.Origin != model.ShareOriginAccount {
			return nil
		}
	}
	seen := map[string]bool{meeting.MeetingID: true}
	for _, id := range meeting.LinkedMeetingIDs {
		if id == "" || seen[id] {
			return ErrSummaryAccess
		}
		seen[id] = true
		pk, sk := model.PrefixUser+meeting.UserID, model.PrefixMeeting+id
		row, err := r.summaryRow(ctx, pk, sk)
		if err != nil {
			return err
		}
		check, err := summaryCheck(pk, sk, row, []string{"meetingId", "userId", "title", "content", "actionItems", "summaryPrivacy"})
		if err != nil {
			return err
		}
		// Pin absent/empty sources too: a predecessor appearing during
		// generation requires a fresh attempt, not retroactive source binding.
		snapshot.Checks = append(snapshot.Checks, check)
		if !check.Exists {
			continue
		}
		source, err := r.captureMeetingSummaryFromRow(row, meeting.UserID, id)
		if err != nil {
			return err
		}
		if strings.TrimSpace(source.Content) == "" {
			continue
		}
		snapshot.LinkedSources = append(snapshot.LinkedSources, model.LinkedSummarySource{
			MeetingID: id, Title: source.Title, Content: source.Content, ActionItems: source.ActionItems,
		})
	}
	body, err := json.Marshal(snapshot.Checks)
	if err != nil {
		return err
	}
	if len(body) > 1024*1024 {
		return ErrSummaryLimit
	}
	return nil
}

// A grant and the revision increment are one transaction item alongside the
// actual recipient/cache writes. Parent conditions include exact provenance,
// content, relation set and derivative values observed before the grant.
func (r *DynamoDBRepository) summaryGrantFence(ctx context.Context, ownerID, meetingID string) (*types.Update, *model.SummarySnapshot, error) {
	return r.summaryAuthorizationFence(ctx, ownerID, meetingID, true)
}

func (r *DynamoDBRepository) summaryAuthorizationFence(ctx context.Context, ownerID, meetingID string, requireSafe bool) (*types.Update, *model.SummarySnapshot, error) {
	snapshot, err := r.CaptureSummaryPrivacy(ctx, ownerID, meetingID)
	if err != nil {
		return nil, nil, err
	}
	if snapshot == nil {
		return nil, nil, ErrConditionFailed
	}
	if requireSafe && !snapshot.Meeting.CanCreateSummaryGrant() {
		return nil, snapshot, ErrSummarySharingUnsafe
	}
	update, err := analysisUpdate(r.tableName, summaryRowKey(snapshot.Checks[0].PK, snapshot.Checks[0].SK),
		expression.Add(expression.Name("authorizationRevision"), expression.Value(1)), summaryCondition(snapshot.Checks[0]))
	return update, snapshot, err
}

func summaryPublicationPrivacy(snapshot *model.SummarySnapshot, content string) *model.SummaryPrivacy {
	state := snapshot.Meeting.SummaryPrivacyState()
	inherited := state
	if snapshot.Meeting.SummaryPrivacy == nil && !snapshot.Meeting.HasSummaryDerivatives() {
		state = model.SummaryPrivacySafe
		inherited = ""
	}
	if len(snapshot.LinkedSources) > 0 {
		state = model.SummaryPrivacyPrivate
	}
	if snapshot.SharingSafe && len(snapshot.LinkedSources) == 0 {
		state = model.SummaryPrivacySafe
		inherited = ""
	}
	result := &model.SummaryPrivacy{Version: model.SummaryPrivacyVersion, State: state,
		GenerationID: uuid.NewString(), ContentSHA256: model.SummaryPrivacyHash(content), InheritedState: inherited}
	for _, source := range snapshot.LinkedSources {
		result.Dependencies = append(result.Dependencies, model.SummaryPrivacyDependency{MeetingID: source.MeetingID,
			TitleSHA256: model.SummaryPrivacyHash(source.Title), ContentSHA256: model.SummaryPrivacyHash(source.Content),
			ActionItemsSHA256: model.SummaryPrivacyHash(source.ActionItems)})
	}
	return result
}

func summaryPrivacyRevisionUpdate(update expression.UpdateBuilder) expression.UpdateBuilder {
	return update.Add(expression.Name("authorizationRevision"), expression.Value(1))
}

// Legacy/content editors cannot attest to the newly written summary. This
// invalidation is independent of the switch that creates new provenance, so
// stored clearance cannot survive writer rollback or an ordinary human edit.
// Explicit source-fenced publication may install fresh provenance separately.
func invalidateSummaryClearance(update expression.UpdateBuilder) expression.UpdateBuilder {
	return summaryPrivacyRevisionUpdate(update.
		Remove(expression.Name("summaryPrivacy")).
		Remove(expression.Name("insightsRevision")))
}

// Model derivatives use the exact stored source snapshot, including the
// generation and task bytes actually consumed. A late worker cannot repopulate
// fields cleared by sharing-safe regeneration, even when new content is equal.
func (r *DynamoDBRepository) PublishSummaryDerivatives(ctx context.Context, snapshot *model.SummarySnapshot, fields map[string]interface{}) error {
	if !validSummarySnapshot(snapshot) || len(fields) == 0 {
		return ErrConditionFailed
	}
	var update expression.UpdateBuilder
	first := true
	for field, value := range fields {
		switch field {
		case "tags", "sentiment", "insights":
		default:
			return ErrConditionFailed
		}
		if first {
			update = expression.Set(expression.Name(field), expression.Value(value))
			first = false
		} else {
			update = update.Set(expression.Name(field), expression.Value(value))
		}
	}
	if _, insights := fields["insights"]; insights {
		update = update.Set(expression.Name("insightsRevision"), expression.Value(uuid.NewString()))
	}
	update = update.Set(expression.Name("updatedAt"), expression.Value(time.Now().UTC()))
	op, err := analysisUpdate(r.tableName, meetingKey(snapshot.Meeting.UserID, snapshot.Meeting.MeetingID),
		update, summaryCondition(snapshot.Checks[0]))
	if err != nil {
		return err
	}
	if err := r.CheckResummaryObjects(ctx, snapshot.Meeting.MeetingID, snapshot.Objects); err != nil {
		return err
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: op.TableName, Key: op.Key, UpdateExpression: op.UpdateExpression,
		ConditionExpression: op.ConditionExpression, ExpressionAttributeNames: op.ExpressionAttributeNames,
		ExpressionAttributeValues: op.ExpressionAttributeValues,
	}, noSummaryRetries)
	return summaryWriteError(err)
}

// Safe declassification is an explicit owner operation. Retained continuity has
// no task text or assignee/deadline; it cannot become evidence for the next model.
func sharingSafeResetFields(fields map[string]interface{}, continuity map[string][]model.ActionItemContinuity) {
	fields["actionItems"], fields["insights"], fields["liveSummary"] = "[]", "[]", ""
	fields["tags"], fields["sentiment"] = []string{}, ""
	fields["insightsRevision"] = uuid.NewString()
	fields["actionItemContinuity"] = continuity
}

func summaryActionContinuity(meeting *model.Meeting) (map[string][]model.ActionItemContinuity, error) {
	result := map[string][]model.ActionItemContinuity{}
	total := 0
	for hash, values := range meeting.ActionItemContinuity {
		if len(hash) != 64 || len(values) > 500 {
			return nil, ErrSummaryLimit
		}
		for _, value := range values {
			if value.ID == "" || len(value.ID) > 128 {
				return nil, ErrSummaryLimit
			}
			result[hash] = append(result[hash], value)
			total++
		}
	}
	if strings.TrimSpace(meeting.ActionItems) != "" {
		if len(meeting.ActionItems) > 256*1024 {
			return nil, ErrSummaryLimit
		}
		var prior []struct {
			ID, Text, Assignee, DueDate string
			Completed                   *bool `json:"completed"`
			Done                        bool  `json:"done"`
		}
		if err := json.Unmarshal([]byte(meeting.ActionItems), &prior); err != nil {
			return nil, ErrSummaryLimit
		}
		for _, task := range prior {
			if task.ID == "" {
				continue
			}
			if len(task.ID) > 128 {
				return nil, ErrSummaryLimit
			}
			completed := task.Done
			if task.Completed != nil {
				completed = *task.Completed
			}
			key := model.ActionContinuityHash(task.Text, task.Assignee, task.DueDate)
			result[key] = append(result[key], model.ActionItemContinuity{ID: task.ID, Completed: completed})
			total++
		}
	}
	if total > 500 {
		return nil, ErrSummaryLimit
	}
	return result, nil
}
