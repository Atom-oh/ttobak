package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// memDictStore is a stateful in-memory dictionary partition that mirrors the
// repository's transaction conditions, including the order in which DynamoDB
// reports the first failed item.
type memDictStore struct {
	terms  map[string]model.DictTerm        // SK -> row
	obs    map[string]model.DictObservation // SK -> row
	hooks  map[string]func()                // run before the named write; return to keep
	calls  map[string]int
	failOn map[string]error
}

func newMemDictStore() *memDictStore {
	return &memDictStore{terms: map[string]model.DictTerm{}, obs: map[string]model.DictObservation{}, hooks: map[string]func(){}, calls: map[string]int{}, failOn: map[string]error{}}
}

func (s *memDictStore) before(op string) error {
	s.calls[op]++
	if hook := s.hooks[op]; hook != nil {
		hook()
	}
	return s.failOn[op]
}

func memConflict() error {
	return fmt.Errorf("%w: %w", repository.ErrConditionFailed, repository.ErrDictTermConflict)
}

func memObsExists() error {
	return fmt.Errorf("%w: %w", repository.ErrConditionFailed, repository.ErrDictObservationExists)
}

func (s *memDictStore) ListDictTerms(context.Context, string) ([]model.DictTerm, error) {
	if err := s.before("list"); err != nil {
		return nil, err
	}
	var out []model.DictTerm
	for _, t := range s.terms {
		out = append(out, t)
	}
	return out, nil
}

func (s *memDictStore) GetDictTerm(_ context.Context, _ string, key string) (*model.DictTerm, error) {
	if t, ok := s.terms[model.DictTermSK(key)]; ok {
		return &t, nil
	}
	return nil, nil
}

func (s *memDictStore) ListDictObs(_ context.Context, _ string, key string) ([]model.DictObservation, error) {
	var out []model.DictObservation
	for sk, o := range s.obs {
		if strings.HasPrefix(sk, model.PrefixDictObs+key+"#") {
			out = append(out, o)
		}
	}
	return out, nil
}

func (s *memDictStore) CreateAutoTerm(_ context.Context, term *model.DictTerm, obs *model.DictObservation) error {
	if err := s.before("create"); err != nil {
		return err
	}
	if _, ok := s.terms[term.SK]; ok {
		return memConflict()
	}
	if _, ok := s.obs[obs.SK]; ok {
		return memObsExists()
	}
	s.terms[term.SK], s.obs[obs.SK] = *term, *obs
	return nil
}

func (s *memDictStore) observe(op, fromState string, in repository.DictMergeInput, promotion *repository.DictPromotion) error {
	if err := s.before(op); err != nil {
		return err
	}
	if _, ok := s.obs[in.Observation.SK]; ok {
		return memObsExists()
	}
	t, ok := s.terms[model.DictTermSK(in.TermKey)]
	if !ok || t.Version != in.ExpectedVersion || t.State != fromState || t.Origin != model.TermOriginAuto || t.UserEditedAt != "" {
		return memConflict()
	}
	t.State, t.HeardAs, t.LastSeenAt, t.UpdatedAt = model.TermStateActive, in.HeardAs, in.SeenAt, in.SeenAt
	if len(in.SourceTypes) > 0 {
		t.SourceTypes = in.SourceTypes
	}
	t.Version++
	switch {
	case promotion != nil:
		t.DisplayAs, t.Phrase, t.Type, t.Confidence = promotion.DisplayAs, promotion.Phrase, promotion.Type, promotion.Confidence
		t.TranscribeKo, t.TranscribeEn, t.FirstSourceMeetingID, t.Hits = promotion.TranscribeKo, promotion.TranscribeEn, promotion.FirstSourceMeetingID, 1
	case !in.SkipHit:
		t.Hits++
	}
	s.terms[t.SK], s.obs[in.Observation.SK] = t, *in.Observation
	return nil
}

func (s *memDictStore) MergeAutoObservation(_ context.Context, in repository.DictMergeInput) error {
	return s.observe("merge", model.TermStateActive, in, nil)
}

