package service

import (
	"context"
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
	state    tt.VocabularyState
	lang     tt.LanguageCode
	uri      string
	modified time.Time // seeded vocabularies are older than any claim
}

// fakeTranscribe: unknown names are BadRequest on Get, NotFound on Delete.
type fakeTranscribe struct {
	vocabs    map[string]*fakeVocabulary
	calls     []string
	createErr error // one-shot
	now       func() time.Time
}

func apiError(code string) error { return &smithy.GenericAPIError{Code: code, Message: "synthetic"} }

func (f *fakeTranscribe) GetVocabulary(_ context.Context, in *transcribe.GetVocabularyInput, _ ...func(*transcribe.Options)) (*transcribe.GetVocabularyOutput, error) {
	v, ok := f.vocabs[aws.ToString(in.VocabularyName)]
	if !ok {
		return nil, apiError("BadRequestException")
	}
	modified := v.modified
	return &transcribe.GetVocabularyOutput{VocabularyName: in.VocabularyName, VocabularyState: v.state, LastModifiedTime: &modified}, nil
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
	f.vocabs[name] = &fakeVocabulary{state: tt.VocabularyStatePending, lang: in.LanguageCode, uri: aws.ToString(in.VocabularyFileUri), modified: f.now()}
	return &transcribe.CreateVocabularyOutput{}, nil
}

