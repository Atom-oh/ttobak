package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// ---- mocks ----

type runsCall struct {
	op, a, b, c string
	n           int
}

type mockRunsRepo struct {
	pending      []model.UpdatePendingMark
	listCalls    int
	clearErr     error
	run          map[string]*model.UpdateRun
	createErr    error
	rows         map[string]*model.RunRows
	readErr      error
	items        map[string]*model.UpdateItem // by urlHash (single user)
	days         map[string]*model.UpdateDay
	decisions    map[string]*model.UpdateDecision
	drafts       map[string]*model.UpdateDraft
	putDraftErr  error
	deleteRunErr error
	// incomplete makes ListEligibleItems report a window short of a full cycle.
	incomplete bool
	cursor     string
	cursorErr  error // returned by both cursor calls
	calls      []runsCall
	now        func() time.Time
	// failOnce makes the next call of an op return the error (a crash point).
	failOnce map[string]error
	// before runs once, at the entry of the next call of an op (interleaving).
	before map[string]func()
}

func newMockRunsRepo() *mockRunsRepo {
	return &mockRunsRepo{
		run: map[string]*model.UpdateRun{}, rows: map[string]*model.RunRows{}, items: map[string]*model.UpdateItem{},
		days: map[string]*model.UpdateDay{}, decisions: map[string]*model.UpdateDecision{}, drafts: map[string]*model.UpdateDraft{},
		failOnce: map[string]error{}, before: map[string]func(){}, now: func() time.Time { return runsNow },
	}
}

func (m *mockRunsRepo) record(op string, a, b, c string, n int) {
	m.calls = append(m.calls, runsCall{op, a, b, c, n})
}

// hook runs a pending interleaving and returns a pending crash for op.
func (m *mockRunsRepo) hook(op string) error {
	if fn := m.before[op]; fn != nil {
		delete(m.before, op)
		fn()
	}
	if err := m.failOnce[op]; err != nil {
		delete(m.failOnce, op)
		return err
	}
	return nil
}

func (m *mockRunsRepo) count(op string) int {
	n := 0
	for _, c := range m.calls {
		if c.op == op {
			n++
		}
	}
	return n
}

