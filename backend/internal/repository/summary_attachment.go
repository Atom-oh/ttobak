package repository

import (
	"context"
	"reflect"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/ttobak/backend/internal/model"
)

// BindSummaryAttachment checks that captured model inputs still describe the
// canonical attachment, then carries exact row conditions into publication.
func (r *DynamoDBRepository) BindSummaryAttachment(ctx context.Context, snapshot *model.SummarySnapshot, att *model.Attachment) error {
	if !validSummarySnapshot(snapshot) || att == nil || att.MeetingID != snapshot.Meeting.MeetingID {
		return ErrConditionFailed
	}
	fields := []string{"attachmentId", "meetingId", "userId", "originalKey", "processedKey", "type", "status", "fileName", "processedContent", "description"}
	row, err := r.summaryRow(ctx, model.PrefixMeeting+att.MeetingID, model.PrefixAttachment+att.AttachmentID)
	if err != nil {
		return err
	}
	var current model.Attachment
	if err := attributevalue.UnmarshalMap(row, &current); err != nil {
		return err
	}
	expected, err := attributevalue.MarshalMap(att)
	if err != nil {
		return err
	}
	actual, err := attributevalue.MarshalMap(current)
	if err != nil {
		return err
	}
	for _, name := range fields {
		if !reflect.DeepEqual(expected[name], actual[name]) {
			return ErrConditionFailed
		}
	}
	check, err := summaryCheck(model.PrefixMeeting+att.MeetingID, model.PrefixAttachment+att.AttachmentID, row, fields)
	if err != nil {
		return err
	}
	checks := []model.SummaryCheck{check}
	if att.ExtractedText != nil {
		if att.SummaryTextState == nil {
			return ErrConditionFailed
		}
		stateRow, err := r.summaryRow(ctx, model.PrefixMeeting+att.MeetingID, model.PrefixAttachmentText+att.AttachmentID)
		if err != nil {
			return err
		}
		var state model.AttachmentTextState
		if err := attributevalue.UnmarshalMap(stateRow, &state); err != nil {
			return err
		}
		expectedState, _ := attributevalue.MarshalMap(att.SummaryTextState)
		actualState, _ := attributevalue.MarshalMap(state)
		stateFields := []string{"runId", "status", "sourceKey", "ownerId", "uploaderId", "sourceETag", "resultKey", "unitCount", "complete"}
		for _, name := range stateFields {
			if !reflect.DeepEqual(expectedState[name], actualState[name]) {
				return ErrConditionFailed
			}
		}
		check, err := summaryCheck(model.PrefixMeeting+att.MeetingID, model.PrefixAttachmentText+att.AttachmentID, stateRow, stateFields)
		if err != nil {
			return err
		}
		checks = append(checks, check)
		source := att.ExtractedText.Source
		object := model.SummaryObject{Bucket: source.Bucket, Key: source.Key, ETag: source.ETag}
		if err := r.CheckResummaryObjects(ctx, att.MeetingID, []model.SummaryObject{object}); err != nil {
			return err
		}
		snapshot.Objects = append(snapshot.Objects, object)
	}
	snapshot.Checks = append(snapshot.Checks, checks...)
	return nil
}
