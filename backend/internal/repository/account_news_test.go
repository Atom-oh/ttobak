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

func TestCreateAccountNewsSourceIsOneConditionalTransaction(t *testing.T) {
	calls := 0
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		calls++
		if target != "DynamoDB_20120810.TransactWriteItems" {
			t.Fatalf("target %s", target)
		}
		items := body["TransactItems"].([]interface{})
		if len(items) != 5 {
			t.Fatalf("items: %d", len(items))
		}
		for i, sk := range []string{"META", "MEMBER#user"} {
			c := items[i].(map[string]interface{})["ConditionCheck"].(map[string]interface{})
			key := c["Key"].(map[string]interface{})
			if key["PK"].(map[string]interface{})["S"] != "ACCOUNT#acc" || key["SK"].(map[string]interface{})["S"] != sk ||
				!strings.Contains(c["ConditionExpression"].(string), "attribute_exists") {
				t.Fatalf("check %d: %#v", i, c)
			}
		}
		config := items[2].(map[string]interface{})["Put"].(map[string]interface{})
		row := config["Item"].(map[string]interface{})
		if row["PK"].(map[string]interface{})["S"] != "CRAWLER#acct_acc_0000abcd" || row["SK"].(map[string]interface{})["S"] != "CONFIG" ||
			row["accountId"].(map[string]interface{})["S"] != "acc" || row["sourceId"].(map[string]interface{})["S"] != "acct_acc_0000abcd" ||
			row["userTiedAt"] == nil || !strings.Contains(config["ConditionExpression"].(string), "attribute_not_exists") {
			t.Fatalf("config: %#v", config)
		}
		if g, ok := row["globalNewsSource"]; ok && g.(map[string]interface{})["BOOL"] != false {
			t.Fatalf("account source cannot be global: %#v", g)
		}
		ref := items[3].(map[string]interface{})["Put"].(map[string]interface{})
		refRow := ref["Item"].(map[string]interface{})
		if refRow["PK"].(map[string]interface{})["S"] != "ACCOUNT#acc" || refRow["SK"].(map[string]interface{})["S"] != "NEWSSRC#acct_acc_0000abcd" ||
			!strings.Contains(ref["ConditionExpression"].(string), "attribute_not_exists") {
			t.Fatalf("ref: %#v", ref)
		}
		counter := items[4].(map[string]interface{})["Update"].(map[string]interface{})
		key := counter["Key"].(map[string]interface{})
		if key["PK"].(map[string]interface{})["S"] != "ACCOUNT#acc" || key["SK"].(map[string]interface{})["S"] != "NEWSSRC_COUNT" {
			t.Fatalf("counter key: %#v", key)
		}
		requireAll(t, "counter condition", resolved(counter, "ConditionExpression"), "attribute_not_exists (sourceCount)", `sourceCount < {"N":"10"}`)
		requireAll(t, "counter update", resolved(counter, "UpdateExpression"), "if_not_exists(sourceCount", `{"N":"3"}`, `{"N":"1"}`, "entityType", `ADD sourceRev {"N":"1"}`)
		return `{}`
	})
	src := &model.CrawlerSource{SourceID: "acct_acc_0000abcd", AccountID: "acc", SourceName: "n", GlobalNewsSource: true}
	ref := &model.AccountNewsSourceRef{AccountID: "acc", SourceID: "acct_acc_0000abcd", CreatedBy: "user"}
	if err := repo.CreateAccountNewsSource(context.Background(), src, ref, 3, 10); err != nil || calls != 1 {
		t.Fatalf("create: %d %v", calls, err)
	}
	if err := repo.CreateAccountNewsSource(context.Background(), src, ref, 10, 10); err == nil || calls != 1 {
		t.Fatal("an observed count at the cap must be rejected before any write")
	}
	ref.AccountID = "other"
	if err := repo.CreateAccountNewsSource(context.Background(), src, ref, 3, 10); err == nil || calls != 1 {
		t.Fatal("mismatched ref must be rejected before any write")
	}
}

