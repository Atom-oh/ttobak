package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

func TestNewsReadBatchesStrongKeysAndRetriesOnlyUnprocessed(t *testing.T) {
	calls := 0
	client := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if req.Header.Get("X-Amz-Target") != "DynamoDB_20120810.BatchGetItem" {
				t.Fatal("unexpected operation")
			}
			var input struct {
				RequestItems map[string]struct {
					ConsistentRead bool
					Keys           []map[string]map[string]string
				}
			}
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			request := input.RequestItems["news"]
			want := 100
			if calls > 1 {
				want = 1
			}
			if !request.ConsistentRead || len(request.Keys) != want {
				t.Fatalf("call %d: unbounded/eventual batch: %+v", calls, request)
			}
			rows := request.Keys
			body := map[string]any{}
			if calls == 1 {
				rows = rows[:99]
				body["UnprocessedKeys"] = map[string]any{"news": map[string]any{"Keys": request.Keys[99:]}}
			} else if calls == 2 && request.Keys[0]["SK"]["S"] != "DOC#099" {
				t.Fatal("retry re-read completed keys")
			}
			body["Responses"] = map[string]any{"news": rows}
			data, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
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
	if err != nil || len(rows) != 101 || calls != 3 {
		t.Fatalf("incomplete batch: rows %d calls %d err %v", len(rows), calls, err)
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
