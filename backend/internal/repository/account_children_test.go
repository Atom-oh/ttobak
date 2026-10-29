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
	"github.com/ttobak/backend/internal/model"
)

func TestAccountChildRelationPublishedAtomically(t *testing.T) {
	for _, create := range []bool{true, false} {
		t.Run(fmt.Sprint("create=", create), func(t *testing.T) {
			r := hierarchyWireRepo(t, func(items []hierarchyWireItem) {
				put, deleted := false, false
				for _, item := range items {
					if item.Put != nil && item.Put.Item["SK"].(map[string]any)["S"] == "CHILDREF#child" {
						row := item.Put.Item
						put = row["PK"].(map[string]any)["S"] == "ACCOUNT#parent" &&
							row["accountId"].(map[string]any)["S"] == "child" &&
							row["parentAccountId"].(map[string]any)["S"] == "parent" &&
							row["entityType"].(map[string]any)["S"] == "ACCOUNT_CHILD_REF"
					}
					if item.Delete != nil && item.Delete.Key["PK"]["S"] == "ACCOUNT#old-parent" &&
						item.Delete.Key["SK"]["S"] == "CHILDREF#child" {
						deleted = true
					}
				}
				if !put || (!create && !deleted) {
					t.Fatalf("canonical hierarchy transaction omitted reverse relation: put=%v delete=%v", put, deleted)
				}
			}, 200, "{}")
			var err error
			if create {
				a := &model.Account{PK: "ACCOUNT#child", SK: "META", AccountID: "child", OwnerUserID: "owner", ParentAccountID: "parent"}
				m := &model.AccountMember{PK: a.PK, SK: "MEMBER#owner", AccountID: "child", UserID: "owner", Role: model.RoleOwner}
				err = r.CreateAccountWithParent(context.Background(), a, m, []AccountParentLink{{AccountID: "parent"}})
			} else {
				err = r.UpdateAccountParent(context.Background(), "child", "owner", "old-parent", "parent", []AccountParentLink{{AccountID: "parent"}})
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestChildDiscoveryPagesAreScopedAndBounded(t *testing.T) {
	calls := 0
	client := dynamodb.New(dynamodb.Options{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			var input struct {
				Limit                     int
				ConsistentRead            bool
				ExclusiveStartKey         map[string]map[string]string
				ExpressionAttributeValues map[string]map[string]string
			}
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.Limit != 20 || !input.ConsistentRead {
				t.Fatalf("unbounded/eventually consistent child discovery: %+v", input)
			}
			found := false
			for _, value := range input.ExpressionAttributeValues {
				found = found || value["S"] == "ACCOUNT#parent"
			}
			if !found {
				t.Fatal("query lost parent partition scope")
			}
			body := `{"Items":[],"LastEvaluatedKey":{"PK":{"S":"ACCOUNT#parent"},"SK":{"S":"CHILDREF#a"}}}`
			if calls == 2 {
				if input.ExclusiveStartKey["PK"]["S"] != "ACCOUNT#parent" || input.ExclusiveStartKey["SK"]["S"] != "CHILDREF#a" {
					t.Fatalf("continuation lost parent scope: %+v", input.ExclusiveStartKey)
				}
				body = `{"Items":[{"PK":{"S":"ACCOUNT#parent"},"SK":{"S":"CHILDREF#b"},"accountId":{"S":"b"},"parentAccountId":{"S":"parent"},"entityType":{"S":"ACCOUNT_CHILD_REF"}},{"PK":{"S":"ACCOUNT#parent"},"SK":{"S":"CHILDREF#c"},"accountId":{"S":"c"},"parentAccountId":{"S":"elsewhere"},"entityType":{"S":"ACCOUNT_CHILD_REF"}}]}`
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})})
	r := NewDynamoDBRepository(client, "accounts-test")
	ids, cursor, err := r.ListChildAccountIDsPage(context.Background(), "parent", "", 20)
	if err != nil || len(ids) != 0 || cursor == nil {
		t.Fatalf("empty advancing page lost cursor: %v/%v/%v", ids, cursor, err)
	}
	ids, cursor, err = r.ListChildAccountIDsPage(context.Background(), "parent", *cursor, 20)
	if err != nil || len(ids) != 1 || ids[0] != "b" || cursor != nil {
		t.Fatalf("next child page accepted invalid identity or lost valid child: %v/%v/%v", ids, cursor, err)
	}
	if _, _, err = r.ListChildAccountIDsPage(context.Background(), "other", "CHILDREF#a", 20); err == nil || calls != 2 {
		t.Fatal("accepted malformed continuation before storage query")
	}
}

func TestChildBootstrapPinsCanonicalParent(t *testing.T) {
	r := hierarchyWireRepo(t, func(items []hierarchyWireItem) {
		if len(items) != 2 || items[0].ConditionCheck == nil || items[1].Put == nil {
			t.Fatalf("bootstrap must atomically check child and write reference: %+v", items)
		}
		check := items[0].ConditionCheck
		requireHierarchyCondition(t, check, "attribute_exists(PK)", `parentAccountId = "parent"`)
		if check.Key["PK"]["S"] != "ACCOUNT#child" || check.Key["SK"]["S"] != "META" {
			t.Fatal("bootstrap checked different canonical child")
		}
	}, 200, "{}")
	if err := r.EnsureAccountChildReference(context.Background(), "child", "parent"); err != nil {
		t.Fatal(err)
	}
}
