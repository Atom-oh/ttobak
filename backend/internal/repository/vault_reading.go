package repository

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
)

const MaxVaultTranscriptSourceBytes = 1 * 1024 * 1024

var ErrVaultSourceTooLarge = errors.New("vault transcript source exceeds 1 MiB")

// Keep room in the 256 MiB API for markdown, rune pagination and the second
// freshness read. Oversized sources fail without clipped or placeholder text.
func (r *DynamoDBRepository) ReadVaultTranscriptField(ctx context.Context, id, field, value string) (string, error) {
	if field != "transcriptA" && field != "transcriptB" && field != "transcriptSegments" {
		return "", ErrInvalidTranscriptRef
	}
	if !strings.HasPrefix(value, "s3://") {
		if len(value) > MaxVaultTranscriptSourceBytes {
			return "", ErrVaultSourceTooLarge
		}
		return value, nil
	}
	if r.s3Client == nil || r.bucketName == "" {
		return "", fmt.Errorf("transcript storage unavailable")
	}
	bucket, key, err := validateTranscriptRef(r.bucketName, id, field, value)
	if err != nil {
		return "", err
	}
	out, err := r.s3Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return "", err
	}
	defer out.Body.Close()
	if out.ContentLength != nil && *out.ContentLength > MaxVaultTranscriptSourceBytes {
		return "", ErrVaultSourceTooLarge
	}
	raw, err := io.ReadAll(io.LimitReader(out.Body, MaxVaultTranscriptSourceBytes+1))
	if err != nil {
		return "", err
	}
	if len(raw) > MaxVaultTranscriptSourceBytes {
		return "", ErrVaultSourceTooLarge
	}
	return string(raw), nil
}

type VaultAttachmentPage struct {
	Rows    []model.Attachment
	NextKey string
}

func (r *DynamoDBRepository) QueryVaultAttachments(ctx context.Context, id, after string) (*VaultAttachmentPage, error) {
	if !navigationID.MatchString(id) || (after != "" && (!strings.HasPrefix(after, model.PrefixAttachment) || len(after) > 256)) {
		return nil, fmt.Errorf("invalid vault attachment selection")
	}
	pk := model.PrefixMeeting + id
	expr, err := expression.NewBuilder().WithKeyCondition(expression.Key("PK").Equal(expression.Value(pk)).And(expression.Key("SK").BeginsWith(model.PrefixAttachment))).WithProjection(expression.NamesList(expression.Name("PK"), expression.Name("SK"), expression.Name("attachmentId"), expression.Name("meetingId"), expression.Name("fileName"), expression.Name("type"), expression.Name("status"), expression.Name("description"))).Build()
	if err != nil {
		return nil, err
	}
	input := &dynamodb.QueryInput{TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true), Limit: aws.Int32(10), ScanIndexForward: aws.Bool(true), KeyConditionExpression: expr.KeyCondition(), ProjectionExpression: expr.Projection(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()}
	if after != "" {
		input.ExclusiveStartKey = map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: pk}, "SK": &types.AttributeValueMemberS{Value: after}}
	}
	out, err := r.client.Query(ctx, input)
	if err != nil {
		return nil, err
	}
	page := &VaultAttachmentPage{Rows: []model.Attachment{}}
	if err := attributevalue.UnmarshalListOfMaps(out.Items, &page.Rows); err != nil {
		return nil, err
	}
	for _, a := range page.Rows {
		if a.PK != pk || a.MeetingID != id || a.SK != model.PrefixAttachment+a.AttachmentID {
			return nil, fmt.Errorf("foreign vault attachment")
		}
	}
	if len(out.LastEvaluatedKey) > 0 {
		key, ok := out.LastEvaluatedKey["SK"].(*types.AttributeValueMemberS)
		partition, valid := out.LastEvaluatedKey["PK"].(*types.AttributeValueMemberS)
		if !ok || !valid || partition.Value != pk || !strings.HasPrefix(key.Value, model.PrefixAttachment) || key.Value == after {
			return nil, fmt.Errorf("invalid attachment continuation")
		}
		page.NextKey = key.Value
	}
	return page, nil
}
