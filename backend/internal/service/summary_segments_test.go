package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/repository"
)

func TestSummarySegmentReadFailuresNeverBecomeSuccessfulAbsence(t *testing.T) {
	for _, test := range [][2]int{{500, 0}, {412, 0}, {500, 2}, {500, 3}} {
		code, failHead := test[0], test[1]
		models, saves, conflicts, heads := 0, 0, 0, 0
		client := noteSourceHTTPClient(func(req *http.Request) (*http.Response, error) {
			status, body := 200, `{}`
			switch req.Header.Get("X-Amz-Target") {
			case "DynamoDB_20120810.GetItem":
				body = `{"Item":{"PK":{"S":"USER#owner"},"SK":{"S":"MEETING#m"},"userId":{"S":"owner"},"meetingId":{"S":"m"},"status":{"S":"summarizing"},"transcriptA":{"S":"실제 발언"},"transcriptSegments":{"S":"s3://bucket/transcripts/m/transcriptSegments.txt"}}}`
			case "DynamoDB_20120810.UpdateItem":
				conflicts++
			case "DynamoDB_20120810.TransactWriteItems":
				saves++
			case "DynamoDB_20120810.Query":
				body = `{"Items":[]}`
			default:
				if req.Method == "HEAD" {
					heads++
					if heads == failHead {
						return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Etag": {`"v1"`}, "Content-Length": {"6"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				if req.Method == "GET" {
					if failHead > 0 {
						return &http.Response{StatusCode: 200, Header: http.Header{"Etag": {`"v1"`}}, Body: io.NopCloser(strings.NewReader("한글"))}, nil
					}
					status = code
					body = `<Error><Code>InternalError</Code></Error>`
					if code == 412 {
						body = `<Error><Code>PreconditionFailed</Code></Error>`
					}
				} else {
					models++
					body = `{"content":[{"type":"text","text":"조용히 시각을 잃은 요약"}],"stop_reason":"end_turn"}`
				}
			}
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		cfg := aws.Config{Region: "ap-northeast-2", HTTPClient: client, Retryer: func() aws.Retryer { return aws.NopRetryer{} },
			Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture"}, nil
			})}
		storage := s3.NewFromConfig(cfg)
		svc := NewBedrockService(bedrockruntime.NewFromConfig(cfg), storage, repository.NewDynamoDBRepositoryWithS3(dynamodb.NewFromConfig(cfg), "table", storage, "bucket"))
		_, err := svc.SummarizeTranscript(context.Background(), "m", "owner", "")
		wantModels := 0
		if failHead == 3 {
			wantModels = 1
		}
		if err == nil || models != wantModels || saves != 0 || (code == 412 && (!errors.Is(err, ErrSummaryConflict) || conflicts != 1)) ||
			code == 500 && (errors.Is(err, ErrSummaryConflict) || conflicts != 0) {
			t.Errorf("code=%d head=%d model=%d save=%d conflict=%d err=%v", code, failHead, models, saves, conflicts, err)
		}
	}
}