func (m *mockRunsRepo) ListPendingUsers(_ context.Context, limit int) ([]model.UpdatePendingMark, error) {
	m.listCalls++
	if len(m.pending) > limit {
		return m.pending[:limit], nil
	}
	return m.pending, nil
}
func (m *mockRunsRepo) GetPendingCursor(context.Context) (string, error) {
	return m.cursor, m.cursorErr
}
func (m *mockRunsRepo) SetPendingCursor(_ context.Context, last string) error {
	m.record("SetPendingCursor", last, "", "", 0)
	if m.cursorErr != nil {
		return m.cursorErr
	}
	m.cursor = last
	return nil
}
func (m *mockRunsRepo) ClearPending(_ context.Context, mark model.UpdatePendingMark) error {
	if err := m.hook("ClearPending"); err != nil {
		return err
	}
	m.record("ClearPending", mark.UserID, "", "", 0)
	return m.clearErr
}
func (m *mockRunsRepo) GetRun(_ context.Context, userID string) (*model.UpdateRun, error) {
	return m.run[userID], nil
}
func (m *mockRunsRepo) CreateRunIfAbsent(_ context.Context, userID string, run *model.UpdateRun) error {
	m.record("CreateRun", userID, run.RunID, "", 0)
	if m.createErr != nil {
		return m.createErr
	}
	if m.run[userID] != nil {
		return repository.ErrConditionFailed
	}
	c := *run
	m.run[userID] = &c
	return nil
}
func (m *mockRunsRepo) DeleteRunIfMatch(_ context.Context, userID, runID string) error {
	if err := m.hook("DeleteRun"); err != nil {
		return err
	}
	m.record("DeleteRun", userID, runID, "", 0)
	if m.deleteRunErr != nil {
		return m.deleteRunErr
	}
	if r := m.run[userID]; r == nil || r.RunID != runID {
		return repository.ErrConditionFailed
	}
	delete(m.run, userID)
	return nil
}
func (m *mockRunsRepo) ReadRunRows(_ context.Context, runID string) (*model.RunRows, error) {
	m.record("ReadRunRows", runID, "", "", 0)
	if m.readErr != nil {
		return nil, m.readErr
	}
	if r := m.rows[runID]; r != nil {
		c := *r
		return &c, nil
	}
	return &model.RunRows{}, nil
}
func (m *mockRunsRepo) DeleteRunRows(_ context.Context, runID string) error {
	m.record("DeleteRunRows", runID, "", "", 0)
	return nil
}
func (m *mockRunsRepo) ListEligibleItems(_ context.Context, _ string, limit int) ([]model.UpdateItem, int, bool, error) {
	out, held := []model.UpdateItem{}, 0
	for _, it := range m.items {
		switch {
		case it.State != model.UpdateItemNew:
		case it.HeldUntil.After(m.now()):
			held++
		default:
			out = append(out, *it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URLHash < out[j].URLHash })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, held, !m.incomplete, nil
}
func (m *mockRunsRepo) SetItemHold(_ context.Context, _, urlHash string, until time.Time) error {
	m.record("SetItemHold", urlHash, until.UTC().Format(time.RFC3339), "", 0)
	it := m.items[urlHash]
	if it == nil {
		return repository.ErrConditionFailed
	}
	it.HeldUntil = until
	return nil
}
func (m *mockRunsRepo) GetItem(_ context.Context, _, urlHash string) (*model.UpdateItem, error) {
	if it := m.items[urlHash]; it != nil {
		c := *it
		return &c, nil
	}
	return nil, nil
}
func (m *mockRunsRepo) SetItemState(_ context.Context, _, urlHash, state string, delta int) error {
	if err := m.hook("SetItemState"); err != nil {
		return err
	}
	m.record("SetItemState", urlHash, state, "", delta)
	it := m.items[urlHash]
	if it == nil {
		return repository.ErrConditionFailed
	}
	it.State = state
	it.Attempts += delta
	if state != model.UpdateItemNew {
		it.HeldUntil = time.Time{}
	}
	return nil
}
func (m *mockRunsRepo) GetDay(_ context.Context, _, day string) (*model.UpdateDay, error) {
	m.record("GetDay", day, "", "", 0)
	if d := m.days[day]; d != nil {
		c := model.UpdateDay{Total: d.Total, Researched: d.Researched, PerAccount: map[string]int{}}
		for k, v := range d.PerAccount {
			c.PerAccount[k] = v
		}
		return &c, nil
	}
	return &model.UpdateDay{PerAccount: map[string]int{}}, nil
}
func (m *mockRunsRepo) AddDayDraft(_ context.Context, _, day, accountID string, maxTotal, maxPerAccount int) error {
	if err := m.hook("AddDayDraft"); err != nil {
		return err
	}
	m.record("AddDayDraft", day, accountID, "", 0)
	d := m.days[day]
	if d == nil {
		d = &model.UpdateDay{PerAccount: map[string]int{}}
		m.days[day] = d
	}
	if d.Total >= maxTotal || d.PerAccount[accountID] >= maxPerAccount {
		return repository.ErrConditionFailed
	}
	d.Total++
	d.PerAccount[accountID]++
	return nil
}
func (m *mockRunsRepo) AddDayResearched(_ context.Context, _, day string, n int) error {
	m.record("AddDayResearched", day, "", "", n)
	d := m.days[day]
	if d == nil {
		d = &model.UpdateDay{PerAccount: map[string]int{}}
		m.days[day] = d
	}
	d.Researched += n
	return nil
}
func (m *mockRunsRepo) PutDecisionIfAbsent(_ context.Context, _ string, d *model.UpdateDecision) error {
	m.record("PutDecision", d.URLHash, d.AccountID, d.Outcome, 0)
	key := d.URLHash + "#" + d.AccountID
	if m.decisions[key] != nil {
		return repository.ErrConditionFailed
	}
	c := *d
	m.decisions[key] = &c
	return nil
}
func (m *mockRunsRepo) ReplaceOpenDecision(_ context.Context, _ string, d *model.UpdateDecision) error {
	if err := m.hook("ReplaceDecision"); err != nil {
		return err
	}
	m.record("ReplaceDecision", d.URLHash, d.AccountID, d.Outcome, 0)
	key := d.URLHash + "#" + d.AccountID
	if s := m.decisions[key]; s == nil || finalDecision(s.Choice, s.Outcome) {
		return repository.ErrConditionFailed
	}
	c := *d
	m.decisions[key] = &c
	return nil
}
func (m *mockRunsRepo) GetDecision(_ context.Context, _, urlHash, accountID string) (*model.UpdateDecision, error) {
	if d := m.decisions[urlHash+"#"+accountID]; d != nil {
		c := *d
		return &c, nil
	}
	return nil, nil
}

// ListDecidedAccounts mirrors the repository filter: not_needed, or an
// outcome of drafted or discarded.
func (m *mockRunsRepo) ListDecidedAccounts(_ context.Context, _, urlHash string) ([]string, error) {
	m.record("ListDecidedAccounts", urlHash, "", "", 0)
	out := []string{}
	for _, d := range m.decisions {
		if d.URLHash == urlHash && (d.Choice == model.UpdateChoiceNotNeeded ||
			d.Outcome == model.UpdateOutcomeDrafted || d.Outcome == model.UpdateOutcomeDiscarded) {
			out = append(out, d.AccountID)
		}
	}
	return out, nil
}
func (m *mockRunsRepo) SetDecisionOutcome(_ context.Context, _, urlHash, accountID, outcome, reason string) error {
	if err := m.hook("SetDecisionOutcome"); err != nil {
		return err
	}
	m.record("SetDecisionOutcome", urlHash, accountID, outcome+"/"+reason, 0)
	d := m.decisions[urlHash+"#"+accountID]
	if d == nil {
		return repository.ErrConditionFailed
	}
	d.Outcome, d.DiscardReason = outcome, reason
	return nil
}
func (m *mockRunsRepo) GetDraft(_ context.Context, _, accountID, urlHash string) (*model.UpdateDraft, error) {
	if d := m.drafts[accountID+"#"+urlHash]; d != nil {
		c := *d
		return &c, nil
	}
	return nil, nil
}
func (m *mockRunsRepo) PutDraftIfAbsent(_ context.Context, _ string, d *model.UpdateDraft) error {
	if err := m.hook("PutDraft"); err != nil {
		return err
	}
	m.record("PutDraft", d.AccountID, d.URLHash, "", 0)
	if m.putDraftErr != nil {
		return m.putDraftErr
	}
	key := d.AccountID + "#" + d.URLHash
	if m.drafts[key] != nil {
		return repository.ErrConditionFailed
	}
	c := *d
	m.drafts[key] = &c
	return nil
}

type mockRunsContext struct {
	packs     []AccountPack
	scope     map[string]bool
	block     map[string][]string
	opps      map[string][]AccountPackOpportunity
	dayCounts map[string]int
	builds    int
	failed    int
	reads     map[string]int // per-account finalize reads
}

// BuildWithStats drops packs whose account is at today's cap (counting them
// Capped), as CustomerUpdateContext does.
func (m *mockRunsContext) BuildWithStats(_ context.Context, _ string, dayCounts map[string]int) ([]AccountPack, BuildStats, error) {
	m.builds++
	m.dayCounts = dayCounts
	stats := BuildStats{Failed: m.failed}
	out := []AccountPack{}
	for _, p := range m.packs {
		left := customerUpdateDailyDrafts - dayCounts[p.AccountID]
		if left <= 0 {
			stats.Capped++
			stats.CappedAccounts = append(stats.CappedAccounts, p.AccountID)
			continue
		}
		p.DraftsRemaining = left
		out = append(out, p)
	}
	return out, stats, nil
}
func (m *mockRunsContext) read(accountID string) {
	if m.reads == nil {
		m.reads = map[string]int{}
	}
	m.reads[accountID]++
}
func (m *mockRunsContext) InScope(_ context.Context, _, accountID string) (bool, error) {
	m.read(accountID)
	return m.scope[accountID], nil
}
func (m *mockRunsContext) BlockList(_ context.Context, _, accountID string) ([]string, error) {
	m.read(accountID)
	return m.block[accountID], nil
}
func (m *mockRunsContext) OpenOpportunities(_ context.Context, _, accountID string) ([]AccountPackOpportunity, error) {
	m.read(accountID)
	return m.opps[accountID], nil
}

type invocation struct {
	runID   string
	payload []byte
}

type mockInvoker struct {
	calls []invocation
	err   error
}

func (m *mockInvoker) Invoke(_ context.Context, runID string, payload []byte) error {
	m.calls = append(m.calls, invocation{runID, append([]byte(nil), payload...)})
	return m.err
}

// ---- fixtures ----

var runsNow = time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC) // 12:00 KST

