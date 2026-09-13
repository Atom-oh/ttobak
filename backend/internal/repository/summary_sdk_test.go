package repository

import (
	"io"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type summaryHTTP func(*http.Request) (*http.Response, error)

func (f summaryHTTP) Do(r *http.Request) (*http.Response, error) { return f(r) }
func summaryHTTPResponse(status int, body string, headers http.Header) *http.Response {
	if headers == nil {
		headers = http.Header{"Content-Type": {"application/x-amz-json-1.0"}}
	}
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body))}
}
func summarySDKRepo(httpClient summaryHTTP) *DynamoDBRepository {
	cfg := aws.Config{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, HTTPClient: httpClient}
	db := dynamodb.NewFromConfig(cfg)
	storage := s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true; o.BaseEndpoint = aws.String("https://fixture.invalid") })
	return NewDynamoDBRepositoryWithS3(db, "table", storage, "bucket")
}
