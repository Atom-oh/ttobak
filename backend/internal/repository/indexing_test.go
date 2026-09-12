package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/model"
)

type indexRepositoryHTTP func(*http.Request) (*http.Response, error)

func (f indexRepositoryHTTP) Do(req *http.Request) (*http.Response, error) { return f(req) }

func indexingWire(t *testing.T, call func(string, map[string]interface{}) string) *DynamoDBRepository {
	t.Helper()
	client := dynamodb.New(dynamodb.Options{Region: "us-east-1", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: indexRepositoryHTTP(func(req *http.Request) (*http.Response, error) {
			var body map[string]interface{}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			result := call(req.Header.Get("X-Amz-Target"), body)
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(result))}, nil
		})})
	return NewDynamoDBRepository(client, "synthetic")
}

func TestIndexSourceReadKeepsRawSpillAndExcludesUnrelatedFields(t *testing.T) {
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		if !strings.HasSuffix(target, ".GetItem") || body["ConsistentRead"] != true {
			t.Fatal("source read must be strongly consistent")
		}
		return `{"Item":{"meetingId":{"S":"m"},"userId":{"S":"owner"},"transcriptB":{"S":"s3://assets/transcripts/m/transcriptB.txt"},"notes":{"NULL":true},"publicShareToken":{"S":"never-project"},"accountIds":{"SS":["team"]}}}`
	})
	key, _ := model.CanonicalIndexResource("USER#owner", "MEETING#m")
	record, err := repo.GetIndexSource(context.Background(), key)
	if err != nil || len(record.Fields) != 4 || record.Fields["transcriptB"] != "s3://assets/transcripts/m/transcriptB.txt" {
		t.Fatalf("raw source/field allowlist lost: %+v %v", record, err)
	}
	if value, present := record.Fields["notes"]; !present || value != nil {
		t.Fatal("NULL and absent fields were conflated")
	}
}

func TestIndexRequestsCoalesceActivePreparationWithoutHidingNewWork(t *testing.T) {
	for _, tc := range []struct {
		name, revision string
		lease          int64
		wantWrites     int
	}{
		{"active same revision", "current", 2000, 0},
		{"expired same revision", "current", 999, 1},
		{"active changed revision", "new", 2000, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writes := 0
			repo := indexingWire(t, func(target string, body map[string]interface{}) string {
				if strings.HasSuffix(target, ".GetItem") {
					return fmt.Sprintf(`{"Item":{"resource":{"M":{"pk":{"S":"USER#owner"},"sk":{"S":"DOC#d"},"kind":{"S":"personalDocument"},"id":{"S":"d"}}},"version":{"N":"3"},"state":{"S":"PREPARING"},"desiredRevision":{"S":"current"},"runId":{"S":"active-run"},"leaseUntil":{"N":"%d"}}}`, tc.lease)
				}
				if !strings.HasSuffix(target, ".UpdateItem") {
					t.Fatalf("unexpected operation %s", target)
				}
				writes++
				return "{}"
			})
			key, ok := model.CanonicalIndexResource("USER#owner", "DOC#d")
			if !ok {
				t.Fatal("invalid fixture")
			}
			if err := repo.RequestIndexResource(context.Background(), key, tc.revision, 1000); err != nil {
				t.Fatal(err)
			}
			if writes != tc.wantWrites {
				t.Fatalf("writes=%d want=%d", writes, tc.wantWrites)
			}
		})
	}
}

func TestIndexJobQueryPaginationAndCursorValidation(t *testing.T) {
	calls := 0
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		calls++
		if !strings.HasSuffix(target, ".Query") || body["ConsistentRead"] != true {
			t.Fatal("expected consistent jobs query")
		}
		if calls == 1 {
			return `{"Items":[],"LastEvaluatedKey":{"PK":{"S":"KBINDEX#JOBS"},"SK":{"S":"first"}}}`
		}
		if body["ExclusiveStartKey"].(map[string]interface{})["SK"].(map[string]interface{})["S"] != "first" {
			t.Fatal("jobs cursor lost")
		}
		return `{"Items":[{"resource":{"M":{"pk":{"S":"USER#owner"},"sk":{"S":"DOC#d"},"kind":{"S":"personalDocument"},"id":{"S":"d"}}},"version":{"N":"2"},"state":{"S":"PENDING"}}]}`
	})
	_, cursor, err := repo.ListIndexJobs(context.Background(), "", 25)
	if err != nil || cursor == "" {
		t.Fatalf("missing jobs continuation: %s %v", cursor, err)
	}
	jobs, cursor, err := repo.ListIndexJobs(context.Background(), cursor, 25)
	if err != nil || cursor != "" || len(jobs) != 1 || jobs[0].Resource.Kind != "personalDocument" {
		t.Fatalf("jobs second page lost: %+v %s %v", jobs, cursor, err)
	}
	if _, _, err := repo.ListIndexJobs(context.Background(), "%%%", 25); !errors.Is(err, ErrInvalidIndexCursor) || calls != 2 {
		t.Fatal("malformed cursor was sent to DynamoDB")
	}
}

