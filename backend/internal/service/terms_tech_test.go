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

type techStoreFake struct {
	docs    []model.CrawledDocument
	history map[string]model.TermsTechHistory
	runs    map[string]model.TermsTechRun
	windows [][2]int64
	listErr error
}

func (f *techStoreFake) ListTechDocumentsCrawledBetween(_ context.Context, from, to int64) ([]model.CrawledDocument, error) {
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

func (f *techStoreFake) GetTermsTechHistory(_ context.Context, termKey string) (*model.TermsTechHistory, error) {
	if h, ok := f.history[termKey]; ok {
		return &h, nil
	}
	return nil, nil
}

func (f *techStoreFake) PutTermsTechHistory(_ context.Context, h *model.TermsTechHistory) error {
	f.history[h.TermKey] = *h
	return nil
}

func (f *techStoreFake) GetTermsTechRun(_ context.Context, day string) (*model.TermsTechRun, error) {
	if run, ok := f.runs[day]; ok {
		return &run, nil
	}
	return nil, nil
}

func (f *techStoreFake) PutTermsTechRun(_ context.Context, run *model.TermsTechRun) error {
	f.runs[run.Day] = *run
	return nil
}

type techTestEnv struct {
	*newsTestEnv
	tech *techStoreFake
}

func techOn() TermsConfig {
	cfg := termsOn
	cfg.TechMode = SourceTermsModeOn
	return cfg
}

func newTechTestEnv(cfg TermsConfig) *techTestEnv {
	env := &techTestEnv{newsTestEnv: newNewsTestEnv(cfg),
		tech: &techStoreFake{history: map[string]model.TermsTechHistory{}, runs: map[string]model.TermsTechRun{}}}
	env.svc.SetTechReader(TermsTechDeps{Documents: env.tech, Store: env.tech})
	return env
}

// techDoc adds a document the tech crawler wrote `minutes` after the window
// opened, on aws.amazon.com.
func (e *techTestEnv) techDoc(hash, title, summary string, minutes int) *model.CrawledDocument {
	e.tech.docs = append(e.tech.docs, model.CrawledDocument{
		DocHash: hash, SourceID: "__tech__", Type: "tech", Title: title, Summary: summary,
		URL: "https://aws.amazon.com/blogs/aws/" + hash, S3Key: "shared/aws-docs/eks/" + hash + ".md",
		CrawledAt: newsTestWindowEnd - 24*3600 + int64(minutes)*60, InKB: true,
	})
	return &e.tech.docs[len(e.tech.docs)-1]
}

func techOutput(display, quote string, docIDs ...string) string {
	return strings.ReplaceAll(newsOutput(display, quote, docIDs...), `"news-`, `"tech-`)
}

func (e *techTestEnv) runTech(t *testing.T) {
	t.Helper()
	if err := e.svc.ProcessTechDaily(context.Background(), newsTestDay); err != nil {
		t.Fatal(err)
	}
}

func TestTechTwoDocumentsAdded(t *testing.T) {
	env := newTechTestEnv(techOn())
	env.techDoc("aaaa1111", "Amazon AlphaGrid now generally available", "AlphaGrid 서비스가 정식 출시되었습니다.", 10)
	env.techDoc("bbbb2222", "AlphaGrid の新機能", "", 20)
	env.llm.outputs = []string{techOutput("AlphaGrid", "AlphaGrid", "tech-1", "tech-2")}
	env.runTech(t)

	if len(env.tech.windows) != 1 || env.tech.windows[0] != [2]int64{newsTestWindowEnd - 24*3600, newsTestWindowEnd} {
		t.Fatalf("window %v, want the news day", env.tech.windows)
	}
	term, ok := env.common("AlphaGrid")
	if !ok || term.State != model.TermStateActive || term.Origin != model.TermOriginAuto || term.Hits != 1 {
		t.Fatalf("common term %+v", term)
	}
	if term.CreatedBy != model.DictTechActor || term.UpdatedBy != model.DictTechActor {
		t.Fatalf("tech writes are system writes: createdBy=%q updatedBy=%q", term.CreatedBy, term.UpdatedBy)
	}
	if logs := env.logs("AlphaGrid"); len(logs) != 1 || logs[0].Actor != model.DictTechActor || logs[0].Action != model.DictLogActionCreate {
		t.Fatalf("create audit %+v", logs)
	}
	if len(term.Sources) != 1 || term.Sources[0].Kind != model.DictSourceTech || term.Sources[0].Ref != newsTestDay {
		t.Fatalf("a Common source is the tech day: %+v", term.Sources)
	}
	for _, obs := range env.dict.at(model.PrefixDictCommon).obs {
		if obs.SourceKind != model.DictSourceTech || obs.MeetingID != "" || obs.OwnerID != model.DictTechActor {
			t.Fatalf("observation %+v", obs)
		}
	}
	h := env.tech.history[TermKey("AlphaGrid")]
	if h.PK != model.PrefixDictCommon || h.SK != model.TermsTechSK(TermKey("AlphaGrid")) ||
		strings.Join(h.Documents, ",") != newsTestDay+"#aaaa1111,"+newsTestDay+"#bbbb2222" || strings.Join(h.Days, ",") != newsTestDay {
		t.Fatalf("history %+v", h)
	}
	if want := termsTestNow.Add(14 * 24 * time.Hour).Unix(); h.ExpiresAt != want {
		t.Fatalf("history expires %d, want %d", h.ExpiresAt, want)
	}
	if len(env.store.history) != 0 {
		t.Fatal("tech must not write the news history")
	}
	// Titles and summaries only: no URL or body key reaches the model.
	req := fmt.Sprint(env.llm.requests)
	if !strings.Contains(req, "Amazon AlphaGrid now generally available") || strings.Contains(req, "aws.amazon.com") || strings.Contains(req, "shared/aws-docs") {
		t.Fatalf("request text %s", req)
	}
	env.onlyCommon(t)

	calls := len(env.llm.requests)
	env.runTech(t)
	if len(env.llm.requests) != calls {
		t.Fatal("an unchanged day must not call the model again")
	}
	delete(env.tech.runs, newsTestDay) // a failed run row: the day is processed again
	env.llm.outputs = []string{techOutput("AlphaGrid", "AlphaGrid", "tech-1", "tech-2")}
	env.runTech(t)
	if again, _ := env.common("AlphaGrid"); again.Hits != 1 {
		t.Fatalf("a replayed day must not recount the term: %+v", again)
	}
}

func TestTechSingleDocumentNotAdded(t *testing.T) {
	env := newTechTestEnv(techOn())
	env.techDoc("aaaa1111", "Amazon AlphaGrid now generally available", "AlphaGrid 서비스가 출시되었습니다.", 10)
	env.techDoc("bbbb2222", "Amazon S3 update", "S3 기능이 추가되었습니다.", 20)
	env.llm.outputs = []string{techOutput("AlphaGrid", "AlphaGrid", "tech-1")}
	env.runTech(t)
	if _, ok := env.common("AlphaGrid"); ok {
		t.Fatal("a term in one technical document must not reach Common")
	}
	if h := env.tech.history[TermKey("AlphaGrid")]; strings.Join(h.Documents, ",") != newsTestDay+"#aaaa1111" {
		t.Fatalf("candidate history %+v", h)
	}
}

func TestTechDistinctDocumentsAcrossDays(t *testing.T) {
	// Today the term is in one document; prior sightings are day#docHash.
	cases := []struct {
		name  string
		prior []string
		want  bool
	}{
		{"another document three days ago", []string{"2026-10-15#cccc3333"}, true},
		{"another document six days ago", []string{"2026-10-12#cccc3333"}, true},
		{"another document eight days ago", []string{"2026-10-10#cccc3333"}, false},
		{"the same document on an earlier day", []string{"2026-10-15#aaaa1111"}, false},
		{"another document only on a later day", []string{"2026-10-20#cccc3333"}, false},
		{"a malformed entry", []string{"2026-10-15#bad/hash"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTechTestEnv(techOn())
			key := TermKey("AlphaGrid")
			env.tech.history[key] = model.TermsTechHistory{PK: model.PrefixDictCommon, SK: model.TermsTechSK(key), TermKey: key, Documents: tc.prior}
			env.techDoc("aaaa1111", "Amazon AlphaGrid now generally available", "AlphaGrid 서비스가 출시되었습니다.", 10)
			env.llm.outputs = []string{techOutput("AlphaGrid", "AlphaGrid", "tech-1")}
			env.runTech(t)
			term, ok := env.common("AlphaGrid")
			if ok != tc.want {
				t.Fatalf("added=%v, want %v (%+v)", ok, tc.want, term)
			}
			for _, e := range env.tech.history[key].Documents {
				if e < "2026-10-12" || strings.Contains(e, "/") {
					t.Fatalf("old or malformed entries are dropped: %+v", env.tech.history[key].Documents)
				}
			}
			env.onlyCommon(t)
		})
	}
}

// Only the reserved source's well-formed rows on an official AWS host count;
// the crawler's web-search results from other sites never reach the model.
func TestTechExcludesUnofficialAndMalformedDocuments(t *testing.T) {
	env := newTechTestEnv(techOn())
	env.techDoc("aaaa1111", "Amazon AlphaGrid now generally available", "AlphaGrid 서비스가 출시되었습니다.", 10)
	env.techDoc("bbbb2222", "AlphaGrid 상세", "AlphaGrid 상세 문서입니다.", 15).URL = "https://docs.aws.amazon.com/alphagrid/latest/userguide/"
	bad := []func(*model.CrawledDocument){
		func(d *model.CrawledDocument) { d.URL = "https://blogs.oracle.com/alphagrid" },
		func(d *model.CrawledDocument) { d.URL = "http://aws.amazon.com/alphagrid" },
		func(d *model.CrawledDocument) { d.URL = "https://aws.amazon.com.evil.example/alphagrid" },
		func(d *model.CrawledDocument) { d.URL = "https://user@aws.amazon.com/alphagrid" },
		func(d *model.CrawledDocument) { d.URL = "https://aws.amazon.com:8443/alphagrid" },
		func(d *model.CrawledDocument) { d.SourceID = "srcA" },
		func(d *model.CrawledDocument) { d.Type = "news" },
		func(d *model.CrawledDocument) { d.QualityStatus = "REJECTED" },
		func(d *model.CrawledDocument) { d.S3Key = "kb/user-1/" + d.DocHash + ".md" },
		func(d *model.CrawledDocument) { d.S3Key = "shared/aws-docs/eks/other.md" },
		func(d *model.CrawledDocument) { d.S3Key = "shared/aws-docs/../" + d.DocHash + ".md" },
		func(d *model.CrawledDocument) { d.DocHash, d.S3Key = "bad/hash", "shared/aws-docs/eks/bad/hash.md" },
	}
	for i, mutate := range bad {
		mutate(env.techDoc(fmt.Sprintf("rej%05d", i), fmt.Sprintf("제외%02d AlphaGrid", i), "AlphaGrid 제외 문서입니다.", 20+i))
	}
	env.llm.outputs = []string{techOutput("AlphaGrid", "AlphaGrid", "tech-1", "tech-2")}
	env.runTech(t)
	if req := fmt.Sprint(env.llm.requests); strings.Contains(req, "제외") {
		t.Fatalf("an excluded document reached the model: %s", req)
	}
	term, ok := env.common("AlphaGrid")
	if !ok || term.Hits != 1 {
		t.Fatalf("the two official documents count: %+v", term)
	}
	if h := env.tech.history[TermKey("AlphaGrid")]; len(h.Documents) != 2 {
		t.Fatalf("only official documents are recorded: %+v", h.Documents)
	}
}

// A duplicate row of one document counts once.
func TestTechDuplicateDocumentCountsOnce(t *testing.T) {
	env := newTechTestEnv(techOn())
	env.techDoc("aaaa1111", "Amazon AlphaGrid now generally available", "AlphaGrid 서비스가 출시되었습니다.", 10)
	env.techDoc("aaaa1111", "Amazon AlphaGrid now generally available", "AlphaGrid 서비스가 출시되었습니다.", 20)
	env.llm.outputs = []string{techOutput("AlphaGrid", "AlphaGrid", "tech-1")}
	env.runTech(t)
	if _, ok := env.common("AlphaGrid"); ok {
		t.Fatal("one document twice is still one document")
	}
}

// Terms in a script Transcribe cannot take (here Japanese) never reach Common.
func TestTechForeignScriptTermNotAdded(t *testing.T) {
	env := newTechTestEnv(techOn())
	env.techDoc("aaaa1111", "新機能 アルファグリッド", "", 10)
	env.techDoc("bbbb2222", "アルファグリッド の発表", "", 20)
	env.llm.outputs = []string{techOutput("アルファグリッド", "アルファグリッド", "tech-1", "tech-2")}
	env.runTech(t)
	if _, ok := env.common("アルファグリッド"); ok {
		t.Fatal("a non-Korean, non-English term must not reach Common")
	}
	if len(env.tech.history) != 0 {
		t.Fatalf("a foreign term is not a candidate: %+v", env.tech.history)
	}
}

func TestTechKeepsUserEditedCommonTerm(t *testing.T) {
	env := newTechTestEnv(techOn())
	common := env.dict.at(model.PrefixDictCommon)
	key := TermKey("AlphaGrid")
	edited := model.DictTerm{PK: model.PrefixDictCommon, SK: model.DictTermSK(key), TermKey: key, DisplayAs: "AlphaGrid", Phrase: "Alpha Grid",
		State: model.TermStateActive, Origin: model.TermOriginAuto, Hits: 4, UserEditedAt: "2026-10-01T00:00:00.000000Z", UpdatedBy: "u1", Version: 3}
	common.terms[edited.SK] = edited
	env.techDoc("aaaa1111", "Amazon AlphaGrid now generally available", "AlphaGrid 서비스가 출시되었습니다.", 10)
	env.techDoc("bbbb2222", "AlphaGrid 상세", "AlphaGrid 상세 문서입니다.", 20)
	env.llm.outputs = []string{techOutput("AlphaGrid", "AlphaGrid", "tech-1", "tech-2")}
	env.runTech(t)
	if got, _ := env.common("AlphaGrid"); got.Version != 3 || got.UpdatedBy != "u1" || got.Hits != 4 || len(common.obs) != 0 {
		t.Fatalf("a user-edited Common term must not be overridden: %+v obs=%d", got, len(common.obs))
	}
}

// News and tech evidence of the same day are separate observations: each
// job's run counts once, and the merge records the later job's actor.
func TestTechAndNewsSameDayAreSeparateEvidence(t *testing.T) {
	cfg := techOn()
	cfg.NewsMode = SourceTermsModeOn
	env := newTechTestEnv(cfg)
	env.doc("aaaa1111", "AlphaGrid 출시", "AlphaGrid 플랫폼이 공개되었습니다.", 10)
	env.doc("bbbb2222", "업계 동향", "여러 기업이 AlphaGrid 도입을 검토합니다.", 20)
	env.techDoc("cccc3333", "Amazon AlphaGrid now generally available", "AlphaGrid 서비스가 출시되었습니다.", 10)
	env.techDoc("dddd4444", "AlphaGrid 상세", "AlphaGrid 상세 문서입니다.", 20)
	env.llm.outputs = []string{newsOutput("AlphaGrid", "AlphaGrid", "news-1", "news-2"), techOutput("AlphaGrid", "AlphaGrid", "tech-1", "tech-2")}
	env.run(t)
	env.runTech(t)
	term, ok := env.common("AlphaGrid")
	if !ok || term.Hits != 2 || term.UpdatedBy != model.DictTechActor || term.CreatedBy != model.DictNewsActor {
		t.Fatalf("term %+v", term)
	}
	kinds := map[string]bool{}
	for _, src := range term.Sources {
		kinds[src.Kind] = true
	}
	if !kinds[model.DictSourceNews] || !kinds[model.DictSourceTech] {
		t.Fatalf("sources %+v", term.Sources)
	}
}

func TestTechDisabledAndUnconfigured(t *testing.T) {
	env := newTechTestEnv(termsOn) // TECH_TERMS_MODE off
	env.techDoc("aaaa1111", "Amazon AlphaGrid now generally available", "AlphaGrid 서비스가 출시되었습니다.", 10)
	env.runTech(t)
	if len(env.tech.windows) != 0 || len(env.llm.requests) != 0 {
		t.Fatal("a disabled run must not read documents or call the model")
	}
	news := newTechTestEnv(newsOn()) // news on does not enable tech
	news.runTech(t)
	if len(news.tech.windows) != 0 {
		t.Fatal("the news flag must not enable tech")
	}

	bare, _ := newTermsTestService(&termsRepoFake{}, &termsModelFake{}, NewTermsMerger(partitionedDictStore{}), techOn())
	if err := bare.ProcessTechDaily(context.Background(), newsTestDay); err != nil {
		t.Fatalf("an unconfigured run is skipped: %v", err)
	}
	on := newTechTestEnv(techOn())
	if err := on.svc.ProcessTechDaily(context.Background(), "2026-13-40"); err != nil || len(on.tech.windows) != 0 {
		t.Fatalf("an invalid day is dropped: %v", err)
	}
	if err := on.svc.ProcessTechDaily(context.Background(), newsTestDay); err != nil || len(on.llm.requests) != 0 {
		t.Fatalf("a day without documents is done: %v", err)
	}
	on.tech.listErr = errors.New("query failed")
	if err := on.svc.ProcessTechDaily(context.Background(), newsTestDay); err == nil {
		t.Fatal("a read failure is retryable")
	}
}

// A provider failure is retryable and records no run row.
func TestTechProviderFailureIsRetryable(t *testing.T) {
	env := newTechTestEnv(techOn())
	env.techDoc("aaaa1111", "Amazon AlphaGrid now generally available", "AlphaGrid 서비스가 출시되었습니다.", 10)
	env.llm.err = errors.New("throttled")
	if err := env.svc.ProcessTechDaily(context.Background(), newsTestDay); err == nil {
		t.Fatal("a provider failure must be retried")
	}
	if len(env.tech.runs) != 0 {
		t.Fatal("a failed day must stay open")
	}
}

// Tech writes Common only, as its own system actor, for one day.
func TestMergeScopedTechOnlyIntoCommon(t *testing.T) {
	tech := TermsSourceRef{Kind: model.DictSourceTech, Ref: newsTestDay, OwnerID: model.DictTechActor}
	res, err := newTestMerger(newMemDictStore()).MergeScoped(context.Background(), TermsScope{PK: model.PrefixDictCommon}, []ExtractedTerm{cand("AlphaGrid")}, tech)
	if err != nil || res.Counters.Created != 1 {
		t.Fatalf("tech into Common: err=%v", err)
	}
	bad := []struct {
		name  string
		scope TermsScope
		ref   TermsSourceRef
	}{
		{"tech into personal", TermsScope{PK: "USER#owner"}, TermsSourceRef{Kind: model.DictSourceTech, Ref: newsTestDay, OwnerID: "owner"}},
		{"tech into account", accountScope("a1", "owner"), TermsSourceRef{Kind: model.DictSourceTech, Ref: newsTestDay, OwnerID: "owner"}},
		{"tech with a member check", TermsScope{PK: model.PrefixDictCommon, Member: &repository.MembershipCheck{AccountID: "a1", UserID: "owner"}}, tech},
		{"tech by a user", TermsScope{PK: model.PrefixDictCommon}, TermsSourceRef{Kind: model.DictSourceTech, Ref: newsTestDay, OwnerID: "owner"}},
		{"tech as the news actor", TermsScope{PK: model.PrefixDictCommon}, TermsSourceRef{Kind: model.DictSourceTech, Ref: newsTestDay, OwnerID: model.DictNewsActor}},
		{"news as the tech actor", TermsScope{PK: model.PrefixDictCommon}, TermsSourceRef{Kind: model.DictSourceNews, Ref: newsTestDay, OwnerID: model.DictTechActor}},
		{"tech with a hash ref", TermsScope{PK: model.PrefixDictCommon}, TermsSourceRef{Kind: model.DictSourceTech, Ref: "aaaa1111", OwnerID: model.DictTechActor}},
		{"tech tainted", TermsScope{PK: model.PrefixDictCommon}, TermsSourceRef{Kind: model.DictSourceTech, Ref: newsTestDay, OwnerID: model.DictTechActor, Tainted: true}},
	}
	for _, tc := range bad {
		if _, err := newTestMerger(newMemDictStore()).MergeScoped(context.Background(), tc.scope, []ExtractedTerm{cand("AlphaGrid")}, tc.ref); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s must be refused: %v", tc.name, err)
		}
	}
}

