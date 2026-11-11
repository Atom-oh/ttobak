package repository

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/sealing"
)

func TestContactMutationsAreAtomicAndConditional(t *testing.T) {
	for _, op := range []string{"create", "update", "delete"} {
		t.Run(op, func(t *testing.T) {
			calls := 0
			repo := contactWire(t, func(target string, body map[string]interface{}) string {
				if op == "delete" && target == "DynamoDB_20120810.Query" {
					return `{"Items":[]}` // the SFDC index lookup: not linked
				}
				calls++
				if target != "DynamoDB_20120810.TransactWriteItems" {
					t.Fatalf("target %s", target)
				}
				items := body["TransactItems"].([]interface{})
				if len(items) != 3 {
					t.Fatalf("expected account, member, contact checks")
				}
				for i, sk := range []string{"META", "MEMBER#user"} {
					c := items[i].(map[string]interface{})["ConditionCheck"].(map[string]interface{})
					key := c["Key"].(map[string]interface{})
					if key["PK"].(map[string]interface{})["S"] != "ACCOUNT#account" || key["SK"].(map[string]interface{})["S"] != sk || c["ConditionExpression"] == nil {
						t.Fatalf("bad authorization check: %#v", c)
					}
				}
				kind := "Put"
				if op == "update" {
					kind = "Update"
				}
				if op == "delete" {
					kind = "Delete"
				}
				c := items[2].(map[string]interface{})[kind].(map[string]interface{})
				if c["ConditionExpression"] == nil {
					t.Fatal("missing contact condition")
				}
				return `{}`
			})
			err := repo.WriteAccountContact(context.Background(), "account", "user", &model.AccountContact{ContactID: "01234567-89ab-4cde-8123-0123456789ab", Name: "a"}, op)
			if err != nil || calls != 1 {
				t.Fatalf("write: %d %v", calls, err)
			}
		})
	}
}
func TestContactTransactionFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		codes    []string
		expected error
	}{{[]string{"ConditionalCheckFailed", "None", "None"}, ErrContactAccess}, {[]string{"None", "ConditionalCheckFailed", "None"}, ErrContactAccess}, {[]string{"None", "None", "ConditionalCheckFailed"}, ErrConditionFailed}, {[]string{"ConditionalCheckFailed", "None", "ValidationError"}, nil}, {nil, nil}} {
		reasons := []types.CancellationReason{}
		for _, c := range tc.codes {
			reasons = append(reasons, types.CancellationReason{Code: aws.String(c)})
		}
		source := &types.TransactionCanceledException{CancellationReasons: reasons}
		got := classifyContactTransaction(source)
		if tc.expected == nil {
			if got != source {
				t.Fatalf("unexpected classification: %v", got)
			}
		} else if !errors.Is(got, tc.expected) {
			t.Fatalf("classification %v: %v", tc.codes, got)
		}
	}
}

func TestContactQueryUsesStrongBoundedAccountPages(t *testing.T) {
	const id = "01234567-89ab-4cde-8123-0123456789ab"
	repo := contactWire(t, func(target string, body map[string]interface{}) string {
		if target != "DynamoDB_20120810.Query" || body["ConsistentRead"] != true || body["Limit"] != float64(25) {
			t.Fatalf("query must be strongly consistent and bounded: %#v", body)
		}
		start := body["ExclusiveStartKey"].(map[string]interface{})
		if start["PK"].(map[string]interface{})["S"] != "ACCOUNT#account" || start["SK"].(map[string]interface{})["S"] != "CONTACT#"+id {
			t.Fatalf("unbound start key: %#v", start)
		}
		sealed, _ := testSealer{}.Seal(context.Background(), []byte(`{"contactId":"`+id+`","name":"Alice"}`), contactBinding("account", id))
		return `{"Items":[{"PK":{"S":"ACCOUNT#account"},"SK":{"S":"CONTACT#` + id + `"},"contactId":{"S":"` + id + `"},"revision":{"S":"r1"},"sealed":{"S":"` + sealed + `"}}],"LastEvaluatedKey":{"PK":{"S":"ACCOUNT#account"},"SK":{"S":"CONTACT#01234567-89ab-4cde-8123-0123456789ab"}}}`
	})
	got, next, err := repo.ListAccountContacts(context.Background(), "account", 25, id)
	if err != nil || len(got) != 1 || got[0].Name != "Alice" || got[0].Revision != "r1" || next != id {
		t.Fatalf("page: %#v %q %v", got, next, err)
	}
}