const runsDay = "20261114"

func runsItem(t *testing.T, path string) *model.UpdateItem {
	t.Helper()
	u, h, err := model.NormalizeUpdateURL("https://example.com/news/" + path)
	if err != nil {
		t.Fatal(err)
	}
	return &model.UpdateItem{URLHash: h, URL: u, State: model.UpdateItemNew, FirstSeenAt: runsNow}
}

func runsPack(id string) AccountPack {
	return AccountPack{
		AccountID: id, Name: "계정 " + id, Services: []string{"Amazon Bedrock"},
		Opportunities: []AccountPackOpportunity{{ProjectID: "p-" + id, Name: "추천 고도화", Stage: "Technical Validation"}},
		News:          []AccountPackNews{{Title: "소식", Summary: "요약"}},
		Insights:      []AccountPackInsight{{Type: model.InsightNeed, Text: "관심"}},
		Notes:         []string{"메모"}, BlockList: []string{"블루웨이브"}, DraftsRemaining: 3,
	}
}

type runsFixture struct {
	repo *mockRunsRepo
	ctx  *mockRunsContext
	inv  *mockInvoker
	runs *CustomerUpdateRuns
	now  time.Time
}

func newRunsFixture() *runsFixture {
	f := &runsFixture{repo: newMockRunsRepo(), ctx: &mockRunsContext{
		scope: map[string]bool{}, block: map[string][]string{}, opps: map[string][]AccountPackOpportunity{},
	}, inv: &mockInvoker{}, now: runsNow}
	f.repo.pending = []model.UpdatePendingMark{{UserID: "u1", MarkedAt: "m1"}}
	// A pack by default, so a start after finalize in the same tick invokes
	// instead of skipping the remaining new items.
	f.ctx.packs = []AccountPack{runsPack("acct-1")}
	f.repo.now = func() time.Time { return f.now }
	f.runs = NewCustomerUpdateRuns(CustomerUpdateRunsDeps{Repo: f.repo, Context: f.ctx, Invoker: f.inv}, func() time.Time { return f.now })
	return f
}

func (f *runsFixture) tick(t *testing.T) RunStats {
	t.Helper()
	stats, err := f.runs.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return stats
}

const runsRunID = "0123456789abcdef0123456789abcdef"

func goodBody(url string) string {
	return "안녕하세요. 관련 발표를 공유드립니다: " + url + " 편하실 때 미팅 한번 잡아도 될까요?"
}

// doneRun installs a lease and a done run with one needed/drafted pair per
// (item, account) given.
func (f *runsFixture) doneRun(items []*model.UpdateItem, accounts []string) *model.RunRows {
	run := &model.UpdateRun{RunID: runsRunID, StartedAt: f.now.Add(-10 * time.Minute), LeaseUntil: f.now.Add(20 * time.Minute)}
	rows := &model.RunRows{Done: true, FinishedAt: f.now}
	for _, it := range items {
		f.repo.items[it.URLHash] = it
		run.ItemHashes = append(run.ItemHashes, it.URLHash)
		rows.Items = append(rows.Items, model.RunItemRow{URLHash: it.URLHash, State: "decided"})
		for _, a := range accounts {
			rows.Decisions = append(rows.Decisions, model.RunDecisionRow{URLHash: it.URLHash, AccountID: a, Choice: model.UpdateChoiceNeeded,
				Score: 0.9, Backend: "luna", Outcome: model.UpdateOutcomeDrafted, Reason: "관련",
				GateInput: &model.UpdateGateInput{Question: "q", Options: []model.UpdateGateOption{{ID: "a", Text: "t"}}}})
			rows.Drafts = append(rows.Drafts, model.RunDraftRow{AccountID: a, URLHash: it.URLHash, SourceURL: it.URL,
				SourceTitle: "  발표\n 제목 ", Why: "고객이 관심 있는 주제와 관련된 발표입니다.", Body: goodBody(it.URL),
				Links: []string{it.URL}, OpportunityIDs: []string{"p-" + a, "unknown"}, FetchedURLs: []string{}})
		}
	}
	for _, a := range accounts {
		run.AccountIDs = append(run.AccountIDs, a)
		f.ctx.scope[a] = true
		f.ctx.block[a] = []string{"블루웨이브"}
		f.ctx.opps[a] = []AccountPackOpportunity{{ProjectID: "p-" + a, Name: "추천 고도화", Stage: "Technical Validation"}}
	}
	f.repo.run["u1"] = run
	f.repo.rows[runsRunID] = rows
	return rows
}

// ---- start ----

func TestUpdateRuns_RuntimeOffDoesNothing(t *testing.T) {
	f := newRunsFixture()
	f.runs = NewCustomerUpdateRuns(CustomerUpdateRunsDeps{Repo: f.repo, Context: f.ctx}, func() time.Time { return f.now })
	f.repo.items["x"] = runsItem(t, "a")
	stats := f.tick(t)
	if stats != (RunStats{}) || f.repo.listCalls != 0 || len(f.repo.calls) != 0 || f.ctx.builds != 0 {
		t.Fatalf("stats=%+v list=%d calls=%v", stats, f.repo.listCalls, f.repo.calls)
	}
}

