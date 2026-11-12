package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	sfntypes "github.com/aws/aws-sdk-go-v2/service/sfn/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// Copied from backend/python/crawler/news_manual.py (_ID_RE,
// _SOURCE_SUFFIX_RE and the acct_ prefix rule in parse_request): every
// emitted event must pass the starter's validation.
var (
	workerIDRE     = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	workerSuffixRE = regexp.MustCompile(`^[0-9a-f]{8}$`)
)

func workerAccepts(e model.NewsCollectRequested) bool {
	prefix := "acct_" + e.AccountID + "_"
	return workerIDRE.MatchString(e.AccountID) && workerIDRE.MatchString(e.RequestID) &&
		len(e.SourceID) > len(prefix) && e.SourceID[:len(prefix)] == prefix && workerSuffixRE.MatchString(e.SourceID[len(prefix):])
}

const collectSource = "acct_acc-a_0000abcd"

var collectNow = time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC)

const (
	collectDailyARN       = "arn:aws:states:ap-northeast-2:123456789012:stateMachine:ttobak-news-daily"
	collectPublicationARN = "arn:aws:states:ap-northeast-2:123456789012:stateMachine:ttobak-news-publication"
)

// fakeRunChecker answers Running per state machine ARN and records lookups.
type fakeRunChecker struct {
	running map[string]bool
	err     error
	calls   []string
}

func (c *fakeRunChecker) Running(_ context.Context, arn string) (bool, error) {
	c.calls = append(c.calls, arn)
	return c.running[arn], c.err
}

type collectFixture struct {
	svc       *AccountNewsService
	repo      *multiAccountNewsRepo
	crawler   *newsSourceTestCrawler
	runs      *fakeRunChecker
	published []model.NewsCollectRequested
	pubErr    error
}

// collectAdmission is the production configuration: daily run at 19:10 UTC,
// 45-minute lead (the child timeout) and 15-minute grace.
func collectAdmission(runs NewsRunChecker) *NewsCollectAdmission {
	return NewNewsCollectAdmission("19", "10", "2700", "900", collectDailyARN, collectPublicationARN, runs)
}

// newCollectTest stores accepted claims on the source CONFIG, like the
// repository, so a later read sees them.
func newCollectTest() *collectFixture {
	f := &collectFixture{}
	f.svc, f.repo, f.crawler = newNewsSourcesTest()
	f.svc.now = func() time.Time { return collectNow }
	f.runs = &fakeRunChecker{running: map[string]bool{}}
	f.svc.SetCollectAdmission(collectAdmission(f.runs))
	addAccountNewsSource(f.repo, f.crawler, newsAcctA, collectSource, "Beta news")
	f.svc.SetCollectPublisher(func(_ context.Context, e model.NewsCollectRequested) error {
		f.published = append(f.published, e)
		return f.pubErr
	})
	f.repo.claim = func(sourceID, requestID string, at, cutoff time.Time) error {
		src := f.crawler.sources[sourceID]
		if last, err := time.Parse(time.RFC3339, src.LastCollectRequestedAt); err == nil && last.After(cutoff) {
			return repository.ErrConditionFailed
		}
		src.LastCollectRequestedAt, src.LastCollectRequestID = at.Format(time.RFC3339), requestID
		return nil
	}
	return f
}

