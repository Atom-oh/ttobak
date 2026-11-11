package service

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
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type newsMetadataHTTP func(*http.Request) (*http.Response, error)

func (f newsMetadataHTTP) Do(req *http.Request) (*http.Response, error) { return f(req) }

func TestNewsPublicListIsolatesUnsupportedIdentitiesThroughRepository(t *testing.T) {
	for _, badIdentity := range []struct{ source, hash string }{
		{strings.Repeat("가", 171), "bad"},
		{"", "bad"},
		{"source", strings.Repeat("b", 129)},
	} {
		t.Run(badIdentity.source+"/"+badIdentity.hash, func(t *testing.T) {
			scanCache.clear()
			defer scanCache.clear()
			good := map[string]any{"PK": map[string]string{"S": "CRAWLER#source"}, "SK": map[string]string{"S": "DOC#good"},
				"type": map[string]string{"S": "news"}, "title": map[string]string{"S": "Valid neighbor"}}
			bad := map[string]any{"PK": map[string]string{"S": "CRAWLER#" + badIdentity.source},
				"SK":   map[string]string{"S": "DOC#" + badIdentity.hash},
				"type": map[string]string{"S": "news"}, "title": map[string]string{"S": "Unsupported identity"}}
			failRead := false
			storageFailure := errors.New("current storage unavailable")
			batches := 0
			client := dynamodb.New(dynamodb.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
				HTTPClient: newsMetadataHTTP(func(req *http.Request) (*http.Response, error) {
					var body map[string]any
					switch req.Header.Get("X-Amz-Target") {
					case "DynamoDB_20120810.GetItem":
						body = map[string]any{} // no source CONFIG row: a common source
					case "DynamoDB_20120810.Scan":
						body = map[string]any{"Items": []any{bad, good}}
					case "DynamoDB_20120810.BatchGetItem":
						batches++
						var input struct {
							RequestItems map[string]struct {
								Keys []map[string]map[string]string
							}
						}
						if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
							t.Fatal(err)
						}
						for _, key := range input.RequestItems["news"].Keys {
							if key["PK"]["S"] != "CRAWLER#source" || (key["SK"]["S"] != "CONFIG" && key["SK"]["S"] != "DOC#good") {
								t.Fatal("unsupported identity reached a canonical batch")
							}
						}
						if failRead {
							return nil, storageFailure
						}
						body = map[string]any{"Responses": map[string]any{"news": []any{good}}}
					default:
						t.Fatal("unexpected storage request")
					}
					data, err := json.Marshal(body)
					if err != nil {
						t.Fatal(err)
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
						Body: io.NopCloser(strings.NewReader(string(data)))}, nil
				}),
			})
			repo := repository.NewCrawlerRepository(client, "news")
			svc := &InsightsService{repo: repo, news: repo}
			got, err := svc.ListInsights(context.Background(), "user-1", "news", "", "", nil, "newest", 1, 20)
			if err != nil || len(got.Documents) != 1 || got.Documents[0].Title != "Valid neighbor" || got.TotalCount != 1 || batches != 1 {
				t.Fatalf("unsupported identity hid valid neighbor: %+v %v batches=%d", got, err, batches)
			}
			failRead = true
			if _, err := svc.ListInsights(context.Background(), "user-1", "news", "", "", nil, "newest", 1, 20); !errors.Is(err, storageFailure) {
				t.Fatalf("real storage failure hidden: %v", err)
			}
			priorBatches := batches
			invalid := model.CrawledDocument{SourceID: badIdentity.source, DocHash: badIdentity.hash, Type: "news"}
			for _, docs := range [][]model.CrawledDocument{
				{invalid},
				{invalid, {SourceID: "tech", Type: "tech", Title: "Retained tech"}},
			} {
				list, err := svc.currentNewsList(context.Background(), docs)
				if err != nil || len(list) != len(docs)-1 || batches != priorBatches {
					t.Fatalf("empty news selection exposed invalid row or read storage: %+v %v", list, err)
				}
			}
		})
	}
}

func TestNewsPublicListIsolatesMetadataThroughRepository(t *testing.T) {
	for _, scope := range []string{"source", ""} {
		t.Run("source_"+scope, func(t *testing.T) {
			scanCache.clear()
			defer scanCache.clear()
			good := map[string]any{"PK": map[string]string{"S": "CRAWLER#source"}, "SK": map[string]string{"S": "DOC#good"},
				"type": map[string]string{"S": "news"}, "title": map[string]string{"S": "Valid neighbor"}}
			bad := map[string]any{"PK": map[string]string{"S": "CRAWLER#source"}, "SK": map[string]string{"S": "DOC#bad"},
				"type": map[string]string{"S": "news"}, "summary": map[string]any{"M": map[string]any{}}}
			client := dynamodb.New(dynamodb.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
				HTTPClient: newsMetadataHTTP(func(req *http.Request) (*http.Response, error) {
					var body map[string]any
					switch req.Header.Get("X-Amz-Target") {
					case "DynamoDB_20120810.GetItem":
						body = map[string]any{} // no source CONFIG row: a common source
					case "DynamoDB_20120810.Query", "DynamoDB_20120810.Scan":
						body = map[string]any{"Items": []any{bad, good}}
					case "DynamoDB_20120810.BatchGetItem":
						body = map[string]any{"Responses": map[string]any{"news": []any{good}}}
					default:
						t.Fatal("unexpected storage request")
					}
					data, err := json.Marshal(body)
					if err != nil {
						t.Fatal(err)
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
						Body: io.NopCloser(strings.NewReader(string(data)))}, nil
				}),
			})
			repo := repository.NewCrawlerRepository(client, "news")
			svc := &InsightsService{repo: repo, news: repo}
			got, err := svc.ListInsights(context.Background(), "user-1", "news", scope, "", nil, "newest", 1, 20)
			if err != nil || len(got.Documents) != 1 || got.Documents[0].Title != "Valid neighbor" || got.TotalCount != 1 {
				t.Fatalf("public list hid valid neighbor: %+v %v", got, err)
			}
		})
	}
}