func TestUpdateRuns_StartBuildsContractPayload(t *testing.T) {
	f := newRunsFixture()
	a, b := runsItem(t, "a"), runsItem(t, "b")
	f.repo.items[a.URLHash], f.repo.items[b.URLHash] = a, b
	f.repo.days[runsDay] = &model.UpdateDay{Total: 5, PerAccount: map[string]int{"acct-1": 1}}
	f.ctx.packs = []AccountPack{runsPack("acct-1"), runsPack("acct-2")}
	stats := f.tick(t)
	if stats.Started != 1 || stats.Users != 1 || len(f.inv.calls) != 1 {
		t.Fatalf("stats=%+v invokes=%d", stats, len(f.inv.calls))
	}
	if f.ctx.dayCounts["acct-1"] != 1 {
		t.Fatalf("dayCounts = %v", f.ctx.dayCounts)
	}
	call := f.inv.calls[0]
	if !updateRunIDPattern.MatchString(call.runID) {
		t.Fatalf("runId = %q", call.runID)
	}
	lease := f.repo.run["u1"]
	if lease == nil || lease.RunID != call.runID || !lease.LeaseUntil.Equal(runsNow.Add(30*time.Minute)) ||
		len(lease.ItemHashes) != 2 || !reflect.DeepEqual(lease.AccountIDs, []string{"acct-1", "acct-2"}) {
		t.Fatalf("lease = %+v", lease)
	}
	var payload map[string]any
	if err := json.Unmarshal(call.payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["runId"] != call.runID {
		t.Fatalf("payload runId = %v", payload["runId"])
	}
	budgets := payload["budgets"].(map[string]any)
	if budgets["maxDrafts"] != float64(15) || budgets["maxResearchPairs"] != float64(10) {
		t.Fatalf("budgets = %v", budgets)
	}
	if strings.Contains(string(call.payload), `<`) || strings.HasSuffix(string(call.payload), "\n") {
		t.Fatal("payload must be marshalled without HTML escaping or a trailing newline")
	}
	assertSameKeys(t, "$", payload, runContractExample(t))
	if f.repo.count("SetItemState") != 0 || f.repo.count("ClearPending") != 0 {
		t.Fatalf("start must not change items or clear pending: %v", f.repo.calls)
	}
	t.Logf("payload: %s", call.payload)
}

func runContractExample(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "customer-updates", "run-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Example map[string]any `json:"example"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Example
}

// assertSameKeys compares object key sets at every level; arrays compare
// each element against the example's first element.
func assertSameKeys(t *testing.T, path string, got, want any) {
	t.Helper()
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			t.Fatalf("%s: got %T, want object", path, got)
		}
		gk, wk := sortedKeys(g), sortedKeys(w)
		if !reflect.DeepEqual(gk, wk) {
			t.Fatalf("%s: keys %v, want %v", path, gk, wk)
		}
		for k := range w {
			assertSameKeys(t, path+"."+k, g[k], w[k])
		}
	case []any:
		g, ok := got.([]any)
		if !ok {
			t.Fatalf("%s: got %T, want array", path, got)
		}
		if len(w) == 0 {
			return
		}
		for i, e := range g {
			assertSameKeys(t, fmt.Sprintf("%s[%d]", path, i), e, w[0])
		}
	}
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestUpdateRuns_FreshRunIDPerAttempt(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	f.repo.items[a.URLHash] = a
	f.ctx.packs = []AccountPack{runsPack("acct-1")}
	f.inv.err = &RunNotAcceptedError{Code: "BUSY"}
	f.tick(t)
	f.tick(t)
	if len(f.inv.calls) != 2 || f.inv.calls[0].runID == f.inv.calls[1].runID {
		t.Fatalf("calls = %d, ids reused", len(f.inv.calls))
	}
}

func httpStatusErr(code int) error {
	return &awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: code}}, Err: errors.New("synthetic")}}
}

func TestUpdateRuns_InvokeFailures(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		keepLease    bool
		wantAttempts int
	}{
		{"busy", &RunNotAcceptedError{Code: "BUSY"}, false, 0},
		{"unavailable", &RunNotAcceptedError{Code: "UNAVAILABLE"}, false, 0},
		{"contract rejection", &RunNotAcceptedError{Code: "item_url", Contract: true}, false, 1},
		{"run exists", &RunNotAcceptedError{Code: "RUN_EXISTS"}, true, 0},
		{"transport", errors.New("transport"), true, 0},
		{"deadline", fmt.Errorf("invoke: %w", context.DeadlineExceeded), true, 0},
		{"unknown answer", fmt.Errorf("%w: body", ErrRunOutcomeUnknown), true, 0},
		{"api access denied", &smithy.GenericAPIError{Code: "AccessDeniedException"}, false, 0},
		{"api throttling", fmt.Errorf("invoke: %w", &smithy.GenericAPIError{Code: "ThrottlingException"}), false, 0},
		{"api validation", &smithy.GenericAPIError{Code: "ValidationException"}, false, 0},
		{"api server fault", &smithy.GenericAPIError{Code: "InternalServerException", Fault: smithy.FaultServer}, true, 0},
		{"http 503", httpStatusErr(503), true, 0},
		{"http 403", httpStatusErr(403), false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newRunsFixture()
			a := runsItem(t, "a")
			f.repo.items[a.URLHash] = a
			f.inv.err = c.err
			stats := f.tick(t)
			if stats.Started != 0 || (f.repo.run["u1"] != nil) != c.keepLease || a.Attempts != c.wantAttempts || a.State != model.UpdateItemNew {
				t.Fatalf("stats=%+v lease=%v item=%+v", stats, f.repo.run["u1"], a)
			}
			if c.keepLease != (stats.StartAmbiguous == 1) || c.keepLease == (stats.StartFailed == 1) {
				t.Fatalf("stats = %+v", stats)
			}
		})
	}
}