func TestCollectNow_PublishesOneContractEvent(t *testing.T) {
	f := newCollectTest()
	resp, err := f.svc.CollectNow(context.Background(), "user-1", newsAcctA, collectSource)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.published) != 1 || len(f.repo.claims) != 1 || len(f.repo.releases) != 0 {
		t.Fatalf("published %+v claims %v", f.published, f.repo.claims)
	}
	event := f.published[0]
	if event.SourceID != collectSource || event.AccountID != newsAcctA || event.RequestID != resp.RequestID || event.RequestID != f.repo.claims[0] || !workerAccepts(event) {
		t.Fatalf("event %+v", event)
	}
	// The worker runs only when the stored claim ID equals the event's.
	if stored := f.crawler.sources[collectSource]; stored.LastCollectRequestID != event.RequestID || stored.LastCollectRequestedAt != resp.RequestedAt {
		t.Fatalf("stored claim %+v", stored)
	}
	raw, _ := json.Marshal(event)
	var keys map[string]any
	if json.Unmarshal(raw, &keys) != nil || len(keys) != 3 {
		t.Fatalf("event must carry only the three IDs: %s", raw)
	}
	if resp.RequestedAt != "2026-11-12T01:00:00Z" || resp.NextAllowedAt != "2026-11-12T02:00:00Z" {
		t.Fatalf("response %+v", resp)
	}

	// Within the hour: 429 with the time it lifts, nothing published.
	f.svc.now = func() time.Time { return collectNow.Add(59 * time.Minute) }
	_, err = f.svc.CollectNow(context.Background(), "user-2", newsAcctA, collectSource)
	var limited *NewsCollectLimitError
	if !errors.As(err, &limited) || !errors.Is(err, ErrNewsCollectTooSoon) || !limited.NextAllowedAt.Equal(collectNow.Add(time.Hour)) {
		t.Fatalf("second request: %v", err)
	}
	if len(f.published) != 1 || len(f.repo.claims) != 1 {
		t.Fatalf("limited request reached the claim or bus: %d %d", len(f.published), len(f.repo.claims))
	}
	// The list shows the same limit.
	listed, err := f.svc.List(context.Background(), "user-1", newsAcctA)
	if err != nil || len(listed) != 1 || listed[0].NextCollectAllowedAt != "2026-11-12T02:00:00Z" {
		t.Fatalf("list: %+v %v", listed, err)
	}
	page, err := f.svc.ListAll(context.Background(), "user-1", "", 0)
	if err != nil || page.Sources[0].NextCollectAllowedAt != "2026-11-12T02:00:00Z" {
		t.Fatalf("directory: %+v %v", page, err)
	}

	// An hour later it succeeds again, and the lists stop showing a limit.
	f.svc.now = func() time.Time { return collectNow.Add(time.Hour) }
	if listed, _ := f.svc.List(context.Background(), "user-1", newsAcctA); listed[0].NextCollectAllowedAt != "" {
		t.Fatalf("expired limit still listed: %+v", listed[0])
	}
	if _, err := f.svc.CollectNow(context.Background(), "user-2", newsAcctA, collectSource); err != nil || len(f.published) != 2 {
		t.Fatalf("after the hour: %v %d", err, len(f.published))
	}
	if f.published[0].RequestID == f.published[1].RequestID {
		t.Fatal("each request needs a fresh ID")
	}
}

func TestCollectNow_ConcurrentClaimIsLimited(t *testing.T) {
	f := newCollectTest()
	// Another member's claim commits between the read and this claim.
	// An eventually consistent read would still return the unclaimed row.
	f.repo.claim = func(sourceID, _ string, at, _ time.Time) error {
		before := *f.crawler.sources[sourceID]
		f.crawler.stale = map[string]*model.CrawlerSource{sourceID: &before}
		f.crawler.sources[sourceID].LastCollectRequestedAt = at.Add(-time.Minute).Format(time.RFC3339)
		return repository.ErrConditionFailed
	}
	_, err := f.svc.CollectNow(context.Background(), "user-1", newsAcctA, collectSource)
	var limited *NewsCollectLimitError
	if !errors.As(err, &limited) || !limited.NextAllowedAt.Equal(collectNow.Add(59*time.Minute)) || len(f.published) != 0 {
		t.Fatalf("concurrent claim: %v %d", err, len(f.published))
	}
	if f.crawler.consistentReads != 1 {
		t.Fatalf("the refused claim must be classified by one consistent read, got %d", f.crawler.consistentReads)
	}
}

