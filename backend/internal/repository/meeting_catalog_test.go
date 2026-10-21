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

func TestMeetingCatalogUsesOneDateSortedCandidateQueryAndCompactEmptyContinuation(t *testing.T) {
	calls := 0
	client := dynamodb.New(dynamodb.Options{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1, HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		var input struct {
			IndexName                string
			Limit                    int
			ScanIndexForward         bool
			ExpressionAttributeNames map[string]string
			ExclusiveStartKey        map[string]map[string]string
		}
		if json.NewDecoder(req.Body).Decode(&input) != nil || input.IndexName != "GSI1" || input.Limit != 1 || input.ScanIndexForward {
			t.Fatal("candidate window lost actual limit or newest-first order")
		}
		for _, name := range input.ExpressionAttributeNames {
			if name == "content" || name == "notes" || name == "transcriptA" {
				t.Fatal("candidate query hydrated body")
			}
		}
		if calls == 2 && input.ExclusiveStartKey["PK"]["S"] != "ACCOUNT#a" {
			t.Fatal("sparse membership continuation lost")
		}
		body := `{"Items":[],"LastEvaluatedKey":{"PK":{"S":"ACCOUNT#a"},"SK":{"S":"MEMBER#reader"},"GSI1PK":{"S":"USER#reader"},"GSI1SK":{"S":"ACCOUNT#a"}}}`
		if calls == 2 {
			body = `{"Items":[]}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	repo := NewDynamoDBRepository(client, "catalog")
	page, err := repo.QueryMeetingCatalog(context.Background(), "reader", "owned", "", "", 1)
	if err != nil || calls != 1 || len(page.Rows) != 0 || page.NextKey == "" {
		t.Fatal("empty bounded query lost continuation", err)
	}
	page, err = repo.QueryMeetingCatalog(context.Background(), "reader", "owned", "", page.NextKey, 1)
	if err != nil || calls != 2 || page.NextKey != "" {
		t.Fatal("bounded continuation failed", err)
	}
	if _, err = repo.QueryMeetingCatalog(context.Background(), "reader", "owned", "", "invalid-cursor", 1); err == nil || calls != 2 {
		t.Fatal("malformed cursor reached DynamoDB")
	}
}
func TestMeetingCatalogReadsCurrentMetadataWithoutTranscriptHydration(t *testing.T) {
	client := dynamodb.New(dynamodb.Options{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1, HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
		var input struct {
			ConsistentRead           bool
			Key                      map[string]map[string]string
			ExpressionAttributeNames map[string]string
		}
		if json.NewDecoder(req.Body).Decode(&input) != nil || !input.ConsistentRead || input.Key["PK"]["S"] != "USER#owner" || input.Key["SK"]["S"] != "MEETING#m" {
			t.Fatal("metadata lookup was not owner-bound current primary")
		}
		hasPrivacy := false
		for _, name := range input.ExpressionAttributeNames {
			if name == "summaryPrivacy" {
				hasPrivacy = true
			}
			if name == "notes" || strings.HasPrefix(name, "transcript") {
				t.Fatal("catalog requested full source text")
			}
		}
		if !hasPrivacy {
			t.Fatal("catalog metadata omitted summary privacy")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(`{"Item":{"meetingId":{"S":"m"},"userId":{"S":"owner"},"title":{"S":"current"}}}`))}, nil
	})})
	repo := NewDynamoDBRepository(client, "catalog")
	m, err := repo.GetMeetingCatalogMetadata(context.Background(), "owner", "m")
	if err != nil || m == nil || m.Title != "current" {
		t.Fatal("metadata lost", err)
	}
}
