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

const newsTestDay = "2026-10-18"

// newsTestWindowEnd is 21:00 UTC on newsTestDay, when the daily run fires.
var newsTestWindowEnd = time.Date(2026, 10, 18, 21, 0, 0, 0, time.UTC).Unix()

type newsStoreFake struct {
	docs     []model.CrawledDocument
	history  map[string]model.TermsNewsHistory
	runs     map[string]model.TermsNewsRun
	windows  [][2]int64
	listErr  error
	global   map[string]bool // global (operator) sources; others are not
	lookups  map[string]int
	lookErr  error
	puts     int
	histGets int
	// accounts maps a source with a CONFIG row to its accountId ("" for a
	// common row); a source absent here has no row.
	accounts      map[string]string
	originLookups map[string]int
	originErr     error
	originErrOnly string // when set, originErr applies to this source only
}

func (f *newsStoreFake) ListDocumentsCrawledBetween(_ context.Context, from, to int64) ([]model.CrawledDocument, error) {
	f.windows = append(f.windows, [2]int64{from, to})
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []model.CrawledDocument
	for _, d := range f.docs {
		if d.CrawledAt >= from && d.CrawledAt < to {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *newsStoreFake) IsGlobalNewsSource(_ context.Context, sourceID string) (bool, error) {
	f.lookups[sourceID]++
	if f.lookErr != nil {
		return false, f.lookErr
	}
	return f.global[sourceID], nil
}

func (f *newsStoreFake) GetSourceOrigin(_ context.Context, sourceID string) (string, string, bool, error) {
	f.originLookups[sourceID]++
	if f.originErr != nil && (f.originErrOnly == "" || f.originErrOnly == sourceID) {
		return "", "", false, f.originErr
	}
	accountID, found := f.accounts[sourceID]
	return "", accountID, found, nil
}

func (f *newsStoreFake) GetTermsNewsHistory(_ context.Context, termKey string) (*model.TermsNewsHistory, error) {
	f.histGets++
	if h, ok := f.history[termKey]; ok {
		return &h, nil
	}
	return nil, nil
}

func (f *newsStoreFake) PutTermsNewsHistory(_ context.Context, h *model.TermsNewsHistory) error {
	f.puts++
	f.history[h.TermKey] = *h
	return nil
}

func (f *newsStoreFake) GetTermsNewsRun(_ context.Context, day string) (*model.TermsNewsRun, error) {
	if run, ok := f.runs[day]; ok {
		return &run, nil
	}
	return nil, nil
}

func (f *newsStoreFake) PutTermsNewsRun(_ context.Context, run *model.TermsNewsRun) error {
	f.runs[run.Day] = *run
	return nil
}

type newsTestEnv struct {
	svc       *TermsAnalysisService
	dict      partitionedDictStore
	store     *newsStoreFake
	llm       *termsModelFake
	refreshed []string
}

func newsOn() TermsConfig {
	cfg := termsOn
	cfg.NewsMode = SourceTermsModeOn
	return cfg
}

func newNewsTestEnv(cfg TermsConfig) *newsTestEnv {
	env := &newsTestEnv{
		dict: partitionedDictStore{},
		store: &newsStoreFake{history: map[string]model.TermsNewsHistory{}, runs: map[string]model.TermsNewsRun{},
			global: map[string]bool{"srcA": true, "srcB": true}, lookups: map[string]int{},
			accounts: map[string]string{}, originLookups: map[string]int{}},
		llm: &termsModelFake{},
	}
	merger := NewTermsMerger(env.dict)
	merger.now = func() time.Time { return termsTestNow }
	env.svc, _ = newTermsTestService(&termsRepoFake{}, env.llm, merger, cfg)
	env.svc.SetVocabularyRefresher(func(_ context.Context, ownerID string) { env.refreshed = append(env.refreshed, ownerID) })
	env.svc.SetNewsReader(TermsNewsDeps{Documents: env.store, Store: env.store})
	return env
}

// doc adds a published, searched news document crawled `minutes` after the
// window opened: bbbb2222 from global source srcB, any other from srcA.
func (e *newsTestEnv) doc(hash, title, summary string, minutes int) *model.CrawledDocument {
	source := "srcA"
	if hash == "bbbb2222" {
		source = "srcB"
	}
	return e.docFrom(source, hash, title, summary, minutes)
}

func (e *newsTestEnv) docFrom(source, hash, title, summary string, minutes int) *model.CrawledDocument {
	e.store.docs = append(e.store.docs, model.CrawledDocument{
		DocHash: hash, SourceID: source, Type: "news", Title: title, Summary: summary, URL: "https://news.example/" + hash,
		CrawledAt: newsTestWindowEnd - 24*3600 + int64(minutes)*60, QualityStatus: "PUBLISHED", IngestSource: "search",
	})
	return &e.store.docs[len(e.store.docs)-1]
}

// logs returns termKey's Common audit rows.
func (e *newsTestEnv) logs(display string) []model.DictTermLog {
	var out []model.DictTermLog
	for _, row := range e.dict.at(model.PrefixDictCommon).logs {
		if row.TermKey == TermKey(display) {
			out = append(out, row)
		}
	}
	return out
}

func (e *newsTestEnv) common(display string) (model.DictTerm, bool) {
	return e.dict.at(model.PrefixDictCommon).term(display)
}

// newsOutput is one model answer naming display once per document ID, with
// quote as the evidence.
func newsOutput(display, quote string, docIDs ...string) string {
	var evidence []string
	for _, id := range docIDs {
		evidence = append(evidence, fmt.Sprintf(`{"source":"document","documentId":%q,"quote":%q}`, id, quote))
	}
	return fmt.Sprintf(`{"terms":[{"displayAs":%q,"heardAs":["알파그리드"],"type":"product","phrase":"","confidence":0.95,"evidence":[%s]}]}`,
		display, strings.Join(evidence, ","))
}

func (e *newsTestEnv) run(t *testing.T) {
	t.Helper()
	if err := e.svc.ProcessNewsDaily(context.Background(), newsTestDay); err != nil {
		t.Fatal(err)
	}
}

// No other partition than DICT#COMMON is ever written by news (R6).
func (e *newsTestEnv) onlyCommon(t *testing.T) {
	t.Helper()
	for pk, store := range e.dict {
		if pk != model.PrefixDictCommon && (len(store.terms) != 0 || len(store.obs) != 0) {
			t.Fatalf("news wrote %s", pk)
		}
	}
	if len(e.refreshed) != 0 {
		t.Fatalf("news must not rebuild per-user vocabularies: %v", e.refreshed)
	}
}

func TestNewsSingleMentionNotAdded(t *testing.T) {
	env := newNewsTestEnv(newsOn())
	env.doc("aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 10)
	env.doc("bbbb2222", "K-culture 행사", "올해 행사가 서울에서 열립니다.", 20)
	env.llm.outputs = []string{newsOutput("AlphaGrid", "AlphaGrid 플랫폼이", "news-1")}
	env.run(t)

	if _, ok := env.common("AlphaGrid"); ok {
		t.Fatal("a term in one document on one day must not reach Common")
	}
	if len(env.store.windows) != 1 || env.store.windows[0] != [2]int64{newsTestWindowEnd - 24*3600, newsTestWindowEnd} {
		t.Fatalf("window %v, want the UTC day ending at 21:00", env.store.windows)
	}
	h, ok := env.store.history[TermKey("AlphaGrid")]
	if !ok || strings.Join(h.Days, ",") != newsTestDay || strings.Join(h.Sources, ",") != newsTestDay+"#srcA" {
		t.Fatalf("candidate history %+v", h)
	}
	if h.PK != model.PrefixDictCommon || h.SK != model.TermsNewsSK(TermKey("AlphaGrid")) {
		t.Fatalf("history key %s/%s", h.PK, h.SK)
	}
	if want := termsTestNow.Add(14 * 24 * time.Hour).Unix(); h.ExpiresAt != want {
		t.Fatalf("history expires %d, want %d", h.ExpiresAt, want)
	}
	// The request carries titles and summaries only.
	req := fmt.Sprint(env.llm.requests)
	if !strings.Contains(req, "AlphaGrid 출시") || strings.Contains(req, "news.example") {
		t.Fatalf("request text %s", req)
	}
	env.onlyCommon(t)
}

func TestNewsTwoDocsAdded(t *testing.T) {
	env := newNewsTestEnv(newsOn())
	env.doc("aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 10)
	env.doc("bbbb2222", "업계 동향", "여러 기업이 AlphaGrid 도입을 검토합니다.", 20)
	env.llm.outputs = []string{newsOutput("AlphaGrid", "AlphaGrid", "news-1", "news-2")}
	env.run(t)

	term, ok := env.common("AlphaGrid")
	// One hit and one observation per day, however many documents named it.
	if !ok || term.State != model.TermStateActive || term.Origin != model.TermOriginAuto || term.Hits != 1 {
		t.Fatalf("common term %+v", term)
	}
	if term.CreatedBy != model.DictNewsActor || term.UpdatedBy != model.DictNewsActor {
		t.Fatalf("news writes are system writes: createdBy=%q updatedBy=%q", term.CreatedBy, term.UpdatedBy)
	}
	// The creation is audited; the second document's merge is not (R14).
	if logs := env.logs("AlphaGrid"); len(logs) != 1 || logs[0].Actor != model.DictNewsActor || logs[0].Action != model.DictLogActionCreate ||
		logs[0].Before != "" || !strings.Contains(logs[0].After, `"DisplayAs":"AlphaGrid"`) {
		t.Fatalf("create audit %+v", logs)
	}
	if len(term.Sources) != 1 || term.Sources[0].Kind != model.DictSourceNews || term.Sources[0].Ref != newsTestDay {
		t.Fatalf("a Common source is the news day: %+v", term.Sources)
	}
	if len(term.HeardAs) != 0 || term.FirstSourceMeetingID != "" {
		t.Fatalf("no transcript grounds aliases and no meeting is the source: %+v", term)
	}
	for _, obs := range env.dict.at(model.PrefixDictCommon).obs {
		if obs.SourceKind != model.DictSourceNews || obs.MeetingID != "" || obs.OwnerID != model.DictNewsActor {
			t.Fatalf("observation %+v", obs)
		}
	}
	env.onlyCommon(t)

	// The same day again: the run row short-circuits the model call.
	calls := len(env.llm.requests)
	env.run(t)
	if len(env.llm.requests) != calls {
		t.Fatal("an unchanged day must not call the model again")
	}
	if term2, _ := env.common("AlphaGrid"); term2.Hits != 1 {
		t.Fatalf("rerun counted again: %+v", term2)
	}
}

func TestNewsTwoDaysAdded(t *testing.T) {
	// Today the term is in one srcA document; prior sightings are day#source.
	cases := []struct {
		name  string
		prior []string
		want  bool
	}{
		{"another source three days ago", []string{"2026-10-15#srcB"}, true},
		{"another source six days ago", []string{"2026-10-12#srcB"}, true},
		{"another source eight days ago", []string{"2026-10-10#srcB"}, false},
		{"the same source three days ago", []string{"2026-10-15#srcA"}, false},
		{"another source only today before", []string{newsTestDay + "#srcB"}, false},
		{"another source only on a later day", []string{"2026-10-20#srcB"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newNewsTestEnv(newsOn())
			key := TermKey("AlphaGrid")
			env.store.history[key] = model.TermsNewsHistory{PK: model.PrefixDictCommon, SK: model.TermsNewsSK(key), TermKey: key, Sources: tc.prior}
			env.doc("aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 10)
			env.llm.outputs = []string{newsOutput("AlphaGrid", "AlphaGrid 플랫폼이", "news-1")}
			env.run(t)
			term, ok := env.common("AlphaGrid")
			if ok != tc.want {
				t.Fatalf("added=%v, want %v (%+v)", ok, tc.want, term)
			}
			if ok && (term.Hits != 1 || term.Sources[0].Ref != newsTestDay) {
				t.Fatalf("term %+v", term)
			}
			h := env.store.history[key]
			if !strings.Contains(strings.Join(h.Sources, ","), newsTestDay+"#srcA") {
				t.Fatalf("today not recorded: %+v", h)
			}
			if tc.prior[0] > newsTestDay && strings.Join(h.Sources, ",") != newsTestDay+"#srcA,"+tc.prior[0] {
				t.Fatalf("a later day must survive a re-run of an earlier one: %+v", h.Sources)
			}
			for _, e := range h.Sources {
				if e < "2026-10-12" {
					t.Fatalf("days older than the 7-day window are dropped: %+v", h.Sources)
				}
			}
			env.onlyCommon(t)
		})
	}
}

func TestNewsCommonCapEvictsOldestAuto(t *testing.T) {
	env := newNewsTestEnv(newsOn())
	common := env.dict.at(model.PrefixDictCommon)
	old := termsTestNow.Add(-90 * 24 * time.Hour).Format(model.DictTimeLayout)
	for i := 0; i < MaxAutoTermsPerScope; i++ {
		display := fmt.Sprintf("Seed%03d", i)
		seen := termsTestNow.Add(-time.Duration(i+1) * time.Hour).Format(model.DictTimeLayout)
		if i == 7 {
			seen = old // the oldest unedited auto term
		}
		key := TermKey(display)
		common.terms[model.DictTermSK(key)] = model.DictTerm{PK: model.PrefixDictCommon, SK: model.DictTermSK(key), TermKey: key, DisplayAs: display,
			State: model.TermStateActive, Origin: model.TermOriginAuto, Hits: 1, LastSeenAt: seen, Version: 1, Scope: model.TermScopeCommon}
	}
	edited := TermKey("EditedOld")
	common.terms[model.DictTermSK(edited)] = model.DictTerm{PK: model.PrefixDictCommon, SK: model.DictTermSK(edited), TermKey: edited, DisplayAs: "EditedOld",
		State: model.TermStateActive, Origin: model.TermOriginAuto, Hits: 0, LastSeenAt: old, UserEditedAt: old, Version: 1}

	env.doc("aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 10)
	env.doc("bbbb2222", "업계 동향", "여러 기업이 AlphaGrid 도입을 검토합니다.", 20)
	env.llm.outputs = []string{newsOutput("AlphaGrid", "AlphaGrid", "news-1", "news-2")}
	env.run(t)

	if _, ok := env.common("AlphaGrid"); !ok {
		t.Fatal("new term missing")
	}
	if _, ok := common.term("Seed007"); ok {
		t.Fatal("the oldest unedited auto term should be evicted")
	}
	if _, ok := common.term("EditedOld"); !ok {
		t.Fatal("a user-edited Common term is never evicted")
	}
	if logs := env.logs("Seed007"); len(logs) != 1 || logs[0].Actor != model.DictNewsActor || logs[0].Action != model.DictLogActionEvict ||
		!strings.Contains(logs[0].Before, `"DisplayAs":"Seed007"`) || logs[0].After != "" {
		t.Fatalf("evict audit %+v", logs)
	}
	auto := 0
	for _, row := range common.terms {
		if evictable(row) {
			auto++
		}
	}
	if auto != MaxAutoTermsPerScope {
		t.Fatalf("auto terms %d, want %d", auto, MaxAutoTermsPerScope)
	}
}

func TestNewsKeepsUserEditedCommonTerm(t *testing.T) {
	env := newNewsTestEnv(newsOn())
	common := env.dict.at(model.PrefixDictCommon)
	key := TermKey("AlphaGrid")
	edited := model.DictTerm{PK: model.PrefixDictCommon, SK: model.DictTermSK(key), TermKey: key, DisplayAs: "AlphaGrid", Phrase: "Alpha Grid",
		State: model.TermStateActive, Origin: model.TermOriginAuto, Hits: 4, UserEditedAt: "2026-10-01T00:00:00.000000Z", UpdatedBy: "u1", Version: 3}
	common.terms[edited.SK] = edited
	env.doc("aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 10)
	env.doc("bbbb2222", "업계 동향", "여러 기업이 AlphaGrid 도입을 검토합니다.", 20)
	env.llm.outputs = []string{newsOutput("AlphaGrid", "AlphaGrid", "news-1", "news-2")}
	env.run(t)
	if got, _ := env.common("AlphaGrid"); got.Version != 3 || got.UpdatedBy != "u1" || got.Hits != 4 || len(common.obs) != 0 {
		t.Fatalf("a user-edited Common term must not be overridden: %+v obs=%d", got, len(common.obs))
	}
}

func TestNewsSkipsUnpublishedDocuments(t *testing.T) {
	env := newNewsTestEnv(newsOn())
	env.doc("aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 10)
	for i, status := range []string{"REJECTED", "DELETED", "FAILED"} {
		env.doc(fmt.Sprintf("rej%05d", i), "AlphaGrid 소식", "AlphaGrid 관련 기사입니다.", 20+i).QualityStatus = status
	}
	// Legacy rows carry neither a quality status nor an ingest source, so a
	// custom URL cannot be ruled out.
	legacy := env.docFrom("srcB", "legacy01", "AlphaGrid 이전", "AlphaGrid 이전 형식 기사입니다.", 30)
	legacy.QualityStatus, legacy.IngestSource = "", ""
	env.docFrom("srcB", "bad/hash", "AlphaGrid 키", "AlphaGrid 잘못된 키입니다.", 40)
	env.doc("bbbb2222", "업계 동향", "여러 기업이 AlphaGrid 도입을 검토합니다.", 50)
	env.llm.outputs = []string{newsOutput("AlphaGrid", "AlphaGrid", "news-1", "news-2")}
	env.run(t)
	req := fmt.Sprint(env.llm.requests)
	if strings.Contains(req, "AlphaGrid 소식") || strings.Contains(req, "잘못된 키") || strings.Contains(req, "AlphaGrid 이전") {
		t.Fatalf("rejected, legacy or malformed documents reached the model: %s", req)
	}
	term, ok := env.common("AlphaGrid")
	if !ok || term.Hits != 1 {
		t.Fatalf("published documents of two sources count: %+v", term)
	}
}

func TestNewsDisabledAndUnconfigured(t *testing.T) {
	env := newNewsTestEnv(termsOn) // NEWS_TERMS_MODE off
	env.doc("aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 10)
	env.run(t)
	if len(env.store.windows) != 0 || len(env.llm.requests) != 0 {
		t.Fatal("a disabled run must not read documents or call the model")
	}

	bare, _ := newTermsTestService(&termsRepoFake{}, &termsModelFake{}, NewTermsMerger(partitionedDictStore{}), newsOn())
	if err := bare.ProcessNewsDaily(context.Background(), newsTestDay); err != nil {
		t.Fatalf("an unconfigured run is skipped: %v", err)
	}
	on := newNewsTestEnv(newsOn())
	if err := on.svc.ProcessNewsDaily(context.Background(), "2026-13-40"); err != nil || len(on.store.windows) != 0 {
		t.Fatalf("an invalid day is dropped: %v", err)
	}
	on.store.listErr = errors.New("query failed")
	if err := on.svc.ProcessNewsDaily(context.Background(), newsTestDay); err == nil {
		t.Fatal("a read failure is retryable")
	}
}

func TestNewsDayFor(t *testing.T) {
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 10, 18, 21, 0, 3, 0, time.UTC), "2026-10-18"},
		{time.Date(2026, 10, 18, 20, 58, 0, 0, time.UTC), "2026-10-18"}, // within the grace
		{time.Date(2026, 10, 19, 2, 0, 0, 0, time.UTC), "2026-10-18"},   // a late retry
		{time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC), "2026-10-17"},
	}
	for _, tc := range cases {
		if got := NewsDayFor(tc.at); got != tc.want {
			t.Fatalf("NewsDayFor(%s) = %s, want %s", tc.at, got, tc.want)
		}
	}
}

// News writes Common only, as the system actor, keyed by a public document
// hash; Common takes no other source, and news reaches no other scope (R6).
func TestMergeScopedNewsOnlyIntoCommon(t *testing.T) {
	news := TermsSourceRef{Kind: model.DictSourceNews, Ref: "aaaa1111", OwnerID: model.DictNewsActor}
	store := newMemDictStore()
	res, err := newTestMerger(store).MergeScoped(context.Background(), TermsScope{PK: model.PrefixDictCommon}, []ExtractedTerm{cand("AlphaGrid")}, news)
	if err != nil || res.Counters.Created != 1 {
		t.Fatalf("news into Common: err=%v", err)
	}
	bad := []struct {
		name  string
		scope TermsScope
		ref   TermsSourceRef
	}{
		{"news into personal", TermsScope{PK: "USER#owner"}, TermsSourceRef{Kind: model.DictSourceNews, Ref: "aaaa1111", OwnerID: "owner"}},
		{"news into account", accountScope("a1", "owner"), TermsSourceRef{Kind: model.DictSourceNews, Ref: "aaaa1111", OwnerID: "owner"}},
		{"news with a member check", TermsScope{PK: model.PrefixDictCommon, Member: &repository.MembershipCheck{AccountID: "a1", UserID: "owner"}}, news},
		{"news by a user", TermsScope{PK: model.PrefixDictCommon}, TermsSourceRef{Kind: model.DictSourceNews, Ref: "aaaa1111", OwnerID: "owner"}},
		{"news with a key-like ref", TermsScope{PK: model.PrefixDictCommon}, TermsSourceRef{Kind: model.DictSourceNews, Ref: "kb/owner/1_a.md", OwnerID: model.DictNewsActor}},
		{"kb into common", TermsScope{PK: model.PrefixDictCommon}, TermsSourceRef{Kind: model.DictSourceKB, Ref: "kb/owner/1_a.md", OwnerID: "owner"}},
		{"meeting into common", TermsScope{PK: model.PrefixDictCommon}, meetingSource("m1")},
	}
	for _, tc := range bad {
		if _, err := newTestMerger(newMemDictStore()).MergeScoped(context.Background(), tc.scope, []ExtractedTerm{cand("AlphaGrid")}, tc.ref); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s must be refused: %v", tc.name, err)
		}
	}
}

func TestNewsMergeRecordsSystemActor(t *testing.T) {
	env := newNewsTestEnv(newsOn())
	key := TermKey("AlphaGrid")
	env.dict.at(model.PrefixDictCommon).terms[model.DictTermSK(key)] = model.DictTerm{PK: model.PrefixDictCommon, SK: model.DictTermSK(key), TermKey: key,
		DisplayAs: "AlphaGrid", State: model.TermStateActive, Origin: model.TermOriginAuto, Hits: 1, UpdatedBy: "earlier", Version: 1,
		SourceTypes: []string{model.TermSourceDocument}}
	env.doc("aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 10)
	env.doc("bbbb2222", "업계 동향", "여러 기업이 AlphaGrid 도입을 검토합니다.", 20)
	env.llm.outputs = []string{newsOutput("AlphaGrid", "AlphaGrid", "news-1", "news-2")}
	env.run(t)
	if got, _ := env.common("AlphaGrid"); got.Hits != 2 || got.UpdatedBy != model.DictNewsActor {
		t.Fatalf("merged term %+v", got)
	}
}

func TestNewsTermsModeFromEnv(t *testing.T) {
	for value, want := range map[string]string{"": SourceTermsModeOff, "on": SourceTermsModeOn, " on ": SourceTermsModeOn, "true": SourceTermsModeOff} {
		t.Setenv("NEWS_TERMS_MODE", value)
		if got := TermsConfigFromEnv().NewsMode; got != want {
			t.Fatalf("NEWS_TERMS_MODE=%q gave %q", value, got)
		}
	}
}

func TestNewsSameSourceTwiceNotAdded(t *testing.T) {
	env := newNewsTestEnv(newsOn())
	env.docFrom("srcA", "aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 10)
	env.docFrom("srcA", "aaaa2222", "업계 동향", "여러 기업이 AlphaGrid 도입을 검토합니다.", 20)
	env.llm.outputs = []string{newsOutput("AlphaGrid", "AlphaGrid", "news-1", "news-2")}
	env.run(t)
	if _, ok := env.common("AlphaGrid"); ok {
		t.Fatal("two documents of one source must not qualify a term")
	}
	if env.store.lookups["srcA"] != 1 {
		t.Fatalf("a source is looked up once per run: %v", env.store.lookups)
	}
}

// Documents from user-configured URLs or from a source users subscribe to
// never feed Common (R15).
func TestNewsCustomSourceExcluded(t *testing.T) {
	env := newNewsTestEnv(newsOn())
	env.doc("aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 10)
	env.doc("bbbb2222", "고객 소식", "고객사 AlphaGrid 계약 내용입니다.", 20).IngestSource = "custom"
	env.docFrom("userSrc", "cccc3333", "AlphaGrid 검토", "구독자가 찾은 AlphaGrid 기사입니다.", 30)
	env.llm.outputs = []string{newsOutput("AlphaGrid", "AlphaGrid 플랫폼이", "news-1")}
	env.run(t)
	req := fmt.Sprint(env.llm.requests)
	if strings.Contains(req, "계약 내용") || strings.Contains(req, "구독자가") {
		t.Fatalf("custom or user-source text reached the model: %s", req)
	}
	if _, ok := env.common("AlphaGrid"); ok {
		t.Fatal("only one global source named the term")
	}
	h := env.store.history[TermKey("AlphaGrid")]
	if strings.Join(h.Sources, ",") != newsTestDay+"#srcA" {
		t.Fatalf("history may hold global sources only: %+v", h.Sources)
	}
	env.onlyCommon(t)
}

func TestNewsUnknownSourceKindExcluded(t *testing.T) {
	env := newNewsTestEnv(newsOn())
	env.docFrom("srcX", "aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 10)
	env.docFrom("srcY", "bbbb2222", "업계 동향", "여러 기업이 AlphaGrid 도입을 검토합니다.", 20)
	env.run(t)
	if len(env.llm.requests) != 0 || len(env.store.history) != 0 || len(env.dict.at(model.PrefixDictCommon).terms) != 0 {
		t.Fatal("documents of sources that are not known to be global are excluded")
	}

	env.store.lookErr = errors.New("read failed")
	if err := env.svc.ProcessNewsDaily(context.Background(), newsTestDay); err == nil {
		t.Fatal("a source lookup failure is retryable, never a pass")
	}
	if len(env.llm.requests) != 0 {
		t.Fatal("nothing runs before the sources are known")
	}
}

func TestNewsCreateAuditTrimmedToTwenty(t *testing.T) {
	env := newNewsTestEnv(newsOn())
	common := env.dict.at(model.PrefixDictCommon)
	key := TermKey("AlphaGrid")
	for i := 0; i < model.MaxDictLogsPerTerm; i++ {
		at := fmt.Sprintf("2026-09-%02dT00:00:00.000000Z", i+1)
		common.logs[model.DictLogSK(key, at)] = model.DictTermLog{PK: model.PrefixDictCommon, SK: model.DictLogSK(key, at), TermKey: key, Actor: "u1", Action: model.DictLogActionDelete, At: at}
	}
	env.doc("aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 10)
	env.doc("bbbb2222", "업계 동향", "여러 기업이 AlphaGrid 도입을 검토합니다.", 20)
	env.llm.outputs = []string{newsOutput("AlphaGrid", "AlphaGrid", "news-1", "news-2")}
	env.run(t)
	logs := env.logs("AlphaGrid")
	if len(logs) != model.MaxDictLogsPerTerm {
		t.Fatalf("audit rows %d, want %d", len(logs), model.MaxDictLogsPerTerm)
	}
	if _, ok := common.logs[model.DictLogSK(key, "2026-09-01T00:00:00.000000Z")]; !ok {
		t.Fatal("the oldest audit row is pinned (R17)")
	}
	if _, ok := common.logs[model.DictLogSK(key, "2026-09-02T00:00:00.000000Z")]; ok {
		t.Fatal("the oldest unpinned audit row should be trimmed")
	}
}

func TestNewsReplayOfADayIsANoOp(t *testing.T) {
	env := newNewsTestEnv(newsOn())
	env.doc("aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 10)
	env.doc("bbbb2222", "업계 동향", "여러 기업이 AlphaGrid 도입을 검토합니다.", 20)
	out := newsOutput("AlphaGrid", "AlphaGrid", "news-1", "news-2")
	env.llm.outputs = []string{out, out}
	env.run(t)
	delete(env.store.runs, newsTestDay) // a failed run row: the day is processed again
	env.run(t)
	if term, ok := env.common("AlphaGrid"); !ok || term.Hits != 1 {
		t.Fatalf("a replayed day must not recount the term: %+v", term)
	}
}

func TestNewsTermSurvivesASaturatedCommonLayer(t *testing.T) {
	env := newNewsTestEnv(newsOn())
	common := env.dict.at(model.PrefixDictCommon)
	for i := 0; i < MaxAutoTermsPerScope; i++ {
		key := TermKey(fmt.Sprintf("Incumbent%03d", i))
		common.terms[model.DictTermSK(key)] = model.DictTerm{PK: model.PrefixDictCommon, SK: model.DictTermSK(key), TermKey: key,
			DisplayAs: fmt.Sprintf("Incumbent%03d", i), State: model.TermStateActive, Origin: model.TermOriginAuto, Hits: 3, Version: 1,
			LastSeenAt: "2026-10-17T09:00:00.000000Z", SourceTypes: []string{model.TermSourceDocument}}
	}
	env.doc("aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 10)
	env.doc("bbbb2222", "업계 동향", "여러 기업이 AlphaGrid 도입을 검토합니다.", 20)
	env.llm.outputs = []string{newsOutput("AlphaGrid", "AlphaGrid", "news-1", "news-2")}
	env.run(t)
	if _, ok := env.common("AlphaGrid"); !ok {
		t.Fatal("a term the day's news confirmed must not be evicted by the merge that adds it")
	}
	live := 0
	for _, row := range common.terms {
		if row.State == model.TermStateActive {
			live++
		}
	}
	if live != MaxAutoTermsPerScope {
		t.Fatalf("the cap still holds: %d live terms", live)
	}
}

// A user source that crawled a shared URL first must not hide the global
// source's copy of the same article.
func TestNewsUserCopyOfASharedArticleDoesNotHideTheGlobalCopy(t *testing.T) {
	env := newNewsTestEnv(newsOn())
	env.docFrom("userSrc", "aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 5)
	env.docFrom("srcA", "aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 10)
	env.docFrom("srcB", "bbbb2222", "업계 동향", "여러 기업이 AlphaGrid 도입을 검토합니다.", 20)
	env.llm.outputs = []string{newsOutput("AlphaGrid", "AlphaGrid", "news-1", "news-2")}
	env.run(t)
	if _, ok := env.common("AlphaGrid"); !ok {
		t.Fatal("two global sources named the term; the user copy must not veto it")
	}
}

// With NEWS_TERMS_SOURCES=all every searched source is eligible for Common
// (no global-source lookup); custom-URL documents and the repeat threshold
// still apply.
func TestNewsAllSourcesMode(t *testing.T) {
	cfg := newsOn()
	cfg.NewsSources = NewsTermsSourcesAll
	env := newNewsTestEnv(cfg)
	env.docFrom("userSrc1", "aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 10)
	env.docFrom("userSrc2", "bbbb2222", "업계 동향", "여러 기업이 AlphaGrid 도입을 검토합니다.", 20)
	env.docFrom("userSrc3", "cccc3333", "고객 소식", "고객사 BetaMesh 계약 내용입니다.", 30).IngestSource = "custom"
	env.llm.outputs = []string{newsOutput("AlphaGrid", "AlphaGrid", "news-1", "news-2")}
	env.run(t)
	if _, ok := env.common("AlphaGrid"); !ok {
		t.Fatal("two subscribed sources named the term; all mode must add it to Common")
	}
	if len(env.store.lookups) != 0 {
		t.Fatalf("all mode never looks up global sources: %v", env.store.lookups)
	}
	if strings.Contains(fmt.Sprint(env.llm.requests), "계약 내용") {
		t.Fatal("custom-URL text must not reach the model in all mode")
	}
	env.onlyCommon(t)
}

func TestNewsAllSourcesModeSingleSourceNotAdded(t *testing.T) {
	cfg := newsOn()
	cfg.NewsSources = NewsTermsSourcesAll
	env := newNewsTestEnv(cfg)
	env.docFrom("userSrc1", "aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 10)
	env.docFrom("userSrc1", "aaaa2222", "업계 동향", "여러 기업이 AlphaGrid 도입을 검토합니다.", 20)
	env.llm.outputs = []string{newsOutput("AlphaGrid", "AlphaGrid", "news-1", "news-2")}
	env.run(t)
	if _, ok := env.common("AlphaGrid"); ok {
		t.Fatal("one source alone never qualifies a term, in any mode")
	}
}

func TestNewsTermsSourcesFromEnv(t *testing.T) {
	for value, want := range map[string]string{"": NewsTermsSourcesGlobal, "global": NewsTermsSourcesGlobal, "all": NewsTermsSourcesAll, " all ": NewsTermsSourcesAll, "ALL": NewsTermsSourcesGlobal, "on": NewsTermsSourcesGlobal} {
		t.Setenv("NEWS_TERMS_SOURCES", value)
		if got := TermsConfigFromEnv().NewsSources; got != want {
			t.Fatalf("NEWS_TERMS_SOURCES=%q gave %q", value, got)
		}
	}
}

// Account-owned (customer-only) sources never feed Common, in either mode:
// ownership follows the Insights rule (a CONFIG accountId, or a row-less
// acct_ ID), is checked before the global marker, and is resolved once per
// distinct source.
func TestNewsAccountOwnedSourcesExcluded(t *testing.T) {
	const custA, custB = "acct_acc1_aaaa0000", "acct_acc1_bbbb0000"
	type source struct {
		id      string
		row     bool   // a CONFIG row exists
		account string // its accountId
		global  bool   // marked global (global mode)
	}
	cases := []struct {
		name      string
		all       bool
		sources   [2]source
		output    string // model answer; "" means no call is expected
		wantAdded bool
		wantHist  string
		wantNoGlb []string // sources IsGlobalNewsSource must not be asked about
	}{
		{name: "all mode, mixed day: account row plus a common source",
			all: true, sources: [2]source{{id: "srcA"}, {id: "custSrc", row: true, account: "acc1"}},
			output: newsOutput("AlphaGrid", "AlphaGrid", "news-1"), wantHist: newsTestDay + "#srcA"},
		{name: "global mode, account source marked global",
			sources: [2]source{{id: "srcA", row: true, global: true}, {id: custA, row: true, account: "acc1", global: true}},
			output:  newsOutput("AlphaGrid", "AlphaGrid", "news-1"), wantHist: newsTestDay + "#srcA", wantNoGlb: []string{custA}},
		{name: "all mode, row-less acct_ ID",
			all: true, sources: [2]source{{id: "srcA"}, {id: custA}},
			output: newsOutput("AlphaGrid", "AlphaGrid", "news-1"), wantHist: newsTestDay + "#srcA"},
		{name: "all mode, row-less other IDs are kept",
			all: true, sources: [2]source{{id: "userSrc1"}, {id: "userSrc2"}},
			output: newsOutput("AlphaGrid", "AlphaGrid", "news-1", "news-2"), wantAdded: true,
			wantHist: newsTestDay + "#userSrc1," + newsTestDay + "#userSrc2"},
		{name: "all mode, two account-owned sources alone",
			all: true, sources: [2]source{{id: custA, row: true, account: "acc1"}, {id: custB}}},
		{name: "global mode, two account-owned sources marked global",
			sources:   [2]source{{id: custA, row: true, account: "acc1", global: true}, {id: custB, row: true, account: "acc2", global: true}},
			wantNoGlb: []string{custA, custB}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newsOn()
			if tc.all {
				cfg.NewsSources = NewsTermsSourcesAll
			}
			env := newNewsTestEnv(cfg)
			env.store.global = map[string]bool{}
			for _, src := range tc.sources {
				if src.row {
					env.store.accounts[src.id] = src.account
				}
				env.store.global[src.id] = src.global
			}
			env.docFrom(tc.sources[0].id, "aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 10)
			env.docFrom(tc.sources[1].id, "bbbb2222", "업계 동향", "여러 기업이 AlphaGrid 도입을 검토합니다.", 20)
			env.docFrom(tc.sources[1].id, "cccc3333", "업계 동향 2", "AlphaGrid 후속 기사입니다.", 30)
			if tc.output != "" {
				env.llm.outputs = []string{tc.output}
			}
			env.run(t)

			if _, ok := env.common("AlphaGrid"); ok != tc.wantAdded {
				t.Fatalf("added=%v, want %v", ok, tc.wantAdded)
			}
			if tc.output == "" && len(env.llm.requests) != 0 {
				t.Fatal("no document of an account-owned source may reach the model")
			}
			if h := env.store.history[TermKey("AlphaGrid")]; strings.Join(h.Sources, ",") != tc.wantHist {
				t.Fatalf("history %v, want %q", h.Sources, tc.wantHist)
			}
			for _, src := range tc.sources {
				if env.store.originLookups[src.id] != 1 {
					t.Fatalf("source %s ownership looked up %d times, want once", src.id, env.store.originLookups[src.id])
				}
			}
			for _, id := range tc.wantNoGlb {
				if env.store.lookups[id] != 0 {
					t.Fatalf("an account-owned source is excluded before the global check: %v", env.store.lookups)
				}
			}
			if tc.all && len(env.store.lookups) != 0 {
				t.Fatalf("all mode never looks up global sources: %v", env.store.lookups)
			}
			env.onlyCommon(t)
		})
	}
}

// A sighting an account-owned source left in the history before this rule
// neither counts toward the 7-day threshold nor is written back.
func TestNewsAccountOwnedHistorySightingsDropped(t *testing.T) {
	cfg := newsOn()
	cfg.NewsSources = NewsTermsSourcesAll
	env := newNewsTestEnv(cfg)
	env.store.accounts["custSrc"] = "acc1"
	prev := "2026-10-16"
	key := TermKey("AlphaGrid")
	env.store.history[key] = model.TermsNewsHistory{TermKey: key, Days: []string{prev},
		Sources: []string{prev + "#acct_acc1_aaaa0000", prev + "#custSrc"}}
	env.docFrom("srcA", "aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 10)
	env.llm.outputs = []string{newsOutput("AlphaGrid", "AlphaGrid", "news-1")}
	env.run(t)
	if _, ok := env.common("AlphaGrid"); ok {
		t.Fatal("account-owned history sightings must not complete the two-day, two-source threshold")
	}
	if h := env.store.history[key]; strings.Join(h.Sources, ",") != newsTestDay+"#srcA" {
		t.Fatalf("history %v keeps account-owned sightings", h.Sources)
	}

	// A common source's past sighting still counts.
	env = newNewsTestEnv(cfg)
	env.store.history[key] = model.TermsNewsHistory{TermKey: key, Days: []string{prev}, Sources: []string{prev + "#srcB"}}
	env.docFrom("srcA", "aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 10)
	env.llm.outputs = []string{newsOutput("AlphaGrid", "AlphaGrid", "news-1")}
	env.run(t)
	if _, ok := env.common("AlphaGrid"); !ok {
		t.Fatal("two days and two common sources still qualify a term")
	}
}

// An ownership lookup failure fails the run (retryable, SOURCE_READ_FAILED)
// and is never taken as "not account-owned".
func TestNewsSourceOriginLookupFailure(t *testing.T) {
	cases := []struct {
		name    string
		all     bool
		only    string
		history bool
		wantLLM bool
	}{
		{name: "all mode, today's source", all: true},
		{name: "global mode, today's source"},
		{name: "history source", all: true, only: "srcOld", history: true, wantLLM: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newsOn()
			if tc.all {
				cfg.NewsSources = NewsTermsSourcesAll
			}
			env := newNewsTestEnv(cfg)
			env.store.originErr, env.store.originErrOnly = errors.New("read failed"), tc.only
			key := TermKey("AlphaGrid")
			if tc.history {
				env.store.history[key] = model.TermsNewsHistory{TermKey: key, Days: []string{"2026-10-16"}, Sources: []string{"2026-10-16#srcOld"}}
			}
			env.doc("aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 10)
			env.doc("bbbb2222", "업계 동향", "여러 기업이 AlphaGrid 도입을 검토합니다.", 20)
			env.llm.outputs = []string{newsOutput("AlphaGrid", "AlphaGrid", "news-1", "news-2")}
			if err := env.svc.ProcessNewsDaily(context.Background(), newsTestDay); err == nil {
				t.Fatal("an ownership lookup failure must fail the run")
			}
			if got := len(env.llm.requests) != 0; got != tc.wantLLM {
				t.Fatalf("model called=%v, want %v", got, tc.wantLLM)
			}
			if _, ok := env.common("AlphaGrid"); ok {
				t.Fatal("nothing is merged when a source cannot be resolved")
			}
			if _, ok := env.store.runs[newsTestDay]; ok {
				t.Fatal("a failed run must not record the day")
			}
			if !tc.history && env.store.puts != 0 {
				t.Fatal("no history is written before the sources are known")
			}
		})
	}
}

func TestAccountOwnedNewsSource(t *testing.T) {
	for _, tc := range []struct {
		id, account string
		found, want bool
	}{
		{"acct_acc1_aaaa0000", "acc1", true, true},
		{"srcA", "acc1", true, true}, // a row's accountId decides, whatever the ID
		{"acct_acc1_aaaa0000", "", true, false},
		{"acct_acc1_aaaa0000", "", false, true},
		{"srcA", "", false, false},
		{"srcA", "", true, false},
	} {
		if got := accountOwnedNewsSource(tc.id, tc.account, tc.found); got != tc.want {
			t.Fatalf("accountOwnedNewsSource(%q, %q, %v) = %v, want %v", tc.id, tc.account, tc.found, got, tc.want)
		}
	}
}
