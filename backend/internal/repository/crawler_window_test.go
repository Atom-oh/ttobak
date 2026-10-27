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
)

// The daily news job reads one crawl window from GSI4 with a key range,
// following continuations, and never falls back to a table scan.
func TestListDocumentsCrawledBetweenQueriesTheWindow(t *testing.T) {
	var targets []string
	calls := 0
	client := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			targets = append(targets, req.Header.Get("X-Amz-Target"))
			var input struct {
				IndexName                 string
				KeyConditionExpression    string
				ExpressionAttributeNames  map[string]string
				ExpressionAttributeValues map[string]map[string]string
				ExclusiveStartKey         map[string]any
				ScanIndexForward          bool
			}
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			cond := input.KeyConditionExpression
			for placeholder, name := range input.ExpressionAttributeNames {
				cond = strings.ReplaceAll(cond, placeholder, name)
			}
			values := map[string]bool{}
			for _, v := range input.ExpressionAttributeValues {
				values[v["S"]+v["N"]] = true
			}
			if input.IndexName != "GSI4" || !strings.Contains(cond, "GSI4PK =") || !strings.Contains(cond, "GSI4SK BETWEEN") ||
				!values["DOC#news"] || !values["1000"] || !values["1999"] || !input.ScanIndexForward {
				t.Fatalf("query %+v (%s)", input, cond)
			}
			body := map[string]any{"Items": []map[string]any{{
				"PK": map[string]string{"S": "CRAWLER#src"}, "SK": map[string]string{"S": "DOC#d" + string(rune('0'+calls))},
				"type": map[string]string{"S": "news"}, "title": map[string]string{"S": "t"}, "url": map[string]string{"S": "u"},
				"crawledAt": map[string]string{"N": "1500"},
			}}}
			if calls == 1 {
				body["LastEvaluatedKey"] = map[string]any{"PK": map[string]string{"S": "CRAWLER#src"}, "SK": map[string]string{"S": "DOC#d1"}}
			} else if input.ExclusiveStartKey == nil {
				t.Fatal("continuation was lost")
			}
			data, _ := json.Marshal(body)
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
				Body: io.NopCloser(strings.NewReader(string(data)))}, nil
		}),
	})
	docs, err := NewCrawlerRepository(client, "t").ListDocumentsCrawledBetween(context.Background(), 1000, 2000)
	if err != nil || len(docs) != 2 || docs[0].DocHash != "d1" || docs[1].SourceID != "src" {
		t.Fatalf("docs=%+v err=%v", docs, err)
	}
	for _, target := range targets {
		if target != "DynamoDB_20120810.Query" {
			t.Fatalf("only Query is allowed, got %s", target)
		}
	}
	if empty, err := NewCrawlerRepository(client, "t").ListDocumentsCrawledBetween(context.Background(), 5, 5); err != nil || empty != nil || calls != 2 {
		t.Fatalf("an empty window reads nothing: %v %v", empty, err)
	}
}