func (s *memDictStore) PromoteSuggestedTerm(_ context.Context, in repository.DictMergeInput, content repository.DictPromotion) error {
	return s.observe("promote", model.TermStateSuggested, in, &content)
}

func (s *memDictStore) EvictAutoTerm(_ context.Context, _ string, key string, v int64) (repository.DictWriteResult, error) {
	if err := s.before("evict"); err != nil {
		return repository.DictWriteResult{}, err
	}
	t, ok := s.terms[model.DictTermSK(key)]
	if !ok || t.Version != v || t.Origin != model.TermOriginAuto || t.UserEditedAt != "" ||
		(t.State != model.TermStateActive && t.State != model.TermStateSuggested) {
		return repository.DictWriteResult{}, memConflict()
	}
	delete(s.terms, t.SK)
	rows, _ := s.ListDictObs(context.Background(), "", key)
	for _, o := range rows {
		delete(s.obs, o.SK)
	}
	return repository.DictWriteResult{}, nil
}

func (s *memDictStore) DeleteDictObs(_ context.Context, _ string, sk string) error {
	if err := s.before("deleteObs"); err != nil {
		return err
	}
	delete(s.obs, sk)
	return nil
}

func (s *memDictStore) term(display string) (model.DictTerm, bool) {
	t, ok := s.terms[model.DictTermSK(TermKey(display))]
	return t, ok
}

func (s *memDictStore) seed(t model.DictTerm) {
	t.PK, t.TermKey, t.SK = "USER#owner", TermKey(t.DisplayAs), model.DictTermSK(TermKey(t.DisplayAs))
	if t.Version == 0 {
		t.Version = 1
	}
	s.terms[t.SK] = t
}

var mergeClock = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

func newTestMerger(store *memDictStore) *TermsMerger {
	m := NewTermsMerger(store)
	tick := 0
	m.now = func() time.Time {
		tick++
		return mergeClock.Add(time.Duration(tick) * time.Millisecond)
	}
	return m
}

func ref(meeting string) TermsMeetingRef {
	return TermsMeetingRef{OwnerID: "owner", MeetingID: meeting, SummaryGeneration: 1}
}

func cand(display string, heard ...string) ExtractedTerm {
	return ExtractedTerm{DisplayAs: display, HeardAs: heard, SourceTypes: []string{model.TermSourceTranscript}, Confidence: 0.9}
}

func mustMerge(t *testing.T, m *TermsMerger, r TermsMeetingRef, cs ...ExtractedTerm) *TermsMergeResult {
	t.Helper()
	res, err := m.MergeExtractedTerms(context.Background(), "USER#owner", cs, r)
	if err != nil {
		t.Fatal(err)
	}
	if res.CleanupErr != nil {
		t.Fatal(res.CleanupErr)
	}
	return res
}

func TestMergeABAOnlyCountsEachMeetingOnce(t *testing.T) {
	store := newMemDictStore()
	m := newTestMerger(store)
	first := mustMerge(t, m, ref("A"), cand("AlphaGrid", "알파그리드"))
	mustMerge(t, m, ref("B"), cand("AlphaGrid", "알파 그리드"))
	again := mustMerge(t, m, TermsMeetingRef{OwnerID: "owner", MeetingID: "A", Accepted: first.Accepted}, cand("AlphaGrid"))
	term, _ := store.term("AlphaGrid")
	if term.Hits != 2 || term.State != model.TermStateActive || term.Origin != model.TermOriginAuto || term.FirstSourceMeetingID != "A" {
		t.Fatalf("A,B,A must give hits=2 on an active auto term: %+v", term)
	}
	if len(store.obs) != 2 || again.Counters.AlreadyCounted != 1 || again.Counters.Merged != 0 {
		t.Fatalf("re-run must be a no-op: obs=%d %+v", len(store.obs), again.Counters)
	}
	if strings.Join(term.HeardAs, "|") != "알파그리드" {
		// "알파 그리드" has the same key as "알파그리드" and is deduped.
		t.Fatalf("heardAs must dedupe by key: %v", term.HeardAs)
	}
	if len(again.Accepted) != 1 || again.Accepted[0].TermKey != TermKey("AlphaGrid") {
		t.Fatalf("accepted terms must list the observed key once: %+v", again.Accepted)
	}
}

