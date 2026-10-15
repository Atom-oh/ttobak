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
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestNavigationSDKReconstructsScopedKeysAndKeepsQueriesBounded(t *testing.T) {
	for _, kind := range []string{"accounts", "owned", "direct", "account-projects"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			client := dynamodb.New(dynamodb.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
				HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					var input struct {
						IndexName                string
						ConsistentRead           bool
						Limit                    int
						ExclusiveStartKey        map[string]map[string]string
						ExpressionAttributeNames map[string]string
					}
					if json.NewDecoder(req.Body).Decode(&input) != nil {
						t.Fatal("invalid wire request")
					}
					indexed := kind == "accounts" || kind == "direct"
					if input.Limit != 2 || indexed != (input.IndexName == "GSI1") || indexed == input.ConsistentRead {
						t.Fatal("incorrect index consistency or query bound")
					}
					for name, value := range navigationKey("reader", kind, "account", "before") {
						if input.ExclusiveStartKey[name]["S"] != value.(*types.AttributeValueMemberS).Value {
							t.Fatal("continuation key escaped caller scope")
						}
					}
					for _, name := range input.ExpressionAttributeNames {
						if name == "description" || name == "name" || name == "content" {
							t.Fatal("candidate query requested resource bodies")
						}
					}
					key := map[string]map[string]string{}
					for name, value := range navigationKey("reader", kind, "account", "next") {
						key[name] = map[string]string{"S": value.(*types.AttributeValueMemberS).Value}
					}
					data, _ := json.Marshal(map[string]interface{}{"Items": []interface{}{key}, "LastEvaluatedKey": key})
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
						Body: io.NopCloser(strings.NewReader(string(data)))}, nil
				}),
			})
			repo := NewDynamoDBRepository(client, "navigation")
			page, err := repo.QueryNavigationCandidates(context.Background(), "reader", kind, "account", "before", 2)
			if err != nil || calls != 1 || len(page.IDs) != 1 || page.IDs[0] != "next" || page.NextID != "next" {
				t.Fatalf("bad bounded navigation page: %+v %v calls=%d", page, err, calls)
			}
		})
	}
}
