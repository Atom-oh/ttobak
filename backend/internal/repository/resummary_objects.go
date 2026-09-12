package repository

import (
	"context"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
)

var ErrSummaryObject = errors.New("summary source object unavailable")

const summaryTranscriptLimit = 8 * 1024 * 1024

// These operations are used only after current metadata authorization.
func (r *DynamoDBRepository) ReadResummaryTranscript(ctx context.Context, meetingID, field, value string) (string, *model.SummaryObject, error) {
	if !strings.HasPrefix(value, "s3://") {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "s3:") {
			return "", nil, ErrInvalidTranscriptRef
		}
		if len(value) > summaryTranscriptLimit || !utf8.ValidString(value) {
			return "", nil, ErrSummaryLimit
		}
		return value, nil, nil
	}
	bucket, key, err := validateTranscriptRef(r.bucketName, meetingID, field, value)
	if err != nil {
		return "", nil, err
	}
	if r.s3Client == nil {
		return "", nil, ErrSummaryObject
	}
	head, err := r.s3Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return "", nil, ErrSummaryObject
	}
	if head.ContentLength == nil || *head.ContentLength < 0 || *head.ContentLength > summaryTranscriptLimit || aws.ToString(head.ETag) == "" {
		return "", nil, ErrSummaryLimit
	}
	out, err := r.s3Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), IfMatch: head.ETag})
	if err != nil {
		return "", nil, ErrSummaryObject
	}
	if out.Body == nil {
		return "", nil, ErrSummaryObject
	}
	defer out.Body.Close()
	if aws.ToString(out.ETag) != aws.ToString(head.ETag) || aws.ToString(out.VersionId) != aws.ToString(head.VersionId) {
		return "", nil, ErrConditionFailed
	}
	body, err := io.ReadAll(io.LimitReader(out.Body, summaryTranscriptLimit+1))
	if err != nil {
		return "", nil, ErrSummaryObject
	}
	if len(body) > summaryTranscriptLimit || int64(len(body)) != *head.ContentLength || !utf8.Valid(body) {
		return "", nil, ErrSummaryLimit
	}
	return string(body), &model.SummaryObject{Bucket: bucket, Key: key, ETag: aws.ToString(out.ETag), VersionID: aws.ToString(out.VersionId)}, nil
}

func (r *DynamoDBRepository) CheckResummaryObjects(ctx context.Context, meetingID string, bindings []model.SummaryObject) error {
	if len(bindings) > 22 {
		return ErrSummaryLimit
	}
	for _, binding := range bindings {
		if r.s3Client == nil || binding.Bucket != r.bucketName || binding.ETag == "" {
			return ErrSummaryObject
		}
		parts := strings.Split(binding.Key, "/")
		allowed := len(parts) == 4 && parts[0] == "files" && parts[1] != "" && parts[2] == meetingID && parts[3] != "" && parts[3] != "." && parts[3] != ".."
		if strings.HasPrefix(binding.Key, "transcripts/") {
			for _, field := range transcriptFamilyFields {
				if _, _, err := validateTranscriptRef(r.bucketName, meetingID, field, "s3://"+binding.Bucket+"/"+binding.Key); err == nil {
					allowed = true
				}
			}
		}
		if !allowed || strings.ContainsAny(binding.Key, "\\\x00\r\n") {
			return ErrInvalidTranscriptRef
		}
		head, err := r.s3Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(binding.Bucket), Key: aws.String(binding.Key)})
		if err != nil {
			return ErrSummaryObject
		}
		if aws.ToString(head.ETag) != binding.ETag || (binding.VersionID != "" && aws.ToString(head.VersionId) != binding.VersionID) {
			return ErrConditionFailed
		}
	}
	return nil
}