func TestMergeNeverTouchesUserOwnedOrDeletedTerms(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  model.DictTerm
		want func(model.TermsCounters) int
	}{
		{"tombstone", model.DictTerm{DisplayAs: "AlphaGrid", Origin: model.TermOriginAuto, State: model.TermStateDeleted}, func(c model.TermsCounters) int { return c.SkippedTombstone }},
		{"manual", model.DictTerm{DisplayAs: "AlphaGrid", Origin: model.TermOriginManual, State: model.TermStateActive}, func(c model.TermsCounters) int { return c.SkippedManual }},
		{"user edited auto", model.DictTerm{DisplayAs: "AlphaGrid", Origin: model.TermOriginAuto, State: model.TermStateActive, UserEditedAt: "x"}, func(c model.TermsCounters) int { return c.SkippedManual }},
		{"user accepted suggestion", model.DictTerm{DisplayAs: "AlphaGrid", Origin: model.TermOriginAuto, State: model.TermStateSuggested, UserEditedAt: "x"}, func(c model.TermsCounters) int { return c.SkippedManual }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemDictStore()
			store.seed(tc.row)
			before, _ := store.term("AlphaGrid")
			res := mustMerge(t, newTestMerger(store), ref("A"), cand("AlphaGrid", "알파"))
			after, _ := store.term("AlphaGrid")
			if tc.want(res.Counters) != 1 || len(store.obs) != 0 || after.Version != before.Version || after.Hits != before.Hits {
				t.Fatalf("row must stay untouched: %+v %+v", res.Counters, after)
			}
			if len(res.Accepted) != 0 {
				t.Fatal("a skipped term has no observation to accept")
			}
		})
	}
}

func TestConcurrentUserEditBlocksMerge(t *testing.T) {
	store := newMemDictStore()
	store.seed(model.DictTerm{DisplayAs: "AlphaGrid", Origin: model.TermOriginAuto, State: model.TermStateActive, Hits: 1})
	store.hooks["merge"] = func() {
		delete(store.hooks, "merge")
		row, _ := store.term("AlphaGrid")
		row.UserEditedAt, row.Version = "now", row.Version+1
		store.terms[row.SK] = row
	}
	res := mustMerge(t, newTestMerger(store), ref("B"), cand("AlphaGrid"))
	row, _ := store.term("AlphaGrid")
	if res.Counters.SkippedManual != 1 || row.Hits != 1 || len(store.obs) != 0 {
		t.Fatalf("an edit that lands first must win: %+v %+v", res.Counters, row)
	}
}

func TestConcurrentTombstoneBlocksCreate(t *testing.T) {
	store := newMemDictStore()
	store.hooks["create"] = func() {
		delete(store.hooks, "create")
		store.seed(model.DictTerm{DisplayAs: "AlphaGrid", Origin: model.TermOriginAuto, State: model.TermStateDeleted, Version: 3})
	}
	res := mustMerge(t, newTestMerger(store), ref("A"), cand("AlphaGrid"))
	row, _ := store.term("AlphaGrid")
	if res.Counters.SkippedTombstone != 1 || row.State != model.TermStateDeleted || len(store.obs) != 0 {
		t.Fatalf("a tombstone must block the automatic create: %+v %+v", res.Counters, row)
	}
}