func (f *fakeTranscribe) UpdateVocabulary(_ context.Context, in *transcribe.UpdateVocabularyInput, _ ...func(*transcribe.Options)) (*transcribe.UpdateVocabularyOutput, error) {
	name := aws.ToString(in.VocabularyName)
	f.calls = append(f.calls, "update:"+name)
	v, ok := f.vocabs[name]
	if !ok || v.state == tt.VocabularyStatePending {
		return nil, apiError("ConflictException")
	}
	v.state, v.uri, v.modified = tt.VocabularyStatePending, aws.ToString(in.VocabularyFileUri), f.now()
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
	fx.tr.now = func() time.Time { return fx.clock }
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

func TestEnsureVocabulariesFlipsOnlyOnReady(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	fx.add("Alpha Grid", "Alpha-Grid")
	fx.ensure(true)
	first, second := name("a", 1), name("a", 2)
	ko := fx.ko()
	if ko.Status != model.VocabStatusBuilding || ko.InactiveName != first || !ko.BuildSubmitted || ko.ActiveName != "" || ko.SubmittedAt == "" {
		t.Fatalf("claim and submit the inactive slot: %+v", ko)
	}
	key := "vocab/" + first + "." + ko.BuildingFingerprint + ".tsv"
	if v := fx.tr.vocabs[first]; len(ko.BuildingFingerprint) != 32 || v.uri != "s3://assets/"+key || v.lang != tt.LanguageCodeKoKr ||
		fx.objects.puts[key] != VocabTSVHeader+"Alpha-Grid\t\t\tAlpha Grid\n" {
		t.Fatalf("create from the fingerprint-bound key: %+v %v", v, fx.objects.puts)
	}
	fx.ensure(false)
	if fx.ko().ActiveName != "" || fx.batch(t) != "" {
		t.Fatal("PENDING is never used")
	}
	fx.tr.settle(tt.VocabularyStateReady)
	if fx.batch(t) != first || fx.ko().ActiveName != "" {
		t.Fatal("batch uses a verified READY build before any API call flips it")
	}
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
	if resp, _ := fx.svc.GetDictionary(context.Background(), "u1"); resp.Status != "READY" {
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
	aged := fx.ko()
	calls := len(fx.tr.calls)
	fx.after(31 * time.Minute)
	if fx.ensure(false); fx.ko().BuildStartedAt != aged.BuildStartedAt || fx.ko().InactiveName != aged.InactiveName || len(fx.tr.calls) != calls {
		t.Fatalf("an aged PENDING build is polled, never reclaimed: %+v %v", fx.ko(), fx.tr.calls)
	}
	fx.tr.settle(tt.VocabularyStateReady)
	if fx.ensure(false); fx.ko().ActiveName != aged.InactiveName || fx.ko().ActiveFingerprint != aged.BuildingFingerprint {
		t.Fatalf("the aged build flips when READY: %+v", fx.ko())
	}
}

func TestAgedFirstBuildIsPolledNotDeleted(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	fx.add("Alpha Grid", "Alpha-Grid")
	fx.ensure(true)
	first := name("a", 1)
	fx.after(2 * time.Hour) // FlippedAt is empty: no flip yet
	if fx.ensure(false); fx.ko().InactiveName != first || fx.ko().RetiredName != "" || fx.tr.count("delete:") != 0 || fx.tr.vocabs[first] == nil {
		t.Fatalf("a PENDING first build is never retired: %+v %v", fx.ko(), fx.tr.calls)
	}
	fx.tr.settle(tt.VocabularyStateReady)
	fx.after(25 * time.Hour) // the user returns much later
	if fx.ensure(false); fx.ko().ActiveName != first || fx.tr.count("delete:") != 0 || fx.tr.count("create:") != 1 {
		t.Fatalf("a READY first build flips instead of restarting: %+v %v", fx.ko(), fx.tr.calls)
	}
}

func TestStaleSubmitterNeverOverwritesNewerTable(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	ctx := context.Background()
	plan := func() vocabPlan {
		rows, _ := fx.store.ListDictTerms(ctx, "USER#u1")
		return planVocabulary(rows, model.VocabLangKo)
	}
	fx.add("Alpha Grid", "Alpha-Grid")
	old := plan()
	target := name("a", 9)
	fx.seedState(model.VocabularyState{Status: model.VocabStatusBuilding, BuildingFingerprint: old.fingerprint, InactiveName: target,
		BuildStartedAt: fx.clock.Format(model.DictTimeLayout), Version: 3})
	stale := fx.ko() // request A read F1 and stalls
	fx.add("Beta Node", "Beta-Node")
	fx.ensure(false) // request B advances to F2 and submits
	next := plan()
	newKey := vocabObjectKey(target, next.fingerprint)
	if ko := fx.ko(); ko.BuildingFingerprint != next.fingerprint || !ko.BuildSubmitted || fx.tr.vocabs[target].uri != "s3://assets/"+newKey {
		t.Fatalf("B submits F2: %+v", ko)
	}
	if out := fx.svc.submitBuild(ctx, "u1", &stale, old); !out.more || fx.tr.count("create:")+fx.tr.count("update:") != 1 {
		t.Fatalf("a stale submitter never reaches the provider: %+v %v", out, fx.tr.calls)
	}
	if fx.objects.puts[newKey] != next.body || fx.objects.puts[vocabObjectKey(target, old.fingerprint)] != old.body ||
		fx.tr.vocabs[target].uri != "s3://assets/"+newKey {
		t.Fatalf("the F1 table has its own key: %v", fx.objects.puts)
	}
	fx.tr.settle(tt.VocabularyStateReady)
	if fx.ensure(false); fx.ko().ActiveName != target || fx.ko().ActiveFingerprint != next.fingerprint || fx.ko().TermCount != 2 {
		t.Fatalf("the flip publishes F2 from the F2 table: %+v", fx.ko())
	}
}

func TestSubmitClaimWithoutProviderCallResubmits(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	fx.add("Alpha Grid", "Alpha-Grid")
	rows, _ := fx.store.ListDictTerms(context.Background(), "USER#u1")
	p := planVocabulary(rows, model.VocabLangKo)
	active, slot := name("a", 1), name("a", 2)
	// The claim was recorded, then the Lambda stopped before UpdateVocabulary:
	// the reused slot still shows its older READY build.
	fx.seedState(model.VocabularyState{ActiveName: active, ActiveReady: true, ActiveFingerprint: "old", InactiveName: slot,
		Status: model.VocabStatusBuilding, BuildingFingerprint: p.fingerprint, BuildSubmitted: true,
		BuildStartedAt: fx.clock.Format(model.DictTimeLayout), SubmittedAt: fx.clock.Format(model.DictTimeLayout),
		FlippedAt: fx.clock.Add(-time.Hour).Format(model.DictTimeLayout), Version: 5}, active, slot)
	fx.tr.vocabs[slot].modified = fx.clock.Add(-time.Hour)
	if fx.batch(t) != active {
		t.Fatal("batch never uses the older build as the claimed one")
	}
	if fx.ensure(false); fx.ko().ActiveName != active || fx.tr.count("update:"+slot) != 1 || !fx.ko().BuildSubmitted {
		t.Fatalf("an older READY result resubmits instead of flipping: %+v %v", fx.ko(), fx.tr.calls)
	}
	fx.tr.settle(tt.VocabularyStateReady)
	if fx.ensure(false); fx.ko().ActiveName != slot || fx.ko().ActiveFingerprint != p.fingerprint {
		t.Fatalf("the resubmitted build flips: %+v", fx.ko())
	}
}

func TestEnsureVocabulariesRetiresLegacyName(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	legacy := legacyVocabularyName("u1")
	fx.seedState(model.VocabularyState{ActiveName: legacy, ActiveReady: true, Status: model.VocabStatusReady, Version: 2}, legacy)
	fx.add("Alpha Grid", "Alpha-Grid")
	fx.ensure(false)
	fx.tr.settle(tt.VocabularyStateReady)
	if fx.ensure(false); fx.ko().RetiredName != legacy || fx.ko().InactiveName != "" || fx.tr.count("update:"+legacy) != 0 || fx.tr.vocabs[legacy] == nil {
		t.Fatalf("legacy retired at the first flip, kept for grace: %+v", fx.ko())
	}
	fx.add("Beta Node", "Beta-Node")
	fx.after(time.Minute)
	if _, warnings := fx.ensure(true); fx.ko().Status != model.VocabStatusBuilding || fx.ko().InactiveName != name("a", 2) || len(warnings) != 0 {
		t.Fatalf("a manual write builds a fresh name during the legacy grace: %+v %v", fx.ko(), warnings)
	}
	fx.tr.settle(tt.VocabularyStateReady)
	if fx.ensure(false); fx.ko().ActiveName != name("a", 2) || fx.ko().RetiredName != legacy || fx.tr.vocabs[legacy] == nil {
		t.Fatalf("second flip keeps the waiting legacy name: %+v", fx.ko())
	}
	fx.after(24 * time.Hour)
	if fx.ensure(false); fx.tr.vocabs[legacy] != nil || fx.ko().RetiredName != "" || fx.ko().InactiveName != "" || fx.tr.vocabs[name("a", 1)] != nil {
		t.Fatalf("legacy and demoted slot deleted after 24 h: %+v", fx.ko())
	}
}

func TestEnsureVocabulariesPollErrorWarns(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	fx.add("Alpha Grid", "Alpha-Grid")
	fx.ensure(true)
	fx.svc.transcribeClient = pollErrorTranscribe{fx.tr}
	if _, warnings := fx.ensure(false); len(warnings) == 0 || fx.ko().Status != model.VocabStatusBuilding {
		t.Fatalf("a failing poll surfaces a warning: %v %+v", warnings, fx.ko())
	}
}

type pollErrorTranscribe struct{ *fakeTranscribe }

func (pollErrorTranscribe) GetVocabulary(context.Context, *transcribe.GetVocabularyInput, ...func(*transcribe.Options)) (*transcribe.GetVocabularyOutput, error) {
	return nil, apiError("AccessDeniedException")
}

func TestBatchUsesProviderReadyLegacyBuild(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	legacy := legacyVocabularyName("u1")
	fx.seedState(model.VocabularyState{ActiveName: legacy, Status: model.VocabStatusBuilding, Version: 2}, legacy)
	if got := fx.batch(t); got != legacy {
		t.Fatalf("a provider-READY legacy in-place build is used: %q", got)
	}
	fx.seedState(model.VocabularyState{ActiveName: legacy, Status: model.VocabStatusNone, EmptySince: "2026-10-18T09:00:00Z", Version: 3}, legacy)
	if got := fx.batch(t); got != "" {
		t.Fatalf("an emptied language is never used: %q", got)
	}
}

func TestPendingLegacyBuildBlocksABClaim(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	legacy := legacyVocabularyName("u1")
	fx.seedState(model.VocabularyState{ActiveName: legacy, Status: model.VocabStatusBuilding, Version: 2}, legacy)
	fx.tr.vocabs[legacy].state = tt.VocabularyStatePending
	fx.add("Alpha Grid", "Alpha-Grid")
	if fx.ensure(true); fx.ko().InactiveName != "" || fx.ko().BuildingFingerprint != "" || fx.tr.count("create:") != 0 {
		t.Fatalf("no A/B claim while the legacy build is pending: %+v", fx.ko())
	}
	fx.tr.settle(tt.VocabularyStateReady)
	if fx.ensure(true); !fx.ko().ActiveReady || fx.ko().ActiveName != legacy || fx.ko().InactiveName != name("a", 1) {
		t.Fatalf("the legacy result is recorded, then the A/B build is claimed: %+v", fx.ko())
	}
	fx.tr.settle(tt.VocabularyStateFailed)
	if fx.ensure(false); fx.ko().Status != model.VocabStatusFailed || fx.batch(t) != legacy {
		t.Fatalf("batch keeps the READY legacy vocabulary after the A/B build fails: %+v", fx.ko())
	}
}

func TestEnsureVocabulariesDefersEnglishAndEmptiesLanguage(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	for _, w := range []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo"} {
		fx.add(w+" Grid", w+"-Grid")
	}
	views, _ := fx.ensure(true)
	fx.tr.settle(tt.VocabularyStateReady)
	fx.ensure(false)
	if _, ok := fx.store.vocab[model.VocabSK(model.VocabContextPersonal, model.VocabLangEn)]; ok || len(fx.tr.vocabs) != 1 ||
		views.En.Status != model.VocabStatusNone || fx.tr.vocabs[fx.ko().ActiveName].lang != tt.LanguageCodeKoKr {
		t.Fatalf("en-US has no consumer and is not built: %v", fx.tr.calls)
	}
	for _, w := range []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo"} {
		delete(fx.store.terms, model.DictTermSK(TermKey(w+" Grid")))
	}
	gone := fx.ko().ActiveName
	fx.after(time.Minute)
	fx.ensure(true)
	if ko := fx.ko(); ko.ActiveReady || ko.Status != model.VocabStatusNone || fx.tr.vocabs[gone] == nil || fx.batch(t) != "" {
		t.Fatalf("an emptied language stops at once, kept 24 h: %+v", ko)
	}
	fx.after(24 * time.Hour)
	if fx.ensure(false); fx.ko().ActiveName != "" || fx.tr.vocabs[gone] != nil {
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

// A request that listed a term before another request deleted it and emptied
// the language must not build it back in from the stale listing.
func TestEnsureVocabulariesRelistsAfterEachStateRead(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	fx.add("Alpha Grid", "Alpha-Grid")
	fx.ensure(true)
	fx.tr.settle(tt.VocabularyStateReady)
	fx.ensure(false)
	active := fx.ko().ActiveName
	fx.after(11 * time.Minute)
	other := fx.newService("b")
	fx.store.hooks["list"] = func() { // A has listed Alpha; B deletes it and empties
		delete(fx.store.terms, model.DictTermSK(TermKey("Alpha Grid")))
		other.EnsureVocabularies(context.Background(), "u1", model.VocabContextPersonal, true)
	}
	fx.ensure(true)
	if ko := fx.ko(); ko.ActiveReady || ko.EmptySince == "" || ko.Status != model.VocabStatusNone || ko.InactiveName != "" ||
		fx.tr.count("create:") != 1 || len(fx.objects.puts) != 0 || fx.batch(t) != "" || ko.ActiveName != active {
		t.Fatalf("a deleted term is never rebuilt from a stale listing: %+v %v", ko, fx.tr.calls)
	}
}

// Emptying the dictionary while a submitted build is PENDING stops serving at
// once and the build's later READY result is never flipped or served.
func TestEmptyingDuringSubmittedBuildDropsIt(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	fx.add("Alpha Grid", "Alpha-Grid")
	fx.ensure(true)
	fx.tr.settle(tt.VocabularyStateReady)
	fx.ensure(false)
	active, pending := fx.ko().ActiveName, name("a", 2)
	fx.add("Beta Node", "Beta-Node")
	if fx.ensure(true); fx.ko().InactiveName != pending || !fx.ko().BuildSubmitted {
		t.Fatalf("a submitted build is pending: %+v", fx.ko())
	}
	fx.store.terms = map[string]model.DictTerm{}
	views, _ := fx.ensure(true)
	if ko := fx.ko(); ko.ActiveReady || ko.EmptySince == "" || ko.Status != model.VocabStatusNone || ko.BuildSubmitted ||
		ko.BuildingFingerprint != "" || views.Ko.ActiveReady || fx.batch(t) != "" {
		t.Fatalf("an emptied language stops advertising during a build: %+v", ko)
	}
	fx.tr.settle(tt.VocabularyStateReady)
	if fx.ensure(false); fx.ko().ActiveName != active || fx.ko().ActiveReady || fx.batch(t) != "" {
		t.Fatalf("the obsolete READY result is ignored: %+v", fx.ko())
	}
	fx.after(25 * time.Hour)
	fx.ensure(false)
	if ko := fx.ko(); ko.ActiveName != "" || ko.InactiveName != "" || ko.RetiredName != "" || fx.tr.vocabs[active] != nil || fx.tr.vocabs[pending] != nil {
		t.Fatalf("both slots are deleted after the grace window: %+v %v", ko, fx.tr.calls)
	}
}

// A manual write that loses the claim race keeps its interval bypass.
func TestLostClaimRaceKeepsBypass(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	fx.add("Alpha Grid", "Alpha-Grid")
	fx.ensure(true)
	fx.tr.settle(tt.VocabularyStateReady)
	fx.ensure(false)
	fx.add("Beta Node", "Beta-Node")
	fx.store.hooks["vocabUpdate"] = func() { // a concurrent no-op write moves the version
		st := fx.ko()
		st.Version++
		fx.store.vocab[st.SK] = st
	}
	if fx.ensure(true); fx.ko().Status != model.VocabStatusBuilding || !fx.ko().BuildSubmitted {
		t.Fatalf("the bypass survives a lost claim and the retry builds: %+v", fx.ko())
	}
}
