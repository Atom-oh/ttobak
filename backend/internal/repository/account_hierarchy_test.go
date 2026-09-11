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
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

type hierarchyWireOperation struct {
	TableName                 string
	Key                       map[string]map[string]string
	Item                      map[string]any
	UpdateExpression          string
	ConditionExpression       string
	ExpressionAttributeNames  map[string]string
	ExpressionAttributeValues map[string]map[string]string
}

type hierarchyWireItem struct {
	Update, Put, ConditionCheck *hierarchyWireOperation
}

func hierarchyWireRepo(t *testing.T, inspect func([]hierarchyWireItem), status int, body string) *DynamoDBRepository {
	t.Helper()
	client := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			if req.Header.Get("X-Amz-Target") != "DynamoDB_20120810.TransactWriteItems" {
				t.Fatalf("unexpected non-transaction request: %s", req.Header.Get("X-Amz-Target"))
			}
			var payload struct{ TransactItems []hierarchyWireItem }
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if inspect != nil {
				inspect(payload.TransactItems)
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
	})
	return NewDynamoDBRepository(client, "accounts-test")
}

func hierarchyCondition(op *hierarchyWireOperation) string {
	s := op.ConditionExpression
	for alias, name := range op.ExpressionAttributeNames {
		s = strings.ReplaceAll(s, alias, name)
	}
	for alias, value := range op.ExpressionAttributeValues {
		s = strings.ReplaceAll(s, alias, `"`+value["S"]+`"`)
	}
	return strings.NewReplacer("attribute_exists (", "attribute_exists(", "attribute_not_exists (", "attribute_not_exists(").Replace(s)
}

func requireHierarchyCondition(t *testing.T, op *hierarchyWireOperation, fragments ...string) {
	t.Helper()
	if op.TableName != "accounts-test" {
		t.Fatalf("wrong transaction table: %s", op.TableName)
	}
	cond := hierarchyCondition(op)
	for _, fragment := range fragments {
		if !strings.Contains(cond, fragment) {
			t.Errorf("condition %q missing %q", cond, fragment)
		}
	}
}

