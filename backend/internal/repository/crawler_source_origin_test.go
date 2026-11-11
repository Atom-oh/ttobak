package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

// GetSourceOrigin projects only PK, sourceName and accountId and decodes the
// two names leniently: a legacy row's scalar list values never reach it, and
// a non-string value reads as "".
func TestGetSourceOriginProjectsAndDecodesLeniently(t *testing.T) {
	for _, tc := range []struct {
		name        string
		response    string
		wantName    string
		wantAccount string
		wantFound   bool
	}{
		{name: "missing row", response: `{}`},
		{name: "string values", response: `{"Item":{"PK":{"S":"CRAWLER#src"},"sourceName":{"S":"Hana"},"accountId":{"S":"acct-1"}}}`,
			wantName: "Hana", wantAccount: "acct-1", wantFound: true},
		{name: "non-string values", response: `{"Item":{"PK":{"S":"CRAWLER#src"},"sourceName":{"BOOL":false},"accountId":{"N":"0"}}}`,
			wantFound: true},
		{name: "null and absent values", response: `{"Item":{"PK":{"S":"CRAWLER#src"},"sourceName":{"NULL":true}}}`,
			wantFound: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := dynamodb.New(dynamodb.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
				HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
					if target := req.Header.Get("X-Amz-Target"); target != "DynamoDB_20120810.GetItem" {
						t.Errorf("unexpected request %s", target)
					}
					var input struct {
						Key                      map[string]map[string]string
						ProjectionExpression     string
						ExpressionAttributeNames map[string]string
					}
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Error(err)
					}
					if input.Key["PK"]["S"] != "CRAWLER#src" || input.Key["SK"]["S"] != "CONFIG" {
						t.Errorf("unexpected key %v", input.Key)
					}
					projected := map[string]bool{}
					for _, token := range strings.Split(input.ProjectionExpression, ",") {
						token = strings.TrimSpace(token)
						if name, ok := input.ExpressionAttributeNames[token]; ok {
							token = name
						}
						projected[token] = true
					}
					if len(projected) != 3 || !projected["PK"] || !projected["sourceName"] || !projected["accountId"] {
						t.Errorf("projection = %v", projected)
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
						Body: io.NopCloser(strings.NewReader(tc.response))}, nil
				}),
			})
			name, account, found, err := NewCrawlerRepository(client, "news").GetSourceOrigin(context.Background(), "src")
			if err != nil || name != tc.wantName || account != tc.wantAccount || found != tc.wantFound {
				t.Fatalf("got (%q, %q, %v, %v), want (%q, %q, %v)", name, account, found, err, tc.wantName, tc.wantAccount, tc.wantFound)
			}
		})
	}
}

func TestGetSourceOriginReturnsReadErrors(t *testing.T) {
	client := dynamodb.New(dynamodb.Options{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("down") })})
	if _, _, found, err := NewCrawlerRepository(client, "news").GetSourceOrigin(context.Background(), "src"); err == nil || found {
		t.Fatalf("read error hidden: found=%v err=%v", found, err)
	}
}