func TestCollectNow_Rejections(t *testing.T) {
	for name, tc := range map[string]struct {
		setup  func(*collectFixture)
		source string
		want   error
	}{
		"non-member":      {func(f *collectFixture) { f.repo.members[newsAcctA] = false }, collectSource, ErrForbidden},
		"unknown account": {func(f *collectFixture) { delete(f.repo.accounts, newsAcctA) }, collectSource, ErrForbidden},
		"invalid id":      {nil, "../x", ErrInvalidInput},
		"unknown source":  {func(f *collectFixture) { f.repo.refsBy[newsAcctA] = nil }, collectSource, ErrNotFound},
		"missing config":  {func(f *collectFixture) { delete(f.crawler.sources, collectSource) }, collectSource, ErrNotFound},
		"foreign config":  {func(f *collectFixture) { f.crawler.sources[collectSource].AccountID = newsAcctB }, collectSource, ErrNotFound},
		"disabled":        {func(f *collectFixture) { f.crawler.sources[collectSource].Status = "disabled" }, collectSource, ErrNotFound},
		"operator":        {func(f *collectFixture) { f.crawler.sources[collectSource].GlobalNewsSource = true }, collectSource, ErrNotFound},
		"off-contract id": {func(f *collectFixture) { addAccountNewsSource(f.repo, f.crawler, newsAcctA, "acct_acc-a_legacy", "x") }, "acct_acc-a_legacy", ErrNotFound},
		"other account's": {func(f *collectFixture) {
			addAccountNewsSource(f.repo, f.crawler, newsAcctB, "acct_acc-b_0000abcd", "x")
		}, "acct_acc-b_0000abcd", ErrNotFound},
		"no publisher": {func(f *collectFixture) { f.svc.SetCollectPublisher(nil) }, collectSource, ErrNewsCollectUnavailable},
		"revoked at commit": {func(f *collectFixture) {
			f.repo.claim = func(string, string, time.Time, time.Time) error { return repository.ErrAccountNewsAccess }
		}, collectSource, ErrForbidden},
		"merging at commit": {func(f *collectFixture) {
			f.repo.claim = func(string, string, time.Time, time.Time) error { return repository.ErrAccountMerging }
		}, collectSource, ErrAccountMerging},
		"disabled at commit": {func(f *collectFixture) { f.repo.claim = disableAtCommit(f) }, collectSource, ErrNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			f := newCollectTest()
			if tc.setup != nil {
				tc.setup(f)
			}
			if _, err := f.svc.CollectNow(context.Background(), "user-1", newsAcctA, tc.source); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if len(f.published) != 0 {
				t.Fatalf("rejected request published: %+v", f.published)
			}
		})
	}
}

// Authorization and ownership refusals answer before any admission lookup,
// so a non-member cannot learn the pipeline state.
func TestCollectNow_OwnershipBeforeAdmission(t *testing.T) {
	for name, tc := range map[string]struct {
		setup  func(*collectFixture)
		source string
		want   error
	}{
		"non-member":     {func(f *collectFixture) { f.repo.members[newsAcctA] = false }, collectSource, ErrForbidden},
		"foreign source": {func(f *collectFixture) { f.crawler.sources[collectSource].AccountID = newsAcctB }, collectSource, ErrNotFound},
		"other account's": {func(f *collectFixture) {
			addAccountNewsSource(f.repo, f.crawler, newsAcctB, "acct_acc-b_0000abcd", "x")
		}, "acct_acc-b_0000abcd", ErrNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			f := newCollectTest()
			tc.setup(f)
			// Every admission condition would refuse.
			f.svc.now = func() time.Time { return time.Date(2026, 11, 12, 19, 10, 0, 0, time.UTC) }
			f.runs.running[collectDailyARN], f.runs.err = true, errors.New("lookup failed")
			if _, err := f.svc.CollectNow(context.Background(), "user-1", newsAcctA, tc.source); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if len(f.runs.calls) != 0 || len(f.repo.claims) != 0 || len(f.published) != 0 {
				t.Fatalf("lookups %v claims %v published %v", f.runs.calls, f.repo.claims, f.published)
			}
		})
	}
}

