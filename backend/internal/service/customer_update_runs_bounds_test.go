package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/aws/smithy-go"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// ---- payload URLs (contract.py _valid_url) ----

func TestUpdateRuns_RuntimeValidURL(t *testing.T) {
	for u, want := range map[string]bool{
		"https://example.com/a?q=hello":                    true,
		"https://example.com/a?q=hello world":              false,
		"https://example.com/a?q=a b":                      false,
		"https://example.com/a?q=a　b":                      false,
		"https://example.com/a?q=a b":                      false,
		"https://example.com/a?q=a\tb":                     false,
		"https://example.com/a?q=a\x7fb":                   false,
		"https://example.com/a?q=a\x01b":                   false,
		"http://example.com/a":                             false,
		"https:///a":                                       false,
		"https://example.com/" + strings.Repeat("a", 2028): true, // 2,048 code points
		"https://example.com/" + strings.Repeat("a", 2029): false,
	} {
		if got := runtimeValidURL(u); got != want {
			t.Errorf("runtimeValidURL(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestUpdateRuns_RuntimeInvalidItemURLSkipped(t *testing.T) {
	f := newRunsFixture()
	good := runsItem(t, "a")
	u, h, err := model.NormalizeUpdateURL("https://example.com/news/b?q=hello world")
	if err != nil {
		t.Skipf("NormalizeUpdateURL refuses the URL: %v", err)
	}
	bad := &model.UpdateItem{URLHash: h, URL: u, State: model.UpdateItemNew, FirstSeenAt: runsNow}
	f.repo.items[good.URLHash], f.repo.items[bad.URLHash] = good, bad
	f.tick(t)
	if bad.State != model.UpdateItemSkipped || bad.Attempts != 0 || len(f.inv.calls) != 1 || strings.Contains(string(f.inv.calls[0].payload), bad.URLHash) {
		t.Fatalf("bad=%+v invokes=%d", bad, len(f.inv.calls))
	}
}

// ---- backfill bound (R21) ----

func TestUpdateRuns_OldItemsSkippedAtBoundary(t *testing.T) {
	f := newRunsFixture()
	edge, old := runsItem(t, "edge"), runsItem(t, "old")
	edge.FirstSeenAt = runsNow.Add(-72 * time.Hour)
	old.FirstSeenAt = runsNow.Add(-72*time.Hour - time.Second)
	f.repo.items[edge.URLHash], f.repo.items[old.URLHash] = edge, old
	f.tick(t)
	if old.State != model.UpdateItemSkipped || old.Attempts != 0 {
		t.Fatalf("old = %+v", old)
	}
	if len(f.inv.calls) != 1 || !strings.Contains(string(f.inv.calls[0].payload), edge.URLHash) || strings.Contains(string(f.inv.calls[0].payload), old.URLHash) {
		t.Fatalf("invokes = %d", len(f.inv.calls))
	}
}

// ---- research day cap (R19) ----

func payloadBudgets(t *testing.T, payload []byte) runPayloadBudgets {
	t.Helper()
	var p runPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatal(err)
	}
	return p.Budgets
}

func TestUpdateRuns_ResearchCapAtStart(t *testing.T) {
	cases := []struct {
		name       string
		cap, used  int
		wantPairs  int // 0: held, no run
		wantInvoke bool
	}{
		{"plenty left", 30, 0, 10, true},
		{"few left", 30, 27, 3, true},
		{"cap reached", 30, 30, 0, false},
		{"cap zero", 0, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newRunsFixture()
			f.runs.SetResearchPairsPerDay(c.cap)
			a := runsItem(t, "a")
			f.repo.items[a.URLHash] = a
			f.repo.days[runsDay] = &model.UpdateDay{Researched: c.used, PerAccount: map[string]int{}}
			stats := f.tick(t)
			if (len(f.inv.calls) == 1) != c.wantInvoke {
				t.Fatalf("invokes = %d", len(f.inv.calls))
			}
			if c.wantInvoke {
				if got := payloadBudgets(t, f.inv.calls[0].payload).MaxResearchPairs; got != c.wantPairs {
					t.Fatalf("maxResearchPairs = %d, want %d", got, c.wantPairs)
				}
				return
			}
			if stats.Held != 1 || !a.HeldUntil.Equal(nextDayStart()) || a.Attempts != 0 || f.repo.count("ClearPending") != 0 || f.ctx.builds != 0 {
				t.Fatalf("stats=%+v item=%+v", stats, a)
			}
		})
	}
}

func TestUpdateRuns_FinalizeCountsResearchedOnce(t *testing.T) {
	f := newRunsFixture()
	a, b, c := runsItem(t, "a"), runsItem(t, "b"), runsItem(t, "c")
	rows := f.doneRun([]*model.UpdateItem{a, b, c}, []string{"acct-1"})
	rows.Decisions[1].Outcome, rows.Decisions[1].DiscardReason = model.UpdateOutcomeDiscarded, "figure"
	rows.Decisions[2].Outcome, rows.Decisions[2].DiscardReason = model.UpdateOutcomeDeferred, "research_budget"
	rows.Drafts = rows.Drafts[:1]
	// A losing overlapping finalize: the lease delete fails its condition.
	f.repo.failOnce["DeleteRun"] = fmt.Errorf("lost: %w", repository.ErrConditionFailed)
	f.tick(t)
	if f.repo.count("AddDayResearched") != 0 {
		t.Fatal("researched counted without the lease delete")
	}
	f.tick(t)
	if f.repo.days[runsDay].Researched != 2 || f.repo.count("AddDayResearched") != 1 {
		t.Fatalf("day = %+v (want drafted + discarded = 2, once)", f.repo.days[runsDay])
	}
}

// ---- runtime budget deferrals (R18) ----

func TestUpdateRuns_RuntimeBudgetDeferrals(t *testing.T) {
	type pairOutcome struct{ outcome, reason string }
	cases := []struct {
		name      string
		pairs     []pairOutcome // one per account acct-1, acct-2
		itemRow   string        // ITEM row state, "" for no row
		wantHeld  bool
		wantTries int
	}{
		{"account budget holds", []pairOutcome{{"deferred", "account_budget"}}, "deferred", true, 0},
		{"draft budget holds", []pairOutcome{{"deferred", "draft_budget"}}, "deferred", true, 0},
		{"research budget stays new", []pairOutcome{{"deferred", "research_budget"}}, "deferred", false, 0},
		{"hold wins over research budget", []pairOutcome{{"deferred", "research_budget"}, {"deferred", "account_budget"}}, "deferred", true, 0},
		{"research budget wins over gate error", []pairOutcome{{"deferred", "research_budget"}}, "deferred_gate", false, 0},
		{"research budget wins over a missing row", []pairOutcome{{"deferred", "research_budget"}}, "", false, 0},
		{"gate error adds an attempt", []pairOutcome{{"", ""}}, "deferred_gate", false, 1},
		{"missing row adds an attempt", []pairOutcome{{"", ""}}, "", false, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newRunsFixture()
			a := runsItem(t, "a")
			accounts := []string{"acct-1", "acct-2"}[:len(c.pairs)]
			rows := f.doneRun([]*model.UpdateItem{a}, accounts)
			rows.Drafts = nil
			// An undecided account keeps the item in the next start's batch.
			f.ctx.packs = append(f.ctx.packs, runsPack("acct-9"))
			for i, p := range c.pairs {
				rows.Decisions[i].Outcome, rows.Decisions[i].DiscardReason = p.outcome, p.reason
				if p.outcome == "" {
					rows.Decisions[i].Choice = model.UpdateChoiceNotNeeded
				}
			}
			switch c.itemRow {
			case "":
				rows.Items = nil
			case "deferred_gate":
				rows.Items[0] = model.RunItemRow{URLHash: a.URLHash, State: "deferred", Reason: "gate_error"}
			default:
				rows.Items[0] = model.RunItemRow{URLHash: a.URLHash, State: c.itemRow, Reason: "budget"}
			}
			f.tick(t)
			if a.State != model.UpdateItemNew || a.Attempts != c.wantTries || a.HeldUntil.Equal(nextDayStart()) != c.wantHeld {
				t.Fatalf("item = %+v", a)
			}
			if !c.wantHeld && !a.HeldUntil.IsZero() {
				t.Fatalf("unexpected hold: %+v", a)
			}
		})
	}
}

