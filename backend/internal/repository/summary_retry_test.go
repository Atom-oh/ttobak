package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestSummaryRetryClaimCountsAndReleaseTerminatesFinalFailure(t *testing.T) {
	for _, attempts := range []int{1, MaxSummaryRetries} {
		calls := 0
		repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
			calls++
			var body map[string]any
			json.NewDecoder(req.Body).Decode(&body)
			wire, _ := json.Marshal(body)
			if !strings.Contains(analysisCondition(t, body), "owned-claim") {
				t.Fatal("release is not owner scoped")
			}
			terminal := strings.Contains(string(wire), "RETRY_EXHAUSTED")
			if terminal && attempts < MaxSummaryRetries {
				return summaryHTTPResponse(400, `{"__type":"ConditionalCheckFailedException"}`, nil), nil
			}
			if terminal != (attempts == MaxSummaryRetries) {
				t.Fatal("attempt limit ignored")
			}
			if terminal && !strings.Contains(string(wire), `"S":"error"`) {
				t.Fatal("exhaustion is not visible")
			}
			return summaryHTTPResponse(200, `{}`, nil), nil
		})
		if err := repo.ReleaseSummaryRetryClaim(context.Background(), "owner", "m", "owned-claim"); err != nil {
			t.Fatal(err)
		}
		if calls != 3-attempts {
			t.Fatal(calls)
		}
	}
	repo := actionAnalysisWire(t, func(_ string, body map[string]any) {
		if !strings.Contains(body["UpdateExpression"].(string), "ADD") || !strings.Contains(analysisCondition(t, body), "summaryRetryAttempts") {
			t.Fatal("claim is not durably bounded")
		}
	}, 200, `{}`)
	if claim, err := repo.ClaimSummaryRetry(context.Background(), "owner", "m"); err != nil || claim == "" {
		t.Fatal(claim, err)
	}
}

func TestSummaryCheckLimitIsNotSourceConflict(t *testing.T) {
	repo := actionAnalysisWire(t, func(string, map[string]any) { t.Fatal("deterministic limit reached SDK") }, 200, `{}`)
	s := &model.SummarySnapshot{Meeting: &model.Meeting{UserID: "owner", MeetingID: "m"}, Checks: make([]model.SummaryCheck, 101)}
	s.Checks[0] = model.SummaryCheck{PK: "USER#owner", SK: "MEETING#m", Exists: true}
	if err := repo.SaveMeetingSummary(context.Background(), s, "summary", ""); !errors.Is(err, ErrSummaryLimit) || errors.Is(err, ErrConditionFailed) {
		t.Fatal(err)
	}
}

func TestSummaryRetryBusyDoesNotAcknowledgeOrSpendAttempts(t *testing.T) {
	for _, state := range []string{"summarizing", "done"} {
		writes, reads := 0, 0
		repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".GetItem") {
				reads++
				return summaryHTTPResponse(200, `{"Item":{"userId":{"S":"owner"},"meetingId":{"S":"m"},"status":{"S":"`+state+`"},"summaryRetryPending":{"BOOL":true},"summaryRetryAttempts":{"N":"1"},"transcriptA":{"S":"s3://bucket/transcripts/m/transcriptA.txt"}}}`, nil), nil
			}
			writes++
			return summaryHTTPResponse(400, `{"__type":"ConditionalCheckFailedException"}`, nil), nil
		})
		claim, err := repo.ClaimSummaryRetry(context.Background(), "owner", "m")
		if claim != "" || writes != 2 || reads != 1 || (state == "summarizing") != errors.Is(err, ErrSummaryRetryBusy) {
			t.Fatalf("claim=%q writes=%d reads=%d error=%v", claim, writes, reads, err)
		}
	}
}
