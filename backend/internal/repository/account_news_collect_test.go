package repository

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// wireExpression resolves placeholders in one pass (string values quoted),
// so a value such as "01:00:00" is never rewritten as an alias.
func wireExpression(op map[string]interface{}, field string) string {
	text, _ := op[field].(string)
	names, _ := op["ExpressionAttributeNames"].(map[string]interface{})
	values, _ := op["ExpressionAttributeValues"].(map[string]interface{})
	return regexp.MustCompile(`[#:][A-Za-z0-9_]+`).ReplaceAllStringFunc(text, func(alias string) string {
		if name, ok := names[alias].(string); ok {
			return name
		}
		if value, ok := values[alias].(map[string]interface{}); ok {
			return fmt.Sprintf("%q", value["S"])
		}
		return alias
	})
}

func TestClaimAccountNewsCollectIsOneFencedTransaction(t *testing.T) {
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.FixedZone("KST", 9*3600))
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		items := body["TransactItems"].([]interface{})
		if target != "DynamoDB_20120810.TransactWriteItems" || len(items) != 4 {
			t.Fatalf("claim tx: %s %d", target, len(items))
		}
		ref := items[0].(map[string]interface{})["ConditionCheck"].(map[string]interface{})
		if ref["Key"].(map[string]interface{})["SK"].(map[string]interface{})["S"] != "NEWSSRC#acct_acc_0000abcd" {
			t.Fatalf("ref check: %#v", ref)
		}
		for i, sk := range []string{"META", "MEMBER#u1"} {
			cc := items[2+i].(map[string]interface{})["ConditionCheck"].(map[string]interface{})
			if cc["Key"].(map[string]interface{})["SK"].(map[string]interface{})["S"] != sk {
				t.Fatalf("access check %d: %#v", i, cc)
			}
		}
		up := items[1].(map[string]interface{})["Update"].(map[string]interface{})
		cond, update := wireExpression(up, "ConditionExpression"), wireExpression(up, "UpdateExpression")
		for _, want := range []string{
			"attribute_exists (PK)", `accountId = "acc"`, `status <> "disabled"`, "attribute_not_exists (globalNewsSource)",
			`(attribute_not_exists (lastCollectRequestedAt)) OR (lastCollectRequestedAt <= "2026-11-12T00:00:00Z")`,
		} {
			if !strings.Contains(cond, want) {
				t.Fatalf("condition %q lacks %q", cond, want)
			}
		}
		if up["Key"].(map[string]interface{})["PK"].(map[string]interface{})["S"] != "CRAWLER#acct_acc_0000abcd" ||
			!strings.Contains(update, `lastCollectRequestedAt = "2026-11-12T01:00:00Z"`) || !strings.Contains(update, `lastCollectRequestId = "r1"`) ||
			strings.Contains(update, "sourceName") || strings.Contains(update, "newsQueries") || strings.Contains(update, "status") {
			t.Fatalf("config update: %s", update)
		}
		return `{}`
	})
	if err := repo.ClaimAccountNewsCollect(context.Background(), "acc", "u1", "acct_acc_0000abcd", "r1", at, at.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClaimAccountNewsCollect(context.Background(), "acc", "u1", "acct_acc_0000abcd", "r1", at, at); err == nil {
		t.Fatal("a cutoff not before the request must be rejected before any write")
	}
	for _, tc := range []struct {
		reasons string
		want    error
	}{
		{`[{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"},{"Code":"None"}]`, ErrConditionFailed},
		{`[{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"}]`, ErrConditionFailed},
		{`[{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"}]`, ErrAccountNewsAccess},
		{`[{"Code":"None"},{"Code":"TransactionConflict"},{"Code":"None"},{"Code":"None"}]`, nil},
	} {
		repo := summarySDKRepo(func(*http.Request) (*http.Response, error) {
			return summaryHTTPResponse(400, `{"__type":"TransactionCanceledException","CancellationReasons":`+tc.reasons+`}`, nil), nil
		})
		err := repo.ClaimAccountNewsCollect(context.Background(), "acc", "u1", "acct_acc_0000abcd", "r1", at, at.Add(-time.Hour))
		if tc.want == nil {
			if err == nil || errors.Is(err, ErrConditionFailed) || errors.Is(err, ErrAccountNewsAccess) {
				t.Fatalf("conflict must stay internal: %v", err)
			}
		} else if !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v", tc.reasons, err)
		}
	}
}

func TestReleaseAccountNewsCollectRemovesOnlyItsClaim(t *testing.T) {
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		cond, update := wireExpression(body, "ConditionExpression"), wireExpression(body, "UpdateExpression")
		if target != "DynamoDB_20120810.UpdateItem" || body["Key"].(map[string]interface{})["PK"].(map[string]interface{})["S"] != "CRAWLER#acct_acc_0000abcd" ||
			cond != `lastCollectRequestId = "r1"` || strings.TrimSpace(update) != "REMOVE lastCollectRequestedAt, lastCollectRequestId" {
			t.Fatalf("release: %s %q %q", target, cond, update)
		}
		return `{}`
	})
	if err := repo.ReleaseAccountNewsCollect(context.Background(), "acct_acc_0000abcd", "r1"); err != nil {
		t.Fatal(err)
	}
	failed := summarySDKRepo(func(*http.Request) (*http.Response, error) {
		return summaryHTTPResponse(400, `{"__type":"ConditionalCheckFailedException"}`, nil), nil
	})
	if err := failed.ReleaseAccountNewsCollect(context.Background(), "acct_acc_0000abcd", "r1"); !errors.Is(err, ErrConditionFailed) {
		t.Fatalf("lost claim: %v", err)
	}
}

// The crawler marks a claimed request started with its own
// lastCollectStartedRequestId/lastCollectStartedAt; such a row still decodes,
// and Go never models (so never rewrites) the worker's attributes.
func TestGetSourceDecodesCollectAttributes(t *testing.T) {
	for _, startedAt := range []string{`{"S":"2026-11-12T01:00:05+00:00"}`, `{"N":"1794013205"}`} {
		repo := indexingWire(t, func(target string, _ map[string]interface{}) string {
			if target != "DynamoDB_20120810.GetItem" {
				t.Fatalf("target %s", target)
			}
			return `{"Item":{"PK":{"S":"CRAWLER#acct_acc_0000abcd"},"SK":{"S":"CONFIG"},"sourceId":{"S":"acct_acc_0000abcd"},` +
				`"sourceName":{"S":"n"},"accountId":{"S":"acc"},"status":{"S":"idle"},"newsQueries":{"L":[{"S":"q"}]},` +
				`"lastCollectRequestedAt":{"S":"2026-11-12T01:00:00Z"},"lastCollectRequestId":{"S":"r1"},` +
				`"lastCollectStartedRequestId":{"S":"r1"},"lastCollectStartedAt":` + startedAt + `}}`
		})
		src, err := NewCrawlerRepository(repo.client, "synthetic").GetSource(context.Background(), "acct_acc_0000abcd")
		if err != nil || src == nil || src.LastCollectRequestedAt != "2026-11-12T01:00:00Z" || src.LastCollectRequestID != "r1" ||
			src.AccountID != "acc" || len(src.NewsQueries) != 1 || src.NextCollectAllowedAt != "" {
			t.Fatalf("%s: %+v %v", startedAt, src, err)
		}
	}
}
