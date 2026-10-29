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

func TestAccountHierarchyScanReadsBoundedMetadataAndKeepsEmptyContinuation(t *testing.T) {
	calls := 0
	client := dynamodb.New(dynamodb.Options{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if !strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".Scan") {
				t.Fatal("expected operator scan")
			}
			var input struct {
				Limit                    int
				ConsistentRead           bool
				ExpressionAttributeNames map[string]string
				ExclusiveStartKey        map[string]map[string]string
			}
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.Limit != 100 || !input.ConsistentRead {
				t.Fatal("scan work is not bounded/strong")
			}
			allowed := map[string]bool{"PK": true, "SK": true, "accountId": true, "parentAccountId": true, "entityType": true}
			for _, name := range input.ExpressionAttributeNames {
				if !allowed[name] {
					t.Fatalf("scan fetched private content attribute %q", name)
				}
			}
			body := `{"Items":[],"LastEvaluatedKey":{"PK":{"S":"USER#owner"},"SK":{"S":"MEETING#meeting"}}}`
			if calls == 2 {
				if input.ExclusiveStartKey["PK"]["S"] != "USER#owner" || input.ExclusiveStartKey["SK"]["S"] != "MEETING#meeting" {
					t.Fatal("empty page continuation lost")
				}
				body = `{"Items":[{"PK":{"S":"ACCOUNT#child"},"SK":{"S":"META"},"accountId":{"S":"child"},"parentAccountId":{"S":"parent"},"entityType":{"S":"ACCOUNT"}}]}`
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})})
	repo := NewDynamoDBRepository(client, "accounts-test")
	page, err := repo.ScanAccountHierarchyPage(context.Background(), "")
	if err != nil || len(page.Accounts) != 0 || page.NextCursor == "" {
		t.Fatalf("empty scan page lost: %+v/%v", page, err)
	}
	page, err = repo.ScanAccountHierarchyPage(context.Background(), page.NextCursor)
	if err != nil || len(page.Accounts) != 1 || page.Accounts[0].AccountID != "child" || page.NextCursor != "" {
		t.Fatalf("scan continuation failed: %+v/%v", page, err)
	}
	if _, err := repo.ScanAccountHierarchyPage(context.Background(), "malformed"); err == nil || calls != 2 {
		t.Fatal("malformed scan cursor reached storage")
	}
}