func TestUpdateRuns_AmbiguousStartFinalizesOrExpires(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	f.repo.items[a.URLHash] = a
	f.inv.err = context.DeadlineExceeded
	f.tick(t)
	lease := f.repo.run["u1"]
	if lease == nil {
		t.Fatal("an ambiguous invoke must keep the lease")
	}
	// The run did start: its DONE rows are finalized normally.
	f.repo.rows[lease.RunID] = &model.RunRows{Done: true, Items: []model.RunItemRow{{URLHash: a.URLHash, State: "decided"}}}
	if stats := f.tick(t); stats.Finalized != 1 || a.State != model.UpdateItemDecided || a.Attempts != 0 {
		t.Fatalf("stats=%+v item=%+v", stats, a)
	}

	// The run never started: the lease expires after 30 minutes with one attempt.
	f = newRunsFixture()
	b := runsItem(t, "b")
	f.repo.items[b.URLHash] = b
	f.inv.err = context.DeadlineExceeded
	f.tick(t)
	f.inv.err = nil
	if stats := f.tick(t); stats.Expired != 0 || len(f.inv.calls) != 1 {
		t.Fatalf("a live lease must block a second start: %+v", stats)
	}
	f.now = runsNow.Add(31 * time.Minute)
	if stats := f.tick(t); stats.Expired != 1 || b.Attempts != 1 || len(f.inv.calls) != 2 {
		t.Fatalf("stats=%+v item=%+v invokes=%d", stats, b, len(f.inv.calls))
	}
}

func TestUpdateRuns_ContractRejectionIsBounded(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	f.repo.items[a.URLHash] = a
	f.inv.err = &RunNotAcceptedError{Code: "item_url", Contract: true}
	for i := 0; i < 5; i++ {
		f.tick(t)
	}
	if len(f.inv.calls) != 3 || a.State != model.UpdateItemSkipped || a.Attempts != 3 {
		t.Fatalf("invokes=%d item=%+v", len(f.inv.calls), a)
	}
}

func TestUpdateRuns_CreateConflictSkipsInvoke(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	f.repo.items[a.URLHash] = a
	f.ctx.packs = []AccountPack{runsPack("acct-1")}
	f.repo.createErr = fmt.Errorf("%w: exists", repository.ErrConditionFailed)
	stats := f.tick(t)
	if len(f.inv.calls) != 0 || stats.Started != 0 {
		t.Fatalf("stats=%+v invokes=%d", stats, len(f.inv.calls))
	}
}

func TestUpdateRuns_NoItemsClearsPending(t *testing.T) {
	f := newRunsFixture()
	f.repo.clearErr = fmt.Errorf("%w: re-marked", repository.ErrConditionFailed)
	if _, err := f.runs.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.repo.count("ClearPending") != 1 || f.ctx.builds != 0 {
		t.Fatalf("calls = %v", f.repo.calls)
	}
}

func TestUpdateRuns_DayFullSkipsUser(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	f.repo.items[a.URLHash] = a
	f.repo.days[runsDay] = &model.UpdateDay{Total: 20}
	f.ctx.packs = []AccountPack{runsPack("acct-1")}
	f.tick(t)
	if f.ctx.builds != 0 || len(f.inv.calls) != 0 || f.repo.count("ClearPending") != 0 {
		t.Fatalf("calls = %v", f.repo.calls)
	}
}

func TestUpdateRuns_NoPackSkipsItems(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	a.Attempts = 1
	f.repo.items[a.URLHash] = a
	f.ctx.packs = nil
	f.tick(t)
	if a.State != model.UpdateItemSkipped || a.Attempts != 1 || len(f.inv.calls) != 0 {
		t.Fatalf("item = %+v", a)
	}
}

func TestUpdateRuns_PayloadOverLimitDropsTailAccounts(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	f.repo.items[a.URLHash] = a
	f.ctx.packs = []AccountPack{runsPack("acct-1"), runsPack("acct-2"), runsPack("acct-3")}
	one, err := marshalRunPayload(runPayload{Version: 1, RunID: runsRunID, Items: []runPayloadItem{{URLHash: a.URLHash, URL: a.URL}},
		Accounts: f.ctx.packs[:2], Budgets: runPayloadBudgets{MaxResearchPairs: 10, MaxDrafts: 20}})
	if err != nil {
		t.Fatal(err)
	}
	f.runs.maxPayloadBytes = len(one)
	f.tick(t)
	if len(f.inv.calls) != 1 || len(f.inv.calls[0].payload) > len(one) {
		t.Fatalf("calls = %d", len(f.inv.calls))
	}
	if got := f.repo.run["u1"].AccountIDs; !reflect.DeepEqual(got, []string{"acct-1", "acct-2"}) {
		t.Fatalf("accounts = %v", got)
	}
}

func TestUpdateRuns_OversizedAccountDropped(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	f.repo.items[a.URLHash] = a
	big := runsPack("acct-2")
	big.Notes = []string{strings.Repeat("가", 3000)}
	f.ctx.packs = []AccountPack{runsPack("acct-1"), big}
	f.tick(t)
	if got := f.repo.run["u1"].AccountIDs; !reflect.DeepEqual(got, []string{"acct-1"}) {
		t.Fatalf("accounts = %v", got)
	}
}

func TestUpdateRuns_LowTimeStartsNothing(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	f.repo.items[a.URLHash] = a
	f.ctx.packs = []AccountPack{runsPack("acct-1")}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(30*time.Second))
	defer cancel()
	stats, err := f.runs.Tick(ctx)
	if err != nil || len(f.inv.calls) != 0 || stats.Started != 0 {
		t.Fatalf("stats=%+v err=%v invokes=%d", stats, err, len(f.inv.calls))
	}
}

func TestUpdateRuns_AtMost25Users(t *testing.T) {
	f := newRunsFixture()
	f.repo.pending = nil
	for i := 0; i < 30; i++ {
		f.repo.pending = append(f.repo.pending, model.UpdatePendingMark{UserID: fmt.Sprintf("u%02d", i)})
	}
	stats := f.tick(t)
	if stats.Users != 25 {
		t.Fatalf("users = %d", stats.Users)
	}
}