func TestMergeVersionRetry(t *testing.T) {
	bump := func(store *memDictStore, times int) {
		store.hooks["merge"] = func() {
			if times == 0 {
				return
			}
			times--
			row, _ := store.term("AlphaGrid")
			row.Version++
			store.terms[row.SK] = row
		}
	}
	store := newMemDictStore()
	store.seed(model.DictTerm{DisplayAs: "AlphaGrid", Origin: model.TermOriginAuto, State: model.TermStateActive, Hits: 1})
	bump(store, 1)
	res := mustMerge(t, newTestMerger(store), ref("B"), cand("AlphaGrid"))
	row, _ := store.term("AlphaGrid")
	if res.Counters.Merged != 1 || row.Hits != 2 || store.calls["merge"] != 2 {
		t.Fatalf("one conflict must re-read and merge once: %+v calls=%d hits=%d", res.Counters, store.calls["merge"], row.Hits)
	}

	store = newMemDictStore()
	store.seed(model.DictTerm{DisplayAs: "AlphaGrid", Origin: model.TermOriginAuto, State: model.TermStateActive, Hits: 1})
	bump(store, 100)
	res = mustMerge(t, newTestMerger(store), ref("B"), cand("AlphaGrid"))
	row, _ = store.term("AlphaGrid")
	if res.Counters.Conflicts != 1 || store.calls["merge"] != 3 || row.Hits != 1 || len(res.Accepted) != 0 {
		t.Fatalf("retries must stop after three attempts: %+v calls=%d", res.Counters, store.calls["merge"])
	}
}

func TestTaintedRunsOnlySuggestAndUntaintedRunsPromote(t *testing.T) {
	store := newMemDictStore()
	m := newTestMerger(store)
	tainted := ref("A")
	tainted.Tainted = true
	planted := cand("AlphaGrid", "planted-alias")
	planted.Phrase, planted.Type = "planted-phrase", "planted-type"
	res := mustMerge(t, m, tainted, planted)
	row, _ := store.term("AlphaGrid")
	if !store.obs[model.DictObsSK(TermKey("AlphaGrid"), "A")].Tainted {
		t.Fatal("a tainted run's observation must be marked")
	}
	if row.State != model.TermStateSuggested || res.Counters.Suggested != 1 || len(res.Accepted) != 1 {
		t.Fatalf("a tainted meeting may only suggest: %+v %+v", res.Counters, row)
	}
	tainted.MeetingID = "B"
	res = mustMerge(t, m, tainted, cand("AlphaGrid"))
	if res.Counters.SkippedTainted != 1 || len(store.obs) != 1 {
		t.Fatalf("a tainted meeting must not reinforce a term: %+v", res.Counters)
	}
	res = mustMerge(t, m, ref("C"), cand("AlphaGrid", "알파그리드"))
	row, _ = store.term("AlphaGrid")
	if res.Counters.Promoted != 1 || row.State != model.TermStateActive || row.UserEditedAt != "" {
		t.Fatalf("an untainted meeting promotes the suggestion: %+v %+v", res.Counters, row)
	}
	// Nothing the tainted run planted survives promotion, and its hit does
	// not count: one tainted plus one untainted meeting is hits=1.
	if row.Hits != 1 || strings.Join(row.HeardAs, "|") != "알파그리드" || row.Phrase != "" || row.Type != "" || row.FirstSourceMeetingID != "C" {
		t.Fatalf("promotion must keep only the untainted run's evidence: %+v", row)
	}
	mustMerge(t, m, ref("D"), cand("AlphaGrid"))
	if row, _ = store.term("AlphaGrid"); row.Hits != 2 {
		t.Fatalf("a second untainted meeting reaches hits=2: %d", row.Hits)
	}
}

func TestPrunedMeetingIsNotCountedAgain(t *testing.T) {
	store := newMemDictStore()
	key := TermKey("AlphaGrid")
	store.seed(model.DictTerm{DisplayAs: "AlphaGrid", Origin: model.TermOriginAuto, State: model.TermStateActive, Hits: 11})
	// m00's observation was pruned earlier; its acceptedTerms still lists the key.
	r := ref("m00")
	r.Accepted = []model.TermsAcceptedTerm{{TermKey: key, SourceTypes: []string{model.TermSourceTranscript}}}
	res := mustMerge(t, newTestMerger(store), r, cand("AlphaGrid"))
	row, _ := store.term("AlphaGrid")
	if row.Hits != 11 || res.Counters.AlreadyCounted != 1 || res.Counters.Merged != 0 {
		t.Fatalf("re-running a pruned meeting must not add a hit: hits=%d %+v", row.Hits, res.Counters)
	}
	if _, ok := store.obs[model.DictObsSK(key, "m00")]; !ok {
		t.Fatal("the observation is recorded again as evidence")
	}
}