func TestAccountNewsTransactionFailureMapping(t *testing.T) {
	src := &model.CrawlerSource{SourceID: "acct_acc_0000abcd", AccountID: "acc"}
	ref := &model.AccountNewsSourceRef{AccountID: "acc", SourceID: "acct_acc_0000abcd", CreatedBy: "user"}
	for _, tc := range []struct {
		reason string
		want   error
	}{
		{`[{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"None"}]`, ErrAccountNewsAccess},
		{`[{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"},{"Code":"None"}]`, ErrAccountNewsAccess},
		{`[{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"}]`, ErrConditionFailed},
		{`[{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"}]`, ErrConditionFailed},
		{`[{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"}]`, ErrAccountNewsSourceLimit},
		{`[{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"}]`, ErrAccountNewsAccess},
		{`[{"Code":"None"},{"Code":"None"},{"Code":"TransactionConflict"},{"Code":"None"},{"Code":"None"}]`, nil},
	} {
		repo := summarySDKRepo(func(*http.Request) (*http.Response, error) {
			return summaryHTTPResponse(400, `{"__type":"TransactionCanceledException","CancellationReasons":`+tc.reason+`}`, nil), nil
		})
		err := repo.CreateAccountNewsSource(context.Background(), src, ref, 0, 10)
		if tc.want == nil {
			if err == nil || errors.Is(err, ErrConditionFailed) || errors.Is(err, ErrAccountNewsAccess) || errors.Is(err, ErrAccountNewsSourceLimit) {
				t.Fatalf("conflict must stay internal: %v", err)
			}
		} else if !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v", tc.reason, err)
		}
	}
	// deleteRepo answers the counter read with an absent row and every
	// transaction with the given cancellation reasons.
	deleteRepo := func(reasons string, txCalls *int) *DynamoDBRepository {
		return summarySDKRepo(func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".GetItem") {
				return summaryHTTPResponse(200, `{}`, nil), nil
			}
			*txCalls++
			return summaryHTTPResponse(400, `{"__type":"TransactionCanceledException","CancellationReasons":`+reasons+`}`, nil), nil
		})
	}
	calls := 0
	if err := deleteRepo(`[{"Code":"None"},{"Code":"ConditionalCheckFailed"}]`, &calls).DeleteAccountNewsSource(context.Background(), "acc", "u1", "acct_acc_0000abcd"); err == nil {
		t.Fatalf("delete mapping: %v", err)
	}
	for _, tc := range []struct {
		reasons string
		want    error
	}{
		{`[{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"},{"Code":"None"}]`, ErrConditionFailed},
		{`[{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"}]`, ErrAccountNewsAccess},
		{`[{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"ConditionalCheckFailed"}]`, ErrAccountNewsAccess},
	} {
		calls := 0
		if err := deleteRepo(tc.reasons, &calls).DeleteAccountNewsSource(context.Background(), "acc", "u1", "acct_acc_0000abcd"); !errors.Is(err, tc.want) || calls != 1 {
			t.Fatalf("%s: %v (%d tx)", tc.reasons, err, calls)
		}
	}
	// A counter that keeps changing is retried a bounded number of times and
	// never reported as not-found or forbidden.
	calls = 0
	err := deleteRepo(`[{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"}]`, &calls).
		DeleteAccountNewsSource(context.Background(), "acc", "u1", "acct_acc_0000abcd")
	if err == nil || errors.Is(err, ErrConditionFailed) || errors.Is(err, ErrAccountNewsAccess) || calls != accountNewsCounterAttempts {
		t.Fatalf("counter conflict: %v (%d tx)", err, calls)
	}
}