func TestTechTermsModeFromEnv(t *testing.T) {
	for value, want := range map[string]string{"": SourceTermsModeOff, "on": SourceTermsModeOn, " on ": SourceTermsModeOn, "true": SourceTermsModeOff, "personal": SourceTermsModeOff} {
		t.Setenv("TECH_TERMS_MODE", value)
		if got := TermsConfigFromEnv().TechMode; got != want {
			t.Fatalf("TECH_TERMS_MODE=%q gave %q", value, got)
		}
	}
}

func TestOfficialAWSDocumentURL(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://aws.amazon.com/blogs/aws/x":            true,
		"https://AWS.amazon.com/about-aws/whats-new/x/": true,
		"https://docs.aws.amazon.com/eks/latest/x.html": true,
		"https://press.aboutamazon.com/x":               false,
		"https://amazon.com/x":                          false,
		"https://evilaws.amazon.com/x":                  false,
		"https://aws.amazon.com.evil.example/x":         false,
		"http://aws.amazon.com/x":                       false,
		"https://aws.amazon.com:443/x":                  false,
		"https://u:p@aws.amazon.com/x":                  false,
		"aws.amazon.com/x":                              false,
		"":                                              false,
	} {
		if got := officialAWSDocumentURL(raw); got != want {
			t.Fatalf("officialAWSDocumentURL(%q) = %v, want %v", raw, got, want)
		}
	}
}
