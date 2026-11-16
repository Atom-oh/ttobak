package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

// Synthetic fixtures only.

type fakeSpokenStore struct {
	rows    map[string]model.SpokenForm // termKey -> row
	listErr error
	puts    int
}

func (f *fakeSpokenStore) ListSpokenForms(context.Context, string) ([]model.SpokenForm, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []model.SpokenForm
	for _, row := range f.rows {
		out = append(out, row)
	}
	return out, nil
}

func (f *fakeSpokenStore) PutSpokenForms(_ context.Context, rows []model.SpokenForm) error {
	f.puts++
	for _, row := range rows {
		f.rows[row.TermKey] = row
	}
	return nil
}

type fakeSpokenModel struct {
	answers map[string][]string
	asked   [][]string
	err     error
}

func (f *fakeSpokenModel) GenerateKoSpokenForms(_ context.Context, terms []string) (map[string][]string, error) {
	f.asked = append(f.asked, append([]string(nil), terms...))
	if f.err != nil {
		return nil, f.err
	}
	out := map[string][]string{}
	for _, term := range terms {
		if forms, ok := f.answers[term]; ok {
			out[term] = forms
		}
	}
	return out, nil
}

func spokenFixture(t *testing.T, cfg SpokenFormConfig) (*builderFixture, *fakeSpokenStore, *fakeSpokenModel) {
	fx := newBuilderFixture(t, "a")
	store := &fakeSpokenStore{rows: map[string]model.SpokenForm{}}
	llm := &fakeSpokenModel{answers: map[string][]string{
		"Claude Code": {"클로드 코드"}, "plan mode": {"플랜 모드", "플랜모드"}, "S3": {"에스쓰리"},
		"Bad": {"not hangul", "한"}, "Service": {"서비스"},
	}}
	fx.svc.SetSpokenForms(store, llm, cfg)
	return fx, store, llm
}

func koTable(fx *builderFixture) string {
	// The newest table is the largest: these tests only add terms.
	table := ""
	for key, body := range fx.objects.puts {
		if strings.HasPrefix(key, "vocab/") && len(body) > len(table) {
			table = body
		}
	}
	return table
}

func TestSpokenFormsAreGeneratedOnAWriteAndCached(t *testing.T) {
	fx, store, llm := spokenFixture(t, SpokenFormConfig{Batch: 10, Timeout: time.Second, WritesOnly: true})
	ctx := context.Background()
	for _, display := range []string{"Claude Code", "plan mode", "Bad", "또박 노트"} {
		if _, err := fx.svc.CreateTerm(ctx, "u1", model.CreateDictTermRequest{DisplayAs: display}); err != nil {
			t.Fatal(display, err)
		}
	}
	// The first write's own build already carries its generated form.
	if table := koTable(fx); !strings.Contains(table, "클로드-코드\t\t\tClaude Code\n") {
		t.Fatalf("table lacks the generated row:\n%s", table)
	}
	// Later forms wait for the next build but are already served to captions.
	// Two spacings of one pronunciation are one form.
	fx.svc.SetLiveVocabulary(true)
	resp, err := fx.svc.GetDictionaryWithTerms(ctx, "u1")
	if err != nil || len(resp.LiveReplacements) != 2 ||
		!strings.Contains(fmt.Sprint(resp.LiveReplacements), "{플랜 모드 plan mode}") || !strings.Contains(fmt.Sprint(resp.LiveReplacements), "{클로드 코드 Claude Code}") {
		t.Fatalf("live replacements: %v %+v", err, resp.LiveReplacements)
	}
	// A term already in Hangul is never sent; an unusable answer is cached as none.
	for _, batch := range llm.asked {
		for _, term := range batch {
			if term == "또박 노트" {
				t.Fatal("a Hangul term was sent to the model")
			}
		}
	}
	empty := 0
	for _, row := range store.rows {
		if len(row.Forms) == 0 {
			empty++
		}
	}
	if len(store.rows) != 3 || empty != 1 {
		t.Fatalf("an unusable answer must be cached as empty: %+v", store.rows)
	}
	// Every term is asked once, however many writes follow.
	asked := 0
	for _, batch := range llm.asked {
		asked += len(batch)
	}
	if asked != 3 {
		t.Fatalf("asked %d terms in %v, want each of three once", asked, llm.asked)
	}
}

