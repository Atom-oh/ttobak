package repository

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/model"
)

func TestDocumentCatalogSDKQueriesAreBoundedConsistentAndMetadataOnly(t *testing.T) {
	calls := 0
	client := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			var payload struct {
				ConsistentRead           bool
				Limit                    int
				ProjectionExpression     string
				ExpressionAttributeNames map[string]string
				ExclusiveStartKey        map[string]map[string]string
			}
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if !payload.ConsistentRead {
				t.Fatal("catalog used an eventual primary read")
			}
			for _, field := range payload.ExpressionAttributeNames {
				if field == "content" || field == "publicShareToken" {
					t.Fatal("catalog requested body/capability fields")
				}
			}
			body := `{"Item":{"PK":{"S":"USER#reader"},"SK":{"S":"DOC#d1"},"docId":{"S":"d1"},"title":{"S":"note"}}}`
			if req.Header.Get("X-Amz-Target") == "DynamoDB_20120810.Query" {
				if payload.Limit != 20 || payload.ExclusiveStartKey["PK"]["S"] != "USER#reader" ||
					payload.ExclusiveStartKey["SK"]["S"] != "DOC#d2" {
					t.Fatal("query was not bounded/scoped")
				}
				body = `{"Items":[{"SK":{"S":"DOC#d1"}}],"LastEvaluatedKey":{"PK":{"S":"USER#reader"},"SK":{"S":"DOC#d1"}}}`
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
				Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
	})
	repo := NewDynamoDBRepository(client, "catalog-test")
	page, err := repo.QueryDocumentCandidates(context.Background(), "USER#reader", model.PrefixDoc, "DOC#d2", 20)
	if err != nil || page.NextKey != "DOC#d1" || len(page.Rows) != 1 || calls != 1 {
		t.Fatalf("query paged unexpectedly: %+v %v calls=%d", page, err, calls)
	}
	doc, err := repo.GetDocumentMetadata(context.Background(), "USER#reader", "d1")
	if err != nil || doc.Title != "note" || doc.Content != "" || calls != 2 {
		t.Fatalf("metadata read failed: %+v %v", doc, err)
	}
}
