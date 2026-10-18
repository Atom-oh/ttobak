package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/transcribe"
	tt "github.com/aws/aws-sdk-go-v2/service/transcribe/types"
	smithy "github.com/aws/smithy-go"

	"github.com/ttobak/backend/internal/model"
)

// Synthetic fixtures only.

type fakeObjects struct{ puts map[string]string }

func (o *fakeObjects) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	body, _ := io.ReadAll(in.Body)
	o.puts[aws.ToString(in.Key)] = string(body)
	return &s3.PutObjectOutput{}, nil
}

func (o *fakeObjects) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	delete(o.puts, aws.ToString(in.Key))
	return &s3.DeleteObjectOutput{}, nil
}

type fakeVocabulary struct {
	state tt.VocabularyState
	lang  tt.LanguageCode
	uri   string
}

// fakeTranscribe: unknown names are BadRequest on Get, NotFound on Delete.
type fakeTranscribe struct {
	vocabs    map[string]*fakeVocabulary
	calls     []string
	createErr error // one-shot
}

func apiError(code string) error { return &smithy.GenericAPIError{Code: code, Message: "synthetic"} }

func (f *fakeTranscribe) GetVocabulary(_ context.Context, in *transcribe.GetVocabularyInput, _ ...func(*transcribe.Options)) (*transcribe.GetVocabularyOutput, error) {
	v, ok := f.vocabs[aws.ToString(in.VocabularyName)]
	if !ok {
		return nil, apiError("BadRequestException")
	}
	return &transcribe.GetVocabularyOutput{VocabularyName: in.VocabularyName, VocabularyState: v.state}, nil
}

func (f *fakeTranscribe) CreateVocabulary(_ context.Context, in *transcribe.CreateVocabularyInput, _ ...func(*transcribe.Options)) (*transcribe.CreateVocabularyOutput, error) {
	name := aws.ToString(in.VocabularyName)
	f.calls = append(f.calls, "create:"+name)
	if err := f.createErr; err != nil {
		f.createErr = nil
		return nil, err
	}
	if _, ok := f.vocabs[name]; ok || len(in.Phrases) > 0 {
		return nil, apiError("ConflictException")
	}
	f.vocabs[name] = &fakeVocabulary{state: tt.VocabularyStatePending, lang: in.LanguageCode, uri: aws.ToString(in.VocabularyFileUri)}
	return &transcribe.CreateVocabularyOutput{}, nil
}

func (f *fakeTranscribe) UpdateVocabulary(_ context.Context, in *transcribe.UpdateVocabularyInput, _ ...func(*transcribe.Options)) (*transcribe.UpdateVocabularyOutput, error) {
	name := aws.ToString(in.VocabularyName)
	f.calls = append(f.calls, "update:"+name)
	v, ok := f.vocabs[name]
	if !ok || v.state == tt.VocabularyStatePending {
		return nil, apiError("ConflictException")
	}
	v.state, v.uri = tt.VocabularyStatePending, aws.ToString(in.VocabularyFileUri)
	return &transcribe.UpdateVocabularyOutput{}, nil
}

func (f *fakeTranscribe) DeleteVocabulary(_ context.Context, in *transcribe.DeleteVocabularyInput, _ ...func(*transcribe.Options)) (*transcribe.DeleteVocabularyOutput, error) {
	name := aws.ToString(in.VocabularyName)
	f.calls = append(f.calls, "delete:"+name)
	if _, ok := f.vocabs[name]; !ok {
		return nil, apiError("NotFoundException")
	}
	delete(f.vocabs, name)
	return &transcribe.DeleteVocabularyOutput{}, nil
}

func (f *fakeTranscribe) settle(state tt.VocabularyState) {
	for _, v := range f.vocabs {
		if v.state == tt.VocabularyStatePending {
			v.state = state
		}
	}
}

func (f *fakeTranscribe) count(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

type builderFixture struct {
	store   *fakeDictStore
	tr      *fakeTranscribe
	objects *fakeObjects
	clock   time.Time
	svc     *DictionaryService
}

func newBuilderFixture(t *testing.T, prefix string) *builderFixture {
	fx := &builderFixture{store: migratedStore(), tr: &fakeTranscribe{vocabs: map[string]*fakeVocabulary{}},
		objects: &fakeObjects{puts: map[string]string{}}, clock: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)}
	fx.svc = fx.newService(prefix)
	return fx
}