func TestUpdateRuns_KSTDay(t *testing.T) {
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 11, 14, 14, 59, 0, 0, time.UTC), "20261114"}, // 23:59 KST
		{time.Date(2026, 11, 14, 15, 0, 0, 0, time.UTC), "20261115"},  // 00:00 KST
	}
	for _, c := range cases {
		if got := kstDay(c.at); got != c.want {
			t.Errorf("kstDay(%v) = %s, want %s", c.at, got, c.want)
		}
	}
}

// ---- finalize ----

func TestUpdateRuns_FinalizeWritesValidDraft(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	rows := f.doneRun([]*model.UpdateItem{a}, []string{"acct-1"})
	rows.Drafts[0].SourceURL = "https://evil.example/other"
	stats := f.tick(t)
	if stats.Finalized != 1 || stats.Drafts != 1 || stats.Discarded != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	d := f.repo.drafts["acct-1#"+a.URLHash]
	if d == nil {
		t.Fatal("draft not written")
	}
	if d.SourceURL != a.URL || d.SourceTitle != "발표 제목" || d.Status != model.UpdateDraftNew || d.Revision != 0 || d.RunID != runsRunID ||
		!d.CreatedAt.Equal(runsNow) || d.ExpiresAt != runsNow.Add(model.UpdateDraftTTL).Unix() {
		t.Fatalf("draft = %+v", d)
	}
	if len(d.Opportunities) != 1 || d.Opportunities[0].ProjectID != "p-acct-1" {
		t.Fatalf("opportunities = %+v", d.Opportunities)
	}
	if day := f.repo.days[runsDay]; day == nil || day.Total != 1 || day.PerAccount["acct-1"] != 1 {
		t.Fatalf("day = %+v", f.repo.days[runsDay])
	}
	dec := f.repo.decisions[a.URLHash+"#acct-1"]
	if dec == nil || dec.RunID != runsRunID || dec.Outcome != model.UpdateOutcomeDrafted || dec.GateInput == nil ||
		dec.ExpiresAt != runsNow.Add(model.UpdateDecisionTTL).Unix() {
		t.Fatalf("decision = %+v", dec)
	}
	if a.State != model.UpdateItemDecided || a.Attempts != 0 || f.repo.run["u1"] != nil || f.repo.count("DeleteRunRows") != 1 {
		t.Fatalf("item=%+v lease=%v", a, f.repo.run["u1"])
	}
}

func TestUpdateRuns_FinalizeRejections(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(f *runsFixture, rows *model.RunRows, it *model.UpdateItem)
		reason string
	}{
		{"figure", func(_ *runsFixture, rows *model.RunRows, it *model.UpdateItem) {
			rows.Drafts[0].Body = "비용을 30% 줄였습니다: " + it.URL + " 미팅 가능하실까요?"
		}, "figure"},
		{"link outside allowed", func(_ *runsFixture, rows *model.RunRows, _ *model.UpdateItem) {
			rows.Drafts[0].Links = append(rows.Drafts[0].Links, "https://other.example/x")
		}, "links_invalid"},
		{"too many links", func(_ *runsFixture, rows *model.RunRows, it *model.UpdateItem) {
			rows.Drafts[0].FetchedURLs = []string{"https://a.example/1", "https://a.example/2", "https://a.example/3", "https://a.example/4", "https://a.example/5"}
			rows.Drafts[0].Links = append(rows.Drafts[0].FetchedURLs, it.URL)
		}, "links_invalid"},
		{"why stage word", func(_ *runsFixture, rows *model.RunRows, _ *model.UpdateItem) {
			rows.Drafts[0].Why = "Technical Validation 단계라서 관련 있습니다."
		}, "why_invalid"},
		{"why empty", func(_ *runsFixture, rows *model.RunRows, _ *model.UpdateItem) { rows.Drafts[0].Why = "  " }, "why_invalid"},
		{"why too long", func(_ *runsFixture, rows *model.RunRows, _ *model.UpdateItem) {
			rows.Drafts[0].Why = strings.Repeat("가", 401)
		}, "why_invalid"},
		{"blocked name and stage", func(_ *runsFixture, rows *model.RunRows, it *model.UpdateItem) {
			rows.Drafts[0].Body = "블루웨이브 Technical Validation " + it.URL + " 미팅"
		}, "blocked_name,stage_word"},
		{"encoding", func(_ *runsFixture, rows *model.RunRows, _ *model.UpdateItem) { rows.Drafts[0].Body = "\xff" }, "encoding"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newRunsFixture()
			a := runsItem(t, "a")
			rows := f.doneRun([]*model.UpdateItem{a}, []string{"acct-1"})
			c.mutate(f, rows, a)
			stats := f.tick(t)
			if stats.Drafts != 0 || stats.Discarded != 1 || len(f.repo.drafts) != 0 {
				t.Fatalf("stats = %+v drafts=%d", stats, len(f.repo.drafts))
			}
			dec := f.repo.decisions[a.URLHash+"#acct-1"]
			if dec.Outcome != model.UpdateOutcomeDiscarded || dec.DiscardReason != c.reason {
				t.Fatalf("decision = %s/%s, want discarded/%s", dec.Outcome, dec.DiscardReason, c.reason)
			}
			if f.repo.days[runsDay] != nil && f.repo.days[runsDay].Total != 0 {
				t.Fatal("a rejected draft consumed a day slot")
			}
		})
	}
}

