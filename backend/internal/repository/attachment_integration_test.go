package repository

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

type attachmentRoundTrip func(*http.Request) (*http.Response, error)

func (f attachmentRoundTrip) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestListAttachmentsContinuesAfterEmptyPage(t *testing.T) {
	calls := 0
	client := dynamodb.New(dynamodb.Options{Region: "ap-northeast-2",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient: attachmentRoundTrip(func(r *http.Request) (*http.Response, error) {
			calls++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["ConsistentRead"] != true {
				t.Fatal("attachment enumeration must be current")
			}
			response := `{"Items":[],"LastEvaluatedKey":{"PK":{"S":"MEETING#m"},"SK":{"S":"ATTACH#a"}}}`
			if calls == 2 {
				if body["ExclusiveStartKey"] == nil {
					t.Fatal("pagination key missing")
				}
				response = `{"Items":[{"PK":{"S":"MEETING#m"},"SK":{"S":"ATTACH#b"},"attachmentId":{"S":"b"},"meetingId":{"S":"m"}}]}`
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(response))}, nil
		}),
		Retryer: aws.NopRetryer{},
	})
	items, err := NewDynamoDBRepository(client, "table").ListAttachments(context.Background(), "m")
	if err != nil || calls != 2 || len(items) != 1 || items[0].AttachmentID != "b" {
		t.Fatalf("items=%v calls=%d error=%v", items, calls, err)
	}
}
