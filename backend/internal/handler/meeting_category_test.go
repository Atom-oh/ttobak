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

func TestSetMeetingCategoryStatus(t *testing.T) {
	const owned = `{"Item":{"PK":{"S":"USER#owner"},"SK":{"S":"MEETING#meeting"},"meetingId":{"S":"meeting"},"userId":{"S":"owner"}}}`
	const linked = `{"Item":{"PK":{"S":"USER#owner"},"SK":{"S":"MEETING#meeting"},"meetingId":{"S":"meeting"},"userId":{"S":"owner"},"accountId":{"S":"account"}}}`
	for _, tc := range []struct {
		name, body, item, update string
		updateStatus             int
		want, writes             int
	}{
		{name: "set", body: `{"category":"internal_1on1"}`, item: owned, updateStatus: 200, want: 200, writes: 1},
		{name: "clear", body: `{"category":""}`, item: owned, updateStatus: 200, want: 200, writes: 1},
		{name: "invalid", body: `{"category":"internal"}`, item: owned, want: 400},
		{name: "malformed", body: `{`, item: owned, want: 400},
		{name: "account linked", body: `{"category":"internal_team"}`, item: linked, want: 409},
		{name: "concurrent link", body: `{"category":"internal_team"}`, item: owned, want: 409, writes: 1,
			updateStatus: 400, update: `{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","message":"The conditional request failed"}`},
		{name: "missing", body: `{"category":"internal_team"}`, item: `{}`, want: 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writes := 0
			db := dynamodb.New(dynamodb.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
				HTTPClient: readingHTTP(func(r *http.Request) (*http.Response, error) {
					switch r.Header.Get("X-Amz-Target") {
					case "DynamoDB_20120810.GetItem":
						var input struct{ Key map[string]map[string]string }
						_ = json.NewDecoder(r.Body).Decode(&input)
						if input.Key["PK"]["S"] == "USER#owner" && input.Key["SK"]["S"] == "MEETING#meeting" {
							return preparationWireResponse(200, tc.item), nil
						}
						return preparationWireResponse(200, `{}`), nil
					case "DynamoDB_20120810.Query":
						return preparationWireResponse(200, `{"Items":[]}`), nil
					case "DynamoDB_20120810.UpdateItem":
						writes++
						body := tc.update
						if body == "" {
							body = `{}`
						}
						return preparationWireResponse(tc.updateStatus, body), nil
					}
					t.Fatalf("unexpected operation %s", r.Header.Get("X-Amz-Target"))
					return nil, nil
				}),
			})
			repo := repository.NewDynamoDBRepository(db, "category-test")
			h := NewMeetingHandler(service.NewMeetingService(repo), repo)
			w := httptest.NewRecorder()
			req := withUserCtx(withChiParam(httptest.NewRequest("PUT", "/api/meetings/meeting/category",
				strings.NewReader(tc.body)), "meetingId", "meeting"), "owner")
			h.SetCategory(w, req)
			if w.Code != tc.want || writes != tc.writes {
				t.Fatalf("status=%d writes=%d body=%s", w.Code, writes, w.Body.String())
			}
		})
	}
}
