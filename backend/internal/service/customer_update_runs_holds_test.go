package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

// ---- day-budget holds ----

func nextDayStart() time.Time { return time.Date(2026, 11, 14, 15, 0, 0, 0, time.UTC) } // 00:00 KST 11-15

func TestUpdateRuns_NextKSTDayStart(t *testing.T) {
	for _, at := range []time.Time{runsNow, time.Date(2026, 11, 14, 14, 59, 59, 0, time.UTC), time.Date(2026, 11, 13, 15, 0, 0, 0, time.UTC)} {
		if got := nextKSTDayStart(at); !got.Equal(nextDayStart()) {
			t.Errorf("nextKSTDayStart(%v) = %v", at, got)
		}
	}
}

func TestUpdateRuns_AccountCapHoldsThenDraftsNextDay(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	f.doneRun([]*model.UpdateItem{a}, []string{"acct-1"})
	f.repo.days[runsDay] = &model.UpdateDay{Total: 3, PerAccount: map[string]int{"acct-1": 3}}

	// Day 1: the draft is deferred and the item held until 00:00 KST.
	stats := f.tick(t)
	dec := f.repo.decisions[a.URLHash+"#acct-1"]
	if stats.Deferred != 1 || dec.Outcome != model.UpdateOutcomeDeferred || dec.DiscardReason != "account_day_budget" {
		t.Fatalf("stats=%+v decision=%+v", stats, dec)
	}
	if a.State != model.UpdateItemNew || a.Attempts != 0 || !a.HeldUntil.Equal(nextDayStart()) {
		t.Fatalf("item = %+v", a)
	}
	if f.repo.count("ClearPending") != 0 || len(f.inv.calls) != 0 {
		t.Fatalf("a held item must keep the mark and start nothing: %v", f.repo.calls)
	}

	// Later the same day: still held, nothing starts, the mark stays.
	f.now = runsNow.Add(10 * time.Hour)
	f.tick(t)
	if len(f.inv.calls) != 0 || f.repo.count("ClearPending") != 0 {
		t.Fatalf("same-day tick started %d runs", len(f.inv.calls))
	}

	// Next KST day: fresh counters, the item goes out with the account present.
	f.now = nextDayStart().Add(time.Minute)
	f.tick(t)
	if len(f.inv.calls) != 1 {
		t.Fatalf("next-day tick started %d runs", len(f.inv.calls))
	}
	lease := f.repo.run["u1"]
	if !reflect.DeepEqual(lease.ItemHashes, []string{a.URLHash}) || !reflect.DeepEqual(lease.AccountIDs, []string{"acct-1"}) {
		t.Fatalf("lease = %+v", lease)
	}
	// The runtime decides the pair again (needed/drafted) and writes the draft.
	f.repo.rows[lease.RunID] = &model.RunRows{Done: true,
		Items: []model.RunItemRow{{URLHash: a.URLHash, State: "decided"}},
		Decisions: []model.RunDecisionRow{{URLHash: a.URLHash, AccountID: "acct-1", Choice: model.UpdateChoiceNeeded,
			Backend: "luna", Outcome: model.UpdateOutcomeDrafted}},
		Drafts: []model.RunDraftRow{{AccountID: "acct-1", URLHash: a.URLHash, Why: "관련 발표입니다.", Body: goodBody(a.URL),
			Links: []string{a.URL}}},
	}
	stats = f.tick(t)
	dec = f.repo.decisions[a.URLHash+"#acct-1"]
	if stats.Drafts != 1 || f.repo.drafts["acct-1#"+a.URLHash] == nil || dec.Outcome != model.UpdateOutcomeDrafted || dec.DiscardReason != "" {
		t.Fatalf("stats=%+v decision=%+v", stats, dec)
	}
	if day := f.repo.days["20261115"]; day == nil || day.PerAccount["acct-1"] != 1 {
		t.Fatalf("next day counters = %+v", f.repo.days["20261115"])
	}
	if a.State != model.UpdateItemDecided || !a.HeldUntil.IsZero() {
		t.Fatalf("item = %+v", a)
	}
}

func TestUpdateRuns_DayBudgetHoldsItem(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	f.doneRun([]*model.UpdateItem{a}, []string{"acct-1"})
	f.repo.days[runsDay] = &model.UpdateDay{Total: 20, PerAccount: map[string]int{}}
	stats := f.tick(t)
	if stats.Held != 1 || !a.HeldUntil.Equal(nextDayStart()) || a.Attempts != 0 || f.repo.count("ClearPending") != 0 {
		t.Fatalf("stats=%+v item=%+v", stats, a)
	}
}

