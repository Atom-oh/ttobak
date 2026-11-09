package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagent"
	"github.com/ttobak/backend/internal/model"
)

// indexJobHistory serves ListIngestionJobs over HTTP from a chronological
// status history (index 0 is the oldest job), the way the provider sees it.
type indexJobHistory struct {
	t        *testing.T
	statuses []string
	// brokenFilter answers filtered requests with unfiltered jobs;
	// emptyFilter answers them with nothing.
	brokenFilter, emptyFilter bool
	fail                      bool
	recent, filtered, walked  int
	failed                    int
}

// The service's page size when maxResults is omitted, as observed with the CLI.
const indexServiceDefaultPage = 25

func (h *indexJobHistory) requests() int { return h.recent + h.filtered + h.walked + h.failed }

func (h *indexJobHistory) serve(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost || req.URL.Path != "/knowledgebases/KB12345678/datasources/DS12345678/ingestionjobs/" {
		h.t.Fatalf("unexpected provider call: %s %s", req.Method, req.URL)
	}
	var body struct {
		Filters []struct {
			Attribute, Operator string
			Values              []string
		}
		MaxResults *int
		NextToken  string
		SortBy     *struct{ Attribute, Order string }
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		h.t.Fatal(err)
	}
	if h.fail {
		h.failed++
		return indexHTTPResponse(500, `{"message":"unavailable"}`, map[string]string{"Content-Type": "application/json", "X-Amzn-Errortype": "InternalServerException"}), nil
	}
	type summary struct {
		Status          string `json:"status"`
		IngestionJobID  string `json:"ingestionJobId"`
		KnowledgeBaseID string `json:"knowledgeBaseId"`
		DataSourceID    string `json:"dataSourceId"`
	}
	// Positions into the chronological history, in reply order.
	jobs := make([]int, len(h.statuses))
	for i := range jobs {
		jobs[i] = i
	}
	switch {
	case body.SortBy != nil:
		if body.SortBy.Attribute != "STARTED_AT" || body.SortBy.Order != "DESCENDING" || len(body.Filters) != 0 || body.NextToken != "" {
			h.t.Fatalf("unexpected sorted request: %+v", body)
		}
		h.recent++
		for i := range jobs {
			jobs[i] = len(jobs) - 1 - i
		}
	case len(body.Filters) != 0:
		h.filtered++
		filter := body.Filters[0]
		if len(body.Filters) != 1 || filter.Attribute != "STATUS" || filter.Operator != "EQ" || len(filter.Values) != 1 {
			h.t.Fatalf("unsupported filter: %+v", body.Filters)
		}
		if h.emptyFilter {
			jobs = nil
		} else if !h.brokenFilter {
			matched := []int{}
			for _, i := range jobs {
				if h.statuses[i] == filter.Values[0] {
					matched = append(matched, i)
				}
			}
			jobs = matched
		}
	default:
		h.walked++
	}
	size := indexServiceDefaultPage
	if body.MaxResults != nil {
		size = *body.MaxResults
	}
	start := 0
	if body.NextToken != "" {
		start, _ = strconv.Atoi(body.NextToken)
	}
	end := min(start+size, len(jobs))
	page := []summary{}
	for _, i := range jobs[start:end] {
		page = append(page, summary{h.statuses[i], fmt.Sprintf("JOB%07d", i), "KB12345678", "DS12345678"})
	}
	out := map[string]interface{}{"ingestionJobSummaries": page}
	if end < len(jobs) && body.SortBy == nil {
		out["nextToken"] = strconv.Itoa(end)
	}
	raw, _ := json.Marshal(out)
	return indexHTTPResponse(200, string(raw), map[string]string{"Content-Type": "application/json"}), nil
}

func newIndexJobHistory(t *testing.T, statuses ...string) (*indexJobHistory, *IndexAWSProvider, *time.Time) {
	t.Helper()
	history := &indexJobHistory{t: t, statuses: statuses}
	provider := indexHTTPProvider(t, history.serve)
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	provider.now = func() time.Time { return now }
	return history, provider, &now
}

