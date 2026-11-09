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

func TestCrawlerDocumentCollectionIsolatesMalformedRows(t *testing.T) {
	for _, method := range []string{"source", "index", "scan"} {
		for _, failed := range []bool{false, true} {
			t.Run(method+map[bool]string{false: "_bad_row", true: "_storage_error"}[failed], func(t *testing.T) {
				client := dynamodb.New(dynamodb.Options{
					Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
					HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
						if failed {
							return nil, errors.New("storage unavailable")
						}
						// The malformed record appears first and must not hide its neighbor.
						data := `{"Items":[{"PK":{"S":"CRAWLER#source"},"SK":{"S":"DOC#bad"},"type":{"S":"news"},"summary":{"M":{}}},{"PK":{"S":"CRAWLER#source"},"SK":{"S":"DOC#good"},"type":{"S":"news"},"title":{"S":"Valid"}}],"LastEvaluatedKey":{"PK":{"S":"CRAWLER#source"},"SK":{"S":"DOC#good"}}}`
						// A subsequent page ends the index/scan pagination.
						var input map[string]json.RawMessage
						if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
							t.Fatal(err)
						}
						if _, ok := input["ExclusiveStartKey"]; ok {
							data = `{"Items":[]}`
						}
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
							Body: io.NopCloser(strings.NewReader(data))}, nil
					}),
				})
				repo := NewCrawlerRepository(client, "news")
				if method == "source" {
					docs, continuation, count, err := repo.ListDocuments(context.Background(), "source", "news", 20, nil)
					if failed {
						if err == nil {
							t.Fatal("storage failure hidden")
						}
					} else if err != nil || len(docs) != 1 || count != 1 || docs[0].DocHash != "good" || continuation != nil {
						t.Fatalf("malformed neighbor or continuation lost: %+v %v", docs, err)
					}
					return
				}
				query := repo.QueryDocumentsByType
				if method == "scan" {
					query = repo.ListAllDocumentsByType
				}
				docs, total, err := query(context.Background(), "news", 20, 0)
				if failed {
					if err == nil {
						t.Fatal("storage failure hidden")
					}
				} else if err != nil || total != 1 || len(docs) != 1 || docs[0].DocHash != "good" {
					t.Fatalf("valid document lost: %+v %v", docs, err)
				}
			})
		}
	}
}

func TestCrawlerInvalidDocumentIsDistinctFromReadFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		client := dynamodb.New(dynamodb.Options{
			Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
			HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
				if failed {
					return nil, errors.New("storage unavailable")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
					Body: io.NopCloser(strings.NewReader(`{"Item":{"type":{"S":"news"},"summary":{"M":{}}}}`))}, nil
			}),
		})
		got, err := NewCrawlerRepository(client, "news").GetDocument(context.Background(), "source", "hash")
		if got != nil || err == nil || errors.Is(err, ErrInvalidDocument) == failed {
			t.Fatalf("data/transport classification lost: %+v %v", got, err)
		}
	}
}

