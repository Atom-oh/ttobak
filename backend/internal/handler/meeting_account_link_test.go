package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

func TestPrivateAccountRelinkUsesSingleAtomicPartialWrite(t *testing.T) {
	writes, sweepReads := 0, 0
	db := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: readingHTTP(func(r *http.Request) (*http.Response, error) {
			switch r.Header.Get("X-Amz-Target") {
			case "DynamoDB_20120810.GetItem":
				var input struct{ Key map[string]map[string]string }
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Fatal(err)
				}
				switch {
				case input.Key["PK"]["S"] == "USER#owner" && input.Key["SK"]["S"] == "MEETING#meeting":
					return preparationWireResponse(200, `{"Item":{"PK":{"S":"USER#owner"},"SK":{"S":"MEETING#meeting"},"meetingId":{"S":"meeting"},"userId":{"S":"owner"},"accountId":{"S":"account-a"},"sharedToAccount":{"BOOL":true},"notes":{"S":"saved notes"}}}`), nil
				case input.Key["PK"]["S"] == "ACCOUNT#account-b" && input.Key["SK"]["S"] == "MEMBER#owner":
					return preparationWireResponse(200, `{"Item":{"accountId":{"S":"account-b"},"userId":{"S":"owner"},"role":{"S":"SA"}}}`), nil
				case input.Key["PK"]["S"] == "MEETING#meeting" && input.Key["SK"]["S"] == "ANALYSIS#terms":
					// The ADR-008 FU4 relink sweep reads the run state after the
					// link commits; no terms run means nothing to sweep.
					if writes != 1 {
						t.Fatal("the term sweep must follow the committed link")
					}
					sweepReads++
					return preparationWireResponse(200, `{}`), nil
				default:
					t.Fatalf("unexpected access lookup: %+v", input)
					return nil, nil
				}
			case "DynamoDB_20120810.TransactWriteItems":
				writes++
				var wire struct {
					TransactItems []struct {
						Update, ConditionCheck *struct {
							Key                       map[string]map[string]string
							ConditionExpression       string
							UpdateExpression          string
							ExpressionAttributeNames  map[string]string
							ExpressionAttributeValues map[string]map[string]any
						}
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
					t.Fatal(err)
				}
				// The target account's META open check commits with the write.
				if len(wire.TransactItems) != 2 || wire.TransactItems[0].ConditionCheck == nil || wire.TransactItems[1].Update == nil ||
					wire.TransactItems[0].ConditionCheck.Key["PK"]["S"] != "ACCOUNT#account-b" || wire.TransactItems[0].ConditionCheck.Key["SK"]["S"] != "META" {
					t.Fatalf("want the account META open check plus the meeting update: %+v", wire.TransactItems)
				}
				input := *wire.TransactItems[1].Update
				if input.Key["PK"]["S"] != "USER#owner" || input.Key["SK"]["S"] != "MEETING#meeting" {
					t.Fatal("private link escaped the owner's meeting")
				}
				names := map[string]string{}
				for alias, field := range input.ExpressionAttributeNames {
					names[field] = alias
					if field != "PK" && field != "accountId" && field != "sharedToAccount" && field != "updatedAt" && field != "summaryAccountApproval" && field != "meetingCategory" {
						t.Fatalf("private link modifies unrelated field %s", field)
					}
				}
				accountValue, privateValue := "", ""
				for alias, value := range input.ExpressionAttributeValues {
					if value["S"] == "account-b" {
						accountValue = alias
					}
					if shared, ok := value["BOOL"].(bool); ok && !shared {
						privateValue = alias
					}
				}
				if names["accountId"] == "" || names["sharedToAccount"] == "" || accountValue == "" || privateValue == "" ||
					!strings.Contains(input.UpdateExpression, names["accountId"]+" = "+accountValue) ||
					!strings.Contains(input.UpdateExpression, names["sharedToAccount"]+" = "+privateValue) {
					t.Fatalf("account and private flag must be set in the same update: %+v", input)
				}
				if names["PK"] == "" || !strings.Contains(strings.ReplaceAll(input.ConditionExpression, " ", ""), "attribute_exists("+names["PK"]+")") {
					t.Fatal("private link lost the meeting existence condition")
				}
				return preparationWireResponse(200, `{}`), nil
			default:
				t.Fatalf("unexpected mutation: %s", r.Header.Get("X-Amz-Target"))
				return nil, nil
			}
		}),
	})
	repo := repository.NewDynamoDBRepository(db, "private-link-test")
	h := NewMeetingHandler(service.NewMeetingService(repo), repo)
	w := httptest.NewRecorder()
	req := withUserCtx(withChiParam(httptest.NewRequest("POST", "/api/meetings/meeting/account",
		strings.NewReader(`{"accountId":"account-b"}`)), "meetingId", "meeting"), "owner")
	h.LinkToAccount(w, req)
	if w.Code != 200 || writes != 1 || sweepReads != 1 {
		t.Fatalf("private link status=%d writes=%d sweepReads=%d body=%s", w.Code, writes, sweepReads, w.Body.String())
	}
}

