package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

func TestNewsReadBatchesStrongKeysAndRetriesOnlyUnprocessed(t *testing.T) {
	// Chunks are read concurrently, so requests are identified by content:
	// the full 100-key chunk (answered with one key left unprocessed), its
	// one-key retry of exactly DOC#099, and the one-key DOC#100 chunk.
	var mu sync.Mutex
	calls, fullChunks, retries, tails := 0, 0, 0, 0
	client := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if req.Header.Get("X-Amz-Target") != "DynamoDB_20120810.BatchGetItem" {
				t.Error("unexpected operation")
			}
			var input struct {
				RequestItems map[string]struct {
					ConsistentRead bool
					Keys           []map[string]map[string]string
				}
			}
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			request := input.RequestItems["news"]
			if !request.ConsistentRead || (len(request.Keys) != 100 && len(request.Keys) != 1) {
				t.Errorf("call %d: unbounded/eventual batch: %d keys, consistent=%v", calls, len(request.Keys), request.ConsistentRead)
			}
			rows := request.Keys
			body := map[string]any{}
			switch {
			case len(request.Keys) == 100:
				fullChunks++
				rows = rows[:99]
				body["UnprocessedKeys"] = map[string]any{"news": map[string]any{"Keys": request.Keys[99:]}}
			case len(request.Keys) == 1 && request.Keys[0]["SK"]["S"] == "DOC#099":
				retries++
			case len(request.Keys) == 1 && request.Keys[0]["SK"]["S"] == "DOC#100":
				tails++
			default:
				t.Errorf("retry re-read completed keys: %v", request.Keys)
			}
			body["Responses"] = map[string]any{"news": rows}
			data, err := json.Marshal(body)
			if err != nil {
				t.Error(err)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
				Body: io.NopCloser(strings.NewReader(string(data)))}, nil
		}),
	})
	keys := []NewsReadKey{}
	for i := 0; i < 101; i++ {
		keys = append(keys, NewsReadKey{SourceID: "source", SortKey: fmt.Sprintf("DOC#%03d", i)})
	}
	keys = append(keys, keys[0])
	rows, err := NewCrawlerRepository(client, "news").ReadNewsItems(context.Background(), keys)
	if err != nil || len(rows) != 101 || calls != 3 || fullChunks != 1 || retries != 1 || tails != 1 {
		t.Fatalf("incomplete batch: rows %d calls %d (full %d retry %d tail %d) err %v", len(rows), calls, fullChunks, retries, tails, err)
	}
}

func TestNewsReadReadsChunksConcurrentlyAndFailsClosed(t *testing.T) {
	keys := []NewsReadKey{}
	for i := 0; i < 600; i++ {
		keys = append(keys, NewsReadKey{SourceID: "source", SortKey: fmt.Sprintf("DOC#%03d", i)})
	}
	for _, tc := range []struct {
		name     string
		failSK   string // a chunk containing this key returns an HTTP error
		dupSK    string // a chunk containing this key also returns this foreign row
		wantErr  string
		wantRows int
	}{
		{name: "all chunks", wantRows: 600},
		{name: "one chunk fails", failSK: "DOC#350", wantErr: "read current news state"},
		{name: "row from another chunk", dupSK: "DOC#450", wantErr: "unexpected news state response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var inFlight, peak int64
			client := dynamodb.New(dynamodb.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
				HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
					now := atomic.AddInt64(&inFlight, 1)
					defer atomic.AddInt64(&inFlight, -1)
					for p := atomic.LoadInt64(&peak); now > p && !atomic.CompareAndSwapInt64(&peak, p, now); p = atomic.LoadInt64(&peak) {
					}
					time.Sleep(5 * time.Millisecond)
					var input struct {
						RequestItems map[string]struct {
							Keys []map[string]map[string]string
						}
					}
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Error(err)
					}
					rows := input.RequestItems["news"].Keys
					for _, key := range rows {
						if tc.failSK != "" && key["SK"]["S"] == tc.failSK {
							return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
								Body: io.NopCloser(strings.NewReader(`{"__type":"com.amazonaws.dynamodb.v20120810#ValidationException","message":"x"}`))}, nil
						}
					}
					if tc.dupSK != "" && rows[0]["SK"]["S"] != "DOC#400" {
						rows = append(rows, map[string]map[string]string{"PK": {"S": "CRAWLER#source"}, "SK": {"S": tc.dupSK}})
					}
					data, _ := json.Marshal(map[string]any{"Responses": map[string]any{"news": rows}})
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
						Body: io.NopCloser(strings.NewReader(string(data)))}, nil
				}),
			})
			rows, err := NewCrawlerRepository(client, "news").ReadNewsItems(context.Background(), keys)
			if tc.wantErr != "" {
				if err == nil || rows != nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want %q failure, got rows=%d err=%v", tc.wantErr, len(rows), err)
				}
			} else if err != nil || len(rows) != tc.wantRows {
				t.Fatalf("rows=%d err=%v", len(rows), err)
			}
			if p := atomic.LoadInt64(&peak); p > newsReadConcurrency {
				t.Fatalf("peak concurrent chunks %d exceeds %d", p, newsReadConcurrency)
			}
		})
	}
}