func TestCrawlerDeletedRowsDoNotConsumeVisiblePage(t *testing.T) {
	for _, method := range []string{"source", "index", "scan"} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			client := dynamodb.New(dynamodb.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
				HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					var input map[string]json.RawMessage
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatal(err)
					}
					rows := []map[string]any{}
					body := map[string]any{}
					if calls == 1 {
						// A complete discovery page contains only retained tombstones.
						for i := 0; i < 500; i++ {
							rows = append(rows, map[string]any{
								"PK":            map[string]string{"S": "CRAWLER#source"},
								"SK":            map[string]string{"S": "DOC#deleted"},
								"type":          map[string]string{"S": "news"},
								"qualityStatus": map[string]string{"S": "DELETED"},
							})
						}
						body["LastEvaluatedKey"] = map[string]any{"PK": map[string]string{"S": "CRAWLER#source"},
							"SK": map[string]string{"S": "DOC#deleted"}}
					} else {
						if calls != 2 || input["ExclusiveStartKey"] == nil {
							t.Fatal("continuation was lost")
						}
						rows = append(rows, map[string]any{
							"PK":            map[string]string{"S": "CRAWLER#source"},
							"SK":            map[string]string{"S": "DOC#live"},
							"type":          map[string]string{"S": "news"},
							"qualityStatus": map[string]string{"S": "PUBLISHED"},
						})
					}
					body["Items"] = rows
					data, err := json.Marshal(body)
					if err != nil {
						t.Fatal(err)
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
						Body: io.NopCloser(strings.NewReader(string(data)))}, nil
				}),
			})
			repo := NewCrawlerRepository(client, "news")
			if method == "source" {
				docs, continuation, total, err := repo.ListDocuments(context.Background(), "source", "news", 500, nil)
				if err != nil || len(docs) != 1 || docs[0].DocHash != "live" || total != 1 || continuation != nil || calls != 2 {
					t.Fatalf("tombstones exhausted visible source page: %+v %v", docs, err)
				}
				return
			}
			query := repo.QueryDocumentsByType
			if method == "scan" {
				query = repo.ListAllDocumentsByType
			}
			docs, total, err := query(context.Background(), "news", 500, 0)
			if err != nil || len(docs) != 1 || docs[0].DocHash != "live" || total != 1 || calls != 2 {
				t.Fatalf("tombstones exhausted visible page: %+v %v", docs, err)
			}
		})
	}
}

func TestDecodeCrawlerDocument_SingularServiceFallback(t *testing.T) {
	row := map[string]types.AttributeValue{
		"PK":      &types.AttributeValueMemberS{Value: "CRAWLER#tech"},
		"SK":      &types.AttributeValueMemberS{Value: "DOC#abc"},
		"type":    &types.AttributeValueMemberS{Value: "tech"},
		"service": &types.AttributeValueMemberS{Value: "Amazon EKS"},
	}
	doc, err := decodeCrawlerDocument(row)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.AWSServices) != 1 || doc.AWSServices[0] != "Amazon EKS" {
		t.Fatalf("AWSServices = %v, want [Amazon EKS]", doc.AWSServices)
	}
	row["awsServices"] = &types.AttributeValueMemberL{Value: []types.AttributeValue{&types.AttributeValueMemberS{Value: "AWS Lambda"}}}
	doc, _ = decodeCrawlerDocument(row)
	if len(doc.AWSServices) != 1 || doc.AWSServices[0] != "AWS Lambda" {
		t.Fatalf("explicit awsServices must win, got %v", doc.AWSServices)
	}
}

func TestDecodeCrawlerDocument_RelevanceFields(t *testing.T) {
	row := map[string]types.AttributeValue{
		"PK":               &types.AttributeValueMemberS{Value: "CRAWLER#news"},
		"SK":               &types.AttributeValueMemberS{Value: "DOC#abc"},
		"type":             &types.AttributeValueMemberS{Value: "news"},
		"awsRelevance":     &types.AttributeValueMemberS{Value: "Amazon Bedrock · Amazon S3"},
		"opportunityCount": &types.AttributeValueMemberN{Value: "2"},
		"qualityScore":     &types.AttributeValueMemberN{Value: "0.9"},
	}
	doc, err := decodeCrawlerDocument(row)
	if err != nil {
		t.Fatal(err)
	}
	if doc.AWSRelevance != "Amazon Bedrock · Amazon S3" || doc.OpportunityCount != 2 || doc.QualityScore != 0.9 {
		t.Fatalf("relevance fields = %q %d %v", doc.AWSRelevance, doc.OpportunityCount, doc.QualityScore)
	}
	delete(row, "awsRelevance")
	delete(row, "opportunityCount")
	delete(row, "qualityScore")
	doc, err = decodeCrawlerDocument(row)
	if err != nil {
		t.Fatal(err)
	}
	if doc.AWSRelevance != "" || doc.OpportunityCount != 0 || doc.QualityScore != 0 {
		t.Fatalf("legacy row must decode to zero values, got %+v", doc)
	}
}