// The delete transaction decrements an existing positive counter by
// compare-and-set, and only pins an absent (pre-counter) counter.
func TestDeleteAccountNewsSourceCounterItem(t *testing.T) {
	for _, tc := range []struct {
		name, item string
		update     bool
		want       []string
	}{
		{name: "existing", item: `{"Item":{"PK":{"S":"ACCOUNT#acc"},"SK":{"S":"NEWSSRC_COUNT"},"sourceCount":{"N":"4"},"sourceRev":{"N":"12"}}}`, update: true,
			want: []string{`sourceCount = {"N":"4"}`, `sourceRev = {"N":"12"}`}},
		{name: "existing without revision", item: `{"Item":{"sourceCount":{"N":"4"}}}`, update: true,
			want: []string{`sourceCount = {"N":"4"}`, "attribute_not_exists (sourceRev)"}},
		{name: "absent", item: `{}`, want: []string{"attribute_not_exists (sourceCount)"}},
		{name: "zero", item: `{"Item":{"sourceCount":{"N":"0"}}}`, want: []string{`sourceCount = {"N":"0"}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			repo := indexingWire(t, func(target string, body map[string]interface{}) string {
				if strings.HasSuffix(target, ".GetItem") {
					reads++
					key := body["Key"].(map[string]interface{})
					if body["ConsistentRead"] != true || key["SK"].(map[string]interface{})["S"] != "NEWSSRC_COUNT" {
						t.Fatalf("counter read: %#v", body)
					}
					return tc.item
				}
				items := body["TransactItems"].([]interface{})
				if len(items) != 5 {
					t.Fatalf("items: %d", len(items))
				}
				item := items[4].(map[string]interface{})
				op, isUpdate := item["Update"].(map[string]interface{})
				if !isUpdate {
					op = item["ConditionCheck"].(map[string]interface{})
				}
				if isUpdate != tc.update || op["Key"].(map[string]interface{})["SK"].(map[string]interface{})["S"] != "NEWSSRC_COUNT" {
					t.Fatalf("counter item: %#v", item)
				}
				requireAll(t, "counter condition", resolved(op, "ConditionExpression"), tc.want...)
				if tc.update {
					requireAll(t, "counter update", resolved(op, "UpdateExpression"), `sourceCount = {"N":"3"}`, `ADD sourceRev {"N":"1"}`)
				}
				return `{}`
			})
			if err := repo.DeleteAccountNewsSource(context.Background(), "acc", "u1", "acct_acc_0000abcd"); err != nil || reads != 1 {
				t.Fatalf("delete: %v reads=%d", err, reads)
			}
		})
	}
}

func TestDeleteAccountNewsSourceDisablesOwnedConfigAtomically(t *testing.T) {
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		if target == "DynamoDB_20120810.GetItem" {
			return `{}`
		}
		items := body["TransactItems"].([]interface{})
		if target != "DynamoDB_20120810.TransactWriteItems" || len(items) != 5 {
			t.Fatalf("delete tx: %s %d", target, len(items))
		}
		for i, sk := range []string{"META", "MEMBER#u1"} {
			cc := items[2+i].(map[string]interface{})["ConditionCheck"].(map[string]interface{})
			key := cc["Key"].(map[string]interface{})
			pk := key["PK"].(map[string]interface{})["S"]
			got := key["SK"].(map[string]interface{})["S"]
			if pk != "ACCOUNT#acc" || got != sk ||
				!strings.Contains(cc["ConditionExpression"].(string), "attribute_exists") {
				t.Fatalf("access check %d: %s %s %#v", i, pk, got, cc)
			}
		}
		del := items[0].(map[string]interface{})["Delete"].(map[string]interface{})
		if del["Key"].(map[string]interface{})["SK"].(map[string]interface{})["S"] != "NEWSSRC#acct_acc_0000abcd" ||
			!strings.Contains(del["ConditionExpression"].(string), "attribute_exists") {
			t.Fatalf("ref delete: %#v", del)
		}
		up := items[1].(map[string]interface{})["Update"].(map[string]interface{})
		names := up["ExpressionAttributeNames"].(map[string]interface{})
		values := up["ExpressionAttributeValues"].(map[string]interface{})
		seen := map[string]bool{}
		for _, v := range names {
			seen["name:"+v.(string)] = true
		}
		for _, v := range values {
			seen["value:"+v.(map[string]interface{})["S"].(string)] = true
		}
		if up["Key"].(map[string]interface{})["PK"].(map[string]interface{})["S"] != "CRAWLER#acct_acc_0000abcd" ||
			!seen["name:accountId"] || !seen["name:status"] || !seen["value:acc"] || !seen["value:disabled"] ||
			!strings.Contains(up["ConditionExpression"].(string), "=") {
			t.Fatalf("config update: %#v", up)
		}
		return `{}`
	})
	if err := repo.DeleteAccountNewsSource(context.Background(), "acc", "u1", "acct_acc_0000abcd"); err != nil {
		t.Fatal(err)
	}
}

func TestListAccountNewsSourcesReadsAllPagesFromKeys(t *testing.T) {
	calls := 0
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		calls++
		if target != "DynamoDB_20120810.Query" || body["ConsistentRead"] != true {
			t.Fatalf("query: %#v", body)
		}
		if calls == 1 {
			return `{"Items":[{"PK":{"S":"ACCOUNT#acc"},"SK":{"S":"NEWSSRC#acct_acc_00000001"},"accountId":{"S":"spoofed"},"sourceId":{"S":"spoofed"},"sourceName":{"S":"n"},"createdBy":{"S":"u"},"createdAt":{"S":"2026-11-09T00:00:00Z"}}],"LastEvaluatedKey":{"PK":{"S":"ACCOUNT#acc"},"SK":{"S":"NEWSSRC#acct_acc_00000001"}}}`
		}
		if body["ExclusiveStartKey"] == nil {
			t.Fatal("second page must continue")
		}
		return `{"Items":[{"PK":{"S":"ACCOUNT#acc"},"SK":{"S":"NEWSSRC#acct_acc_00000002"}}]}`
	})
	refs, err := repo.ListAccountNewsSources(context.Background(), "acc")
	if err != nil || calls != 2 || len(refs) != 2 || refs[0].SourceID != "acct_acc_00000001" || refs[0].AccountID != "acc" || refs[1].SourceID != "acct_acc_00000002" {
		t.Fatalf("refs: %+v %v", refs, err)
	}
}

// Per-user partial source writes refuse account-owned sources in the write
// condition itself, and report the refusal as ErrAccountSource (not retryable).
func TestUserSourceWritesRefuseAccountOwnedSource(t *testing.T) {
	for _, owned := range []bool{false, true} {
		var cond string
		client := dynamodb.New(dynamodb.Options{
			Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
			HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
				var body map[string]any
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				cond = resolved(body, "ConditionExpression")
				item := `{}`
				if owned {
					item = `{"accountId":{"S":"acc"}}`
				}
				return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
					Body: io.NopCloser(strings.NewReader(`{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","message":"failed","Item":` + item + `}`))}, nil
			}),
		})
		queries := []string{"q"}
		err := NewCrawlerRepository(client, "t").UpdateSourcePartial(context.Background(), "src", &model.CrawlerSource{}, SourcePartialFields{NewsQueries: &queries})
		requireAll(t, "account guard", cond, "attribute_not_exists (accountId)")
		if owned != errors.Is(err, ErrAccountSource) || owned == errors.Is(err, ErrConditionFailed) {
			t.Fatalf("owned=%v err=%v", owned, err)
		}
	}
}

