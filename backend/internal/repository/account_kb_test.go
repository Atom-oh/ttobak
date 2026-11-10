package repository

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func TestPutAccountKBRefIsConditionalCreate(t *testing.T) {
	key := "kb/u1/1_a.pdf"
	id := model.AccountKBRefID(key)
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		items := body["TransactItems"].([]interface{})
		if target != "DynamoDB_20120810.TransactWriteItems" || len(items) != 3 {
			t.Fatalf("not a transaction: %s %#v", target, body)
		}
		put := items[0].(map[string]interface{})["Put"].(map[string]interface{})
		if !strings.Contains(put["ConditionExpression"].(string), "attribute_not_exists") {
			t.Fatalf("not a conditional create: %#v", put)
		}
		item := put["Item"].(map[string]interface{})
		if item["PK"].(map[string]interface{})["S"] != "ACCOUNT#acc" ||
			item["SK"].(map[string]interface{})["S"] != "KBREF#"+id ||
			item["s3Key"].(map[string]interface{})["S"] != key || item["uploadedBy"].(map[string]interface{})["S"] != "u1" {
			t.Fatalf("item: %#v", item)
		}
		check := items[1].(map[string]interface{})["ConditionCheck"].(map[string]interface{})
		if pk, sk := wireKey(items[1].(map[string]interface{}), "ConditionCheck"); pk != "ACCOUNT#acc" || sk != "KBUNTAG#"+id ||
			!strings.Contains(check["ConditionExpression"].(string), "attribute_not_exists") {
			t.Fatalf("untag tombstone check: %s %s %#v", pk, sk, check)
		}
		return `{}`
	})
	ref := &model.AccountKBRef{AccountID: "acc", RefID: id, S3Key: key, FileName: "a.pdf", UploadedBy: "u1", CreatedAt: time.Now()}
	if err := repo.PutAccountKBRef(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []*model.AccountKBRef{
		{AccountID: "acc", RefID: "x", S3Key: key, UploadedBy: "u1"},
		{AccountID: "acc", RefID: model.AccountKBRefID("shared/x"), S3Key: "shared/x", UploadedBy: "u1"},
		{AccountID: "a#b", RefID: model.AccountKBRefID(key), S3Key: key, UploadedBy: "u1"},
	} {
		if err := repo.PutAccountKBRef(context.Background(), bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	for _, tc := range []struct {
		reasons  string
		untagged bool
	}{
		{`[{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"}]`, false},
		{`[{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"}]`, true},
	} {
		repo := summarySDKRepo(func(*http.Request) (*http.Response, error) {
			return summaryHTTPResponse(400, `{"__type":"TransactionCanceledException","CancellationReasons":`+tc.reasons+`}`, nil), nil
		})
		err := repo.PutAccountKBRef(context.Background(), ref)
		if !errors.Is(err, ErrConditionFailed) || errors.Is(err, ErrAccountKBRefUntagged) != tc.untagged {
			t.Fatalf("%s: %v", tc.reasons, err)
		}
	}
}

func TestDeleteAccountKBRefWritesTombstone(t *testing.T) {
	var del, put map[string]interface{}
	var items []interface{}
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		if target != "DynamoDB_20120810.TransactWriteItems" {
			t.Fatalf("target %s", target)
		}
		items = body["TransactItems"].([]interface{})
		del = items[0].(map[string]interface{})["Delete"].(map[string]interface{})
		put = items[1].(map[string]interface{})["Put"].(map[string]interface{})
		return `{}`
	})
	id := strings.Repeat("a", 64)
	if err := repo.DeleteAccountKBRef(context.Background(), "acc", id, "u1"); err != nil {
		t.Fatal(err)
	}
	values, _ := del["ExpressionAttributeValues"].(map[string]interface{})
	if !strings.Contains(del["ConditionExpression"].(string), "attribute_exists") || len(values) != 1 {
		t.Fatalf("uploader delete: %#v", del)
	}
	if pk, sk := wireKey(items[0].(map[string]interface{}), "Delete"); pk != "ACCOUNT#acc" || sk != "KBREF#"+id {
		t.Fatalf("ref delete key: %s %s", pk, sk)
	}
	if pk, sk := wireKey(items[1].(map[string]interface{}), "Put"); pk != "ACCOUNT#acc" || sk != "KBUNTAG#"+id ||
		wireS(put["Item"], "refId") != id {
		t.Fatalf("tombstone: %s %s %#v", pk, sk, put)
	}
	if err := repo.DeleteAccountKBRef(context.Background(), "acc", id, ""); err != nil {
		t.Fatal(err)
	}
	if values, _ := del["ExpressionAttributeValues"].(map[string]interface{}); len(values) != 0 {
		t.Fatalf("owner delete: %v", values)
	}
	if err := repo.DeleteAccountKBRef(context.Background(), "acc", "../x", ""); err == nil {
		t.Fatal("accepted bad ref id")
	}
	missing := summarySDKRepo(func(*http.Request) (*http.Response, error) {
		return summaryHTTPResponse(400, `{"__type":"TransactionCanceledException","CancellationReasons":[{"Code":"ConditionalCheckFailed"},{"Code":"None"}]}`, nil), nil
	})
	if err := missing.DeleteAccountKBRef(context.Background(), "acc", id, "u1"); !errors.Is(err, ErrConditionFailed) {
		t.Fatalf("missing ref: %v", err)
	}
}

func TestListAccountKBRefsDropsForeignRows(t *testing.T) {
	key := "kb/u1/1_a.pdf"
	id := model.AccountKBRefID(key)
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		return `{"Items":[` +
			`{"PK":{"S":"ACCOUNT#acc"},"SK":{"S":"KBREF#` + id + `"},"entityType":{"S":"ACCOUNT_KB_REF"},"accountId":{"S":"acc"},"refId":{"S":"` + id + `"},"s3Key":{"S":"` + key + `"},"uploadedBy":{"S":"u1"},"fileName":{"S":"a.pdf"},"createdAt":{"S":"2026-11-01T00:00:00Z"}},` +
			`{"PK":{"S":"ACCOUNT#acc"},"SK":{"S":"KBREF#` + id + `"},"entityType":{"S":"ACCOUNT_KB_REF"},"accountId":{"S":"other"},"refId":{"S":"` + id + `"},"s3Key":{"S":"` + key + `"},"uploadedBy":{"S":"u1"}}` +
			`],"LastEvaluatedKey":{"PK":{"S":"ACCOUNT#acc"},"SK":{"S":"KBREF#` + id + `"}}}`
	})
	refs, next, err := repo.ListAccountKBRefs(context.Background(), "acc", "", 25)
	if err != nil || len(refs) != 1 || refs[0].FileName != "a.pdf" || next == nil {
		t.Fatalf("%+v %v %v", refs, next, err)
	}
	if _, _, err := repo.ListAccountKBRefs(context.Background(), "acc", *next, 25); err != nil {
		t.Fatalf("own cursor rejected: %v", err)
	}
	if _, _, err := repo.ListAccountKBRefs(context.Background(), "acc", "bm90LWEtcmVm", 25); err != ErrInvalidAccountKBCursor {
		t.Fatalf("bad cursor: %v", err)
	}
}
