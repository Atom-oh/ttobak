package service

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
)

func (p *IndexAWSProvider) KnowledgeBucket() string { return p.bucket }

func (p *IndexAWSProvider) HeadKnowledge(ctx context.Context, key string) (IndexObject, error) {
	if _, ok := model.KnowledgeIndexResource(key); !ok {
		return IndexObject{}, ErrIndexInvalid
	}
	return p.headObject(ctx, p.bucket, key)
}

func (p *IndexAWSProvider) ReadKnowledge(ctx context.Context, object IndexObject) ([]byte, error) {
	if _, ok := model.KnowledgeIndexResource(object.Key); !ok {
		return nil, ErrIndexInvalid
	}
	return p.readObject(ctx, p.bucket, object)
}

// One bounded page per prefix per tick. Cursors persist in the same coordinator
// as canonical work, including when a page contains only filtered text/sidecars.
func (p *IndexAWSProvider) KnowledgePage(ctx context.Context, prefix, cursor string) ([]string, string, error) {
	if prefix != "kb/" && prefix != "shared/" {
		return nil, "", ErrIndexInvalid
	}
	input := &s3.ListObjectsV2Input{Bucket: aws.String(p.bucket), Prefix: aws.String(prefix), MaxKeys: aws.Int32(100)}
	if cursor != "" {
		input.ContinuationToken = aws.String(cursor)
	}
	page, err := p.s3.ListObjectsV2(ctx, input)
	if err != nil {
		return nil, "", err
	}
	next := aws.ToString(page.NextContinuationToken)
	if aws.ToBool(page.IsTruncated) && (next == "" || next == cursor) {
		return nil, "", ErrIndexInvalid
	}
	keys := make([]string, 0, len(page.Contents))
	for _, object := range page.Contents {
		keys = append(keys, aws.ToString(object.Key))
	}
	return keys, next, nil
}