func TestMergeRetriesTransactionConflicts(t *testing.T) {
	store := newMemDictStore()
	store.seed(model.DictTerm{DisplayAs: "AlphaGrid", Origin: model.TermOriginAuto, State: model.TermStateActive, Hits: 1})
	store.failOn["merge"] = fmt.Errorf("%w: %w", repository.ErrDictTransactionConflict, repository.ErrDictTransactionCancelled)
	store.hooks["merge"] = func() {
		if store.calls["merge"] == 2 {
			delete(store.failOn, "merge")
		}
	}
	res := mustMerge(t, newTestMerger(store), ref("B"), cand("AlphaGrid"), cand("BetaGrid"))
	row, _ := store.term("AlphaGrid")
	if res.Counters.Merged != 1 || res.Counters.Created != 1 || row.Hits != 2 || store.calls["merge"] != 2 {
		t.Fatalf("a transaction conflict must re-read and retry: %+v calls=%d", res.Counters, store.calls["merge"])
	}

	store = newMemDictStore()
	store.failOn["create"] = fmt.Errorf("throttled: %w", repository.ErrDictTransactionCancelled)
	res, err := newTestMerger(store).MergeExtractedTerms(context.Background(), "USER#owner", []ExtractedTerm{cand("AlphaGrid")}, ref("A"))
	if err == nil || len(res.Accepted) != 0 {
		t.Fatalf("a cancelled transaction applied nothing, so its key is not accepted: %v %+v", err, res)
	}
}

func TestAliasEqualToAnotherTermIsDropped(t *testing.T) {
	store := newMemDictStore()
	store.seed(model.DictTerm{DisplayAs: "Beta Hub", Origin: model.TermOriginManual, State: model.TermStateActive})
	res := mustMerge(t, newTestMerger(store), ref("A"),
		cand("AlphaGrid", "betahub", "alpha-grid", "알파", "Gamma"), cand("Gamma"))
	row, _ := store.term("AlphaGrid")
	if strings.Join(row.HeardAs, "|") != "알파" || res.Counters.AliasesDropped != 2 {
		t.Fatalf("aliases equal to another term's key or its own must be dropped: %v %+v", row.HeardAs, res.Counters)
	}
}