func TestNewsLegacyDetailRejectsUnrenderableCurrentMetadataBeforeS3(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		svc, fixture, object, keys := newNewsReadFixture(t)
		row := newsAV(t, map[string]any{"PK": "CRAWLER#우리은행", "SK": "DOC#" + strings.Repeat("a", 16),
			"type": "news", "title": "Legacy", "summary": strings.Repeat("x", 25000),
			"s3Key": "shared/news/우리은행/" + strings.Repeat("a", 16) + ".md"})
		if malformed {
			row["summary"] = &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{}}
		}
		fixture.rows[keys[0]] = row
		got, err := svc.GetDocumentDetail(context.Background(), "user-1", keys[0].SourceID, strings.Repeat("a", 16))
		if got != nil || !errors.Is(err, ErrNotFound) || len(object.gets) != 0 {
			t.Fatalf("unrenderable current metadata reached S3: %+v %v %v", got, err, object.gets)
		}
	}
}

func TestNewsDetailInitialMetadataFailureClassification(t *testing.T) {
	for _, failed := range []bool{false, true} {
		client := dynamodb.New(dynamodb.Options{
			Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
			HTTPClient: newsMetadataHTTP(func(req *http.Request) (*http.Response, error) {
				if failed {
					return nil, errors.New("storage unavailable")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
					Body: io.NopCloser(strings.NewReader(`{"Item":{"type":{"S":"news"},"summary":{"M":{}}}}`))}, nil
			}),
		})
		svc := &InsightsService{repo: repository.NewCrawlerRepository(client, "news")}
		got, err := svc.GetDocumentDetail(context.Background(), "user-1", "source", "hash")
		if got != nil || err == nil || errors.Is(err, ErrNotFound) == failed {
			t.Fatalf("data/transport classification lost: %+v %v", got, err)
		}
	}
}

// A CONFIG row with legacy scalar list values or a non-string accountId still
// lists and reads its documents as common.
func TestInsightsOriginToleratesLegacySourceRows(t *testing.T) {
	for _, tc := range []struct {
		name      string
		accountID any
	}{
		{name: "legacy scalar lists"},
		{name: "numeric accountId", accountID: map[string]string{"N": "7"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scanCache.clear()
			defer scanCache.clear()
			config := map[string]any{
				"PK": map[string]string{"S": "CRAWLER#legacy"}, "SK": map[string]string{"S": "CONFIG"},
				"sourceName": map[string]string{"S": "Legacy Bank"},
				"aliases":    map[string]bool{"BOOL": false}, "newsQueries": map[string]string{"N": "0"},
				"customUrls": map[string]string{"S": ""}, "subscribers": map[string]bool{"BOOL": false},
			}
			if tc.accountID != nil {
				config["accountId"] = tc.accountID
			}
			doc := map[string]any{
				"PK": map[string]string{"S": "CRAWLER#legacy"}, "SK": map[string]string{"S": "DOC#legacydoc"},
				"type": map[string]string{"S": "tech"}, "title": map[string]string{"S": "Legacy article"},
				"s3Key": map[string]string{"S": "shared/aws-docs/legacy/legacydoc.md"},
			}
			client := dynamodb.New(dynamodb.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
				HTTPClient: newsMetadataHTTP(func(req *http.Request) (*http.Response, error) {
					var body map[string]any
					switch req.Header.Get("X-Amz-Target") {
					case "DynamoDB_20120810.Query", "DynamoDB_20120810.Scan":
						body = map[string]any{"Items": []any{doc}}
					case "DynamoDB_20120810.GetItem":
						var input struct{ Key map[string]map[string]string }
						if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
							t.Error(err)
						}
						body = map[string]any{"Item": doc}
						if input.Key["SK"]["S"] == "CONFIG" {
							body = map[string]any{"Item": config}
						}
					default:
						t.Errorf("unexpected storage request %s", req.Header.Get("X-Amz-Target"))
						body = map[string]any{}
					}
					data, err := json.Marshal(body)
					if err != nil {
						t.Fatal(err)
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
						Body: io.NopCloser(strings.NewReader(string(data)))}, nil
				}),
			})
			repo := repository.NewCrawlerRepository(client, "news")
			svc := &InsightsService{repo: repo, news: repo, s3: &newsS3Fixture{data: "# Legacy"}, kbBucketName: "knowledge"}
			for _, scope := range []string{"", "legacy"} {
				got, err := svc.ListInsights(context.Background(), "user-1", "tech", scope, "", nil, "newest", 1, 20)
				if err != nil || got.TotalCount != 1 || len(got.Documents) != 1 {
					t.Fatalf("source=%q legacy row failed the list: %+v %v", scope, got, err)
				}
				if doc := got.Documents[0]; doc.SourceKind != "common" || doc.SourceName != "Legacy Bank" || doc.AccountID != "" {
					t.Fatalf("source=%q origin = %+v", scope, doc)
				}
			}
			detail, err := svc.GetDocumentDetail(context.Background(), "user-1", "legacy", "legacydoc")
			if err != nil || detail.SourceKind != "common" || detail.Content != "# Legacy" {
				t.Fatalf("legacy row failed the detail: %+v %v", detail, err)
			}
		})
	}
}