func TestUpdateRuns_DraftNeedsDraftedDecision(t *testing.T) {
	for _, name := range []string{"discarded", "missing", "not_needed"} {
		t.Run(name, func(t *testing.T) {
			f := newRunsFixture()
			a := runsItem(t, "a")
			rows := f.doneRun([]*model.UpdateItem{a}, []string{"acct-1"})
			switch name {
			case "discarded":
				rows.Decisions[0].Outcome = model.UpdateOutcomeDiscarded
				rows.Decisions[0].DiscardReason = "figure"
			case "missing":
				rows.Decisions = nil
			case "not_needed":
				rows.Decisions[0].Choice = model.UpdateChoiceNotNeeded
				rows.Decisions[0].GateInput = nil
			}
			stats := f.tick(t)
			if len(f.repo.drafts) != 0 || stats.Drafts != 0 || stats.Ignored != 1 {
				t.Fatalf("stats = %+v", stats)
			}
		})
	}
}

func TestUpdateRuns_DecisionOutsideLeaseIgnored(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	rows := f.doneRun([]*model.UpdateItem{a}, []string{"acct-1"})
	rows.Decisions = append(rows.Decisions,
		model.RunDecisionRow{URLHash: strings.Repeat("e", 64), AccountID: "acct-1", Choice: model.UpdateChoiceNotNeeded, Backend: "luna"},
		model.RunDecisionRow{URLHash: a.URLHash, AccountID: "acct-zz", Choice: model.UpdateChoiceNotNeeded, Backend: "luna"})
	rows.Drafts = append(rows.Drafts, model.RunDraftRow{AccountID: "acct-zz", URLHash: a.URLHash, Body: goodBody(a.URL), Links: []string{a.URL}, Why: "w"})
	f.ctx.scope["acct-zz"] = true
	stats := f.tick(t)
	if stats.Ignored != 3 || stats.Drafts != 1 || len(f.repo.decisions) != 1 || len(f.repo.drafts) != 1 {
		t.Fatalf("stats = %+v decisions=%d", stats, len(f.repo.decisions))
	}
}

func TestUpdateRuns_DayBudgets(t *testing.T) {
	t.Run("21st draft of the day", func(t *testing.T) {
		f := newRunsFixture()
		a := runsItem(t, "a")
		f.doneRun([]*model.UpdateItem{a}, []string{"acct-1"})
		f.repo.days[runsDay] = &model.UpdateDay{Total: 20, PerAccount: map[string]int{}}
		stats := f.tick(t)
		dec := f.repo.decisions[a.URLHash+"#acct-1"]
		if stats.Deferred != 1 || len(f.repo.drafts) != 0 || dec.Outcome != model.UpdateOutcomeDeferred || dec.DiscardReason != "day_budget" {
			t.Fatalf("stats=%+v decision=%+v", stats, dec)
		}
		if a.State != model.UpdateItemNew || a.Attempts != 0 {
			t.Fatalf("item = %+v", a)
		}
	})
	t.Run("4th draft for one account", func(t *testing.T) {
		f := newRunsFixture()
		a := runsItem(t, "a")
		f.doneRun([]*model.UpdateItem{a}, []string{"acct-1"})
		f.repo.days[runsDay] = &model.UpdateDay{Total: 3, PerAccount: map[string]int{"acct-1": 3}}
		stats := f.tick(t)
		dec := f.repo.decisions[a.URLHash+"#acct-1"]
		if stats.Deferred != 1 || dec.DiscardReason != "account_day_budget" || a.State != model.UpdateItemNew {
			t.Fatalf("stats=%+v decision=%+v item=%+v", stats, dec, a)
		}
	})
	t.Run("deferred pair drafts on a later run", func(t *testing.T) {
		f := newRunsFixture()
		a := runsItem(t, "a")
		f.doneRun([]*model.UpdateItem{a}, []string{"acct-1"})
		f.repo.decisions[a.URLHash+"#acct-1"] = &model.UpdateDecision{URLHash: a.URLHash, AccountID: "acct-1",
			Choice: model.UpdateChoiceNeeded, RunID: strings.Repeat("9", 32), Outcome: model.UpdateOutcomeDeferred, DiscardReason: "day_budget"}
		stats := f.tick(t)
		dec := f.repo.decisions[a.URLHash+"#acct-1"]
		if stats.Drafts != 1 || dec.Outcome != model.UpdateOutcomeDrafted || dec.DiscardReason != "" {
			t.Fatalf("stats=%+v decision=%+v", stats, dec)
		}
	})
	t.Run("pair decided earlier is not drafted again", func(t *testing.T) {
		f := newRunsFixture()
		a := runsItem(t, "a")
		f.doneRun([]*model.UpdateItem{a}, []string{"acct-1"})
		f.repo.decisions[a.URLHash+"#acct-1"] = &model.UpdateDecision{URLHash: a.URLHash, AccountID: "acct-1",
			Choice: model.UpdateChoiceNotNeeded, RunID: strings.Repeat("9", 32)}
		stats := f.tick(t)
		if stats.Drafts != 0 || len(f.repo.drafts) != 0 {
			t.Fatalf("stats=%+v", stats)
		}
	})
}

func TestUpdateRuns_RefinalizeIsIdempotent(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	f.doneRun([]*model.UpdateItem{a}, []string{"acct-1"})
	// The first finalize dies after writing drafts but before deleting the lease.
	f.repo.deleteRunErr = errors.New("crash")
	if _, err := f.runs.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.repo.drafts) != 1 || f.repo.run["u1"] == nil || a.Attempts != 0 || a.State != model.UpdateItemNew {
		t.Fatalf("after crash: drafts=%d lease=%v item=%+v", len(f.repo.drafts), f.repo.run["u1"], a)
	}
	f.repo.deleteRunErr = nil
	stats := f.tick(t)
	if len(f.repo.drafts) != 1 || f.repo.count("PutDraft") != 1 || f.repo.days[runsDay].Total != 1 || stats.Drafts != 0 {
		t.Fatalf("second finalize: stats=%+v puts=%d day=%+v", stats, f.repo.count("PutDraft"), f.repo.days[runsDay])
	}
	if a.State != model.UpdateItemDecided || a.Attempts != 0 || stats.Finalized != 1 {
		t.Fatalf("item=%+v stats=%+v", a, stats)
	}
}

