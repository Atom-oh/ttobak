package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/repository"
)

type recoveryHTTPClient func(*http.Request) (*http.Response, error)

func (f recoveryHTTPClient) Do(r *http.Request) (*http.Response, error) { return f(r) }
func recoveryResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestRejectedRecoveryCleansOnlyItsUniqueCopy(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		ambiguous, cleanupFailure bool
	}{
		{"definitive conflict", false, false}, {"cleanup failure surfaces", false, true}, {"ambiguous write retained", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			updates := 0
			ddb := dynamodb.New(dynamodb.Options{Region: "us-west-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 3,
				HTTPClient: recoveryHTTPClient(func(r *http.Request) (*http.Response, error) {
					if strings.HasSuffix(r.Header.Get("X-Amz-Target"), "GetItem") {
						return recoveryResponse(200, `{"Item":{"PK":{"S":"USER#owner"},"SK":{"S":"MEETING#draft"},"userId":{"S":"owner"},"meetingId":{"S":"draft"},"status":{"S":"recording"},"updatedAt":{"S":"2026-09-25T00:00:00Z"}}}`), nil
					}
					updates++
					if tc.ambiguous {
						return recoveryResponse(500, `{"__type":"InternalServerError","message":"response uncertain"}`), nil
					}
					return recoveryResponse(400, `{"__type":"ConditionalCheckFailedException","message":"changed"}`), nil
				})})
			copied, deleted := "", ""
			client := s3.New(s3.Options{Region: "us-west-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
				BaseEndpoint: aws.String("http://storage.example.test"), UsePathStyle: true,
				HTTPClient: recoveryHTTPClient(func(r *http.Request) (*http.Response, error) {
					switch r.Method {
					case "GET":
						return recoveryResponse(200, `<ListBucketResult><Contents><Key>audio/owner/draft/recording_progress.webm</Key></Contents></ListBucketResult>`), nil
					case "PUT":
						copied = r.URL.Path
						return recoveryResponse(200, `<CopyObjectResult><ETag>"copy"</ETag></CopyObjectResult>`), nil
					case "DELETE":
						deleted = r.URL.Path
						if tc.cleanupFailure {
							return nil, errors.New("cleanup unavailable")
						}
						return recoveryResponse(204, ""), nil
					default:
						t.Fatalf("unexpected S3 method %s", r.Method)
						return nil, nil
					}
				})})
			service := NewUploadService(client, repository.NewDynamoDBRepository(ddb, "test"), "bucket", nil)
			err := service.RecoverMeeting(context.Background(), "owner", "draft")
			if updates != 1 {
				t.Fatalf("binding must not be retried: %d updates", updates)
			}
			if err == nil {
				t.Fatal("expected recovery failure")
			}
			if !strings.Contains(copied, "/recording_recovered_") || strings.Contains(copied, "recording_progress") {
				t.Fatalf("unexpected copy %q", copied)
			}
			if tc.ambiguous {
				if deleted != "" {
					t.Fatal("ambiguous commit must retain audio")
				}
				return
			}
			if !errors.Is(err, repository.ErrConditionFailed) || deleted != copied {
				t.Fatalf("cleanup=%q copy=%q err=%v", deleted, copied, err)
			}
			if errors.Is(err, ErrRecordingRecoveryCleanup) != tc.cleanupFailure {
				t.Fatalf("cleanup error not represented: %v", err)
			}
		})
	}
}
