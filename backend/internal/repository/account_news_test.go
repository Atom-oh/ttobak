package repository

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

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
		if len(items) != 4 {
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
		return `{}`
	})
	src := &model.CrawlerSource{SourceID: "acct_acc_0000abcd", AccountID: "acc", SourceName: "n", GlobalNewsSource: true}
	ref := &model.AccountNewsSourceRef{AccountID: "acc", SourceID: "acct_acc_0000abcd", CreatedBy: "user"}
	if err := repo.CreateAccountNewsSource(context.Background(), src, ref); err != nil || calls != 1 {
		t.Fatalf("create: %d %v", calls, err)
	}
	ref.AccountID = "other"
	if err := repo.CreateAccountNewsSource(context.Background(), src, ref); err == nil || calls != 1 {
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
		{`[{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"},{"Code":"None"}]`, ErrAccountNewsAccess},
		{`[{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"}]`, ErrAccountNewsAccess},
		{`[{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"}]`, ErrConditionFailed},
		{`[{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"}]`, ErrConditionFailed},
		{`[{"Code":"None"},{"Code":"None"},{"Code":"TransactionConflict"},{"Code":"None"}]`, nil},
	} {
		repo := summarySDKRepo(func(*http.Request) (*http.Response, error) {
			return summaryHTTPResponse(400, `{"__type":"TransactionCanceledException","CancellationReasons":`+tc.reason+`}`, nil), nil
		})
		err := repo.CreateAccountNewsSource(context.Background(), src, ref)
		if tc.want == nil {
			if err == nil || errors.Is(err, ErrConditionFailed) || errors.Is(err, ErrAccountNewsAccess) {
				t.Fatalf("conflict must stay internal: %v", err)
			}
		} else if !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v", tc.reason, err)
		}
	}
	repo := summarySDKRepo(func(*http.Request) (*http.Response, error) {
		return summaryHTTPResponse(400, `{"__type":"TransactionCanceledException","CancellationReasons":[{"Code":"None"},{"Code":"ConditionalCheckFailed"}]}`, nil), nil
	})
	if err := repo.DeleteAccountNewsSource(context.Background(), "acc", "acct_acc_0000abcd"); !errors.Is(err, ErrConditionFailed) {
		t.Fatalf("delete mapping: %v", err)
	}
}

func TestDeleteAccountNewsSourceDisablesOwnedConfigAtomically(t *testing.T) {
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		items := body["TransactItems"].([]interface{})
		if target != "DynamoDB_20120810.TransactWriteItems" || len(items) != 2 {
			t.Fatalf("delete tx: %s %d", target, len(items))
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
	if err := repo.DeleteAccountNewsSource(context.Background(), "acc", "acct_acc_0000abcd"); err != nil {
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