// Reconcile reads the counter before the refs and writes the ref count
// conditioned on the count and revision read, advancing the revision; a counter
// changed in between is re-read. An absent counter is never created.
func TestReconcileAccountNewsSourceCount(t *testing.T) {
	type counter struct{ count, rev int } // rev < 0: no sourceRev attribute
	page := func(n int) string {
		items := []string{}
		for i := 0; i < n; i++ {
			items = append(items, fmt.Sprintf(`{"PK":{"S":"ACCOUNT#acc"},"SK":{"S":"NEWSSRC#acct_acc_%08d"}}`, i))
		}
		return `{"Items":[` + strings.Join(items, ",") + `]}`
	}
	for _, tc := range []struct {
		name     string
		counters []*counter // GetItem results in order; nil is an absent row
		refs     []int      // ref counts listed after each read
		failCAS  int        // leading UpdateItem calls that fail the condition
		want     int
		writes   int
		wantErr  bool
	}{
		{name: "drifted", counters: []*counter{{3, 5}}, refs: []int{2}, want: 2, writes: 1},
		{name: "drifted without revision", counters: []*counter{{3, -1}}, refs: []int{2}, want: 2, writes: 1},
		{name: "in sync", counters: []*counter{{2, 5}}, refs: []int{2}, want: 2},
		{name: "absent", counters: []*counter{nil}, refs: []int{2}, want: 2},
		// The ABA interleaving: reconcile reads count 10 (rev 7), a delete
		// (10->9, rev 8) commits, reconcile lists 9 refs, a create (9->10,
		// rev 9) commits. The count matches again but the revision does not, so
		// the stale write of 9 is refused; the re-read sees 10 refs in sync.
		{name: "delete then create", counters: []*counter{{10, 7}, {10, 9}}, refs: []int{9, 10}, failCAS: 1, want: 10, writes: 1},
		{name: "keeps changing", counters: []*counter{{3, 5}}, refs: []int{2}, failCAS: 99, writes: 3, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads, lists, writes, lastOp := 0, 0, 0, ""
			repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
				target := req.Header.Get("X-Amz-Target")
				var body map[string]interface{}
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				switch {
				case strings.HasSuffix(target, ".GetItem"):
					if body["ConsistentRead"] != true {
						t.Fatal("counter read must be strongly consistent")
					}
					lastOp = "read"
					c := tc.counters[min(reads, len(tc.counters)-1)]
					reads++
					switch {
					case c == nil:
						return summaryHTTPResponse(200, `{}`, nil), nil
					case c.rev < 0:
						return summaryHTTPResponse(200, fmt.Sprintf(`{"Item":{"sourceCount":{"N":"%d"}}}`, c.count), nil), nil
					}
					return summaryHTTPResponse(200, fmt.Sprintf(`{"Item":{"sourceCount":{"N":"%d"},"sourceRev":{"N":"%d"}}}`, c.count, c.rev), nil), nil
				case strings.HasSuffix(target, ".Query"):
					if lastOp != "read" {
						t.Fatal("refs must be listed after the counter read")
					}
					lastOp = "list"
					n := tc.refs[min(lists, len(tc.refs)-1)]
					lists++
					return summaryHTTPResponse(200, page(n), nil), nil
				case strings.HasSuffix(target, ".UpdateItem"):
					writes++
					c := tc.counters[min(reads-1, len(tc.counters)-1)]
					n := tc.refs[min(lists-1, len(tc.refs)-1)]
					cond := resolved(body, "ConditionExpression")
					requireAll(t, "reconcile condition", cond, fmt.Sprintf(`sourceCount = {"N":"%d"}`, c.count))
					if c.rev < 0 {
						requireAll(t, "reconcile condition", cond, "attribute_not_exists (sourceRev)")
					} else {
						requireAll(t, "reconcile condition", cond, fmt.Sprintf(`sourceRev = {"N":"%d"}`, c.rev))
					}
					requireAll(t, "reconcile update", resolved(body, "UpdateExpression"), fmt.Sprintf(`sourceCount = {"N":"%d"}`, n), `ADD sourceRev {"N":"1"}`)
					if writes <= tc.failCAS {
						return summaryHTTPResponse(400, `{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","message":"changed"}`, nil), nil
					}
					return summaryHTTPResponse(200, `{}`, nil), nil
				}
				t.Fatalf("unexpected %s", target)
				return nil, nil
			})
			got, err := repo.ReconcileAccountNewsSourceCount(context.Background(), "acc")
			if tc.wantErr != (err != nil) || (!tc.wantErr && got != tc.want) || writes != tc.writes {
				t.Fatalf("got %d err=%v writes=%d", got, err, writes)
			}
		})
	}
}
