package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

func preparationWireResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
		Body: io.NopCloser(strings.NewReader(body))}
}

// Exercise the HTTP -> service -> real SDK boundary: preparation must reach the
// first PutItem, and rejected preparation must never create a meeting.
func TestCreateMeetingPreparationInitialWrite(t *testing.T) {
	for _, tc := range []struct {
		name, notes, account string
		member, lookupError  bool
		status               int
	}{
		{"notes and member account", "준비한 질문 😀", "account", true, false, 201},
		{"notes only", "private preparation", "", false, false, 201},
		{"account only", "", "account", true, false, 201},
		{"legacy caller", "", "", false, false, 201},
		{"unicode boundary", strings.Repeat("😀", 32000), "", false, false, 201},
		{"notes too long", strings.Repeat("😀", 32001), "", false, false, 400},
		{"nonmember", "private", "account", false, false, 403},
		{"membership failure", "private", "account", false, true, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			puts, membershipReads := 0, 0
			var item map[string]map[string]any
			db := dynamodb.New(dynamodb.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
				HTTPClient: readingHTTP(func(r *http.Request) (*http.Response, error) {
					switch r.Header.Get("X-Amz-Target") {
					case "DynamoDB_20120810.GetItem":
						membershipReads++
						var input struct{ Key map[string]map[string]string }
						if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
							t.Fatal(err)
						}
						if puts != 0 || input.Key["PK"]["S"] != "ACCOUNT#account" || input.Key["SK"]["S"] != "MEMBER#owner" {
							t.Fatalf("membership not validated before creation: %+v", input)
						}
						if tc.lookupError {
							return preparationWireResponse(400, `{"__type":"AccessDeniedException","message":"synthetic lookup failure"}`), nil
						}
						if tc.member {
							return preparationWireResponse(200, `{"Item":{"accountId":{"S":"account"},"userId":{"S":"owner"},"role":{"S":"SA"}}}`), nil
						}
						return preparationWireResponse(200, `{}`), nil
					case "DynamoDB_20120810.PutItem":
						puts++
						var input struct{ Item map[string]map[string]any }
						if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
							t.Fatal(err)
						}
						item = input.Item
						return preparationWireResponse(200, `{}`), nil
					default:
						t.Fatalf("unexpected operation: %s", r.Header.Get("X-Amz-Target"))
						return nil, nil
					}
				}),
			})
			repo := repository.NewDynamoDBRepository(db, "preparation-test")
			h := NewMeetingHandler(service.NewMeetingService(repo), repo)
			body := map[string]any{"title": "SA meeting", "notesRevision": "client-cannot-assign"}
			if tc.notes != "" {
				body["notes"] = tc.notes
			}
			if tc.account != "" {
				body["accountId"] = tc.account
			}
			data, _ := json.Marshal(body)
			w := httptest.NewRecorder()
			h.CreateMeeting(w, withUserCtx(httptest.NewRequest("POST", "/api/meetings", strings.NewReader(string(data))), "owner"))
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
			}
			if tc.status != 201 {
				if puts != 0 {
					t.Fatal("invalid preparation created an orphan meeting")
				}
				return
			}
			if puts != 1 || item["PK"]["S"] != "USER#owner" || item["status"]["S"] != "recording" {
				t.Fatalf("unexpected initial meeting: puts=%d item=%v", puts, item)
			}
			if tc.notes != "" && item["notes"]["S"] != tc.notes {
				t.Fatal("initial PutItem dropped preparation notes")
			}
			if tc.account != "" && (membershipReads != 1 || item["accountId"]["S"] != tc.account) {
				t.Fatal("initial PutItem dropped the authorized account")
			}
			if item["sharedToAccount"]["BOOL"] == true {
				t.Fatal("preparation unexpectedly shared the meeting")
			}
			var response map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			version, _ := response["notesRevision"].(string)
			if _, err := uuid.Parse(version); err != nil || item["notesRevision"]["S"] != version {
				t.Fatalf("initial write/response lost server-generated notes revision: %q", version)
			}
			if response["supportsNotesComparison"] != true || response["supportsPrivateAccountLink"] != true {
				t.Fatalf("missing guarded workflow capabilities: %v", response)
			}
			applied, _ := response["preparationApplied"].(bool)
			if applied != (tc.notes != "" || tc.account != "") {
				t.Fatalf("incorrect preparation acknowledgement: %v", response)
			}
		})
	}
}