func completedIndexJobs(n int) []string {
	statuses := make([]string, n)
	for i := range statuses {
		statuses[i] = "COMPLETE"
	}
	return statuses
}

// legacyIndexBusy is the Busy implementation this change replaced, kept to
// measure the request count of the old idle check against the same history.
func legacyIndexBusy(ctx context.Context, p *IndexAWSProvider) (bool, error) {
	pages := bedrockagent.NewListIngestionJobsPaginator(p.bedrock, &bedrockagent.ListIngestionJobsInput{
		KnowledgeBaseId: aws.String(p.kbID), DataSourceId: aws.String(p.dataSourceID)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return false, err
		}
		for _, job := range page.IngestionJobSummaries {
			if active, err := indexIngestionActive(job.Status); err != nil || active {
				return active, err
			}
		}
	}
	return false, nil
}

// Production on 2026-11-09 had 24,236 COMPLETE jobs and no active one.
const indexProductionJobHistory = 24236

func TestIndexBusyIdleRequestCounts(t *testing.T) {
	ctx := context.Background()
	history, provider, now := newIndexJobHistory(t, completedIndexJobs(indexProductionJobHistory)...)
	if busy, err := legacyIndexBusy(ctx, provider); busy || err != nil || history.requests() != 970 {
		t.Fatalf("legacy idle check: busy=%v err=%v requests=%d", busy, err, history.requests())
	}
	steps := []struct {
		name                     string
		advance                  time.Duration
		recent, filtered, walked int
	}{
		{"first check audits the full history", 0, 1, 3, 25},
		{"checks inside the audit interval stay constant", time.Minute, 1, 3, 0},
		{"just before the interval", indexJobAuditInterval - 2*time.Minute, 1, 3, 0},
		{"the periodic audit runs again", time.Minute, 1, 3, 25},
		{"a clock moved backwards audits instead of trusting the record", -2 * indexJobAuditInterval, 1, 3, 25},
		{"after the next interval", 3 * indexJobAuditInterval, 1, 3, 25},
	}
	for _, step := range steps {
		*now = now.Add(step.advance)
		history.recent, history.filtered, history.walked = 0, 0, 0
		busy, err := provider.Busy(ctx)
		if busy || err != nil || history.recent != step.recent || history.filtered != step.filtered || history.walked != step.walked {
			t.Fatalf("%s: busy=%v err=%v requests=%d/%d/%d", step.name, busy, err, history.recent, history.filtered, history.walked)
		}
	}
}

func TestIndexBusyDetectsActiveAndFailsClosed(t *testing.T) {
	old := func(status string) []string {
		statuses := completedIndexJobs(500)
		statuses[3] = status // older than the newest-jobs page
		return statuses
	}
	cases := []struct {
		name            string
		statuses        []string
		brokenFilter    bool
		emptyFilter     bool
		fail            bool
		busy            bool
		err             error
		requests        int
		auditsAgainNext bool
	}{
		{"recent active job", append(completedIndexJobs(500), "IN_PROGRESS"), false, false, false, true, nil, 1, false},
		{"recent unknown status", append(completedIndexJobs(500), "QUEUED"), false, false, false, false, ErrIndexInvalid, 1, false},
		{"old starting job found by filter", old("STARTING"), false, false, false, true, nil, 2, false},
		{"old stopping job found by filter", old("STOPPING"), false, false, false, true, nil, 4, false},
		{"filter not applied", completedIndexJobs(500), true, false, false, false, ErrIndexInvalid, 2, false},
		{"filter misses an active job; audit catches it", old("IN_PROGRESS"), false, true, false, true, nil, 5, true},
		{"old unknown status caught by audit", old("QUEUED"), false, false, false, false, ErrIndexInvalid, 5, true},
		{"provider unavailable", completedIndexJobs(10), false, false, true, false, nil, 1, false},
		{"empty history", nil, false, false, false, false, nil, 5, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			history, provider, _ := newIndexJobHistory(t, tc.statuses...)
			history.brokenFilter, history.emptyFilter, history.fail = tc.brokenFilter, tc.emptyFilter, tc.fail
			busy, err := provider.Busy(context.Background())
			if tc.fail {
				if err == nil || errors.Is(err, ErrIndexInvalid) {
					t.Fatalf("provider failure hidden: %v", err)
				}
			} else if !errors.Is(err, tc.err) || (tc.err == nil && err != nil) {
				t.Fatalf("error = %v, want %v", err, tc.err)
			}
			if busy != tc.busy || history.requests() != tc.requests {
				t.Fatalf("busy=%v requests=%d", busy, history.requests())
			}
			// Only a walk that saw every job as terminal satisfies the audit.
			history.recent, history.filtered, history.walked = 0, 0, 0
			history.fail = false
			_, _ = provider.Busy(context.Background())
			if audited := history.walked > 0; audited != tc.auditsAgainNext && tc.requests == 5 {
				t.Fatalf("audit repeated=%v, want %v", audited, tc.auditsAgainNext)
			}
		})
	}
}

