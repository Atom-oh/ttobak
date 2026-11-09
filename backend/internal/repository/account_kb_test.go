package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func TestPutAccountKBRefIsConditionalCreate(t *testing.T) {
	key := "kb/u1/1_a.pdf"
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		if target != "DynamoDB_20120810.PutItem" || !strings.Contains(body["ConditionExpression"].(string), "attribute_not_exists") {
			t.Fatalf("not a conditional create: %s %#v", target, body)
		}
		item := body["Item"].(map[string]interface{})
		if item["PK"].(map[string]interface{})["S"] != "ACCOUNT#acc" ||
			item["SK"].(map[string]interface{})["S"] != "KBREF#"+model.AccountKBRefID(key) ||
			item["s3Key"].(map[string]interface{})["S"] != key || item["uploadedBy"].(map[string]interface{})["S"] != "u1" {
			t.Fatalf("item: %#v", item)
		}
		return `{}`
	})
	ref := &model.AccountKBRef{AccountID: "acc", RefID: model.AccountKBRefID(key), S3Key: key, FileName: "a.pdf", UploadedBy: "u1", CreatedAt: time.Now()}
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
}

func TestDeleteAccountKBRefConditions(t *testing.T) {
	var cond string
	var values map[string]interface{}
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		cond = body["ConditionExpression"].(string)
		values, _ = body["ExpressionAttributeValues"].(map[string]interface{})
		return `{}`
	})
	id := strings.Repeat("a", 64)
	if err := repo.DeleteAccountKBRef(context.Background(), "acc", id, "u1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cond, "attribute_exists") || len(values) != 1 {
		t.Fatalf("uploader delete: %s %v", cond, values)
	}
	if err := repo.DeleteAccountKBRef(context.Background(), "acc", id, ""); err != nil || len(values) != 0 {
		t.Fatalf("owner delete: %s %v %v", cond, values, err)
	}
	if err := repo.DeleteAccountKBRef(context.Background(), "acc", "../x", ""); err == nil {
		t.Fatal("accepted bad ref id")
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
