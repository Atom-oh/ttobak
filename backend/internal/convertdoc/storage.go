package convertdoc

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

var ErrPreviewSourceChanged = errors.New("preview source changed")

type PreviewS3 interface {
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

type PreviewSource struct {
	Key, ETag, VersionID string
}
type PreviewDestination struct{ Key, ETag string }
type PreviewStorage struct {
	client PreviewS3
	bucket string
}

func NewPreviewStorage(client PreviewS3, bucket string) *PreviewStorage {
	return &PreviewStorage{client: client, bucket: bucket}
}

func validPreviewKey(key, prefix string) bool {
	parts := strings.Split(key, "/")
	return len(parts) == 3 && parts[0] == prefix && parts[1] != "" && parts[2] != "" &&
		parts[1] != "." && parts[1] != ".." && parts[2] != "." && parts[2] != ".." &&
		!strings.ContainsAny(key, "\\\x00")
}

// Snapshot runs before conversion. A late converter cannot unconditionally
// overwrite a different preview published while it was processing.
func (s *PreviewStorage) Snapshot(ctx context.Context, key string) (PreviewDestination, error) {
	destination := PreviewDestination{Key: key}
	if !validPreviewKey(key, "docs-pdf") {
		return destination, errors.New("invalid preview key")
	}
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	var missing *types.NotFound
	if errors.As(err, &missing) {
		return destination, nil
	}
	if err != nil {
		return destination, err
	}
	if out == nil || aws.ToString(out.ETag) == "" {
		return destination, errors.New("preview has no ETag")
	}
	destination.ETag = *out.ETag
	return destination, nil
}

// Download binds metadata to the actual GET response, not the upload event.
func (s *PreviewStorage) Download(ctx context.Context, key string, target io.Writer) (PreviewSource, error) {
	source := PreviewSource{Key: key}
	if !validPreviewKey(key, "docs") || !IsSlideExtension(key) {
		return source, errors.New("invalid slide source")
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return source, err
	}
	if out == nil || out.Body == nil {
		return source, errors.New("slide source has no body")
	}
	defer out.Body.Close()
	if aws.ToString(out.ETag) == "" || out.ContentLength == nil || *out.ContentLength < 0 || *out.ContentLength == 1<<63-1 {
		return source, errors.New("invalid slide source metadata")
	}
	source.ETag, source.VersionID = *out.ETag, aws.ToString(out.VersionId)
	n, err := io.Copy(target, io.LimitReader(out.Body, *out.ContentLength+1))
	if err != nil {
		return source, err
	}
	if n != *out.ContentLength {
		return source, errors.New("slide source length mismatch")
	}
	return source, nil
}

func (s *PreviewStorage) Publish(ctx context.Context, source PreviewSource, destination PreviewDestination, body io.Reader) error {
	if source.ETag == "" || !validPreviewKey(source.Key, "docs") ||
		destination.Key != "docs-pdf/"+strings.TrimPrefix(source.Key, "docs/")+".pdf" {
		return errors.New("invalid preview binding")
	}
	current, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(source.Key)})
	if err != nil {
		return err
	}
	if current == nil || aws.ToString(current.ETag) != source.ETag || aws.ToString(current.VersionId) != source.VersionID {
		return ErrPreviewSourceChanged
	}
	metadata := map[string]string{"source-etag": source.ETag}
	if source.VersionID != "" {
		metadata["source-version-id"] = source.VersionID
	}
	input := &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(destination.Key), Body: body,
		ContentType: aws.String("application/pdf"), Metadata: metadata,
	}
	if destination.ETag == "" {
		input.IfNoneMatch = aws.String("*")
	} else {
		input.IfMatch = aws.String(destination.ETag)
	}
	// A transport failure can follow a successful PUT. Propagate it without
	// deleting the preview or retrying a stale destination precondition.
	_, err = s.client.PutObject(ctx, input, func(options *s3.Options) { options.Retryer = aws.NopRetryer{} })
	return err
}