// ---- scope at finalize (M6) ----

func TestUpdateRuns_OutOfScopeDecisionNotStored(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	f.doneRun([]*model.UpdateItem{a}, []string{"acct-1"})
	f.ctx.scope["acct-1"] = false
	stats := f.tick(t)
	if stats.OutOfScope != 1 || len(f.repo.decisions) != 0 || len(f.repo.drafts) != 0 || f.repo.count("PutDecision") != 0 {
		t.Fatalf("stats=%+v decisions=%d", stats, len(f.repo.decisions))
	}
	if f.repo.count("AddDayResearched") != 0 {
		t.Fatal("an out-of-scope pair counted as researched")
	}
}

// ---- pending mark (I4) and rotation (I5) ----

func TestUpdateRuns_MarkClearedOnlyAfterFullCycle(t *testing.T) {
	f := newRunsFixture()
	f.repo.incomplete = true
	f.tick(t)
	if f.repo.count("ClearPending") != 0 {
		t.Fatal("mark cleared after a partial window")
	}
	f.repo.incomplete = false
	f.tick(t)
	if f.repo.count("ClearPending") != 1 {
		t.Fatal("mark kept after a full cycle with nothing new")
	}
}

func pendingIDs(marks []model.UpdatePendingMark) string {
	ids := make([]string, 0, len(marks))
	for _, m := range marks {
		ids = append(ids, m.UserID)
	}
	return strings.Join(ids, ",")
}