func (fx *builderFixture) newService(prefix string) *DictionaryService {
	svc := NewDictionaryServiceWithStore(fx.store, fx.tr)
	svc.now = func() time.Time { return fx.clock }
	svc.SetVocabularyBuilder(fx.objects, "assets", 10*time.Minute)
	n := 0
	svc.builder.newName = func() (string, error) { n++; return fmt.Sprintf("ttobak-vocab-%s%015d", prefix, n), nil }
	return svc
}

func (fx *builderFixture) ensure(manual bool) (*model.VocabularyViews, []string) {
	return fx.svc.EnsureVocabularies(context.Background(), "u1", model.VocabContextPersonal, manual)
}

func (fx *builderFixture) ko() model.VocabularyState { return fx.state(model.VocabLangKo) }
func (fx *builderFixture) state(lang string) model.VocabularyState {
	return fx.store.vocab[model.VocabSK(model.VocabContextPersonal, lang)]
}
func (fx *builderFixture) after(d time.Duration) { fx.clock = fx.clock.Add(d) }

func (fx *builderFixture) batch(t *testing.T) string {
	names, err := fx.svc.GetVocabularyForTranscription(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func (fx *builderFixture) add(display, phrase string) {
	key := TermKey(display)
	fx.store.terms[model.DictTermSK(key)] = model.DictTerm{PK: "USER#u1", SK: model.DictTermSK(key), TermKey: key,
		DisplayAs: display, Phrase: phrase, Origin: model.TermOriginManual, State: model.TermStateActive,
		TranscribeKo: true, Version: 1, AddedAt: fmt.Sprintf("2026-10-01T00:00:00.%06dZ", len(fx.store.terms))}
}

func (fx *builderFixture) seedState(st model.VocabularyState, live ...string) {
	st.SK, st.Context, st.Language = model.VocabSK(model.VocabContextPersonal, model.VocabLangKo), model.VocabContextPersonal, model.VocabLangKo
	fx.store.vocab[st.SK] = st
	for _, name := range live {
		fx.tr.vocabs[name] = &fakeVocabulary{state: tt.VocabularyStateReady}
	}
}

func name(prefix string, n int) string { return fmt.Sprintf("ttobak-vocab-%s%015d", prefix, n) }

func TestVocabularyTSVGolden(t *testing.T) {
	row := func(display, phrase, origin, state string, hits int, added int) model.DictTerm {
		return model.DictTerm{SK: "DICTTERM#" + display, DisplayAs: display, Phrase: phrase, Origin: origin, State: state,
			Hits: hits, TranscribeKo: phrase != "", AddedAt: fmt.Sprintf("2026-10-01T00:00:00.%06dZ", added)}
	}
	m, a, on := model.TermOriginManual, model.TermOriginAuto, model.TermStateActive
	rows := []model.DictTerm{
		row("Beta Node", "Beta-Node", a, on, 1, 3), row("Alpha Grid", "Alpha Grid", m, on, 0, 1),
		row("또박 노트", "또박-노트", a, on, 3, 0), row("AlphaGrid", "alpha-grid", a, on, 9, 0), // duplicate phrase
		row("TtobakSync", "Ttobak-Sync", m, on, 0, 2), row("EKS", "E.K.S.", m, on, 0, 4),
		row("S3 Bucket", "S3-Bucket", m, on, 0, 5), row("Delta", "Delta", a, model.TermStateSuggested, 5, 6),
		row("Gone", "Gone", m, model.TermStateDeleted, 0, 7), row("No Phrase", "", m, on, 0, 8),
	}
	head := "Phrase\tSoundsLike\tIPA\tDisplayAs\n"
	for lang, want := range map[string]string{
		model.VocabLangKo: head + "Alpha-Grid\t\t\tAlpha Grid\nTtobak-Sync\t\t\tTtobakSync\n또박-노트\t\t\t또박 노트\nBeta-Node\t\t\tBeta Node\n",
		model.VocabLangEn: head + "Alpha-Grid\t\t\tAlpha Grid\nTtobak-Sync\t\t\tTtobakSync\nE.K.S.\t\t\tEKS\nBeta-Node\t\t\tBeta Node\n",
	} {
		got, n := RenderVocabularyTSV(vocabularyEntries(rows, lang))
		if got != want || n != 4 {
			t.Fatalf("%s table:\n%q\nwant\n%q", lang, got, want)
		}
	}
	if !planVocabulary(rows, model.VocabLangEn).empty || planVocabulary(rows, model.VocabLangKo).empty {
		t.Fatal("en needs five terms, ko one")
	}
	var many []vocabEntry
	for i := 0; i < VocabMaxRows+10; i++ {
		many = append(many, vocabEntry{Phrase: "Term", DisplayAs: "T"})
	}
	if _, n := RenderVocabularyTSV(many); n != VocabMaxRows {
		t.Fatalf("row cap: %d", n)
	}
	long := strings.Repeat("가", 80)
	many = many[:400]
	for i := range many {
		many[i] = vocabEntry{Phrase: long, DisplayAs: long}
	}
	if body, n := RenderVocabularyTSV(many); len(body) > VocabMaxBytes || n == 400 {
		t.Fatalf("byte cap: %d bytes %d rows", len(body), n)
	}
}

func TestValidTranscribePhrase(t *testing.T) {
	for _, tc := range []struct {
		phrase string
		ko, en bool
	}{
		{"Alpha-Grid", true, true}, {"또박-노트", true, false},
		{"E.K.S.", false, true}, // ko-KR dotted acronyms await acceptance
		{"Dynamo-D.B.", false, true}, {"O'Neil", false, true},
		{"AB.C", false, false}, {".NET", false, false}, {"-Alpha", false, false}, {"Alpha-", false, false},
		{"'Alpha", false, false}, {"Alpha'", false, false}, {"Alpha--Grid", false, false}, {"A..B", false, false},
		{"S3", false, false}, {"Alpha Grid", false, false}, {"Alpha\tGrid", false, false}, {"ㄱㄴ", false, false},
		{"", false, false}, {strings.Repeat("a", 256), true, true}, {strings.Repeat("a", 257), false, false},
	} {
		if ValidTranscribePhrase(tc.phrase, model.VocabLangKo) != tc.ko || ValidTranscribePhrase(tc.phrase, model.VocabLangEn) != tc.en {
			t.Errorf("%q: want ko=%v en=%v", tc.phrase, tc.ko, tc.en)
		}
	}
	if TranscribePhrase("  Alpha   Grid ") != "Alpha-Grid" {
		t.Fatal("space runs become one hyphen")
	}
}

func TestEnsureVocabulariesFlipsOnlyOnReady(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	fx.add("Alpha Grid", "Alpha-Grid")
	fx.ensure(true)
	first, second := name("a", 1), name("a", 2)
	if ko := fx.ko(); ko.Status != model.VocabStatusBuilding || ko.InactiveName != first || !ko.BuildSubmitted || ko.ActiveName != "" {
		t.Fatalf("claim and submit the inactive slot: %+v", ko)
	}
	if v := fx.tr.vocabs[first]; v.uri != "s3://assets/vocab/"+first+".tsv" || v.lang != tt.LanguageCodeKoKr ||
		fx.objects.puts["vocab/"+first+".tsv"] != VocabTSVHeader+"Alpha-Grid\t\t\tAlpha Grid\n" {
		t.Fatalf("create from the fixed key: %+v %v", v, fx.objects.puts)
	}
	fx.ensure(false)
	if fx.ko().ActiveName != "" || fx.batch(t) != "" {
		t.Fatal("PENDING is never used")
	}
	fx.tr.settle(tt.VocabularyStateReady)
	views, _ := fx.ensure(false)
	if ko := fx.ko(); ko.ActiveName != first || ko.InactiveName != "" || !ko.ActiveReady || ko.FlippedAt == "" ||
		len(fx.objects.puts) != 0 || views.Ko.Status != model.VocabStatusReady || fx.batch(t) != first {
		t.Fatalf("READY flips and deletes the table: %+v", ko)
	}
	fx.add("Beta Node", "Beta-Node")
	fx.ensure(true)
	if ko := fx.ko(); ko.ActiveName != first || ko.InactiveName != second || fx.tr.count("update:"+first) != 0 || fx.batch(t) != first {
		t.Fatalf("active is never updated in place: %+v", ko)
	}
	fx.tr.settle(tt.VocabularyStateReady)
	fx.ensure(false)
	if ko := fx.ko(); ko.ActiveName != second || ko.InactiveName != first {
		t.Fatalf("old slot kept: %+v", ko)
	}
	fx.after(11 * time.Minute)
	fx.add("Gamma Cloud", "Gamma-Cloud")
	fx.ensure(false)
	if ko := fx.ko(); ko.InactiveName != first || fx.tr.count("update:"+first) != 1 || fx.tr.count("delete:") != 0 {
		t.Fatalf("in grace the inactive slot is the target: %v", fx.tr.calls)
	}
	fx.tr.settle(tt.VocabularyStateReady)
	fx.ensure(false)
	fx.after(24 * time.Hour)
	fx.ensure(false)
	if ko := fx.ko(); ko.ActiveName != first || ko.InactiveName != "" || ko.RetiredName != "" || fx.tr.vocabs[second] != nil {
		t.Fatalf("old slot deleted at 24 h: %+v %v", ko, fx.tr.calls)
	}
}

func TestEnsureVocabulariesFailedKeepsActive(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	fx.add("Alpha Grid", "Alpha-Grid")
	fx.ensure(true)
	fx.tr.settle(tt.VocabularyStateReady)
	fx.ensure(false)
	active := fx.ko().ActiveName
	fx.add("Beta Node", "Beta-Node")
	fx.ensure(true)
	fx.tr.settle(tt.VocabularyStateFailed)
	views, warnings := fx.ensure(false)
	if ko := fx.ko(); ko.Status != model.VocabStatusFailed || ko.FailureCode != "PROVIDER" || ko.ActiveName != active ||
		!views.Ko.ActiveReady || len(warnings) == 0 || len(fx.objects.puts) != 0 || fx.batch(t) != active {
		t.Fatalf("FAILED keeps the active slot: %+v %v", ko, warnings)
	}
	if resp, _ := fx.svc.GetDictionary(context.Background(), "u1"); resp.Status != "READY" || resp.VocabularyName != active {
		t.Fatalf("legacy client keeps the active name: %+v", resp)
	}
	submits := len(fx.tr.calls)
	fx.after(time.Hour)
	if fx.ensure(false); len(fx.tr.calls) != submits {
		t.Fatal("same failing table not rebuilt")
	}
	if fx.ensure(true); fx.tr.count("update:") != 1 {
		t.Fatalf("a manual write retries once: %v", fx.tr.calls)
	}
}

func TestEnsureVocabulariesClaimRace(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	other := fx.newService("b")
	fx.add("Alpha Grid", "Alpha-Grid")
	fx.seedState(model.VocabularyState{Status: model.VocabStatusNone, Version: 1})
	fx.store.hooks["vocabUpdate"] = func() { // B claims between A's read and A's CAS
		other.EnsureVocabularies(context.Background(), "u1", model.VocabContextPersonal, true)
	}
	fx.ensure(true)
	if ko := fx.ko(); ko.InactiveName != name("b", 1) || !ko.BuildSubmitted || fmt.Sprint(fx.tr.calls) != "[create:"+name("b", 1)+"]" {
		t.Fatalf("exactly one claimant builds: %+v %v", ko, fx.tr.calls)
	}
}

func TestEnsureVocabulariesProviderErrors(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	fx.add("Alpha Grid", "Alpha-Grid")
	fx.tr.createErr = apiError("ConflictException")
	fx.ensure(true)
	if ko := fx.ko(); ko.Status != model.VocabStatusBuilding || ko.BuildSubmitted {
		t.Fatalf("ConflictException keeps the claim: %+v", ko)
	}
	if fx.ensure(false); !fx.ko().BuildSubmitted || fx.tr.count("create:") != 2 {
		t.Fatalf("a later call resubmits: %v", fx.tr.calls)
	}

	q := newBuilderFixture(t, "a")
	q.add("Alpha Grid", "Alpha-Grid")
	q.tr.createErr = &tt.LimitExceededException{Message: aws.String("synthetic")}
	views, warnings := q.ensure(true)
	if ko := q.ko(); ko.Status != model.VocabStatusQuotaExceeded || ko.FailureCode != "QUOTA" || views.Ko.Status != model.VocabStatusQuotaExceeded ||
		len(q.objects.puts) != 0 || !strings.Contains(strings.Join(warnings, "|"), "quota") {
		t.Fatalf("LimitExceeded is QUOTA_EXCEEDED: %+v %v", ko, warnings)
	}
	q.after(11 * time.Minute)
	if q.ensure(false); q.ko().Status != model.VocabStatusBuilding {
		t.Fatal("quota retries after the interval")
	}
}

func TestEnsureVocabulariesIntervalBypassAndStaleBuild(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	fx.add("Alpha Grid", "Alpha-Grid")
	fx.ensure(true)
	fx.add("Beta Node", "Beta-Node")
	if fx.ensure(true); !fx.ko().BypassPending {
		t.Fatal("manual write during a build leaves a bypass")
	}
	fx.tr.settle(tt.VocabularyStateReady)
	if fx.ensure(false); fx.ko().Status != model.VocabStatusBuilding || fx.ko().BypassPending || fx.ko().TermCount != 2 {
		t.Fatalf("pending bypass builds once: %+v", fx.ko())
	}
	fx.tr.settle(tt.VocabularyStateReady)
	fx.ensure(false)
	fx.add("Gamma Cloud", "Gamma-Cloud")
	if fx.ensure(false); fx.ko().Status != model.VocabStatusReady {
		t.Fatal("waits for the interval")
	}
	fx.after(10 * time.Minute)
	if fx.ensure(false); fx.ko().Status != model.VocabStatusBuilding {
		t.Fatal("builds after the interval")
	}
	started := fx.ko().BuildStartedAt
	fx.after(31 * time.Minute)
	if fx.ensure(false); fx.ko().BuildStartedAt == started {
		t.Fatal("stale build reclaimed")
	}
}

func TestEnsureVocabulariesRetiresLegacyName(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	legacy := legacyVocabularyName("u1")
	fx.seedState(model.VocabularyState{ActiveName: legacy, ActiveReady: true, Status: model.VocabStatusReady, Version: 2}, legacy)
	fx.add("Alpha Grid", "Alpha-Grid")
	fx.ensure(false)
	fx.tr.settle(tt.VocabularyStateReady)
	if fx.ensure(false); fx.ko().InactiveName != legacy || fx.tr.count("update:"+legacy) != 0 {
		t.Fatalf("legacy demoted, not rebuilt: %+v", fx.ko())
	}
	fx.add("Beta Node", "Beta-Node")
	if fx.ensure(true); fx.ko().Status == model.VocabStatusBuilding {
		t.Fatal("the legacy slot is never a build target")
	}
	fx.after(24 * time.Hour)
	if fx.ensure(false); fx.tr.vocabs[legacy] != nil || fx.ko().Status != model.VocabStatusBuilding || fx.ko().InactiveName == legacy {
		t.Fatalf("legacy deleted after 24 h: %+v", fx.ko())
	}
}

func TestEnsureVocabulariesEnglishThresholdAndEmptyLanguage(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	for _, w := range []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo"} {
		fx.add(w+" Grid", w+"-Grid")
	}
	fx.ensure(true)
	fx.tr.settle(tt.VocabularyStateReady)
	fx.ensure(false)
	en := fx.state(model.VocabLangEn)
	if !en.ActiveReady || en.TermCount != 5 || fx.tr.vocabs[en.ActiveName].lang != tt.LanguageCodeEnUs {
		t.Fatalf("five Latin terms build en-US: %+v", en)
	}
	delete(fx.store.terms, model.DictTermSK(TermKey("Echo Grid")))
	fx.after(time.Minute)
	fx.ensure(true)
	if en = fx.state(model.VocabLangEn); en.ActiveReady || en.Status != model.VocabStatusNone || fx.tr.vocabs[en.ActiveName] == nil {
		t.Fatalf("under five, en stops at once, kept 24 h: %+v", en)
	}
	gone := en.ActiveName
	fx.after(24 * time.Hour)
	if fx.ensure(false); fx.state(model.VocabLangEn).ActiveName != "" || fx.tr.vocabs[gone] != nil {
		t.Fatalf("empty language deleted after 24 h")
	}
}

func TestSharedReadersNeverBuild(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	fx.add("Alpha Grid", "Alpha-Grid")
	if _, err := fx.svc.GetDictionary(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	fx.batch(t)
	plain := NewDictionaryServiceWithStore(fx.store, fx.tr) // no SetVocabularyBuilder: transcribe Lambda
	plain.EnsureVocabularies(context.Background(), "u1", model.VocabContextPersonal, true)
	if len(fx.tr.calls) != 0 || fx.store.vocabCalls != 0 || len(fx.store.vocab) != 0 || len(fx.objects.puts) != 0 {
		t.Fatalf("shared readers must not write: %v", fx.tr.calls)
	}
}

func TestDictionaryGetStartsBuildAndReportsVocabularies(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	fx.add("Alpha Grid", "Alpha-Grid")
	resp, err := fx.svc.GetDictionaryWithTerms(context.Background(), "u1")
	if err != nil || resp.Vocabularies.Ko.Status != model.VocabStatusBuilding || resp.VocabularyName != "" || resp.Status != "PENDING" {
		t.Fatalf("GET builds: %+v %v", resp, err)
	}
	fx.tr.settle(tt.VocabularyStateReady)
	resp, _ = fx.svc.GetDictionaryWithTerms(context.Background(), "u1")
	raw, _ := json.Marshal(resp)
	want := fmt.Sprintf(`"status":"READY","vocabularyName":%q,"vocabularies":{"ko":{"status":"READY","activeReady":true,"termCount":1},"en":{"status":"NONE","activeReady":false,"termCount":0}}`, name("a", 1))
	if !strings.Contains(string(raw), want) {
		t.Fatalf("response = %s", raw)
	}
}