func TestNewsReadNeverTreatsUnprocessedKeysAsMissing(t *testing.T) {
	calls := 0
	client := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			var input map[string]json.RawMessage
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(map[string]any{"UnprocessedKeys": input["RequestItems"]})
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
				Body: io.NopCloser(strings.NewReader(string(data)))}, nil
		}),
	})
	rows, err := NewCrawlerRepository(client, "news").ReadNewsItems(context.Background(), []NewsReadKey{{SourceID: "source", SortKey: "CONFIG"}})
	if err == nil || rows != nil || calls != 3 {
		t.Fatalf("partial state was accepted: %v %v %d", rows, err, calls)
	}
}

func TestNewsReadRejectsOversizedOrForeignSelectionWithoutAWS(t *testing.T) {
	repo := NewCrawlerRepository(nil, "news")
	for _, keys := range [][]NewsReadKey{
		make([]NewsReadKey, 1001),
		{{SourceID: "source", SortKey: "PROFILE"}},
		{{SourceID: "", SortKey: "CONFIG"}},
	} {
		if rows, err := repo.ReadNewsItems(context.Background(), keys); err == nil || rows != nil {
			t.Fatalf("invalid selection was accepted: %v", keys)
		}
	}
}

// The fake applies the request's own ProjectionExpression to full stored rows,
// so a field missing from the canonical projection is missing from the result.
func TestNewsReadProjectionCarriesEnrichmentAndAccountBinding(t *testing.T) {
	stored := map[string]map[string]any{
		"DOC#article": {
			"PK": map[string]string{"S": "CRAWLER#source"}, "SK": map[string]string{"S": "DOC#article"},
			"type": map[string]string{"S": "news"}, "title": map[string]string{"S": "Title"},
			"awsRelevance":     map[string]string{"S": "Amazon EKS · AWS Lambda"},
			"opportunityCount": map[string]string{"N": "2"}, "qualityScore": map[string]string{"N": "0.8"},
			"unprojected": map[string]string{"S": "hidden"}, "matchedTerm": map[string]string{"S": "Woori cloud"},
		},
		"CONFIG": {
			"PK": map[string]string{"S": "CRAWLER#source"}, "SK": map[string]string{"S": "CONFIG"},
			"sourceId": map[string]string{"S": "source"}, "ownerId": map[string]string{"S": "creator"},
			"accountId": map[string]string{"S": "acct-1"}, "subscribers": map[string]any{"SS": []string{"creator"}},
		},
	}
	client := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			var input struct {
				RequestItems map[string]struct {
					Keys                     []map[string]map[string]string
					ProjectionExpression     string
					ExpressionAttributeNames map[string]string
				}
			}
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			request := input.RequestItems["news"]
			projected := map[string]bool{}
			for _, token := range strings.Split(request.ProjectionExpression, ",") {
				token = strings.TrimSpace(token)
				if name, ok := request.ExpressionAttributeNames[token]; ok {
					token = name
				}
				projected[token] = true
			}
			rows := []map[string]any{}
			for _, key := range request.Keys {
				row := map[string]any{}
				for name, value := range stored[key["SK"]["S"]] {
					if projected[name] {
						row[name] = value
					}
				}
				rows = append(rows, row)
			}
			data, _ := json.Marshal(map[string]any{"Responses": map[string]any{"news": rows}})
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
				Body: io.NopCloser(strings.NewReader(string(data)))}, nil
		}),
	})
	doc, config := NewsReadKey{SourceID: "source", SortKey: "DOC#article"}, NewsReadKey{SourceID: "source", SortKey: "CONFIG"}
	rows, err := NewCrawlerRepository(client, "news").ReadNewsItems(context.Background(), []NewsReadKey{doc, config})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"awsRelevance", "opportunityCount", "qualityScore", "matchedTerm"} {
		if rows[doc][name] == nil {
			t.Errorf("projection drops %s", name)
		}
	}
	if rows[config]["accountId"] == nil || rows[config]["ownerId"] == nil {
		t.Error("projection drops the source's account binding")
	}
	if rows[doc]["unprojected"] != nil || rows[config]["subscribers"] != nil {
		t.Error("fake did not apply the projection")
	}
}