// A concurrent unpublish/move or membership change surfaces as 409, not 500.
func TestAccountLinkConditionFailureIsConflict(t *testing.T) {
	db := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: readingHTTP(func(r *http.Request) (*http.Response, error) {
			switch r.Header.Get("X-Amz-Target") {
			case "DynamoDB_20120810.GetItem":
				var input struct{ Key map[string]map[string]string }
				json.NewDecoder(r.Body).Decode(&input)
				switch {
				case input.Key["PK"]["S"] == "USER#owner" && input.Key["SK"]["S"] == "MEETING#meeting":
					return preparationWireResponse(200, `{"Item":{"PK":{"S":"USER#owner"},"SK":{"S":"MEETING#meeting"},"meetingId":{"S":"meeting"},"userId":{"S":"owner"}}}`), nil
				case input.Key["PK"]["S"] == "ACCOUNT#account-b" && input.Key["SK"]["S"] == "MEMBER#owner":
					return preparationWireResponse(200, `{"Item":{"accountId":{"S":"account-b"},"userId":{"S":"owner"},"role":{"S":"SA"}}}`), nil
				}
				return preparationWireResponse(200, `{}`), nil
			case "DynamoDB_20120810.TransactWriteItems":
				return preparationWireResponse(400, `{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException","message":"canceled","CancellationReasons":[{"Code":"None"},{"Code":"ConditionalCheckFailed"}]}`), nil
			}
			return preparationWireResponse(200, `{}`), nil
		}),
	})
	repo := repository.NewDynamoDBRepository(db, "private-link-test")
	h := NewMeetingHandler(service.NewMeetingService(repo), repo)
	w := httptest.NewRecorder()
	req := withUserCtx(withChiParam(httptest.NewRequest("POST", "/api/meetings/meeting/account",
		strings.NewReader(`{"accountId":"account-b"}`)), "meetingId", "meeting"), "owner")
	h.LinkToAccount(w, req)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "CONFLICT") {
		t.Fatalf("condition failure status=%d body=%s", w.Code, w.Body.String())
	}
}

// A target account that is merging or merged rejects classification with 409
// ACCOUNT_MERGING, not the generic concurrent-change conflict.
func TestAccountLinkToMergingAccountIsAccountMerging(t *testing.T) {
	db := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: readingHTTP(func(r *http.Request) (*http.Response, error) {
			switch r.Header.Get("X-Amz-Target") {
			case "DynamoDB_20120810.GetItem":
				var input struct{ Key map[string]map[string]string }
				json.NewDecoder(r.Body).Decode(&input)
				switch {
				case input.Key["PK"]["S"] == "USER#owner" && input.Key["SK"]["S"] == "MEETING#meeting":
					return preparationWireResponse(200, `{"Item":{"PK":{"S":"USER#owner"},"SK":{"S":"MEETING#meeting"},"meetingId":{"S":"meeting"},"userId":{"S":"owner"}}}`), nil
				case input.Key["PK"]["S"] == "ACCOUNT#account-b" && input.Key["SK"]["S"] == "MEMBER#owner":
					return preparationWireResponse(200, `{"Item":{"accountId":{"S":"account-b"},"userId":{"S":"owner"},"role":{"S":"SA"}}}`), nil
				}
				return preparationWireResponse(200, `{}`), nil
			case "DynamoDB_20120810.TransactWriteItems":
				return preparationWireResponse(400, `{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException","message":"canceled","CancellationReasons":[{"Code":"None"},{"Code":"ConditionalCheckFailed","Item":{"mergingInto":{"S":"account-c"}}}]}`), nil
			}
			return preparationWireResponse(200, `{}`), nil
		}),
	})
	repo := repository.NewDynamoDBRepository(db, "private-link-test")
	h := NewMeetingHandler(service.NewMeetingService(repo), repo)
	w := httptest.NewRecorder()
	req := withUserCtx(withChiParam(httptest.NewRequest("POST", "/api/meetings/meeting/account",
		strings.NewReader(`{"accountId":"account-b"}`)), "meetingId", "meeting"), "owner")
	h.LinkToAccount(w, req)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "ACCOUNT_MERGING") {
		t.Fatalf("status=%d body=%s, want 409 ACCOUNT_MERGING", w.Code, w.Body.String())
	}
}