func TestUpdateRuns_NoPacks(t *testing.T) {
	cases := []struct {
		name      string
		setup     func(f *runsFixture)
		wantState string
		wantHeld  time.Time
	}{
		{"every in-scope account capped", func(f *runsFixture) {
			f.repo.days[runsDay] = &model.UpdateDay{Total: 3, PerAccount: map[string]int{"acct-1": 3}}
		}, model.UpdateItemNew, nextDayStart()},
		{"account read failed", func(f *runsFixture) { f.ctx.packs, f.ctx.failed = nil, 1 }, model.UpdateItemNew, runsNow.Add(time.Hour)},
		{"nothing in scope", func(f *runsFixture) { f.ctx.packs = nil }, model.UpdateItemSkipped, time.Time{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newRunsFixture()
			a := runsItem(t, "a")
			f.repo.items[a.URLHash] = a
			c.setup(f)
			f.tick(t)
			if a.State != c.wantState || a.Attempts != 0 || !a.HeldUntil.Equal(c.wantHeld) || len(f.inv.calls) != 0 {
				t.Fatalf("item = %+v", a)
			}
			if f.repo.count("ClearPending") != 0 {
				t.Fatal("mark cleared")
			}
		})
	}
}

func TestUpdateRuns_OnlyHeldItemsKeepMark(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	a.HeldUntil = nextDayStart()
	f.repo.items[a.URLHash] = a
	f.tick(t)
	if f.repo.count("ClearPending") != 0 || len(f.inv.calls) != 0 || f.ctx.builds != 0 {
		t.Fatalf("calls = %v", f.repo.calls)
	}
}

// ---- validation details ----

func TestUpdateRuns_ItemURLMustBeNormalized(t *testing.T) {
	f := newRunsFixture()
	good := runsItem(t, "a")
	bad := &model.UpdateItem{URL: "https://Example.com/news/b?utm_source=x", State: model.UpdateItemNew}
	sum := sha256.Sum256([]byte(bad.URL))
	bad.URLHash = hex.EncodeToString(sum[:])
	f.repo.items[good.URLHash], f.repo.items[bad.URLHash] = good, bad
	f.tick(t)
	if bad.State != model.UpdateItemSkipped || len(f.inv.calls) != 1 || strings.Contains(string(f.inv.calls[0].payload), bad.URLHash) {
		t.Fatalf("bad=%+v calls=%d", bad, len(f.inv.calls))
	}
}

func TestUpdateRuns_FetchedURLsMustBePublicHTTPS(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	rows := f.doneRun([]*model.UpdateItem{a}, []string{"acct-1"})
	rows.Drafts[0].FetchedURLs = []string{"https://w.amazon.com/x", "http://example.org/y"}
	rows.Drafts[0].Links = []string{a.URL, "https://w.amazon.com/x", "http://example.org/y"}
	stats := f.tick(t)
	dec := f.repo.decisions[a.URLHash+"#acct-1"]
	if stats.Discarded != 1 || len(f.repo.drafts) != 0 || dec.DiscardReason != "links_invalid" {
		t.Fatalf("stats=%+v decision=%+v", stats, dec)
	}
	got := allowedFetched([]string{"https://example.org/ok", "https://w.amazon.com/x", "http://example.org/y",
		"https://127.0.0.1/z", "https://" + strings.Repeat("a", 2050) + ".com/"})
	if !reflect.DeepEqual(got, []string{"https://example.org/ok"}) {
		t.Fatalf("allowedFetched = %v", got)
	}
}

