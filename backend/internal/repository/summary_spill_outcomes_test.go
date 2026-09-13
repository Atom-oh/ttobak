package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/smithy-go"
	"github.com/ttobak/backend/internal/model"
)

func TestSummarySpillsAreImmutableAndRetainedAfterAmbiguousCommit(t *testing.T) {
	exerciseSummarySpillOutcomes(t, func(repo *DynamoDBRepository, snapshot *model.SummarySnapshot, _ time.Time) error {
		return repo.publishMeetingSummary(context.Background(), snapshot, strings.Repeat("n", 60*1024), "coverage")
	})
}

func exerciseSummarySpillOutcomes(t *testing.T, publish func(*DynamoDBRepository, *model.SummarySnapshot, time.Time) error) {
	for _, tc := range []struct {
		name          string
		status        int
		errorType     string
		reasons       []string
		deletes       int
		conditional   bool
		cleanupFailed bool
	}{
		{"success", 200, "", nil, 0, false, false},
		{"condition", 400, "TransactionCanceledException", []string{"ConditionalCheckFailed", "None"}, 1, true, false},
		{"validation", 400, "ValidationException", nil, 1, false, false},
		{"cancelled_validation", 400, "TransactionCanceledException", []string{"None", "ValidationError"}, 1, false, false},
		{"cancelled_mixed", 400, "TransactionCanceledException", []string{"ConditionalCheckFailed", "ValidationError"}, 1, false, false},
		{"cancelled_without_reasons", 400, "TransactionCanceledException", nil, 1, false, false},
		{"in_progress", 400, "TransactionInProgressException", nil, 0, false, false},
		{"ambiguous", 500, "InternalServerError", nil, 0, false, false},
		{"cleanup", 400, "TransactionCanceledException", []string{"ConditionalCheckFailed", "None"}, 1, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			puts, deletes, transactions := 0, 0, 0
			key := ""
			repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
				if strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".TransactWriteItems") {
					transactions++
					var body map[string]any
					json.NewDecoder(req.Body).Decode(&body)
					update := body["TransactItems"].([]any)[0].(map[string]any)["Update"].(map[string]any)
					raw, _ := json.Marshal(update["ExpressionAttributeValues"])
					if !strings.Contains(string(raw), "s3://bucket/"+strings.TrimPrefix(key, "/bucket/")) {
						t.Fatal("immutable ref not atomically published")
					}
					response := map[string]any{}
					if tc.errorType != "" {
						response["__type"] = tc.errorType
					}
					if len(tc.reasons) > 0 {
						var reasons []map[string]string
						for _, code := range tc.reasons {
							reasons = append(reasons, map[string]string{"Code": code})
						}
						response["CancellationReasons"] = reasons
					}
					payload, _ := json.Marshal(response)
					return summaryHTTPResponse(tc.status, string(payload), nil), nil
				}
				switch req.Method {
				case "PUT":
					puts++
					key = req.URL.Path
					if !regexp.MustCompile(`^/bucket/transcripts/meeting/transcriptA\.[a-f0-9]{32}\.txt$`).MatchString(key) {
						t.Fatalf("mutable spill: %s", key)
					}
					body, _ := io.ReadAll(req.Body)
					if len(body) != 290*1024 {
						t.Fatal("spill content changed")
					}
				case "DELETE":
					deletes++
					if req.URL.Path != key {
						t.Fatal("deleted an existing referenced object")
					}
					if tc.cleanupFailed {
						return summaryHTTPResponse(403, `<Error><Code>AccessDenied</Code></Error>`, nil), nil
					}
				default:
					t.Fatalf("unexpected request %s", req.Method)
				}
				return summaryHTTPResponse(200, "", http.Header{}), nil
			})
			text := strings.Repeat("x", 290*1024)
			now := time.Now().UTC()
			snapshot := &model.SummarySnapshot{Meeting: &model.Meeting{UserID: "owner", MeetingID: "meeting"},
				Stored: map[string]interface{}{"content": "old", "transcriptA": text},
				Checks: []model.SummaryCheck{{PK: "USER#owner", SK: "MEETING#meeting", Exists: true, Fields: map[string]model.SummaryValue{"content": {Present: true, Value: "old"}, "transcriptA": {Present: true, Value: text}}}}}
			err := publish(repo, snapshot, now)
			if (err == nil) != (tc.errorType == "") {
				t.Fatal(err)
			}
			if errors.Is(err, ErrConditionFailed) != tc.conditional {
				t.Fatal(err)
			}
			if tc.errorType != "" && !tc.conditional {
				var apiErr smithy.APIError
				if !errors.As(err, &apiErr) || apiErr.ErrorCode() != tc.errorType {
					t.Fatal("non-condition service error was hidden", err)
				}
			}
			if tc.cleanupFailed {
				var apiErr smithy.APIError
				if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "AccessDenied" {
					t.Fatal("SDK cleanup failure was lost", err)
				}
			}
			if puts != 1 || transactions != 1 || deletes != tc.deletes {
				t.Fatalf("puts=%d tx=%d deletes=%d", puts, transactions, deletes)
			}
			if snapshot.Stored["transcriptA"] != text {
				t.Fatal("snapshot mutated")
			}
		})
	}
}
