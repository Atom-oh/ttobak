package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/model"
)

func TestNotesWritesDoNotReplayObservableRevisions(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"create", "legacy partial", "generic conditional", "text CAS", "version CAS", "whole item", "revision response"} {
		t.Run(name, func(t *testing.T) {
			currentText, currentVersion, meetingID := "A", "initial", "m"
			var firstVersion, fenceVersion string
			attempts := 0
			fencing, probing := false, false
			var repo *DynamoDBRepository
			client := dynamodb.New(dynamodb.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{},
				Retryer: retry.NewStandard(func(o *retry.StandardOptions) {
					o.MaxAttempts = 2
					o.Backoff = retry.BackoffDelayerFunc(func(int, error) (time.Duration, error) { return 0, nil })
				}),
				HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
					var input notesRevisionRequest
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Error(err)
						return nil, err
					}
					status, body := 200, `{}`
					switch req.Header.Get("X-Amz-Target") {
					case "DynamoDB_20120810.GetItem":
						body = `{"Item":{}}` // no project relations to preserve
					case "DynamoDB_20120810.PutItem", "DynamoDB_20120810.UpdateItem":
						if !fencing && !probing {
							attempts++
						}
						text, _ := notesWireValue(input, input.UpdateExpression, "notes")
						version, _ := notesWireValue(input, input.UpdateExpression, "notesRevision")
						if input.Item != nil {
							text, _ = input.Item["notes"]["S"].(string)
							version, _ = input.Item["notesRevision"]["S"].(string)
							meetingID, _ = input.Item["meetingId"]["S"].(string)
						}
						expectedText, checksText := notesWireValue(input, input.ConditionExpression, "notes")
						expectedVersion, checksVersion := notesWireValue(input, input.ConditionExpression, "notesRevision")
						if checksText && expectedText != currentText || checksVersion && expectedVersion != currentVersion {
							status, body = 400, `{"__type":"ConditionalCheckFailedException"}`
						} else {
							currentText, currentVersion = text, version
							if !fencing && !probing && attempts == 1 {
								firstVersion = version
								fencing = true
								var err error
								fenceVersion, err = repo.UpdateMeetingNotesWithRevision(ctx, "owner", meetingID, "A", "A", &firstVersion)
								fencing = false
								if err != nil {
									t.Errorf("interleaved fence: %v", err)
									return nil, err
								}
								// The first commit was observable, but its reply was lost.
								status, body = 500, `{"__type":"InternalServerError","message":"synthetic ambiguous commit"}`
							}
						}
					default:
						return nil, errors.New("unexpected synthetic storage operation")
					}
					return &http.Response{StatusCode: status,
						Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
						Body:   io.NopCloser(strings.NewReader(body))}, nil
				}),
			})
			repo = NewDynamoDBRepository(client, "notes-retry")
			var err error
			switch name {
			case "create":
				_, err = repo.CreateMeeting(ctx, "owner", "Meeting", time.Now(), nil, "", model.MeetingPreparation{Notes: "A"})
			case "legacy partial":
				err = repo.UpdateMeetingFields(ctx, "owner", meetingID, map[string]any{"notes": "A"})
			case "generic conditional":
				err = repo.UpdateMeetingFieldsIfMatch(ctx, "owner", meetingID, map[string]any{"notes": "A"}, map[string]any{"notes": "A"})
			case "text CAS":
				err = repo.UpdateMeetingNotesIfMatch(ctx, "owner", meetingID, "A", "A")
			case "version CAS":
				initial := "initial"
				_, err = repo.UpdateMeetingNotesWithRevision(ctx, "owner", meetingID, "A", "A", &initial)
			case "whole item":
				err = repo.UpdateMeeting(ctx, &model.Meeting{UserID: "owner", MeetingID: meetingID, Notes: "A"})
			case "revision response":
				_, err = repo.UpdateMeetingFieldsWithNotesRevision(ctx, "owner", meetingID, map[string]any{"notes": "A"})
			}
			if err == nil || errors.Is(err, ErrConditionFailed) {
				t.Errorf("ambiguous committed response must remain an error for readback, got %v", err)
			}
			if attempts != 1 || firstVersion == "" || fenceVersion == "" || fenceVersion == firstVersion || currentVersion != fenceVersion {
				t.Errorf("SDK replayed or invalidated the fence: attempts=%d first=%q fence=%q current=%q",
					attempts, firstVersion, fenceVersion, currentVersion)
			}
			probing = true
			_, err = repo.UpdateMeetingNotesWithRevision(ctx, "owner", meetingID, "A", "B", &firstVersion)
			if !errors.Is(err, ErrConditionFailed) || currentText != "A" || currentVersion != fenceVersion {
				t.Fatalf("stale B survived acknowledged fence: err=%v text=%q version=%q", err, currentText, currentVersion)
			}
		})
	}
}

func TestUnrelatedMeetingUpdatesRetainConfiguredRetries(t *testing.T) {
	attempts := 0
	client := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{},
		Retryer: retry.NewStandard(func(o *retry.StandardOptions) {
			o.MaxAttempts = 2
			o.Backoff = retry.BackoffDelayerFunc(func(int, error) (time.Duration, error) { return 0, nil })
		}),
		HTTPClient: meetingListHTTPClientFunc(func(*http.Request) (*http.Response, error) {
			attempts++
			status, body := 200, `{}`
			if attempts == 1 {
				status, body = 500, `{"__type":"InternalServerError"}`
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
				Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
	})
	err := NewDynamoDBRepository(client, "notes-retry").UpdateMeetingFields(context.Background(), "owner", "m", map[string]any{"title": "Title"})
	if err != nil || attempts != 2 {
		t.Fatalf("unrelated write retry policy changed: attempts=%d err=%v", attempts, err)
	}
}