func TestCollectNow_AdmissionRefusals(t *testing.T) {
	window := &NewsCollectWindowError{}
	for name, tc := range map[string]struct {
		now     time.Time
		setup   func(*collectFixture)
		want    error
		next    string // daily-window end, RFC3339
		lookups []string
	}{
		"daily window": {now: time.Date(2026, 11, 12, 19, 0, 0, 0, time.UTC), want: ErrNewsCollectDailyWindow, next: "2026-11-12T19:25:00Z"},
		"daily running": {setup: func(f *collectFixture) { f.runs.running[collectDailyARN] = true },
			want: ErrNewsCollectBusy, lookups: []string{collectDailyARN}},
		"publication running": {setup: func(f *collectFixture) { f.runs.running[collectPublicationARN] = true },
			want: ErrNewsCollectBusy, lookups: []string{collectDailyARN, collectPublicationARN}},
		"lookup error": {setup: func(f *collectFixture) { f.runs.err = errors.New("throttled") },
			want: ErrNewsCollectUnavailable, lookups: []string{collectDailyARN}},
		"no admission": {setup: func(f *collectFixture) { f.svc.SetCollectAdmission(nil) }, want: ErrNewsCollectUnavailable},
		"missing window value": {setup: func(f *collectFixture) {
			f.svc.SetCollectAdmission(NewNewsCollectAdmission("19", "", "2700", "900", collectDailyARN, collectPublicationARN, f.runs))
		}, want: ErrNewsCollectUnavailable},
		"missing arn": {setup: func(f *collectFixture) {
			f.svc.SetCollectAdmission(NewNewsCollectAdmission("19", "10", "2700", "900", "", collectPublicationARN, f.runs))
		}, want: ErrNewsCollectUnavailable},
		"no checker": {setup: func(f *collectFixture) {
			f.svc.SetCollectAdmission(NewNewsCollectAdmission("19", "10", "2700", "900", collectDailyARN, collectPublicationARN, nil))
		}, want: ErrNewsCollectUnavailable},
		// Admission precedes the hourly limit.
		"busy and limited": {setup: func(f *collectFixture) {
			f.runs.running[collectDailyARN] = true
			f.crawler.sources[collectSource].LastCollectRequestedAt = collectNow.Add(-time.Minute).Format(time.RFC3339)
		}, want: ErrNewsCollectBusy, lookups: []string{collectDailyARN}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newCollectTest()
			if !tc.now.IsZero() {
				f.svc.now = func() time.Time { return tc.now }
			}
			if tc.setup != nil {
				tc.setup(f)
			}
			_, err := f.svc.CollectNow(context.Background(), "user-1", newsAcctA, collectSource)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if tc.next != "" {
				if !errors.As(err, &window) || window.NextAllowedAt.Format(time.RFC3339) != tc.next {
					t.Fatalf("window end: %v", err)
				}
			} else if errors.As(err, &window) {
				t.Fatalf("unexpected window error: %v", err)
			}
			if !reflect.DeepEqual(f.runs.calls, tc.lookups) && !(len(f.runs.calls) == 0 && len(tc.lookups) == 0) {
				t.Fatalf("lookups %v, want %v", f.runs.calls, tc.lookups)
			}
			if len(f.repo.claims) != 0 || len(f.repo.releases) != 0 || len(f.published) != 0 {
				t.Fatalf("refusal claimed or published: claims %v releases %v published %v", f.repo.claims, f.repo.releases, f.published)
			}
		})
	}
}

