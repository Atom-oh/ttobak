package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

func TestUpdateMeetingExpectedNotesValidationAndAccess(t *testing.T) {
	for _, tc := range []struct {
		name, body, permission string
		want                   int
	}{
		{"missing notes", `{"expectedNotes":"before"}`, "owner", 400},
		{"title mutation", `{"notes":"after","expectedNotes":"before","title":"rename"}`, "owner", 400},
		{"summary mutation", `{"notes":"after","expectedNotes":"before","content":"summary"}`, "owner", 400},
		{"live mutation", `{"notes":"after","expectedNotes":"before","liveSummary":""}`, "owner", 400},
		{"transcript mutation", `{"notes":"after","expectedNotes":"before","transcriptA":"new"}`, "owner", 400},
		{"selection mutation", `{"notes":"after","expectedNotes":"before","selectedTranscript":"B"}`, "owner", 400},
		{"participants mutation", `{"notes":"after","expectedNotes":"before","participants":[]}`, "owner", 400},
		{"status mutation", `{"notes":"after","expectedNotes":"before","status":"done"}`, "owner", 400},
		{"oversize notes", `{"notes":"` + strings.Repeat("한", 32001) + `","expectedNotes":"before"}`, "owner", 400},
		{"oversize comparison", `{"notes":"after","expectedNotes":"` + strings.Repeat("😀", 32001) + `"}`, "owner", 400},
		{"readonly", `{"notes":"after","expectedNotes":"before"}`, "read", 403},
		{"no grant", `{"notes":"after","expectedNotes":"before"}`, "", 404},
		{"owner save", `{"notes":"after","expectedNotes":"before"}`, "owner", 200},
		{"editor clear", `{"notes":"","expectedNotes":"before"}`, "edit", 200},
		{"stale notes", `{"notes":"after","expectedNotes":"older"}`, "owner", 409},
		{"legacy save", `{"notes":"after","title":"rename"}`, "owner", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, repo := newStubMeetingHandler()
			repo.addMeeting(&model.Meeting{MeetingID: "m1", UserID: "owner", Title: "title",
				Notes: "before", Content: "human summary", Status: model.StatusDone, UpdatedAt: time.Now().UTC()})
			caller := "viewer"
			if tc.permission == "owner" {
				caller = "owner"
			} else if tc.permission != "" {
				repo.shares[hKey("viewer", "m1")] = &model.Share{OwnerID: "owner", MeetingID: "m1", Permission: tc.permission}
			}
			w := httptest.NewRecorder()
			req := withUserCtx(withChiParam(httptest.NewRequest("PUT", "/api/meetings/m1", strings.NewReader(tc.body)), "meetingId", "m1"), caller)
			h.UpdateMeeting(w, req)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.want, w.Body.String())
			}
			stored := repo.meetings[hKey("owner", "m1")]
			if tc.want != 200 && (stored.Notes != "before" || stored.Title != "title" || stored.Content != "human summary") {
				t.Fatal("rejected update changed human content")
			}
			if tc.want == 409 && !strings.Contains(w.Body.String(), `"code":"CONFLICT"`) {
				t.Fatal("notes conflict did not return the agreed code")
			}
			if tc.want == 200 {
				var sent struct{ Notes string }
				json.Unmarshal([]byte(tc.body), &sent)
				if stored.Notes != sent.Notes || stored.Content != "human summary" {
					t.Fatal("notes save changed the wrong fields")
				}
			}
		})
	}
}

// The real repository must send the comparison to DynamoDB, including the
// legacy absent-notes case. A service-only comparison cannot protect autosave.
func TestUpdateMeetingExpectedNotesWire(t *testing.T) {
	for _, tc := range []struct {
		name, expected string
		absent         bool
		fail           bool
	}{
		{"nonempty", "before", false, false},
		{"absent legacy notes", "", true, false},
		{"empty stored notes", "", false, false},
		{"concurrent edit", "before", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writes := 0
			db := dynamodb.New(dynamodb.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
				HTTPClient: readingHTTP(func(r *http.Request) (*http.Response, error) {
					switch r.Header.Get("X-Amz-Target") {
					case "DynamoDB_20120810.GetItem":
						item := map[string]any{"PK": map[string]string{"S": "USER#owner"}, "SK": map[string]string{"S": "MEETING#m1"},
							"meetingId": map[string]string{"S": "m1"}, "userId": map[string]string{"S": "owner"}}
						if !tc.absent {
							item["notes"] = map[string]string{"S": tc.expected}
						}
						body, _ := json.Marshal(map[string]any{"Item": item})
						return preparationWireResponse(200, string(body)), nil
					case "DynamoDB_20120810.UpdateItem":
						writes++
						var input struct {
							Key                       map[string]map[string]string
							ConditionExpression       string
							UpdateExpression          string
							ExpressionAttributeNames  map[string]string
							ExpressionAttributeValues map[string]map[string]string
						}
						if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
							t.Fatal(err)
						}
						if input.Key["PK"]["S"] != "USER#owner" || input.Key["SK"]["S"] != "MEETING#m1" {
							t.Fatal("notes update escaped owner partition")
						}
						names := map[string]string{}
						for alias, field := range input.ExpressionAttributeNames {
							names[field] = alias
							if field != "PK" && field != "notes" && field != "notesRevision" && field != "updatedAt" {
								t.Fatalf("notes write touched unrelated field %s", field)
							}
						}
						expectedAlias := ""
						for alias, value := range input.ExpressionAttributeValues {
							if value["S"] == tc.expected {
								expectedAlias = alias
							}
						}
						condition := strings.ReplaceAll(input.ConditionExpression, " ", "")
						if names["PK"] == "" || !strings.Contains(condition, "attribute_exists("+names["PK"]+")") ||
							names["notes"] == "" || expectedAlias == "" || !strings.Contains(condition, names["notes"]+"="+expectedAlias) {
							t.Fatalf("missing server-side existence/notes comparison: %+v", input)
						}
						absent := "attribute_not_exists(" + names["notes"] + ")"
						if strings.Contains(condition, absent) != (tc.expected == "") ||
							(tc.expected == "" && !strings.Contains(condition, "AND(("+names["notes"]+"="+expectedAlias+")OR("+absent+"))")) {
							t.Fatalf("incorrect absent-notes comparison: %s", input.ConditionExpression)
						}
						if tc.fail {
							return preparationWireResponse(400, `{"__type":"ConditionalCheckFailedException","message":"synthetic concurrent edit"}`), nil
						}
						return preparationWireResponse(200, `{}`), nil
					default:
						t.Fatalf("unexpected operation %s", r.Header.Get("X-Amz-Target"))
						return nil, nil
					}
				}),
			})
			repo := repository.NewDynamoDBRepository(db, "notes-test")
			h := NewMeetingHandler(service.NewMeetingService(repo), repo)
			body, _ := json.Marshal(map[string]string{"notes": "after", "expectedNotes": tc.expected})
			w := httptest.NewRecorder()
			h.UpdateMeeting(w, withUserCtx(withChiParam(httptest.NewRequest("PUT", "/api/meetings/m1", strings.NewReader(string(body))), "meetingId", "m1"), "owner"))
			want := 200
			if tc.fail {
				want = 409
			}
			if writes != 1 || w.Code != want {
				t.Fatalf("writes=%d status=%d want=%d body=%s", writes, w.Code, want, w.Body.String())
			}
		})
	}
}