func TestUpdateRuns_RotatePending(t *testing.T) {
	marks := []model.UpdatePendingMark{{UserID: "u4"}, {UserID: "u1"}, {UserID: "u3"}, {UserID: "u2"}}
	for _, c := range []struct {
		cursor string
		n      int
		want   string
	}{
		{"", 3, "u1,u2,u3"},       // no cursor: from the first id
		{"u2", 3, "u3,u4,u1"},     // resumes after the cursor and wraps
		{"u4", 2, "u1,u2"},        // cursor at the last id wraps to the first
		{"u25", 2, "u3,u4"},       // cursor id no longer among the marks
		{"u9", 10, "u1,u2,u3,u4"}, // past every id; never more than the marks
	} {
		if got := pendingIDs(rotatePending(marks, c.cursor, c.n)); got != c.want {
			t.Errorf("cursor %q n %d: got %s, want %s", c.cursor, c.n, got, c.want)
		}
	}
	if len(rotatePending(nil, "u1", 25)) != 0 {
		t.Fatal("empty list")
	}
}

func TestUpdateRuns_TickResumesAfterCursor(t *testing.T) {
	f := newRunsFixture()
	f.repo.pending = nil
	for i := 0; i < 30; i++ {
		f.repo.pending = append(f.repo.pending, model.UpdatePendingMark{UserID: fmt.Sprintf("u%02d", i)})
	}
	f.repo.cursor = "u09"
	served := func() map[string]bool {
		out := map[string]bool{}
		for _, c := range f.repo.calls {
			if c.op == "ClearPending" {
				out[c.a] = true
			}
		}
		return out
	}
	stats := f.tick(t)
	got := served()
	if stats.Users != 25 || len(got) != 25 || got["u09"] || !got["u10"] || !got["u29"] || !got["u04"] || got["u05"] {
		t.Fatalf("users=%d served=%v", stats.Users, got)
	}
	if f.repo.cursor != "u04" {
		t.Fatalf("cursor = %q, want the last served u04", f.repo.cursor)
	}

	// A failed cursor read starts at the first mark; a failed write is counted.
	f.repo.calls, f.repo.cursorErr = nil, errors.New("boom")
	stats = f.tick(t)
	if got := served(); !got["u00"] || !got["u24"] || got["u25"] || stats.Errors != 1 {
		t.Fatalf("stats=%+v served=%v", stats, got)
	}
}

func TestUpdateRuns_PartialTickSavesLastServed(t *testing.T) {
	f := newRunsFixture()
	f.repo.pending = []model.UpdatePendingMark{{UserID: "a"}, {UserID: "b"}, {UserID: "c"}}
	// After the first user the remaining time drops below the 60-second floor.
	deadline := time.Now().Add(61 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	f.repo.before["ClearPending"] = func() { time.Sleep(time.Until(deadline.Add(-59 * time.Second))) }
	stats, err := f.runs.Tick(ctx)
	if err != nil || stats.Users != 1 || f.repo.cursor != "a" {
		t.Fatalf("stats=%+v cursor=%q err=%v", stats, f.repo.cursor, err)
	}
}

// ---- fan-out budget (I5) ----

func TestFanout_StopsAtBudget(t *testing.T) {
	m := newFanoutMock()
	m.pages[""] = []model.UpdateFeedSub{{UserID: "u1", FeedHash: "f1", Status: model.UpdateSourceActive}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stats, err := NewCustomerUpdateFanout(m, func() time.Time { return fanoutNow }).Run(ctx)
	if err != nil || stats.Subs != 0 || len(m.fannedAt) != 0 || len(m.marked) != 0 {
		t.Fatalf("stats=%+v err=%v fanned=%v", stats, err, m.fannedAt)
	}
}

// ---- services AccessDenied (M3) ----

func TestServicesInUseAccessDeniedIsUnavailable(t *testing.T) {
	f := &fakeServicesS3{err: &smithy.GenericAPIError{Code: "AccessDenied", Message: "synthetic"}}
	if got, err := newTestServicesReader(f, "billing-bucket").ServicesInUse(context.Background(), "acct-1"); got != nil || !errors.Is(err, ErrServicesUnavailable) {
		t.Fatalf("got %v, %v", got, err)
	}
	f.err = &smithy.GenericAPIError{Code: "SlowDown"}
	if _, err := newTestServicesReader(f, "billing-bucket").ServicesInUse(context.Background(), "acct-1"); err == nil || errors.Is(err, ErrServicesUnavailable) {
		t.Fatalf("other errors must be returned as errors: %v", err)
	}
}

func TestUpdateContextServicesUnavailableNotLogged(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	f := newCtxFake()
	f.addAccount("a1", "A1")
	f.addAccount("a2", "A2")
	f.servicesErr["a1"] = ErrServicesUnavailable
	f.servicesErr["a2"] = errors.New("s3 throttled")
	_, stats, err := newTestUpdateContext(t, f).BuildWithStats(context.Background(), "user-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ServicesUnavailable != 1 || stats.ServicesErrors != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if lines := strings.Count(buf.String(), "services failed"); lines != 1 {
		t.Fatalf("want one log line (the throttled account), got %d: %s", lines, buf.String())
	}
}