func TestSpeakerTeamQueryUsesStrongBoundedAccountPages(t *testing.T) {
	repo := contactWire(t, func(target string, body map[string]interface{}) string {
		if target != "DynamoDB_20120810.Query" || body["ConsistentRead"] != true || body["Limit"] != float64(25) {
			t.Fatalf("team query: %#v", body)
		}
		start := body["ExclusiveStartKey"].(map[string]interface{})
		if start["PK"].(map[string]interface{})["S"] != "ACCOUNT#account" || start["SK"].(map[string]interface{})["S"] != "MEMBER#user" {
			t.Fatalf("team cursor: %#v", start)
		}
		return `{"Items":[{"userId":{"S":"user2"}}],"LastEvaluatedKey":{"PK":{"S":"ACCOUNT#account"},"SK":{"S":"MEMBER#user2"}}}`
	})
	got, next, err := repo.ListSpeakerTeamMembers(context.Background(), "account", 25, "user")
	if err != nil || len(got) != 1 || got[0].UserID != "user2" || next != "user2" {
		t.Fatalf("page: %#v %q %v", got, next, err)
	}
}

func TestContactMutationConditionsAndRetention(t *testing.T) {
	for _, op := range []string{"create", "update", "delete"} {
		t.Run(op, func(t *testing.T) {
			repo := contactWire(t, func(target string, body map[string]interface{}) string {
				if target == "DynamoDB_20120810.Query" {
					return `{"Items":[]}` // the SFDC index lookup: not linked
				}
				items := body["TransactItems"].([]interface{})
				kind := "Put"
				if op == "update" {
					kind = "Update"
				}
				if op == "delete" {
					kind = "Delete"
				}
				action := items[2].(map[string]interface{})[kind].(map[string]interface{})
				cond := action["ConditionExpression"].(string)
				if op == "create" && !strings.Contains(cond, "attribute_not_exists") || op != "create" && !strings.Contains(cond, "attribute_exists") {
					t.Fatalf("bad contact condition: %s", cond)
				}
				if op == "create" {
					row := action["Item"].(map[string]interface{})
					if _, ok := row["pendingShareExpiresAt"]; ok {
						t.Fatal("contact must not expire")
					}
					for _, key := range []string{"name", "company", "email", "address", "workPhone", "fax", "phone", "cardRegisteredAt", "cardBook", "group", "notes"} {
						if _, ok := row[key]; ok {
							t.Fatalf("contact field %s stored in plaintext", key)
						}
					}
					sealed := row["sealed"].(map[string]interface{})["S"].(string)
					plain, err := testSealer{}.Open(context.Background(), sealed, contactBinding("account", "01234567-89ab-4cde-8123-0123456789ab"))
					if err != nil || !strings.Contains(string(plain), `"notes":""`) || !strings.Contains(string(plain), `"name":"A"`) {
						t.Fatalf("sealed payload must carry every field: %s %v", plain, err)
					}
				}
				if op == "update" {
					names := fmt.Sprint(action["ExpressionAttributeNames"])
					if !strings.Contains(names, "sealed") || strings.Contains(names, "phone") || strings.Contains(names, "name:") {
						t.Fatalf("update must replace only the sealed payload: %s", names)
					}
				}
				return `{}`
			})
			if err := repo.WriteAccountContact(context.Background(), "account", "user", &model.AccountContact{ContactID: "01234567-89ab-4cde-8123-0123456789ab", Name: "A"}, op); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestContactTransactionSDKRejectionMapping(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   error
	}{
		{`[{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"}]`, ErrContactAccess},
		{`[{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"}]`, ErrContactAccess},
		{`[{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"}]`, ErrConditionFailed},
	} {
		repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
			return summaryHTTPResponse(400, `{"__type":"TransactionCanceledException","CancellationReasons":`+tc.reason+`}`, nil), nil
		})
		repo.SetRecordSealer(testSealer{})
		err := repo.WriteAccountContact(context.Background(), "account", "user", &model.AccountContact{ContactID: "01234567-89ab-4cde-8123-0123456789ab", Name: "A"}, "update")
		if !errors.Is(err, tc.want) {
			t.Fatalf("cancellation mapping: %v", err)
		}
	}
}

func TestContactUpdateRequiresObservedRevision(t *testing.T) {
	repo := contactWire(t, func(_ string, body map[string]interface{}) string {
		items := body["TransactItems"].([]interface{})
		update, ok := items[2].(map[string]interface{})["Update"].(map[string]interface{})
		if !ok {
			t.Fatal("contact updates must use conditional UpdateItem")
		}
		if !strings.Contains(update["ConditionExpression"].(string), "=") {
			t.Fatal("update must reject a stale revision")
		}
		return `{}`
	})
	if err := repo.WriteAccountContact(context.Background(), "account", "user", &model.AccountContact{ContactID: "01234567-89ab-4cde-8123-0123456789ab", Name: "A"}, "update"); err != nil {
		t.Fatal(err)
	}
}

// testSealer is a reversible stand-in for KMS envelope sealing that still
// enforces the record binding.
type testSealer struct{}

func testBindingID(b map[string]string) string {
	return b["purpose"] + "/" + b["accountId"] + "/" + b["contactId"]
}

func (testSealer) Seal(_ context.Context, p []byte, b map[string]string) (string, error) {
	return base64.StdEncoding.EncodeToString([]byte(testBindingID(b) + "|" + string(p))), nil
}

func (testSealer) Open(_ context.Context, sealed string, b map[string]string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	prefix := testBindingID(b) + "|"
	if err != nil || !strings.HasPrefix(string(raw), prefix) {
		return nil, sealing.ErrInvalid
	}
	return raw[len(prefix):], nil
}

func contactWire(t *testing.T, call func(string, map[string]interface{}) string) *DynamoDBRepository {
	t.Helper()
	repo := indexingWire(t, call)
	repo.SetRecordSealer(testSealer{})
	return repo
}

func TestContactsFailClosedWithoutEncryptionKey(t *testing.T) {
	calls := 0
	repo := indexingWire(t, func(string, map[string]interface{}) string { calls++; return `{}` })
	c := &model.AccountContact{ContactID: "01234567-89ab-4cde-8123-0123456789ab", Name: "A"}
	for _, op := range []string{"create", "update"} {
		if err := repo.WriteAccountContact(context.Background(), "account", "user", c, op); !errors.Is(err, ErrContactEncryptionUnavailable) {
			t.Fatalf("%s without a key: %v", op, err)
		}
	}
	if _, err := repo.SealedContactItem(context.Background(), "account", c); !errors.Is(err, ErrContactEncryptionUnavailable) {
		t.Fatalf("item without a key: %v", err)
	}
	if calls != 0 {
		t.Fatalf("no plaintext write may reach DynamoDB: %d calls", calls)
	}
}

func TestContactRowCannotBeOpenedUnderAnotherAccount(t *testing.T) {
	const id = "01234567-89ab-4cde-8123-0123456789ab"
	sealed, _ := testSealer{}.Seal(context.Background(), []byte(`{"contactId":"`+id+`","name":"Alice"}`), contactBinding("other", id))
	repo := contactWire(t, func(string, map[string]interface{}) string {
		return `{"Item":{"PK":{"S":"ACCOUNT#account"},"SK":{"S":"CONTACT#` + id + `"},"contactId":{"S":"` + id + `"},"revision":{"S":"r"},"sealed":{"S":"` + sealed + `"}}}`
	})
	if _, err := repo.GetAccountContact(context.Background(), "account", id); !errors.Is(err, ErrContactUnreadable) || errors.Is(err, ErrContactEncryptionUnavailable) {
		t.Fatalf("a payload sealed for another account must be unreadable: %v", err)
	}
	plain := contactWire(t, func(string, map[string]interface{}) string {
		return `{"Item":{"PK":{"S":"ACCOUNT#account"},"SK":{"S":"CONTACT#` + id + `"},"contactId":{"S":"` + id + `"},"name":{"S":"Alice"}}}`
	})
	if _, err := plain.GetAccountContact(context.Background(), "account", id); !errors.Is(err, ErrContactUnreadable) {
		t.Fatalf("an unsealed row must not be served: %v", err)
	}
	keyless := indexingWire(t, func(string, map[string]interface{}) string {
		return `{"Item":{"PK":{"S":"ACCOUNT#account"},"SK":{"S":"CONTACT#` + id + `"},"contactId":{"S":"` + id + `"},"revision":{"S":"r"},"sealed":{"S":"` + sealed + `"}}}`
	})
	if _, err := keyless.GetAccountContact(context.Background(), "account", id); !errors.Is(err, ErrContactEncryptionUnavailable) || errors.Is(err, ErrContactUnreadable) {
		t.Fatalf("a missing key must stay unavailable: %v", err)
	}
}
