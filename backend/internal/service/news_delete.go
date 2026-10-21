package service

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/repository"
)

type newsPublicationDeleter interface {
	MarkNewsDeleted(context.Context, repository.NewsReadKey, map[string]types.AttributeValue, string, bool) error
}

// deletePublishedNews returns handled=false only for a current legacy row.
func (s *InsightsService) deletePublishedNews(ctx context.Context, userID string, isAdmin bool, sourceID, docHash string) (bool, error) {
	keys := newsKeys(sourceID, docHash)
	rows, err := s.newsRows(ctx, keys)
	if err != nil {
		return true, err
	}
	row, config := rows[keys[0]], rows[keys[1]]
	if len(row) == 0 || len(config) == 0 {
		return true, ErrNotFound
	}
	if !isAdmin && newsString(config, "ownerId") != userID {
		return true, ErrForbidden
	}
	if !isVersionedNews(row) {
		return false, nil
	}
	// A tombstone remains retryable even after the source's aliases, status or
	// fingerprint changed. Its immutable identity must still be well formed.
	validation := make(map[string]types.AttributeValue, len(row))
	for field, value := range row {
		validation[field] = value
	}
	if newsString(row, "qualityStatus") == "DELETED" {
		validation["qualityStatus"] = &types.AttributeValueMemberS{Value: "PUBLISHED"}
	}
	if !validNewsPublication(validation, sourceID, docHash) {
		return true, ErrNotFound
	}
	writer, ok := s.news.(newsPublicationDeleter)
	if !ok || s.artifactBucketName == "" {
		return true, fmt.Errorf("news publication deletion is unavailable")
	}
	if err := writer.MarkNewsDeleted(ctx, keys[0], row, userID, isAdmin); err != nil {
		return true, err
	}
	scanCache.clear()
	for _, object := range []struct{ bucket, key string }{
		{s.kbBucketName, newsString(row, "s3Key")},
		{s.artifactBucketName, newsString(row, "artifactKey")},
	} {
		if _, err := s.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(object.bucket), Key: aws.String(object.key),
		}); err != nil {
			return true, fmt.Errorf("news unpublished; object cleanup needs retry: %w", err)
		}
	}
	return true, nil
}