func TestAutoCapEvictsLowestScoreThenOldest(t *testing.T) {
	recent := mergeClock.Add(-time.Hour).Format(model.DictTimeLayout)
	old := mergeClock.Add(-60 * 24 * time.Hour).Format(model.DictTimeLayout)
	older := mergeClock.Add(-90 * 24 * time.Hour).Format(model.DictTimeLayout)
	// 296 + 3 seeded auto terms + 2 created = 301: one must go. "OlderTerm"
	// (score 2, oldest) is the first target.
	setup := func() *memDictStore {
		store := newMemDictStore()
		for i := 0; i < MaxAutoTermsPerScope-4; i++ {
			store.seed(model.DictTerm{DisplayAs: fmt.Sprintf("Term%03d", i), Origin: model.TermOriginAuto, State: model.TermStateActive, Hits: 1, LastSeenAt: recent})
		}
		store.seed(model.DictTerm{DisplayAs: "OlderTerm", Origin: model.TermOriginAuto, State: model.TermStateActive, Hits: 1, LastSeenAt: older})
		store.seed(model.DictTerm{DisplayAs: "OldTerm", Origin: model.TermOriginAuto, State: model.TermStateSuggested, Hits: 1, LastSeenAt: old})
		store.seed(model.DictTerm{DisplayAs: "PopularOld", Origin: model.TermOriginAuto, State: model.TermStateActive, Hits: 5, LastSeenAt: older})
		store.seed(model.DictTerm{DisplayAs: "ManualOld", Origin: model.TermOriginManual, State: model.TermStateActive, LastSeenAt: older})
		return store
	}
	for _, tc := range []struct {
		name      string
		race      func(row *model.DictTerm, store *memDictStore)
		evicted   int
		wantTerms map[string]bool
	}{
		{"no race", nil, 1, map[string]bool{"OlderTerm": false, "OldTerm": true, "PopularOld": true, "ManualOld": true, "NewTermA": true}},
		{"target merged meanwhile: next lowest goes", func(row *model.DictTerm, store *memDictStore) {
			row.Version++
			row.LastSeenAt = recent
			store.terms[row.SK] = *row
		}, 1, map[string]bool{"OlderTerm": true, "OldTerm": false, "PopularOld": true}},
		{"target edited meanwhile: it no longer counts", func(row *model.DictTerm, store *memDictStore) {
			row.UserEditedAt, row.Version = "now", row.Version+1
			store.terms[row.SK] = *row
		}, 0, map[string]bool{"OlderTerm": true, "OldTerm": true, "PopularOld": true}},
		{"target evicted by a concurrent merge: no extra eviction", func(row *model.DictTerm, store *memDictStore) {
			delete(store.terms, row.SK)
		}, 0, map[string]bool{"OlderTerm": false, "OldTerm": true, "PopularOld": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := setup()
			if tc.race != nil {
				store.hooks["evict"] = func() {
					delete(store.hooks, "evict")
					row, _ := store.term("OlderTerm")
					tc.race(&row, store)
				}
			}
			res := mustMerge(t, newTestMerger(store), ref("A"), cand("NewTermA"), cand("NewTermB"))
			if res.Counters.Created != 2 || res.Counters.Evicted != tc.evicted {
				t.Fatalf("unexpected eviction: %+v", res.Counters)
			}
			auto := 0
			for _, row := range store.terms {
				if evictable(row) {
					auto++
				}
			}
			if auto != MaxAutoTermsPerScope {
				t.Fatalf("the scope must end with exactly %d automatic terms, got %d", MaxAutoTermsPerScope, auto)
			}
			for display, want := range tc.wantTerms {
				if _, ok := store.term(display); ok != want {
					t.Errorf("%s present=%v, want %v", display, ok, want)
				}
			}
		})
	}
}

func TestObservationCapKeepsNewestTen(t *testing.T) {
	store := newMemDictStore()
	store.seed(model.DictTerm{DisplayAs: "AlphaGrid", Origin: model.TermOriginAuto, State: model.TermStateActive, Hits: 10})
	key := TermKey("AlphaGrid")
	for i := 0; i < MaxObservationsPerTerm; i++ {
		sk := model.DictObsSK(key, fmt.Sprintf("m%02d", i))
		store.obs[sk] = model.DictObservation{SK: sk, TermKey: key, CreatedAt: mergeClock.Add(time.Duration(i-100) * time.Minute).Format(model.DictTimeLayout)}
	}
	res := mustMerge(t, newTestMerger(store), ref("m10"), cand("AlphaGrid"))
	row, _ := store.term("AlphaGrid")
	if _, ok := store.obs[model.DictObsSK(key, "m00")]; ok || len(store.obs) != MaxObservationsPerTerm || res.Counters.ObsPruned != 1 {
		t.Fatalf("the oldest observation beyond ten must be deleted: %d %+v", len(store.obs), res.Counters)
	}
	if _, ok := store.obs[model.DictObsSK(key, "m10")]; !ok || row.Hits != 11 {
		t.Fatalf("pruning keeps the new observation and never decrements hits: %d", row.Hits)
	}
}

