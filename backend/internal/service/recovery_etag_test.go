package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type s3RoundTrip func(*http.Request) (*http.Response, error)

func (f s3RoundTrip) Do(r *http.Request) (*http.Response, error) { return f(r) }

// The recovery copy is bound to the exact checkpoint that passed the quiet
// check, so a checkpoint the creator rewrote meanwhile fails the copy.
func TestRecoveryCopyIsBoundToTheObservedCheckpoint(t *testing.T) {
	listing := `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><Name>bucket</Name><KeyCount>1</KeyCount>` +
		`<Contents><Key>audio/owner/m/recording_progress.webm</Key><ETag>&quot;abc123&quot;</ETag>` +
		`<LastModified>2026-11-06T11:40:00.000Z</LastModified><Size>10</Size></Contents></ListBucketResult>`
	client := s3.New(s3.Options{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, UsePathStyle: true,
		BaseEndpoint: aws.String("https://s3.test"),
		HTTPClient: s3RoundTrip(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/xml"}},
				Body: io.NopCloser(strings.NewReader(listing)), Request: r}, nil
		})})
	svc := &UploadService{s3Client: client, bucketName: "bucket"}
	key, ext, modified, etag, err := svc.findProgressFile(context.Background(), "owner", "m")
	if err != nil || key != "audio/owner/m/recording_progress.webm" || ext != "webm" || etag != `"abc123"` ||
		!modified.Equal(time.Date(2026, 11, 6, 11, 40, 0, 0, time.UTC)) {
		t.Fatalf("checkpoint: %q %q %v %q %v", key, ext, modified, etag, err)
	}
	input := recoveryCopyInput("bucket", key, etag, "audio/owner/m/recording_recovered_x.webm")
	if aws.ToString(input.CopySourceIfMatch) != `"abc123"` || aws.ToString(input.CopySource) != "bucket/"+key {
		t.Fatalf("copy is not bound to the observed checkpoint: %+v", input)
	}
	if recoveryCopyInput("bucket", key, "", "final").CopySourceIfMatch != nil {
		t.Fatal("an unknown ETag must not invent a precondition")
	}
}