func TestAccountHierarchyTransactionGuards(t *testing.T) {
	for _, old := range []string{"", "old-parent"} {
		t.Run("old="+old, func(t *testing.T) {
			r := hierarchyWireRepo(t, func(items []hierarchyWireItem) {
				if len(items) != 5 {
					t.Fatalf("items=%d, want child + owner + parent membership + two ancestors", len(items))
				}
				seen := map[string]bool{}
				for _, item := range items {
					op := item.ConditionCheck
					if item.Update != nil {
						op = item.Update
						if op.Key["PK"]["S"] != "ACCOUNT#child" || op.Key["SK"]["S"] != "META" {
							t.Fatalf("wrong updated child: %+v", op.Key)
						}
						requireHierarchyCondition(t, op, "attribute_exists(PK)", `ownerUserId = "owner"`)
						if old == "" {
							requireHierarchyCondition(t, op, "attribute_not_exists(parentAccountId)", `parentAccountId = ""`, " OR ")
						} else {
							requireHierarchyCondition(t, op, `parentAccountId = "old-parent"`)
						}
						update := op.UpdateExpression
						for alias, name := range op.ExpressionAttributeNames {
							update = strings.ReplaceAll(update, alias, name)
						}
						for alias, value := range op.ExpressionAttributeValues {
							update = strings.ReplaceAll(update, alias, `"`+value["S"]+`"`)
						}
						if !strings.Contains(update, `parentAccountId = "parent"`) || !strings.Contains(update, "updatedAt =") ||
							strings.Contains(update, "name =") || strings.Contains(update, "ownerUserId =") {
							t.Fatalf("not a partial parent/timestamp update: %s", update)
						}
					}
					if op == nil || item.Put != nil {
						t.Fatal("reparent must not put whole items")
					}
					key := op.Key["PK"]["S"] + "/" + op.Key["SK"]["S"]
					if seen[key] {
						t.Fatalf("multiple operations on same item: %s", key)
					}
					seen[key] = true
					switch key {
					case "ACCOUNT#child/MEMBER#owner":
						requireHierarchyCondition(t, op, "attribute_exists(PK)", `role = "owner"`)
					case "ACCOUNT#parent/MEMBER#owner":
						requireHierarchyCondition(t, op, "attribute_exists(PK)")
					case "ACCOUNT#parent/META":
						requireHierarchyCondition(t, op, "attribute_exists(PK)", `parentAccountId = "root"`)
					case "ACCOUNT#root/META":
						requireHierarchyCondition(t, op, "attribute_exists(PK)", "attribute_not_exists(parentAccountId)", `parentAccountId = ""`, " OR ")
					case "ACCOUNT#child/META":
					default:
						t.Fatalf("unexpected transaction key %s", key)
					}
				}
			}, 200, `{}`)
			err := r.UpdateAccountParent(context.Background(), "child", "owner", old, "parent", []AccountParentLink{{"parent", "root"}, {"root", ""}})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAccountHierarchyDetachRemovesAttribute(t *testing.T) {
	r := hierarchyWireRepo(t, func(items []hierarchyWireItem) {
		if len(items) != 2 {
			t.Fatalf("detach should guard child and owner only: %+v", items)
		}
		op := items[0].Update
		if op == nil {
			t.Fatal("no child update")
		}
		update := op.UpdateExpression
		for alias, name := range op.ExpressionAttributeNames {
			update = strings.ReplaceAll(update, alias, name)
		}
		if !strings.Contains(update, "REMOVE parentAccountId") {
			t.Fatalf("detach must remove attribute: %s", update)
		}
		requireHierarchyCondition(t, op, `parentAccountId = "old-parent"`)
	}, 200, `{}`)
	if err := r.UpdateAccountParent(context.Background(), "child", "owner", "old-parent", "", nil); err != nil {
		t.Fatal(err)
	}
}

func TestAccountHierarchyCreateConditionalAtomic(t *testing.T) {
	for _, parent := range []string{"", "parent"} {
		t.Run("parent="+parent, func(t *testing.T) {
			r := hierarchyWireRepo(t, func(items []hierarchyWireItem) {
				want := 2
				if parent != "" {
					want = 4
				}
				if len(items) != want {
					t.Fatalf("items=%d, want %d", len(items), want)
				}
				for _, item := range items[:2] {
					if item.Put == nil {
						t.Fatal("creation needs account + owner puts")
					}
					requireHierarchyCondition(t, item.Put, "attribute_not_exists(PK)")
				}
				if parent != "" {
					if items[0].Put.Item["parentAccountId"].(map[string]any)["S"] != "parent" {
						t.Fatal("parent not part of creation transaction")
					}
					for _, item := range items[2:] {
						op := item.ConditionCheck
						if op == nil || op.Key["PK"]["S"] != "ACCOUNT#parent" {
							t.Fatalf("missing parent guards: %+v", item)
						}
						requireHierarchyCondition(t, op, "attribute_exists(PK)")
						if op.Key["SK"]["S"] == "META" {
							requireHierarchyCondition(t, op, "attribute_not_exists(parentAccountId)")
						} else if op.Key["SK"]["S"] != "MEMBER#owner" {
							t.Fatalf("wrong membership guarded: %+v", op.Key)
						}
					}
				}
			}, 200, `{}`)
			a := &model.Account{PK: "ACCOUNT#child", SK: "META", AccountID: "child", OwnerUserID: "owner", ParentAccountID: parent}
			owner := &model.AccountMember{PK: "ACCOUNT#child", SK: "MEMBER#owner", AccountID: "child", UserID: "owner", Role: model.RoleOwner}
			var err error
			if parent == "" {
				err = r.CreateAccount(context.Background(), a, owner)
			} else {
				err = r.CreateAccountWithParent(context.Background(), a, owner, []AccountParentLink{{"parent", ""}})
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAccountHierarchyTransactionConflicts(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		conflict   bool
	}{
		{"conditional", `{"__type":"TransactionCanceledException","CancellationReasons":[{"Code":"None"},{"Code":"ConditionalCheckFailed"}]}`, true},
		{"transaction race", `{"__type":"TransactionCanceledException","CancellationReasons":[{"Code":"TransactionConflict"}]}`, true},
		{"direct race", `{"__type":"TransactionConflictException"}`, true},
		{"throttled", `{"__type":"TransactionCanceledException","CancellationReasons":[{"Code":"ProvisionedThroughputExceeded"}]}`, false},
		{"validation", `{"__type":"TransactionCanceledException","CancellationReasons":[{"Code":"ValidationError"}]}`, false},
		{"mixed validation and condition", `{"__type":"TransactionCanceledException","CancellationReasons":[{"Code":"ConditionalCheckFailed"},{"Code":"ValidationError"}]}`, false},
		{"missing reasons", `{"__type":"TransactionCanceledException"}`, false},
		{"access denied", `{"__type":"AccessDeniedException"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := hierarchyWireRepo(t, nil, 400, tc.body)
			err := r.UpdateAccountParent(context.Background(), "child", "owner", "", "", nil)
			if err == nil || errors.Is(err, ErrConditionFailed) != tc.conflict {
				t.Fatalf("error=%v, want conflict=%v", err, tc.conflict)
			}
		})
	}
}

func TestAccountHierarchyLegacyStorage(t *testing.T) {
	var a model.Account
	if err := attributevalue.UnmarshalMap(map[string]types.AttributeValue{
		"accountId": &types.AttributeValueMemberS{Value: "legacy"},
	}, &a); err != nil || a.ParentAccountID != "" {
		t.Fatalf("legacy root: %+v, %v", a, err)
	}
	item, err := attributevalue.MarshalMap(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := item["parentAccountId"]; ok {
		t.Fatal("root must omit parent in storage")
	}
}

func TestAccountHierarchyRejectsIncompleteObservations(t *testing.T) {
	for _, tc := range []struct {
		name, parent string
		ancestors    []AccountParentLink
	}{
		{"missing parent guard", "parent", nil},
		{"missing root guard", "parent", []AccountParentLink{{"parent", "root"}}},
		{"wrong first ancestor", "parent", []AccountParentLink{{"other", ""}}},
		{"cycle through child", "parent", []AccountParentLink{{"parent", "child"}, {"child", ""}}},
		{"duplicate ancestor", "parent", []AccountParentLink{{"parent", "parent"}, {"parent", ""}}},
		{"extraneous detach ancestor", "", []AccountParentLink{{"other", ""}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := hierarchyWireRepo(t, func([]hierarchyWireItem) {
				t.Fatal("invalid ancestry reached DynamoDB")
			}, 200, `{}`)
			if err := r.UpdateAccountParent(context.Background(), "child", "owner", "", tc.parent, tc.ancestors); err == nil {
				t.Fatal("accepted an incomplete or cyclic ancestry")
			}
		})
	}
}

func TestAccountHierarchyTransactionDepthLimit(t *testing.T) {
	for _, count := range []int{64, 65} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			ancestors := make([]AccountParentLink, count)
			for i := range ancestors {
				ancestors[i].AccountID = fmt.Sprintf("a%d", i)
				if i+1 < count {
					ancestors[i].ParentAccountID = fmt.Sprintf("a%d", i+1)
				}
			}
			r := hierarchyWireRepo(t, func(items []hierarchyWireItem) {
				if count == 65 {
					t.Fatal("over-limit ancestry reached DynamoDB")
				}
				if len(items) != 67 {
					t.Fatalf("64 ancestors should produce 67 operations, got %d", len(items))
				}
			}, 200, `{}`)
			err := r.UpdateAccountParent(context.Background(), "child", "owner", "", "a0", ancestors)
			if count == 64 && err != nil || count == 65 && err == nil {
				t.Fatalf("depth %d: %v", count, err)
			}
		})
	}
}