func TestIndexConditionalFailuresRemainTyped(t *testing.T) {
	for _, kind := range []string{"ConditionalCheckFailedException", "TransactionCanceledException"} {
		t.Run(kind, func(t *testing.T) {
			client := dynamodb.New(dynamodb.Options{Region: "us-east-1", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
				HTTPClient: indexRepositoryHTTP(func(*http.Request) (*http.Response, error) {
					body := `{"__type":"` + kind + `","CancellationReasons":[{"Code":"ConditionalCheckFailed"},{"Code":"None"}]}`
					return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
						Body: io.NopCloser(strings.NewReader(body))}, nil
				})})
			repo := NewDynamoDBRepository(client, "synthetic")
			key, _ := model.CanonicalIndexResource("USER#owner", "DOC#d")
			prior := &model.IndexJob{Resource: key, Version: 2, State: model.IndexWaitingSync}
			next := *prior
			next.State = model.IndexDeleted
			if err := repo.SaveIndexJob(context.Background(), prior, &next, nil, kind == "TransactionCanceledException", 100); !errors.Is(err, ErrConditionFailed) {
				t.Fatalf("lost condition sentinel: %v", err)
			}
		})
	}
}
func TestIndexJobWritesGuardSourceAndKeepProjectionLedger(t *testing.T) {
	key, _ := model.CanonicalIndexResource("USER#owner", "MEETING#m")
	prior := &model.IndexJob{Resource: key, Version: 3, State: model.IndexPreparing, RunID: "run", LeaseUntil: 2000}
	next := *prior
	next.State = model.IndexWaitingSync
	next.Keys = []string{"canonical/v1/meeting/hash/run/meeting.md"}
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		if !strings.HasSuffix(target, ".TransactWriteItems") {
			t.Fatalf("expected source+job transaction, got %s", target)
		}
		ops := body["TransactItems"].([]interface{})
		if len(ops) != 2 {
			t.Fatal("missing transactional guard")
		}
		check := ops[0].(map[string]interface{})["ConditionCheck"].(map[string]interface{})
		if check["Key"].(map[string]interface{})["PK"].(map[string]interface{})["S"] != "USER#owner" {
			t.Fatal("wrong source partition")
		}
		names := check["ExpressionAttributeNames"].(map[string]interface{})
		for _, required := range []string{"notes", "content", "updatedAt", "transcriptA", "transcriptB"} {
			found := false
			for _, name := range names {
				found = found || name == required
			}
			if !found {
				t.Fatalf("source guard omitted %s", required)
			}
		}
		update := ops[1].(map[string]interface{})["Update"].(map[string]interface{})
		values := update["ExpressionAttributeValues"].(map[string]interface{})
		for alias, name := range update["ExpressionAttributeNames"].(map[string]interface{}) {
			if name != "keys" {
				continue
			}
			if !strings.Contains(update["UpdateExpression"].(string), alias) {
				t.Fatal("projection ledger not persisted")
			}
		}
		hasLedger := false
		for _, value := range values {
			v := value.(map[string]interface{})
			if list, ok := v["L"].([]interface{}); ok && len(list) == 1 {
				hasLedger = list[0].(map[string]interface{})["S"] == next.Keys[0] || hasLedger
			}
		}
		if !hasLedger {
			t.Fatalf("private JSON fields lost from DynamoDB update: %+v", values)
		}
		return "{}"
	})
	record := &model.IndexRecord{Resource: key, Fields: map[string]interface{}{"notes": "현재 메모", "updatedAt": "old"}}
	if err := repo.SaveIndexJob(context.Background(), prior, &next, record, true, 1000); err != nil {
		t.Fatal(err)
	}
}
func TestIndexDeletionUsesAbsentSourceCondition(t *testing.T) {
	key, _ := model.CanonicalIndexResource("USER#owner", "DOC#d")
	prior := &model.IndexJob{Resource: key, Version: 2, State: model.IndexWaitingSync, RunID: "run"}
	next := *prior
	next.State = model.IndexDeleted
	repo := indexingWire(t, func(_ string, body map[string]interface{}) string {
		check := body["TransactItems"].([]interface{})[0].(map[string]interface{})["ConditionCheck"].(map[string]interface{})
		if !strings.Contains(check["ConditionExpression"].(string), "attribute_not_exists") {
			t.Fatal("deleted source can be resurrected before completion")
		}
		return "{}"
	})
	if err := repo.SaveIndexJob(context.Background(), prior, &next, nil, true, 100); err != nil {
		t.Fatal(err)
	}
}
func TestIndexSourceScanCarriesContinuationEvenForFilteredEmptyPage(t *testing.T) {
	calls := 0
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		calls++
		if !strings.HasSuffix(target, ".Scan") || body["ConsistentRead"] != true {
			t.Fatal("expected consistent scan")
		}
		if calls == 1 {
			return `{"Items":[],"LastEvaluatedKey":{"PK":{"S":"USER#u"},"SK":{"S":"DOC#d0"}}}`
		}
		if body["ExclusiveStartKey"].(map[string]interface{})["SK"].(map[string]interface{})["S"] != "DOC#d0" {
			t.Fatal("cursor was lost")
		}
		return `{"Items":[{"PK":{"S":"ACCOUNT#a"},"SK":{"S":"DOC#d1"}}]}`
	})
	keys, cursor, err := repo.ScanIndexSources(context.Background(), "", 25)
	if err != nil || len(keys) != 0 || cursor == "" {
		t.Fatalf("empty page ended scan: %v %s %v", keys, cursor, err)
	}
	keys, cursor, err = repo.ScanIndexSources(context.Background(), cursor, 25)
	if err != nil || len(keys) != 1 || keys[0].Kind != "accountDocument" || cursor != "" {
		t.Fatalf("second page lost: %v %s %v", keys, cursor, err)
	}
}