func TestSpokenFormsReadsNeverGenerateOnTheAPI(t *testing.T) {
	fx, store, llm := spokenFixture(t, SpokenFormConfig{Batch: 10, Timeout: time.Second, WritesOnly: true})
	ctx := context.Background()
	fx.store.terms[model.DictTermSK("claudecode")] = model.DictTerm{PK: "USER#u1", SK: model.DictTermSK("claudecode"), TermKey: "claudecode",
		DisplayAs: "Claude Code", Phrase: "Claude-Code", Origin: model.TermOriginManual, State: model.TermStateActive, TranscribeKo: true, Version: 1}
	fx.svc.SetLiveVocabulary(true)
	resp, err := fx.svc.GetDictionaryWithTerms(ctx, "u1")
	if err != nil || len(llm.asked) != 0 || len(resp.LiveReplacements) != 0 {
		t.Fatalf("a read must not call the model: %v asked=%v", err, llm.asked)
	}
	// A cached form (written by the background builder) is used by the read.
	store.rows["claudecode"] = model.SpokenForm{TermKey: "claudecode", Forms: []string{"클로드 코드"}}
	resp, err = fx.svc.GetDictionaryWithTerms(ctx, "u1")
	if err != nil || len(resp.LiveReplacements) != 1 || resp.LiveReplacements[0].Heard != "클로드 코드" {
		t.Fatalf("cached forms must reach live replacements: %v %+v", err, resp.LiveReplacements)
	}
}

func TestSpokenFormsBackgroundBatchAndFailure(t *testing.T) {
	fx, store, llm := spokenFixture(t, SpokenFormConfig{Batch: 2, Timeout: time.Second})
	for _, display := range []string{"Claude Code", "plan mode", "S3"} {
		key := NormalizeTermKey(display)
		fx.store.terms[model.DictTermSK(key)] = model.DictTerm{PK: "USER#u1", SK: model.DictTermSK(key), TermKey: key, DisplayAs: display,
			Phrase: TranscribePhrase(display), Origin: model.TermOriginAuto, State: model.TermStateActive, TranscribeKo: true, Version: 1,
			AddedAt: "2026-10-01T00:00:00.00000" + string(rune('0'+len(fx.store.terms))) + "Z"}
	}
	// A model failure is not cached and does not stop the build.
	llm.err = errors.New("synthetic")
	if _, warnings := fx.ensure(false); len(warnings) != 0 || len(store.rows) != 0 || koTable(fx) == "" {
		t.Fatalf("failure: warnings=%v cached=%v table=%q", warnings, store.rows, koTable(fx))
	}
	llm.err, llm.asked = nil, nil
	fx.ensure(false)
	if len(llm.asked) != 1 || len(llm.asked[0]) != 2 || len(store.rows) != 2 {
		t.Fatalf("one batch of two per call: asked=%v cached=%d", llm.asked, len(store.rows))
	}
	fx.ensure(false)
	if len(store.rows) != 3 {
		t.Fatalf("the next call takes the rest: %d", len(store.rows))
	}
}

func TestSpokenFormsSkipCommonTermsAndOptOuts(t *testing.T) {
	personal := []model.DictTerm{
		{TermKey: "claudecode", DisplayAs: "Claude Code", Phrase: "Claude-Code", Origin: model.TermOriginAuto, State: model.TermStateActive, TranscribeKo: true},
		{TermKey: "optout", DisplayAs: "Opt Out", Phrase: "", Origin: model.TermOriginManual, State: model.TermStateActive},
	}
	common := []model.DictTerm{{TermKey: "service", DisplayAs: "Service", Phrase: "Service", Origin: model.TermOriginAuto, State: model.TermStateActive, TranscribeKo: true}}
	layers := []vocabLayer{{name: "personal-auto", rows: personal}, {name: "common", rows: common}}
	if got := spokenFormCandidates(layers, map[string][]string{}); len(got) != 1 || got[0].DisplayAs != "Claude Code" {
		t.Fatalf("candidates: %+v", got)
	}
	attachSpokenForms(layers, map[string][]string{"claudecode": {"클로드 코드"}, "service": {"서비스"}, "optout": {"옵트 아웃"}})
	table, _ := RenderVocabularyTSV(layeredVocabularyEntries(layers, model.VocabLangKo))
	if !strings.Contains(table, "클로드-코드\t\t\tClaude Code\n") || strings.Contains(table, "서비스") || strings.Contains(table, "옵트") {
		t.Fatalf("table:\n%s", table)
	}
}

