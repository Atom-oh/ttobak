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
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestNewsDeleteTransactionPinsPublicationAndCurrentOwner(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		admin                  bool
		accountID              string
		wantOwner, wantAccount bool
		wantValue              string
		wantItems              int
	}{
		{name: "personal owner", wantOwner: true, wantAccount: true, wantValue: "owner", wantItems: 2},
		{name: "admin", admin: true, wantItems: 2},
		{name: "account member", accountID: "acct-1", wantAccount: true, wantValue: "acct-1", wantItems: 4},
	} {
		seen := false
		client := dynamodb.New(dynamodb.Options{
			Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
			HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
				seen = true
				var input struct {
					TransactItems []map[string]struct {
						Key                       map[string]map[string]string
						ConditionExpression       string
						UpdateExpression          string
						ExpressionAttributeNames  map[string]string
						ExpressionAttributeValues map[string]map[string]any
					}
				}
				if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
					t.Fatal(err)
				}
				if len(input.TransactItems) != tc.wantItems {
					t.Fatalf("%s: %d transaction items, want %d", tc.name, len(input.TransactItems), tc.wantItems)
				}
				if tc.wantItems == 4 {
					// The account branch fences the account and the caller's
					// direct membership in the same transaction.
					for i, sk := range []string{"META", "MEMBER#owner"} {
						check := input.TransactItems[2+i]["ConditionCheck"]
						if check.Key["PK"]["S"] != "ACCOUNT#acct-1" || check.Key["SK"]["S"] != sk ||
							!strings.Contains(check.ConditionExpression, "attribute_exists") {
							t.Fatalf("%s: access check %d: %+v", tc.name, i, check)
						}
					}
				}
				source := input.TransactItems[0]["ConditionCheck"]
				doc := input.TransactItems[1]["Update"]
				if source.Key["SK"]["S"] != "CONFIG" || doc.Key["SK"]["S"] != "DOC#article" ||
					!strings.Contains(doc.UpdateExpression, "SET") {
					t.Fatal("incorrect transaction shape")
				}
				sourceFields := map[string]bool{}
				for _, field := range source.ExpressionAttributeNames {
					sourceFields[field] = true
				}
				if !sourceFields["sourceId"] || sourceFields["ownerId"] != tc.wantOwner || sourceFields["accountId"] != tc.wantAccount {
					t.Fatalf("%s: current ownership policy missing: %v", tc.name, sourceFields)
				}
				if tc.wantValue != "" {
					found := false
					for _, value := range source.ExpressionAttributeValues {
						found = found || value["S"] == tc.wantValue
					}
					if !found {
						t.Fatalf("%s: source condition does not pin %q", tc.name, tc.wantValue)
					}
				}
				docFields := map[string]bool{}
				for _, field := range doc.ExpressionAttributeNames {
					docFields[field] = true
				}
				for _, field := range []string{"attemptToken", "sourceRevision", "s3Key", "artifactKey", "qualityStatus", "markdownSha256", "inKB"} {
					if !docFields[field] {
						t.Fatalf("publication CAS omitted %s", field)
					}
				}
				hasDeleted, hasObservedToken := false, false
				for _, value := range doc.ExpressionAttributeValues {
					hasDeleted = hasDeleted || value["S"] == "DELETED"
					hasObservedToken = hasObservedToken || value["S"] == "observed-attemptToken"
					if value["M"] != nil {
						t.Fatal("raw attribute value was encoded as a map")
					}
				}
				if !hasDeleted || !hasObservedToken {
					t.Fatal("transaction values lost the observed identity")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
					Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			}),
		})
		observed := map[string]types.AttributeValue{}
		for _, field := range []string{"schemaVersion", "qualityStatus", "sourceId", "docHash", "attemptToken", "sourceRevision", "s3Key", "artifactKey", "markdownSha256", "artifactSha256", "type"} {
			observed[field] = &types.AttributeValueMemberS{Value: "observed-" + field}
		}
		err := NewCrawlerRepository(client, "news").MarkNewsDeleted(context.Background(), NewsReadKey{SourceID: "source", SortKey: "DOC#article"}, observed, "owner", tc.admin, tc.accountID)
		if err != nil || !seen {
			t.Fatalf("transaction failed: %v", err)
		}
	}
}

func TestLegacyDocumentDeleteCannotEraseConcurrentPublication(t *testing.T) {
	client := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			var input struct {
				ConditionExpression      string
				ExpressionAttributeNames map[string]string
			}
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			fields := map[string]bool{}
			for _, name := range input.ExpressionAttributeNames {
				fields[name] = true
			}
			if !fields["schemaVersion"] || !fields["qualityStatus"] || !fields["attemptToken"] ||
				strings.Count(input.ConditionExpression, "attribute_not_exists") != 3 {
				t.Fatal("legacy deletion did not exclude versioned publication")
			}
			return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
				Body: io.NopCloser(strings.NewReader(`{"__type":"ConditionalCheckFailedException","message":"replaced"}`))}, nil
		}),
	})
	err := NewCrawlerRepository(client, "news").DeleteDocument(context.Background(), "source", "article")
	if !errors.Is(err, ErrConditionFailed) {
		t.Fatalf("lost publication race was not a conflict: %v", err)
	}
}

func TestNewsDeleteRevokedMemberIsAccessDenied(t *testing.T) {
	for _, tc := range []struct {
		reasons string
		access  bool
	}{
		{`[{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"}]`, true},
		{`[{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"}]`, false},
	} {
		repo := &CrawlerRepository{client: summarySDKRepo(func(*http.Request) (*http.Response, error) {
			return summaryHTTPResponse(400, `{"__type":"TransactionCanceledException","CancellationReasons":`+tc.reasons+`}`, nil), nil
		}).client, tableName: "news"}
		observed := map[string]types.AttributeValue{}
		for _, field := range []string{"schemaVersion", "qualityStatus", "sourceId", "docHash", "attemptToken", "sourceRevision", "s3Key", "artifactKey", "markdownSha256", "artifactSha256", "type"} {
			observed[field] = &types.AttributeValueMemberS{Value: "observed-" + field}
		}
		err := repo.MarkNewsDeleted(context.Background(), NewsReadKey{SourceID: "source", SortKey: "DOC#article"}, observed, "member", false, "acct-1")
		if errors.Is(err, ErrAccountNewsAccess) != tc.access || (!tc.access && !errors.Is(err, ErrConditionFailed)) {
			t.Fatalf("%s: %v", tc.reasons, err)
		}
	}
}
