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

// A non-admin legacy delete under an account source is one transaction that
// pins the CONFIG account, the legacy document shape and the caller's direct
// membership before any object is removed.
func TestClaimLegacyAccountDocumentDeleteFencesMembership(t *testing.T) {
	calls := 0
	repo := &CrawlerRepository{client: indexingWire(t, func(target string, body map[string]interface{}) string {
		calls++
		if target != "DynamoDB_20120810.TransactWriteItems" {
			t.Fatalf("target %s", target)
		}
		items := body["TransactItems"].([]interface{})
		if len(items) != 4 {
			t.Fatalf("items: %d", len(items))
		}
		source := items[0].(map[string]interface{})["ConditionCheck"].(map[string]interface{})
		key := source["Key"].(map[string]interface{})
		if key["PK"].(map[string]interface{})["S"] != "CRAWLER#acct_acct-1_0123abcd" || key["SK"].(map[string]interface{})["S"] != "CONFIG" {
			t.Fatalf("source check: %#v", source)
		}
		requireAll(t, "source condition", resolved(source, "ConditionExpression"), "attribute_exists (PK)", `accountId = {"S":"acct-1"}`)
		doc := items[1].(map[string]interface{})["Update"].(map[string]interface{})
		if doc["Key"].(map[string]interface{})["SK"].(map[string]interface{})["S"] != "DOC#doc1" {
			t.Fatalf("doc update: %#v", doc)
		}
		requireAll(t, "doc condition", resolved(doc, "ConditionExpression"), "attribute_exists (PK)",
			"attribute_not_exists (schemaVersion)", "attribute_not_exists (qualityStatus)", "attribute_not_exists (attemptToken)")
		requireAll(t, "doc update", resolved(doc, "UpdateExpression"), `deleteClaimedBy = {"S":"teammate"}`, "deleteClaimedAt")
		for i, sk := range []string{"META", "MEMBER#teammate"} {
			check := items[2+i].(map[string]interface{})["ConditionCheck"].(map[string]interface{})
			k := check["Key"].(map[string]interface{})
			if k["PK"].(map[string]interface{})["S"] != "ACCOUNT#acct-1" || k["SK"].(map[string]interface{})["S"] != sk ||
				!strings.Contains(check["ConditionExpression"].(string), "attribute_exists") {
				t.Fatalf("access check %d: %#v", i, check)
			}
		}
		return `{}`
	}).client, tableName: "news"}
	if err := repo.ClaimLegacyAccountDocumentDelete(context.Background(), "acct_acct-1_0123abcd", "doc1", "acct-1", "teammate"); err != nil || calls != 1 {
		t.Fatalf("claim: %v %d", err, calls)
	}
	if err := repo.ClaimLegacyAccountDocumentDelete(context.Background(), "acct_acct-1_0123abcd", "doc1", "acct-1", ""); err == nil || calls != 1 {
		t.Fatal("an empty caller must be rejected before any write")
	}
}

func TestClaimLegacyAccountDocumentDeleteFailureMapping(t *testing.T) {
	for _, tc := range []struct {
		reasons string
		want    error
	}{
		{`[{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"}]`, ErrAccountNewsAccess},
		{`[{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"}]`, ErrAccountNewsAccess},
		{`[{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"},{"Code":"None"}]`, ErrAccountNewsAccess},
		{`[{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"}]`, ErrConditionFailed},
		{`[{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"ConditionalCheckFailed"}]`, ErrAccountNewsAccess},
		{`[{"Code":"None"},{"Code":"TransactionConflict"},{"Code":"None"},{"Code":"None"}]`, nil},
	} {
		repo := &CrawlerRepository{client: summarySDKRepo(func(*http.Request) (*http.Response, error) {
			return summaryHTTPResponse(400, `{"__type":"TransactionCanceledException","CancellationReasons":`+tc.reasons+`}`, nil), nil
		}).client, tableName: "news"}
		err := repo.ClaimLegacyAccountDocumentDelete(context.Background(), "src", "doc1", "acct-1", "member")
		if tc.want == nil {
			if err == nil || errors.Is(err, ErrAccountNewsAccess) || errors.Is(err, ErrConditionFailed) {
				t.Fatalf("%s: conflict must stay internal: %v", tc.reasons, err)
			}
			continue
		}
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v", tc.reasons, err)
		}
	}
}