func TestAcceptedTermsStayWithinForty(t *testing.T) {
	store := newMemDictStore()
	var prior []model.TermsAcceptedTerm
	for i := 0; i < model.MaxTermsAccepted-1; i++ {
		prior = append(prior, model.TermsAcceptedTerm{TermKey: TermKey(fmt.Sprintf("Prior%02d", i))})
	}
	r := ref("A")
	r.Accepted = prior
	res := mustMerge(t, newTestMerger(store), r, cand("Prior05"), cand("FreshOne"), cand("FreshTwo"), cand("FreshThree"))
	if len(res.Accepted) != model.MaxTermsAccepted || res.Counters.Capped != 2 || res.Counters.Created != 2 {
		t.Fatalf("new keys only while acceptedTerms has room: %d %+v", len(res.Accepted), res.Counters)
	}
	if err := model.ValidateTermsAccepted(res.Accepted); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.term("FreshTwo"); ok {
		t.Fatal("a capped candidate must not be written, or deletion cleanup would miss it")
	}
}

func TestSkippedCandidatesDoNotUseAcceptedRoom(t *testing.T) {
	store := newMemDictStore()
	store.seed(model.DictTerm{DisplayAs: "TombA", Origin: model.TermOriginAuto, State: model.TermStateDeleted})
	store.seed(model.DictTerm{DisplayAs: "TombB", Origin: model.TermOriginAuto, State: model.TermStateDeleted})
	var prior []model.TermsAcceptedTerm
	for i := 0; i < model.MaxTermsAccepted-2; i++ {
		prior = append(prior, model.TermsAcceptedTerm{TermKey: TermKey(fmt.Sprintf("Prior%02d", i))})
	}
	r := ref("A")
	r.Accepted = prior
	res := mustMerge(t, newTestMerger(store), r, cand("TombA"), cand("TombB"), cand("NewC"))
	if _, ok := store.term("NewC"); !ok || res.Counters.Capped != 0 || len(res.Accepted) != model.MaxTermsAccepted-1 {
		t.Fatalf("skipped tombstones must leave room for a later candidate: %d %+v", len(res.Accepted), res.Counters)
	}
}

func TestMergeRejectsOtherScopesAndInvalidCandidates(t *testing.T) {
	store := newMemDictStore()
	m := newTestMerger(store)
	for _, pk := range []string{"ACCOUNT#a1", "USER#someone-else"} {
		if _, err := m.MergeExtractedTerms(context.Background(), pk, []ExtractedTerm{cand("AlphaGrid")}, ref("A")); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s must be rejected: %v", pk, err)
		}
	}
	bad := cand("AlphaGrid")
	bad.SourceTypes = []string{"live"}
	res := mustMerge(t, m, ref("A"), bad, cand(" - . "), ExtractedTerm{DisplayAs: "NoSource"})
	if res.Counters.Invalid != 3 || len(store.terms) != 0 || store.calls["create"] != 0 {
		t.Fatalf("structurally invalid candidates must be dropped: %+v", res.Counters)
	}
}

func TestStaleObservationIsReplacedOnCreate(t *testing.T) {
	store := newMemDictStore()
	sk := model.DictObsSK(TermKey("AlphaGrid"), "A")
	store.obs[sk] = model.DictObservation{SK: sk}
	res := mustMerge(t, newTestMerger(store), ref("A"), cand("AlphaGrid"))
	if res.Counters.Created != 1 || store.obs[sk].MeetingID != "A" {
		t.Fatalf("a leftover observation of an evicted term must not block re-creation: %+v", res.Counters)
	}
}

func TestMergeFailureStillReportsAcceptedTerms(t *testing.T) {
	store := newMemDictStore()
	m := newTestMerger(store)
	store.hooks["create"] = func() {
		if store.calls["create"] == 2 {
			store.failOn["create"] = errors.New("throttled")
		}
	}
	res, err := m.MergeExtractedTerms(context.Background(), "USER#owner", []ExtractedTerm{cand("AlphaGrid"), cand("BetaGrid")}, ref("A"))
	if err == nil || res == nil || len(res.Accepted) != 2 {
		t.Fatalf("an ambiguous write failure must still record both keys for cleanup: %v %+v", err, res)
	}
}
