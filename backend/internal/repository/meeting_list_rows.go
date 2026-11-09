package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// MeetingListRowFields are the only meeting attributes the list path reads
// for shared and team rows: row identity, MeetingListItem fields, the
// account-publication fields the access checks use, and the summary privacy
// fields of the account summary guard. Transcripts, notes, live summaries and
// other unbounded fields are never transferred.
var MeetingListRowFields = []string{
	"PK", "SK", "entityType", "meetingId", "userId", "accountId", "sharedToAccount",
	"title", "date", "status", "content", "participants", "tags", "sentiment", "duration",
	"createdAt", "updatedAt", "summaryPrivacy", "summaryAccountApproval",
}

// MeetingListRowsMaxKeys is the largest selection BatchGetMeetingListRows reads.
const MeetingListRowsMaxKeys = batchReadKeyLimit

// BatchGetMeetingListRows reads the canonical USER#/MEETING# rows for keys with
// strongly consistent, projected batch reads. The returned meetings are partial
// (MeetingListRowFields only), must not be written back, and are keyed by the
// requested key; missing rows are absent. Transcript references are never
// rehydrated. Any unprocessed, unexpected or undecodable row fails the read.
func (r *DynamoDBRepository) BatchGetMeetingListRows(ctx context.Context, keys []MeetingKey) (map[MeetingKey]*model.Meeting, error) {
	names := make([]expression.NameBuilder, 0, len(MeetingListRowFields))
	for _, name := range MeetingListRowFields {
		names = append(names, expression.Name(name))
	}
	projection, err := expression.NewBuilder().WithProjection(expression.NamesList(names[0], names[1:]...)).Build()
	if err != nil {
		return nil, fmt.Errorf("build meeting list projection: %w", err)
	}
	rowKeys := make([]batchRowKey, 0, len(keys))
	for _, key := range keys {
		if key.OwnerID == "" || key.MeetingID == "" {
			// Legacy share rows can lack ownerId. BatchGetMeetings looked
			// such a key up as "USER#"/"MEETING#..." and found nothing, so
			// the row is reported missing here as well; failing the whole
			// list would hide every other meeting.
			continue
		}
		rowKeys = append(rowKeys, batchRowKey{PK: model.PrefixUser + key.OwnerID, SK: model.PrefixMeeting + key.MeetingID})
	}
	out := make(map[MeetingKey]*model.Meeting, len(rowKeys))
	err = r.batchGetConsistent(ctx, rowKeys, &projection, func(row batchRowKey, item map[string]types.AttributeValue) error {
		var meeting model.Meeting
		if err := attributevalue.UnmarshalMap(item, &meeting); err != nil {
			return fmt.Errorf("unmarshal meeting: %w", err)
		}
		key := MeetingKey{OwnerID: strings.TrimPrefix(row.PK, model.PrefixUser), MeetingID: strings.TrimPrefix(row.SK, model.PrefixMeeting)}
		out[key] = &meeting
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("batch get meeting list rows: %w", err)
	}
	return out, nil
}