func TestUpdateRuns_RefinalizeDeferredDoesNotAddAttemptsTwice(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	rows := f.doneRun([]*model.UpdateItem{a}, []string{"acct-1"})
	rows.Items[0].State, rows.Items[0].Reason = "deferred", "budget"
	rows.Decisions, rows.Drafts = nil, nil
	f.repo.deleteRunErr = errors.New("crash")
	f.tick(t)
	f.repo.deleteRunErr = nil
	f.tick(t)
	if a.Attempts != 1 || a.State != model.UpdateItemNew {
		t.Fatalf("item = %+v", a)
	}
}

func TestUpdateRuns_DeleteRunConflictStops(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	f.doneRun([]*model.UpdateItem{a}, []string{"acct-1"})
	f.repo.deleteRunErr = fmt.Errorf("%w: changed", repository.ErrConditionFailed)
	stats := f.tick(t)
	if f.repo.count("SetItemState") != 0 || stats.Finalized != 0 || len(f.inv.calls) != 0 {
		t.Fatalf("stats=%+v calls=%v", stats, f.repo.calls)
	}
}

func TestUpdateRuns_ExpiredLease(t *testing.T) {
	f := newRunsFixture()
	a, b := runsItem(t, "a"), runsItem(t, "b")
	a.Attempts, b.Attempts = 0, 2
	f.repo.items[a.URLHash], f.repo.items[b.URLHash] = a, b
	f.repo.run["u1"] = &model.UpdateRun{RunID: runsRunID, LeaseUntil: runsNow.Add(-time.Minute), ItemHashes: []string{a.URLHash, b.URLHash}, AccountIDs: []string{"acct-1"}}
	f.repo.rows[runsRunID] = &model.RunRows{Done: false}
	stats := f.tick(t)
	if stats.Expired != 1 || a.Attempts != 1 || a.State != model.UpdateItemNew || b.Attempts != 3 || b.State != model.UpdateItemSkipped {
		t.Fatalf("stats=%+v a=%+v b=%+v", stats, a, b)
	}
	// The expired lease is gone; item a starts a fresh run in the same tick.
	if l := f.repo.run["u1"]; l == nil || l.RunID == runsRunID || f.repo.count("DeleteRunRows") != 1 || stats.Started != 1 {
		t.Fatalf("lease=%+v stats=%+v", l, stats)
	}
}

func TestUpdateRuns_LiveLeaseSkipsUser(t *testing.T) {
	f := newRunsFixture()
	f.repo.run["u1"] = &model.UpdateRun{RunID: runsRunID, LeaseUntil: runsNow.Add(time.Minute)}
	stats := f.tick(t)
	if stats.Expired != 0 || stats.Finalized != 0 || len(f.inv.calls) != 0 || f.repo.count("DeleteRun") != 0 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestUpdateRuns_ReadErrorKeepsLease(t *testing.T) {
	f := newRunsFixture()
	f.repo.run["u1"] = &model.UpdateRun{RunID: runsRunID, LeaseUntil: runsNow.Add(-time.Minute)}
	f.repo.readErr = repository.ErrUpdateDraftEncryptionUnavailable
	stats, err := f.runs.Tick(context.Background())
	if err != nil || stats.Errors != 1 || f.repo.run["u1"] == nil || stats.Expired != 0 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}

func TestUpdateRuns_OversizedPartitionWaitsForExpiry(t *testing.T) {
	f := newRunsFixture()
	f.repo.readErr = repository.ErrUpdateRunTooLarge
	f.repo.run["u1"] = &model.UpdateRun{RunID: runsRunID, LeaseUntil: runsNow.Add(time.Minute)}
	if stats := f.tick(t); stats.Errors != 0 || stats.Finalized != 0 || f.repo.run["u1"] == nil {
		t.Fatalf("live lease: stats=%+v", stats)
	}
	f.now = runsNow.Add(2 * time.Minute)
	if stats := f.tick(t); stats.Expired != 1 || stats.Finalized != 0 {
		t.Fatalf("expired lease: stats=%+v", stats)
	}
}

func TestUpdateRuns_ItemRows(t *testing.T) {
	f := newRunsFixture()
	a, b, c, d := runsItem(t, "a"), runsItem(t, "b"), runsItem(t, "c"), runsItem(t, "d")
	c.Attempts = 2
	rows := f.doneRun([]*model.UpdateItem{a, b, c, d}, nil)
	rows.Items = []model.RunItemRow{
		{URLHash: a.URLHash, State: "decided"},
		{URLHash: b.URLHash, State: "skipped", Reason: "insufficient"},
		{URLHash: c.URLHash, State: "deferred", Reason: "gate_error"},
		{URLHash: strings.Repeat("f", 64), State: "decided"},
	}
	f.repo.run["u1"].AccountIDs = []string{"acct-1"}
	stats := f.tick(t)
	if a.State != model.UpdateItemDecided || b.State != model.UpdateItemSkipped || c.State != model.UpdateItemSkipped || c.Attempts != 3 ||
		d.State != model.UpdateItemNew || d.Attempts != 1 || stats.Ignored != 1 {
		t.Fatalf("a=%+v b=%+v c=%+v d=%+v stats=%+v", a, b, c, d, stats)
	}
}

func TestUpdateRuns_FinalizeThenStartSameTick(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	f.doneRun([]*model.UpdateItem{a}, []string{"acct-1"})
	b := runsItem(t, "b")
	f.repo.items[b.URLHash] = b
	f.ctx.packs = []AccountPack{runsPack("acct-1")}
	stats := f.tick(t)
	if stats.Finalized != 1 || stats.Started != 1 || len(f.inv.calls) != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if hs := f.repo.run["u1"].ItemHashes; len(hs) != 1 || hs[0] != b.URLHash {
		t.Fatalf("new lease items = %v", hs)
	}
}

func TestUpdateRuns_CountsRowFaults(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	rows := f.doneRun([]*model.UpdateItem{a}, []string{"acct-1"})
	rows.Malformed, rows.Unreadable = 2, 1
	stats := f.tick(t)
	if stats.Malformed != 2 || stats.Unreadable != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}
