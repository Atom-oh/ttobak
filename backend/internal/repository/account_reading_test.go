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

func TestAccountReadingQueriesOneConsistentSourceWindowWithoutEvidence(t *testing.T) {
	calls := 0
	client := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			var input struct {
				ConsistentRead           bool
				Limit                    int
				ExpressionAttributeNames map[string]string
				ExclusiveStartKey        map[string]map[string]string
			}
			if json.NewDecoder(req.Body).Decode(&input) != nil || !input.ConsistentRead || input.Limit != 10 ||
				input.ExclusiveStartKey["PK"]["S"] != "ACCOUNT#acct" ||
				input.ExclusiveStartKey["SK"]["S"] != "INSIGHT#before" {
				t.Fatal("source window was unbounded or incorrectly scoped")
			}
			for _, name := range input.ExpressionAttributeNames {
				if name == "evidence" || name == "content" || name == "transcriptA" {
					t.Fatal("source query requested suppressed/private body fields")
				}
			}
			body := `{"Items":[],"LastEvaluatedKey":{"PK":{"S":"ACCOUNT#acct"},"SK":{"S":"INSIGHT#next"}}}`
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
				Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
	})
	repo := NewDynamoDBRepository(client, "account-reading")
	page, err := repo.QueryAccountReading(context.Background(), "acct", "insights", "INSIGHT#before")
	if err != nil || calls != 1 || page.NextKey != "INSIGHT#next" {
		t.Fatalf("empty source page lost continuation: %+v %v", page, err)
	}
}

func TestAccountReadingProjectionPreservesInsightFreshness(t *testing.T) {
	client := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			var input struct {
				ExpressionAttributeNames map[string]string
			}
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			names := map[string]bool{}
			for _, name := range input.ExpressionAttributeNames {
				names[name] = true
			}
			if !names["summaryGeneration"] || !names["insightsRevision"] || names["evidence"] {
				t.Fatal("bounded projection cannot enforce current private/fresh insight rules")
			}
			body := `{"Items":[{"PK":{"S":"ACCOUNT#acct"},"SK":{"S":"INSIGHT#current"},"summaryGeneration":{"S":"generation"},"insightsRevision":{"S":"revision"}}]}`
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
				Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
	})
	page, err := NewDynamoDBRepository(client, "account-reading").QueryAccountReading(context.Background(), "acct", "insights", "")
	if err != nil || len(page.Insights) != 1 || page.Insights[0].SummaryGeneration != "generation" ||
		page.Insights[0].InsightsRevision != "revision" {
		t.Fatalf("insight source identity was lost: %+v %v", page, err)
	}
}

func TestAccountReadingRejectsUnboundOrNonAdvancingSourceContinuation(t *testing.T) {
	for name, key := range map[string]string{
		"foreign partition": `{"PK":{"S":"ACCOUNT#other"},"SK":{"S":"INSIGHT#next"}}`,
		"foreign section":   `{"PK":{"S":"ACCOUNT#acct"},"SK":{"S":"MEMBER#next"}}`,
		"repeated":          `{"PK":{"S":"ACCOUNT#acct"},"SK":{"S":"INSIGHT#before"}}`,
		"backwards":         `{"PK":{"S":"ACCOUNT#acct"},"SK":{"S":"INSIGHT#a"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			client := dynamodb.New(dynamodb.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
				HTTPClient: meetingListHTTPClientFunc(func(*http.Request) (*http.Response, error) {
					body := `{"Items":[],"LastEvaluatedKey":` + key + `}`
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
						Body: io.NopCloser(strings.NewReader(body))}, nil
				}),
			})
			page, err := NewDynamoDBRepository(client, "account-reading").QueryAccountReading(context.Background(), "acct", "insights", "INSIGHT#before")
			if err == nil || page != nil {
				t.Fatal("unbound/nonadvancing source key became a continuation", page)
			}
		})
	}
}