// The window boundaries equal the starter's test_news_manual cases.
func TestNewsDailyWindowBoundaries(t *testing.T) {
	prod, err := parseNewsDailyWindow("19", "10", "2700", "900")
	if err != nil {
		t.Fatal(err)
	}
	wrap, err := parseNewsDailyWindow("0", "10", "2700", "900")
	if err != nil {
		t.Fatal(err)
	}
	day := func(d, h, m, s int) time.Time { return time.Date(2026, 11, d, h, m, s, 0, time.UTC) }
	for name, tc := range map[string]struct {
		w    newsDailyWindow
		now  time.Time
		want string // window end, or "" when allowed
	}{
		"before lead":         {prod, day(12, 18, 24, 59), ""},
		"lead start":          {prod, day(12, 18, 25, 0), "2026-11-12T19:25:00Z"},
		"before schedule":     {prod, day(12, 19, 9, 59), "2026-11-12T19:25:00Z"},
		"schedule":            {prod, day(12, 19, 10, 0), "2026-11-12T19:25:00Z"},
		"last grace second":   {prod, day(12, 19, 24, 59), "2026-11-12T19:25:00Z"},
		"grace end":           {prod, day(12, 19, 25, 0), ""},
		"midday":              {prod, day(12, 7, 10, 0), ""},
		"wrap before":         {wrap, day(11, 23, 24, 59), ""},
		"wrap lead start":     {wrap, day(11, 23, 25, 0), "2026-11-12T00:25:00Z"},
		"wrap after midnight": {wrap, day(12, 0, 0, 0), "2026-11-12T00:25:00Z"},
		"wrap last second":    {wrap, day(12, 0, 24, 59), "2026-11-12T00:25:00Z"},
		"wrap grace end":      {wrap, day(12, 0, 25, 0), ""},
		"wrap noon":           {wrap, day(12, 12, 0, 0), ""},
	} {
		end := tc.w.end(tc.now)
		got := ""
		if !end.IsZero() {
			got = end.Format(time.RFC3339)
		}
		if got != tc.want {
			t.Fatalf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

// Configuration validation matches daily_window's: digits only (1-5),
// hour <= 23, minute <= 59, 0 < lead, grace < 43200.
func TestParseNewsDailyWindow(t *testing.T) {
	for _, bad := range [][4]string{
		{"", "10", "2700", "900"}, {"24", "10", "2700", "900"}, {"19", "60", "2700", "900"},
		{"19", "10", "0", "900"}, {"19", "10", "2700", "0"}, {"19", "10", "43200", "900"},
		{"19", "10", "2700", "43200"}, {"-1", "10", "2700", "900"}, {"19", "10", "+2700", "900"},
		{"19", "10", "123456", "900"}, {"19", "10", " 2700", "900"}, {"19.5", "10", "2700", "900"},
	} {
		if _, err := parseNewsDailyWindow(bad[0], bad[1], bad[2], bad[3]); !errors.Is(err, ErrNewsCollectUnavailable) {
			t.Fatalf("%q accepted", bad)
		}
	}
	if _, err := parseNewsDailyWindow("23", "59", "43199", "1"); err != nil {
		t.Fatal(err)
	}
}

type fakeExecutionLister struct {
	in  *sfn.ListExecutionsInput
	out *sfn.ListExecutionsOutput
	err error
}

func (c *fakeExecutionLister) ListExecutions(_ context.Context, in *sfn.ListExecutionsInput, _ ...func(*sfn.Options)) (*sfn.ListExecutionsOutput, error) {
	c.in = in
	return c.out, c.err
}

func TestSFNRunChecker(t *testing.T) {
	running := &fakeExecutionLister{out: &sfn.ListExecutionsOutput{Executions: []sfntypes.ExecutionListItem{{}}}}
	got, err := NewSFNRunChecker(running).Running(context.Background(), collectDailyARN)
	if err != nil || !got {
		t.Fatalf("running: %v %v", got, err)
	}
	if aws.ToString(running.in.StateMachineArn) != collectDailyARN || running.in.StatusFilter != sfntypes.ExecutionStatusRunning || running.in.MaxResults != 1 {
		t.Fatalf("input %+v", running.in)
	}
	if got, err := NewSFNRunChecker(&fakeExecutionLister{out: &sfn.ListExecutionsOutput{}}).Running(context.Background(), collectDailyARN); err != nil || got {
		t.Fatalf("idle: %v %v", got, err)
	}
	for name, c := range map[string]sfnExecutionLister{
		"error": &fakeExecutionLister{err: errors.New("denied")}, "nil output": &fakeExecutionLister{}, "nil client": nil,
	} {
		if _, err := NewSFNRunChecker(c).Running(context.Background(), collectDailyARN); err == nil {
			t.Fatalf("%s: no error", name)
		}
	}
}

func disableAtCommit(f *collectFixture) func(string, string, time.Time, time.Time) error {
	return func(sourceID, _ string, _, _ time.Time) error {
		f.crawler.sources[sourceID].Status = "disabled"
		return repository.ErrConditionFailed
	}
}

func TestCollectNow_PublishFailureReleasesClaim(t *testing.T) {
	for _, releaseErr := range []error{nil, errors.New("release failed")} {
		f := newCollectTest()
		f.pubErr = errors.New("bus down")
		f.repo.releaseErr = releaseErr
		_, err := f.svc.CollectNow(context.Background(), "user-1", newsAcctA, collectSource)
		if !errors.Is(err, ErrNewsCollectUnavailable) || len(f.repo.claims) != 1 || !reflect.DeepEqual(f.repo.releases, f.repo.claims) {
			t.Fatalf("release %v: err %v claims %v releases %v", releaseErr, err, f.repo.claims, f.repo.releases)
		}
	}
}

func TestNextCollectAllowedAt(t *testing.T) {
	for raw, want := range map[string]string{
		"":                          "",
		"garbage":                   "",
		"2026-11-12T00:00:00Z":      "",
		"2026-11-12T00:00:01Z":      "2026-11-12T01:00:01Z",
		"2026-11-12T09:30:00+09:00": "2026-11-12T01:30:00Z",
	} {
		if got := nextCollectAllowedAt(raw, collectNow); got != want {
			t.Fatalf("%q: got %q, want %q", raw, got, want)
		}
	}
}

type collectEventClient struct {
	in  *eventbridge.PutEventsInput
	out *eventbridge.PutEventsOutput
	err error
}

func (c *collectEventClient) PutEvents(_ context.Context, in *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	c.in = in
	return c.out, c.err
}

func TestNewsCollectPublisher(t *testing.T) {
	event := model.NewsCollectRequested{SourceID: collectSource, AccountID: newsAcctA, RequestID: "6f1c0e8e-1b7a-4e8e-9d55-0d3c2f1e9a10"}
	ok := &eventbridge.PutEventsOutput{Entries: []ebtypes.PutEventsResultEntry{{EventId: aws.String("e1")}}}
	client := &collectEventClient{out: ok}
	if err := NewsCollectPublisher(client)(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	entry := client.in.Entries[0]
	if len(client.in.Entries) != 1 || aws.ToString(entry.EventBusName) != "default" || aws.ToString(entry.Source) != "ttobak.crawler" ||
		aws.ToString(entry.DetailType) != "NewsCollectRequested" ||
		aws.ToString(entry.Detail) != `{"sourceId":"`+collectSource+`","accountId":"acc-a","requestId":"`+event.RequestID+`"}` {
		t.Fatalf("entry %+v", entry)
	}
	for name, c := range map[string]*collectEventClient{
		"error":        {err: errors.New("x")},
		"failed entry": {out: &eventbridge.PutEventsOutput{FailedEntryCount: 1, Entries: []ebtypes.PutEventsResultEntry{{ErrorCode: aws.String("E")}}}},
		"missing id":   {out: &eventbridge.PutEventsOutput{Entries: []ebtypes.PutEventsResultEntry{{}}}},
		"no entries":   {out: &eventbridge.PutEventsOutput{}},
		"nil output":   {},
	} {
		if err := NewsCollectPublisher(c)(context.Background(), event); !errors.Is(err, ErrNewsCollectUnavailable) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if err := NewsCollectPublisher(nil)(context.Background(), event); !errors.Is(err, ErrNewsCollectUnavailable) {
		t.Fatalf("nil client: %v", err)
	}
}

// The API's claim and the worker's start marker are outside the source
// revision, so neither changes which articles are current.
func TestCollectAttributesStayOutsideSourceRevision(t *testing.T) {
	base := map[string]any{
		"PK": "CRAWLER#" + collectSource, "SK": "CONFIG", "sourceId": collectSource, "sourceName": "Beta news",
		"ownerId": "owner", "status": "idle", "accountId": newsAcctA, "newsQueries": []string{"Beta"},
	}
	want, err := newsSourceRevision(newsAV(t, base))
	if err != nil {
		t.Fatal(err)
	}
	marked := map[string]any{}
	for k, v := range base {
		marked[k] = v
	}
	marked["lastCollectRequestedAt"], marked["lastCollectRequestId"] = "2026-11-12T01:00:00Z", "r1"
	marked["lastCollectStartedRequestId"], marked["lastCollectStartedAt"] = "r1", 1794013205
	if got, err := newsSourceRevision(newsAV(t, marked)); err != nil || got != want {
		t.Fatalf("revision changed: %q %q %v", got, want, err)
	}
}

// A listed source whose CONFIG carries the claim lists normally.
func TestCollectClaimedSourceListsNormally(t *testing.T) {
	f := newCollectTest()
	f.crawler.sources[collectSource].LastCollectRequestedAt = "2026-11-11T00:00:00Z"
	f.crawler.sources[collectSource].LastCollectRequestID = "r0"
	listed, err := f.svc.List(context.Background(), "user-1", newsAcctA)
	if err != nil || len(listed) != 1 || listed[0].NextCollectAllowedAt != "" {
		t.Fatalf("list: %+v %v", listed, err)
	}
	raw, _ := json.Marshal(listed[0])
	if strings.Contains(string(raw), "lastCollect") {
		t.Fatalf("claim attributes are internal: %s", raw)
	}
}