func TestParseSpokenFormsAnswer(t *testing.T) {
	terms := []string{"Claude Code", "plan mode", "Service"}
	answer := "```json\n{\"items\":[{\"i\":0,\"ko\":[\"클로드 코드\",\"클로드 코드\",\"Claude\",\"클\"]},{\"i\":1,\"ko\":[\"플랜 모드\",\"플랜모드\"]},{\"i\":9,\"ko\":[\"없음\"]},{\"i\":2,\"ko\":[\"회의\"]}]}\n```"
	got, err := parseSpokenFormsAnswer(answer, terms)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got["Claude Code"], ",") != "클로드 코드" || strings.Join(got["plan mode"], ",") != "플랜 모드" {
		t.Fatalf("got %v", got)
	}
	// A blocklisted everyday word is dropped; the term is still answered (as none).
	if forms, ok := got["Service"]; !ok || len(forms) != 0 {
		t.Fatalf("blocklisted form must be dropped: %v", got)
	}
	if _, err := parseSpokenFormsAnswer("sorry", terms); err == nil {
		t.Fatal("an answer with no JSON must fail, so nothing is cached")
	}
}

func TestSpokenFormsCoverTermsKoreanCannotRecognizeByPhrase(t *testing.T) {
	// An automatic acronym keeps a dotted phrase ko-KR rejects (TranscribeKo
	// false): it is exactly the term that needs a spoken form.
	rows := []model.DictTerm{
		{TermKey: "eks", DisplayAs: "EKS", Phrase: "E.K.S.", Origin: model.TermOriginAuto, State: model.TermStateActive, TranscribeEn: true, AddedAt: "2"},
		{TermKey: "claudecode", DisplayAs: "Claude Code", Phrase: "Claude-Code", Origin: model.TermOriginManual, State: model.TermStateActive, TranscribeKo: true, AddedAt: "3"},
	}
	layers := []vocabLayer{{name: "personal-auto", rows: rows}}
	got := spokenFormCandidates(layers, map[string][]string{})
	if len(got) != 2 || got[0].DisplayAs != "Claude Code" || got[1].DisplayAs != "EKS" {
		t.Fatalf("candidates in table priority order (manual first): %+v", got)
	}
	attachSpokenForms(layers, map[string][]string{"eks": {"이케이에스"}})
	table, _ := RenderVocabularyTSV(layeredVocabularyEntries(layers, model.VocabLangKo))
	if !strings.Contains(table, "이케이에스\t\t\tEKS\n") || strings.Contains(table, "E.K.S.") {
		t.Fatalf("table:\n%s", table)
	}
}

func TestSpokenFormsSkipGenerationNearTheDeadline(t *testing.T) {
	fx, store, llm := spokenFixture(t, SpokenFormConfig{Batch: 10, Timeout: 45 * time.Second, MinRemaining: 3 * time.Minute})
	fx.store.terms[model.DictTermSK("claudecode")] = model.DictTerm{PK: "USER#u1", SK: model.DictTermSK("claudecode"), TermKey: "claudecode",
		DisplayAs: "Claude Code", Phrase: "Claude-Code", Origin: model.TermOriginAuto, State: model.TermStateActive, TranscribeKo: true, Version: 1}
	short, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, warnings := fx.svc.EnsureVocabularies(short, "u1", model.VocabContextPersonal, false); len(warnings) != 0 || len(llm.asked) != 0 {
		t.Fatalf("too little time left: the build runs, the model is not called: warnings=%v asked=%v", warnings, llm.asked)
	}
	long, cancelLong := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancelLong()
	fx.svc.EnsureVocabularies(long, "u1", model.VocabContextPersonal, false)
	if len(llm.asked) != 1 || len(store.rows) != 1 {
		t.Fatalf("enough time left: asked=%v cached=%d", llm.asked, len(store.rows))
	}
}

func TestSpokenFormsCacheReadFailureSkipsTheBuild(t *testing.T) {
	fx, store, _ := spokenFixture(t, SpokenFormConfig{Batch: 10, Timeout: time.Second})
	fx.store.terms[model.DictTermSK("claudecode")] = model.DictTerm{PK: "USER#u1", SK: model.DictTermSK("claudecode"), TermKey: "claudecode",
		DisplayAs: "Claude Code", Phrase: "Claude-Code", Origin: model.TermOriginAuto, State: model.TermStateActive, TranscribeKo: true, Version: 1}
	store.listErr = errors.New("synthetic")
	// A table planned without the cached forms would differ from the active
	// one and trigger a rebuild, then another to restore: skip instead.
	if _, warnings := fx.ensure(false); len(warnings) != 1 || koTable(fx) != "" {
		t.Fatalf("warnings=%v table=%q", warnings, koTable(fx))
	}
}
