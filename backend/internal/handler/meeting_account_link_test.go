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
	writes := 0
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
				default:
					t.Fatalf("unexpected access lookup: %+v", input)
					return nil, nil
				}
			case "DynamoDB_20120810.UpdateItem":
				writes++
				var input struct {
					Key                       map[string]map[string]string
					ConditionExpression       string
					UpdateExpression          string
					ExpressionAttributeNames  map[string]string
					ExpressionAttributeValues map[string]map[string]any
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Fatal(err)
				}
				if input.Key["PK"]["S"] != "USER#owner" || input.Key["SK"]["S"] != "MEETING#meeting" {
					t.Fatal("private link escaped the owner's meeting")
				}
				names := map[string]string{}
				for alias, field := range input.ExpressionAttributeNames {
					names[field] = alias
					if field != "PK" && field != "accountId" && field != "sharedToAccount" && field != "updatedAt" {
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
	if w.Code != 200 || writes != 1 {
		t.Fatalf("private link status=%d writes=%d body=%s", w.Code, writes, w.Body.String())
	}
}