// indexBusyFromProvider keeps the in-memory sync double for submission and
// status, but answers Busy through the real provider and its HTTP history.
type indexBusyFromProvider struct {
	*indexSync
	provider *IndexAWSProvider
}

func (p indexBusyFromProvider) Busy(ctx context.Context) (bool, error) { return p.provider.Busy(ctx) }

func TestIndexTickWithProviderBusyCheck(t *testing.T) {
	cases := []struct {
		name     string
		history  []string
		fail     bool
		lease    bool
		phase    string
		err      bool
		requests int
		queued   bool
	}{
		{"changed source starts a sync", completedIndexJobs(2500), false, false, "RUNNING", false, 1 + 3 + 3, true},
		{"external sync defers reconciliation", append(completedIndexJobs(2500), "IN_PROGRESS"), false, false, "EXTERNAL_SYNC_WAIT", false, 1, false},
		{"provider error fails the tick", completedIndexJobs(10), true, false, "IDLE", true, 1, false},
		{"active lease makes no provider request", completedIndexJobs(10), false, true, "LEASE_WAIT", false, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, _, sync, now := newIndexTest()
			history, provider, _ := newIndexJobHistory(t, tc.history...)
			history.fail = tc.fail
			s.ingestion = indexBusyFromProvider{sync, provider}
			key := addIndexMeeting(repo, "m", "edited notes")
			// The job claims an older revision: only reconciliation can see the edit.
			repo.jobs[key.Hash()] = &model.IndexJob{Resource: key, Version: 3, State: model.IndexIndexed,
				Revision: "old", DesiredRevision: "old"}
			if tc.lease {
				repo.control.Owner, repo.control.LeaseUntil, repo.control.Version = "other", now.Add(time.Minute).UnixMilli(), 1
			}
			before := *repo.control
			result, err := s.Tick(context.Background())
			if (err != nil) != tc.err || result.Phase != tc.phase || history.requests() != tc.requests {
				t.Fatalf("phase=%q err=%v requests=%d", result.Phase, err, history.requests())
			}
			job := repo.jobs[key.Hash()]
			if queued := job.Version != 3; queued != tc.queued {
				t.Fatalf("changed source queued=%v: %+v", queued, job)
			}
			if tc.lease {
				if !reflect.DeepEqual(*repo.control, before) {
					t.Fatalf("lease holder's control changed: %+v", repo.control)
				}
				return
			}
			if repo.control.LeaseUntil != 0 || (!tc.queued && repo.control.Phase != "IDLE") {
				t.Fatalf("lease or phase not restored: %+v", repo.control)
			}
			if tc.queued && !strings.HasPrefix(result.ProviderJobID, "job-") {
				t.Fatalf("sync not submitted: %+v", result)
			}
		})
	}
}