func TestUpdateRuns_SameRunTrustsStoredChoice(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	f.doneRun([]*model.UpdateItem{a}, []string{"acct-1"})
	f.repo.decisions[a.URLHash+"#acct-1"] = &model.UpdateDecision{URLHash: a.URLHash, AccountID: "acct-1",
		Choice: model.UpdateChoiceNotNeeded, RunID: runsRunID}
	stats := f.tick(t)
	if len(f.repo.drafts) != 0 || stats.Drafts != 0 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestUpdateRuns_NotNeededStoredWithoutOutcome(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	rows := f.doneRun([]*model.UpdateItem{a}, []string{"acct-1"})
	rows.Decisions[0].Choice = model.UpdateChoiceNotNeeded
	rows.Decisions[0].Outcome, rows.Decisions[0].DiscardReason = model.UpdateOutcomeDrafted, "figure"
	f.tick(t)
	dec := f.repo.decisions[a.URLHash+"#acct-1"]
	if dec.Outcome != "" || dec.DiscardReason != "" || dec.GateInput != nil || len(f.repo.drafts) != 0 {
		t.Fatalf("decision = %+v", dec)
	}
}

func TestUpdateRuns_FinalizeReadsEachAccountOnce(t *testing.T) {
	f := newRunsFixture()
	f.doneRun([]*model.UpdateItem{runsItem(t, "a"), runsItem(t, "b"), runsItem(t, "c")}, []string{"acct-1"})
	stats := f.tick(t)
	if stats.Drafts != 3 || f.ctx.reads["acct-1"] != 3 {
		t.Fatalf("stats=%+v reads=%d (want InScope, BlockList and OpenOpportunities once)", stats, f.ctx.reads["acct-1"])
	}
}

// ---- crash points and overlap ----

func TestUpdateRuns_CrashBetweenDayCounterAndPut(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	f.doneRun([]*model.UpdateItem{a}, []string{"acct-1"})
	f.repo.failOnce["PutDraft"] = errors.New("crash")
	stats := f.tick(t)
	if stats.Leaked != 1 || stats.Errors != 1 || len(f.repo.drafts) != 0 || f.repo.run["u1"] == nil {
		t.Fatalf("crash tick: stats=%+v", stats)
	}
	stats = f.tick(t)
	if len(f.repo.drafts) != 1 || stats.Drafts != 1 || f.repo.days[runsDay].Total != 2 ||
		f.repo.decisions[a.URLHash+"#acct-1"].Outcome != model.UpdateOutcomeDrafted {
		t.Fatalf("retry: stats=%+v day=%+v (want one leaked slot and one draft)", stats, f.repo.days[runsDay])
	}
}

func TestUpdateRuns_CrashBetweenPutAndOutcome(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	f.doneRun([]*model.UpdateItem{a}, []string{"acct-1"})
	key := a.URLHash + "#acct-1"
	f.repo.decisions[key] = &model.UpdateDecision{URLHash: a.URLHash, AccountID: "acct-1", Choice: model.UpdateChoiceNeeded,
		RunID: strings.Repeat("9", 32), Outcome: model.UpdateOutcomeDeferred, DiscardReason: "day_budget"}
	f.repo.failOnce["SetDecisionOutcome"] = errors.New("crash")
	f.tick(t)
	if len(f.repo.drafts) != 1 || f.repo.decisions[key].Outcome != model.UpdateOutcomeDeferred {
		t.Fatalf("crash tick: drafts=%d decision=%+v", len(f.repo.drafts), f.repo.decisions[key])
	}
	f.tick(t)
	if len(f.repo.drafts) != 1 || f.repo.count("PutDraft") != 1 || f.repo.days[runsDay].Total != 1 ||
		f.repo.decisions[key].Outcome != model.UpdateOutcomeDrafted {
		t.Fatalf("retry: puts=%d day=%+v decision=%+v", f.repo.count("PutDraft"), f.repo.days[runsDay], f.repo.decisions[key])
	}
}

func TestUpdateRuns_CrashAfterLeaseDeleteUnderCounts(t *testing.T) {
	f := newRunsFixture()
	a := runsItem(t, "a")
	rows := f.doneRun([]*model.UpdateItem{a}, nil)
	f.repo.run["u1"].AccountIDs = []string{"acct-1"}
	rows.Items[0].State = "deferred"
	f.repo.failOnce["SetItemState"] = errors.New("crash")
	stats := f.tick(t)
	if stats.Finalized != 1 || stats.ItemErrors != 1 || a.Attempts != 0 || a.State != model.UpdateItemNew {
		t.Fatalf("stats=%+v item=%+v", stats, a)
	}
	// The lease is gone, so no later finalize of this run can add the attempt:
	// attempts are under-counted, never doubled.
	if l := f.repo.run["u1"]; l == nil || l.RunID == runsRunID {
		t.Fatal("a fresh run should have started for the still-new item")
	}
}

func TestUpdateRuns_OverlappingFinalizes(t *testing.T) {
	for _, at := range []string{"DeleteRun", "PutDraft"} {
		t.Run("second finalize runs at "+at, func(t *testing.T) {
			f := newRunsFixture()
			a, b := runsItem(t, "a"), runsItem(t, "b")
			rows := f.doneRun([]*model.UpdateItem{a, b}, []string{"acct-1"})
			rows.Items[1].State = "deferred"
			rows.Decisions, rows.Drafts = rows.Decisions[:1], rows.Drafts[:1]
			other := NewCustomerUpdateRuns(CustomerUpdateRunsDeps{Repo: f.repo, Context: f.ctx, Invoker: f.inv}, func() time.Time { return f.now })
			var inner RunStats
			f.repo.before[at] = func() {
				var err error
				if inner, err = other.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			outer := f.tick(t)
			if inner.Finalized+outer.Finalized != 1 || len(f.repo.drafts) != 1 {
				t.Fatalf("inner=%+v outer=%+v drafts=%d", inner, outer, len(f.repo.drafts))
			}
			if b.Attempts != 1 || a.State != model.UpdateItemDecided {
				t.Fatalf("a=%+v b=%+v (attempts must be added once)", a, b)
			}
			wantTotal := 1
			if at == "PutDraft" {
				wantTotal = 2 // the losing put leaked one slot
			}
			if f.repo.days[runsDay].Total != wantTotal || f.repo.decisions[a.URLHash+"#acct-1"].Outcome != model.UpdateOutcomeDrafted {
				t.Fatalf("day=%+v decision=%+v", f.repo.days[runsDay], f.repo.decisions[a.URLHash+"#acct-1"])
			}
		})
	}
}
