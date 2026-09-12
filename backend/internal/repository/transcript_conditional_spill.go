package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
)

// Conditional spills never overwrite an object that another read can reference.
// Readers accept both the legacy key and this strictly bounded version suffix.
func (r *DynamoDBRepository) storeConditionalTranscript(ctx context.Context, meetingID, field, text string, keys *[]string) (string, error) {
	version := strings.ReplaceAll(uuid.NewString(), "-", "")
	key := fmt.Sprintf("transcripts/%s/%s.%s.txt", meetingID, field, version)
	*keys = append(*keys, key)
	_, err := r.s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(r.bucketName), Key: aws.String(key),
		Body: strings.NewReader(text), ContentType: aws.String("text/plain; charset=utf-8"),
	})
	if err != nil {
		return "", fmt.Errorf("store conditional transcript spill: %w", err)
	}
	return fmt.Sprintf("s3://%s/%s", r.bucketName, key), nil
}

func (r *DynamoDBRepository) deleteUncommittedTranscriptSpills(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	var failures []error
	for _, key := range keys {
		if _, err := r.s3Client.DeleteObject(cleanup, &s3.DeleteObjectInput{
			Bucket: aws.String(r.bucketName), Key: aws.String(key),
		}); err != nil {
			failures = append(failures, fmt.Errorf("remove uncommitted transcript spill %s: %w", key, err))
		}
	}
	return errors.Join(failures...)
}
