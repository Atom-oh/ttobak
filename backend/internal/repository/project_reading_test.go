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

func TestProjectReadingQueriesOneConsistentSourceWindowWithoutEvidence(t *testing.T) {
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
				input.ExclusiveStartKey["PK"]["S"] != "PROJECT#p" ||
				input.ExclusiveStartKey["SK"]["S"] != "MEETINGREF#before" {
				t.Fatal("source window was unbounded or incorrectly scoped")
			}
			for _, name := range input.ExpressionAttributeNames {
				if name == "evidence" || name == "content" || name == "transcriptA" {
					t.Fatal("source query requested suppressed/private body fields")
				}
			}
			body := `{"Items":[],"LastEvaluatedKey":{"PK":{"S":"PROJECT#p"},"SK":{"S":"MEETINGREF#next"}}}`
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
				Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
	})
	repo := NewDynamoDBRepository(client, "project-reading")
	page, err := repo.QueryProjectReading(context.Background(), "p", "insights", "MEETINGREF#before")
	if err != nil || calls != 1 || page.NextKey != "MEETINGREF#next" {
		t.Fatalf("empty source page lost continuation: %+v %v", page, err)
	}
}

func TestProjectReadingResearchUsesConsistentPrimaryIdentity(t *testing.T) {
	calls := 0
	client := dynamodb.New(dynamodb.Options{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			var input struct {
				ConsistentRead bool
				Key            map[string]map[string]string
			}
			if json.NewDecoder(req.Body).Decode(&input) != nil || !input.ConsistentRead || input.Key["PK"]["S"] != "RESEARCH#r" || input.Key["SK"]["S"] != "CONFIG" {
				t.Fatal("research was not a consistent primary read")
			}
			id := "r"
			if calls == 2 {
				id = "foreign"
			}
			body := `{"Item":{"researchId":{"S":"` + id + `"},"userId":{"S":"owner"}}}`
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})})
	repo := NewDynamoDBRepository(client, "project-reading")
	item, err := repo.GetProjectReadingResearch(context.Background(), "r")
	if err != nil || item == nil || item.ResearchID != "r" {
		t.Fatal("valid research missing", err)
	}
	if _, err = repo.GetProjectReadingResearch(context.Background(), "r"); err == nil {
		t.Fatal("foreign identity accepted")
	}
}
