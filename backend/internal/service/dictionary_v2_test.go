package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/transcribe"
	transcribeTypes "github.com/aws/aws-sdk-go-v2/service/transcribe/types"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// fakeDictStore mirrors the repository's conditions in memory.
type fakeDictStore struct {
	blob        *model.UserDictionary
	terms       map[string]model.DictTerm // SK -> row
	vocab       map[string]model.VocabularyState
	saveCalls   int
	legacyCalls []string
	vocabCalls  int
	putErr      error
	putErrAfter int               // rows written before putErr is returned
	hooks       map[string]func() // one-shot, run before the named write
	cleanupErr  error
	onUpdate    func() // runs after a successful UpdateDictTerm
}

// fire runs and clears the hook for op, simulating a concurrent writer that
// commits just before this write reaches DynamoDB.
func (f *fakeDictStore) fire(op string) {
	if hook := f.hooks[op]; hook != nil {
		delete(f.hooks, op)
		hook()
	}
}

// blobGuard mirrors the migration ConditionCheck on the legacy item.
func (f *fakeDictStore) blobGuard(g repository.LegacyBlobGuard) error {
	if f.blob == nil || f.blob.MigratedAt != "" || f.blob.BlobRevision != g.Revision || f.blob.UpdatedAt != g.UpdatedAt {
		return fmt.Errorf("%w: %w", repository.ErrConditionFailed, repository.ErrLegacyBlobChanged)
	}
	return nil
}

func conflict() error {
	return fmt.Errorf("%w: %w", repository.ErrConditionFailed, repository.ErrDictTermConflict)
}

func newFakeDictStore() *fakeDictStore {
	return &fakeDictStore{terms: map[string]model.DictTerm{}, vocab: map[string]model.VocabularyState{}, hooks: map[string]func(){}}
}

func cond() error { return fmt.Errorf("%w", repository.ErrConditionFailed) }

func (f *fakeDictStore) GetDictionary(context.Context, string) (*model.UserDictionary, error) {
	if f.blob == nil {
		return nil, nil
	}
	copy := *f.blob
	return &copy, nil
}
func (f *fakeDictStore) SaveDictionary(_ context.Context, d *model.UserDictionary) error {
	f.saveCalls++
	if f.blob == nil {
		f.blob = &model.UserDictionary{UserID: d.UserID}
	}
	f.blob.Terms = append([]model.DictionaryTerm(nil), d.Terms...)
	f.blob.VocabularyName, f.blob.VocabularyStatus = d.VocabularyName, d.VocabularyStatus
	f.blob.BlobRevision++
	f.blob.UpdatedAt = fmt.Sprintf("rev-%d", f.blob.BlobRevision)
	return nil
}
func (f *fakeDictStore) UpdateLegacyVocabularyStatus(_ context.Context, _ string, name, from, to string) error {
	f.legacyCalls = append(f.legacyCalls, from+"->"+to)
	if f.blob == nil || f.blob.VocabularyName != name || f.blob.VocabularyStatus != from {
		return cond()
	}
	f.blob.VocabularyStatus = to
	return nil
}
func (f *fakeDictStore) MarkDictionaryMigrated(_ context.Context, _ string, now string, rev int64, updatedAt string) error {
	f.fire("mark")
	if f.blobGuard(repository.LegacyBlobGuard{Revision: rev, UpdatedAt: updatedAt}) != nil {
		return cond()
	}
	f.blob.MigratedAt = now
	return nil
}
func (f *fakeDictStore) ReplaceLegacyMigratedTerm(_ context.Context, g repository.LegacyBlobGuard, _ string, key string, v int64, next *model.DictTerm) error {
	f.fire("replace")
	if err := f.blobGuard(g); err != nil {
		return err
	}
	sk := model.DictTermSK(key)
	row, ok := f.terms[sk]
	if !ok || !pristineLegacyRow(row) || row.Version != v {
		return conflict()
	}
	if next == nil {
		delete(f.terms, sk)
		return nil
	}
	n := *next
	n.Version = v + 1
	f.terms[sk] = n
	return nil
}

// legacySave mimics a legacy PUT: new terms, revision and updatedAt.
func (f *fakeDictStore) legacySave(terms ...model.DictionaryTerm) {
	f.blob.Terms = terms
	f.blob.BlobRevision++
	f.blob.UpdatedAt = fmt.Sprintf("2026-10-18T09:00:%02dZ", f.blob.BlobRevision)
}

func (f *fakeDictStore) phrases(svc *DictionaryService) string {
	resp, err := svc.GetDictionary(context.Background(), "u1")
	if err != nil {
		panic(err)
	}
	var got []string
	for _, term := range resp.Terms {
		got = append(got, term.Phrase+"/"+term.DisplayAs)
	}
	return strings.Join(got, "|")
}
func (f *fakeDictStore) ListDictTerms(context.Context, string) ([]model.DictTerm, error) {
	out := []model.DictTerm{}
	for _, t := range f.terms {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SK < out[j].SK })
	return out, nil
}
func (f *fakeDictStore) PutDictTermsIfAbsent(_ context.Context, g repository.LegacyBlobGuard, terms []model.DictTerm) (int, error) {
	f.fire("put")
	if err := f.blobGuard(g); err != nil {
		return 0, err
	}
	n := 0
	for i, t := range terms {
		if f.putErr != nil && i >= f.putErrAfter {
			return n, f.putErr
		}
		if _, ok := f.terms[t.SK]; ok {
			continue
		}
		f.terms[t.SK] = t
		n++
	}
	return n, nil
}
func (f *fakeDictStore) GetVocabularyState(_ context.Context, _ string, c, l string) (*model.VocabularyState, error) {
	s, ok := f.vocab[model.VocabSK(c, l)]
	if !ok {
		return nil, nil
	}
	return &s, nil
}
func (f *fakeDictStore) CreateLegacyVocabularyState(_ context.Context, g repository.LegacyBlobGuard, s *model.VocabularyState) error {
	f.fire("vocab")
	if err := f.blobGuard(g); err != nil {
		return err
	}
	if _, ok := f.vocab[s.SK]; ok {
		return conflict()
	}
	f.vocab[s.SK] = *s
	return nil
}
func (f *fakeDictStore) ReconcileLegacyVocabularyState(_ context.Context, g repository.LegacyBlobGuard, c, l string, v int64, ch repository.VocabularyStateChange, now string) error {
	if err := f.blobGuard(g); err != nil {
		return err
	}
	s, ok := f.vocab[model.VocabSK(c, l)]
	if !ok || s.Version != v {
		return conflict()
	}
	if ch.ActiveName != nil {
		s.ActiveName = *ch.ActiveName
	}
	if ch.TermCount != nil {
		s.TermCount = *ch.TermCount
	}
	f.vocab[s.SK] = s
	return f.UpdateVocabularyState(context.Background(), "", c, l, v, repository.VocabularyStateChange{Status: ch.Status, ActiveReady: ch.ActiveReady, FailureCode: ch.FailureCode}, now)
}
func (f *fakeDictStore) UpdateVocabularyState(_ context.Context, _ string, c, l string, v int64, ch repository.VocabularyStateChange, _ string) error {
	f.vocabCalls++
	s, ok := f.vocab[model.VocabSK(c, l)]
	if !ok || s.Version != v {
		return cond()
	}
	if ch.Status != nil {
		s.Status = *ch.Status
	}
	if ch.ActiveReady != nil {
		s.ActiveReady = *ch.ActiveReady
	}
	if ch.FailureCode != nil {
		s.FailureCode = *ch.FailureCode
	}
	if ch.ActiveName != nil {
		s.ActiveName = *ch.ActiveName
	}
	if ch.TermCount != nil {
		s.TermCount = *ch.TermCount
	}
	s.Version++
	f.vocab[s.SK] = s
	return nil
}

type fakeVocabClient struct {
	TranscribeVocabularyAPI // writes are not used by these tests
	states                  map[string]transcribeTypes.VocabularyState
	onGet                   func()
}

func (c *fakeVocabClient) GetVocabulary(_ context.Context, in *transcribe.GetVocabularyInput, _ ...func(*transcribe.Options)) (*transcribe.GetVocabularyOutput, error) {
	if c.onGet != nil {
		c.onGet()
	}
	state, ok := c.states[*in.VocabularyName]
	if !ok {
		return nil, errors.New("not found")
	}
	return &transcribe.GetVocabularyOutput{VocabularyName: in.VocabularyName, VocabularyState: state}, nil
}
func newTestDictService(store *fakeDictStore, client TranscribeVocabularyAPI) *DictionaryService {
	svc := NewDictionaryServiceWithStore(store, client)
	svc.now = func() time.Time { return time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC) }
	return svc
}

// Synthetic fixture: no real meeting or user content.
func legacyBlob(status string) *model.UserDictionary {
	return &model.UserDictionary{
		PK: "USER#u1", SK: model.PrefixDictionary, UserID: "u1",
		Terms: []model.DictionaryTerm{
			{Phrase: "Ttobak-Sync", SoundsLike: "또박 싱크", DisplayAs: "TtobakSync"},
			{Phrase: "Alpha-Grid", DisplayAs: "Alpha Grid"},
			{Phrase: "", DisplayAs: "베타 노드"},
		},
		VocabularyName: "ttobak-vocab-0123456789abcdef", VocabularyStatus: status,
	}
}

func TestNormalizeTermKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Amazon Bedrock", "amazonbedrock"},
		{"  amazon-bedrock ", "amazonbedrock"},
		{"Amazon_Bedrock.", "amazonbedrock"},
		{"Ａｍａｚｏｎ　Ｂｅｄｒｏｃｋ", "amazonbedrock"}, // NFKC full width
		{"베드·록", "베드록"},
		{"K8s\tCluster\n", "k8scluster"},
		{"- . _ ·", ""},
	}
	for _, tc := range cases {
		if got := NormalizeTermKey(tc.in); got != tc.want {
			t.Errorf("NormalizeTermKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	hex32 := regexp.MustCompile(`^[0-9a-f]{32}$`)
	if k := TermKey("Amazon Bedrock"); !hex32.MatchString(k) || k != TermKey("amazon-bedrock") {
		t.Fatalf("TermKey must be 32 lowercase hex and normalization-stable, got %q", k)
	}
	if TermKey("Bedrock") == TermKey("Bedrocks") {
		t.Fatal("distinct terms must not share a key")
	}
	if TermKey(" .-") != "" {
		t.Fatal("empty normalized input must have no key")
	}
}

func TestMigrateLegacyDictionaryIsNonDestructiveAndIdempotent(t *testing.T) {
	store := newFakeDictStore()
	store.blob = legacyBlob("READY")
	svc := newTestDictService(store, nil)
	tomb := TermKey("Alpha Grid") // deleted earlier: must stay deleted
	store.terms[model.DictTermSK(tomb)] = model.DictTerm{SK: model.DictTermSK(tomb), TermKey: tomb, State: model.TermStateDeleted, Version: 4}

	store.putErr = errors.New("throttled")
	if _, err := svc.MigrateLegacyDictionary(context.Background(), "u1"); err == nil || store.blob.MigratedAt != "" {
		t.Fatal("a failed copy must not mark the blob migrated")
	}
	store.putErr = nil
	first, err := svc.MigrateLegacyDictionary(context.Background(), "u1")
	if err != nil || first.TermsCreated != 2 || first.AlreadyMigrated {
		t.Fatalf("first run = %+v %v", first, err)
	}
	if store.blob.MigratedAt == "" || len(store.blob.Terms) != 3 || store.blob.VocabularyName == "" {
		t.Fatalf("migration must keep blob terms/vocabulary and set migratedAt: %+v", store.blob)
	}
	if store.saveCalls != 0 {
		t.Fatal("migration must not rewrite the blob")
	}
	for _, row := range store.terms {
		if row.TermKey != tomb && (row.Origin != model.TermOriginManual || row.State != model.TermStateActive || row.Version != 1 || row.TermKey != TermKey(row.DisplayAs)) {
			t.Fatalf("unexpected migrated row %+v", row)
		}
	}
	if store.terms[model.DictTermSK(tomb)].Version != 4 {
		t.Fatal("migration resurrected a tombstone")
	}
	resp, _ := svc.GetDictionary(context.Background(), "u1")
	if len(resp.Terms) != 2 || resp.Terms[0].SoundsLike != "또박 싱크" || resp.Terms[1].DisplayAs != "베타 노드" || resp.Terms[0].DisplayAs != "TtobakSync" {
		t.Fatalf("migrated reader must keep blob order without the tombstone: %+v", resp.Terms)
	}
	state := store.vocab[model.VocabSK(model.VocabContextPersonal, model.VocabLangKo)]
	if state.ActiveName != "ttobak-vocab-0123456789abcdef" || state.Status != model.VocabStatusReady || !state.ActiveReady {
		t.Fatalf("vocabulary state must adopt the legacy name: %+v", state)
	}

	second, err := svc.MigrateLegacyDictionary(context.Background(), "u1")
	if err != nil || !second.AlreadyMigrated || len(store.terms) != 3 {
		t.Fatalf("second run must be a no-op: %+v %v", second, err)
	}
}

func TestGetDictionaryUnionRowsWinUntilMigrated(t *testing.T) {
	store := newFakeDictStore()
	store.blob = legacyBlob("")
	row := func(display, phrase, state, added string) {
		key := TermKey(display)
		store.terms[model.DictTermSK(key)] = model.DictTerm{SK: model.DictTermSK(key), TermKey: key, State: state, DisplayAs: display, Phrase: phrase, AddedAt: added}
	}
	row("Alpha Grid", "Alpha-Grid-2", model.TermStateActive, "2026-10-01T00:00:00.000000Z")
	row("TtobakSync", "", model.TermStateDeleted, "")
	row("Gamma", "Gamma", model.TermStateActive, "2026-10-02T00:00:00.000000Z")
	svc := newTestDictService(store, nil)
	list := func() string {
		resp, err := svc.GetDictionary(context.Background(), "u1")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, term := range resp.Terms {
			got = append(got, term.DisplayAs+"="+term.Phrase)
		}
		return strings.Join(got, "|")
	}
	if got := list(); got != "Alpha Grid=Alpha-Grid-2|베타 노드=|Gamma=Gamma" {
		t.Fatalf("rows must win (tombstones hide), got %q", got)
	}
	store.blob.MigratedAt = "2026-10-03T00:00:00.000000Z"
	if got := list(); got != "Alpha Grid=Alpha-Grid-2|Gamma=Gamma" {
		t.Fatalf("a migrated blob must be ignored, got %q", got)
	}
	if resp, _ := newTestDictService(newFakeDictStore(), nil).GetDictionary(context.Background(), "x"); resp.Terms == nil || resp.Status != "" {
		t.Fatalf("empty response shape changed: %+v", resp)
	}
}

func TestGetDictionaryStatusSyncNeverRewritesBlob(t *testing.T) {
	sk := model.VocabSK(model.VocabContextPersonal, model.VocabLangKo)
	for _, tc := range []struct {
		withVocab bool
		provider  transcribeTypes.VocabularyState
		want      string
	}{
		{false, transcribeTypes.VocabularyStateReady, "READY"},
		{false, transcribeTypes.VocabularyStatePending, "PENDING"},
		{true, transcribeTypes.VocabularyStateReady, "READY"},
		{true, transcribeTypes.VocabularyStateFailed, "FAILED"},
	} {
		store := newFakeDictStore()
		store.blob = legacyBlob("PENDING")
		if tc.withVocab {
			store.vocab[sk] = model.VocabularyState{SK: sk, Context: model.VocabContextPersonal, Language: model.VocabLangKo, ActiveName: store.blob.VocabularyName, Status: model.VocabStatusBuilding, Version: 7}
		}
		client := &fakeVocabClient{states: map[string]transcribeTypes.VocabularyState{store.blob.VocabularyName: tc.provider}}
		resp, err := newTestDictService(store, client).GetDictionary(context.Background(), "u1")
		if err != nil || resp.Status != tc.want || store.saveCalls != 0 {
			t.Fatalf("%+v: status %q err %v saves %d; GetDictionary must never rewrite the blob", tc, resp.Status, err, store.saveCalls)
		}
		changed := tc.want != "PENDING"
		switch {
		case tc.withVocab && (len(store.legacyCalls) != 0 || changed != (store.vocab[sk].Version == 8)):
			t.Fatalf("%+v: VOCAB# is authoritative and synced under its version guard", tc)
		case !tc.withVocab && changed && fmt.Sprint(store.legacyCalls) != "[PENDING->"+tc.want+"]":
			t.Fatalf("%+v: legacy sync must be conditional on the observed status: %v", tc, store.legacyCalls)
		}
	}
}

func TestVocabularySyncLosesToConcurrentWriter(t *testing.T) {
	store := newFakeDictStore()
	store.blob = legacyBlob("PENDING")
	client := &fakeVocabClient{states: map[string]transcribeTypes.VocabularyState{store.blob.VocabularyName: transcribeTypes.VocabularyStateReady}}
	// A concurrent legacy PUT rewrites the blob while the status is polled.
	client.onGet = func() {
		store.blob.Terms = []model.DictionaryTerm{{Phrase: "Delta", DisplayAs: "Delta"}}
		store.blob.VocabularyName = "ttobak-vocab-other"
	}
	if _, err := newTestDictService(store, client).GetDictionary(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	if store.blob.VocabularyStatus != "PENDING" || len(store.blob.Terms) != 1 || store.saveCalls != 0 {
		t.Fatalf("stale status sync overwrote a concurrent writer: %+v", store.blob)
	}

	// VOCAB#: the builder moves the row while the reader polls; the reader
	// must report the builder's state, not its own lost update.
	sk := model.VocabSK(model.VocabContextPersonal, model.VocabLangKo)
	store = newFakeDictStore()
	store.vocab[sk] = model.VocabularyState{SK: sk, Context: model.VocabContextPersonal, Language: model.VocabLangKo, ActiveName: "n1", Status: model.VocabStatusBuilding, Version: 3}
	client = &fakeVocabClient{states: map[string]transcribeTypes.VocabularyState{"n1": transcribeTypes.VocabularyStateReady}}
	client.onGet = func() {
		s := store.vocab[sk]
		s.Status, s.FailureCode, s.Version = model.VocabStatusQuotaExceeded, "QUOTA", 4
		store.vocab[sk] = s
	}
	resp, err := newTestDictService(store, client).GetDictionary(context.Background(), "u1")
	if err != nil || resp.Status != "FAILED" || store.vocab[sk].Version != 4 {
		t.Fatalf("lost CAS must report the winner's state, got %q %v %+v", resp.Status, err, store.vocab[sk])
	}
}

func TestMigrationPinsTheCopiedBlob(t *testing.T) {
	store := newFakeDictStore()
	store.blob = legacyBlob("")
	store.blob.BlobRevision, store.blob.UpdatedAt = 1, "2026-10-18T09:00:01Z"
	svc := newTestDictService(store, nil)
	clock := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { clock = clock.Add(time.Second); return clock }
	// Between the copy and the mark the user adds Gamma and removes Alpha Grid.
	store.hooks["mark"] = func() {
		store.legacySave(store.blob.Terms[0], store.blob.Terms[2], model.DictionaryTerm{Phrase: "Gamma", DisplayAs: "Gamma"})
	}
	if _, err := svc.MigrateLegacyDictionary(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	if store.blob.MigratedAt == "" {
		t.Fatal("migration must finish against the re-read blob")
	}
	if got := store.phrases(svc); got != "Ttobak-Sync/TtobakSync|/베타 노드|Gamma/Gamma" {
		t.Fatalf("migrated rows must match the blob that was marked, got %q", got)
	}
}

func TestPartialMigrationKeepsBlobAuthoritative(t *testing.T) {
	store := newFakeDictStore()
	store.blob = legacyBlob("")
	svc := newTestDictService(store, nil)
	store.putErr, store.putErrAfter = errors.New("throttled"), 2
	if _, err := svc.MigrateLegacyDictionary(context.Background(), "u1"); err == nil || len(store.terms) != 2 || store.blob.MigratedAt != "" {
		t.Fatalf("want a partial copy without migratedAt: %d rows %+v", len(store.terms), store.blob)
	}
	// The legacy DELETE drops Alpha Grid and edits SoundsLike on Ttobak-Sync.
	store.legacySave(model.DictionaryTerm{Phrase: "Ttobak-Sync", SoundsLike: "또박", DisplayAs: "TtobakSync"}, store.blob.Terms[2])
	if got := store.phrases(svc); got != "Ttobak-Sync/TtobakSync|/베타 노드" {
		t.Fatalf("partial copies must not outlive a legacy delete, got %q", got)
	}
	store.putErr = nil
	if _, err := svc.MigrateLegacyDictionary(context.Background(), "u1"); err != nil || store.blob.MigratedAt == "" {
		t.Fatalf("re-run must complete: %v", err)
	}
	resp, _ := svc.GetDictionary(context.Background(), "u1")
	if len(resp.Terms) != 2 || resp.Terms[0].SoundsLike != "또박" || len(store.terms) != 2 {
		t.Fatalf("re-run must reconcile stale copies: %+v rows=%d", resp.Terms, len(store.terms))
	}
}

func TestMigrationRefusesLossyBlobs(t *testing.T) {
	for name, terms := range map[string][]model.DictionaryTerm{
		"shared display": {{Phrase: "AWS", DisplayAs: "AWS"}, {Phrase: "에이더블유에스", DisplayAs: "AWS"}},
		"same key":       {{Phrase: "A.I"}, {Phrase: "AI"}},
		"no key":         {{Phrase: "Delta"}, {Phrase: "-"}},
	} {
		store := newFakeDictStore()
		store.blob = &model.UserDictionary{UserID: "u1", Terms: terms}
		_, err := newTestDictService(store, nil).MigrateLegacyDictionary(context.Background(), "u1")
		if !errors.Is(err, ErrLegacyDictionaryConflict) || len(store.terms) != 0 || store.blob.MigratedAt != "" {
			t.Fatalf("%s: lossy blob must not migrate: %v rows=%d", name, err, len(store.terms))
		}
	}
	store := newFakeDictStore()
	store.blob = &model.UserDictionary{UserID: "u1", Terms: []model.DictionaryTerm{{Phrase: "Delta"}, {Phrase: "Delta"}}}
	if _, err := newTestDictService(store, nil).MigrateLegacyDictionary(context.Background(), "u1"); err != nil || len(store.terms) != 1 {
		t.Fatalf("exact duplicates collapse: %v", err)
	}
	if got := store.phrases(newTestDictService(store, nil)); got != "Delta/" {
		t.Fatalf("empty legacy displayAs must stay empty after migration, got %q", got)
	}
}

func TestGetVocabularyForTranscriptionPrefersVocabState(t *testing.T) {
	sk := model.VocabSK(model.VocabContextPersonal, model.VocabLangKo)
	cases := []struct {
		name   string
		blob   *model.UserDictionary
		state  *model.VocabularyState
		client *fakeVocabClient
		want   string
	}{
		{"legacy fallback ready", legacyBlob("READY"), nil, &fakeVocabClient{states: map[string]transcribeTypes.VocabularyState{"ttobak-vocab-0123456789abcdef": transcribeTypes.VocabularyStateReady}}, "ttobak-vocab-0123456789abcdef"},
		{"legacy fallback no client trusts READY", legacyBlob("READY"), nil, nil, "ttobak-vocab-0123456789abcdef"},
		{"vocab row wins over blob", legacyBlob("READY"), &model.VocabularyState{SK: sk, ActiveName: "ttobak-vocab-randomslot000001", ActiveReady: true}, &fakeVocabClient{states: map[string]transcribeTypes.VocabularyState{"ttobak-vocab-randomslot000001": transcribeTypes.VocabularyStateReady, "ttobak-vocab-0123456789abcdef": transcribeTypes.VocabularyStateReady}}, "ttobak-vocab-randomslot000001"},
		{"provider not ready", nil, &model.VocabularyState{SK: sk, ActiveName: "n1", ActiveReady: true}, &fakeVocabClient{states: map[string]transcribeTypes.VocabularyState{"n1": transcribeTypes.VocabularyStatePending}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeDictStore()
			store.blob = tc.blob
			if tc.state != nil {
				store.vocab[sk] = *tc.state
			}
			var client TranscribeVocabularyAPI
			if tc.client != nil {
				client = tc.client
			}
			got, err := newTestDictService(store, client).GetVocabularyForTranscription(context.Background(), "u1")
			if err != nil || got != tc.want {
				t.Fatalf("got %q %v, want %q", got, err, tc.want)
			}
			if store.saveCalls != 0 || store.vocabCalls != 0 || len(store.legacyCalls) != 0 {
				t.Fatal("transcription lookup must not write")
			}
		})
	}
}

// TestConcurrentMigrationNeverResurrectsARemovedTerm replays run A copying
// blob revision 1 (Alpha) while a legacy save replaces Alpha with Beta and run
// B migrates revision 2, at each of A's later writes. The blob ConditionCheck
// must refuse A's late write, and A must re-read instead of leaving Alpha.
func TestConcurrentMigrationNeverResurrectsARemovedTerm(t *testing.T) {
	alpha := model.DictionaryTerm{Phrase: "Alpha", SoundsLike: "알파", DisplayAs: "Alpha"}
	beta := model.DictionaryTerm{Phrase: "Beta", DisplayAs: "Beta"}
	for _, tc := range []struct {
		name, at string
		prior    *model.DictionaryTerm // pristine copy left by an earlier partial run
		final    model.DictionaryTerm  // Alpha-keyed content in revision 2, or Beta
	}{
		{name: "late term create", at: "put", final: beta},
		{name: "late vocabulary state", at: "vocab", final: beta},
		{name: "late mark", at: "mark", final: beta},
		{name: "late refresh of an earlier copy", at: "replace",
			prior: &model.DictionaryTerm{Phrase: "Alpha", SoundsLike: "알파0", DisplayAs: "Alpha"},
			final: model.DictionaryTerm{Phrase: "Alpha", SoundsLike: "알파2", DisplayAs: "Alpha"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeDictStore()
			store.blob = &model.UserDictionary{UserID: "u1", Terms: []model.DictionaryTerm{alpha},
				VocabularyName: "ttobak-vocab-rev1", VocabularyStatus: "READY", BlobRevision: 1, UpdatedAt: "2026-10-18T09:00:01Z"}
			svc := newTestDictService(store, nil)
			if tc.prior != nil {
				rows, _ := legacyBlobRows("u1", &model.UserDictionary{Terms: []model.DictionaryTerm{*tc.prior}}, svc.now())
				store.terms[rows[0].SK] = rows[0]
			}
			var runB LegacyMigrationResult
			var errB error
			store.hooks[tc.at] = func() {
				store.legacySave(tc.final)
				store.blob.VocabularyName = "ttobak-vocab-rev2"
				runB, errB = svc.MigrateLegacyDictionary(context.Background(), "u1")
			}
			runA, errA := svc.MigrateLegacyDictionary(context.Background(), "u1")
			if errB != nil || runB.AlreadyMigrated || store.hooks[tc.at] != nil {
				t.Fatalf("run B must migrate revision 2 at %s: %+v %v", tc.at, runB, errB)
			}
			if errA != nil || !runA.AlreadyMigrated {
				t.Fatalf("run A must re-read and see the migration, got %+v %v", runA, errA)
			}
			want := tc.final.Phrase + "/" + tc.final.DisplayAs
			if got := store.phrases(svc); got != want || len(store.terms) != 1 {
				t.Fatalf("rows must match revision 2 only, got %q (%d rows)", got, len(store.terms))
			}
			if row := store.terms[model.DictTermSK(TermKey(tc.final.DisplayAs))]; strings.Join(row.HeardAs, ",") != tc.final.SoundsLike {
				t.Fatalf("stale content survived: %+v", row)
			}
			if _, ok := store.terms[model.DictTermSK(TermKey("Alpha"))]; ok && tc.prior == nil {
				t.Fatal("run A resurrected the removed term")
			}
			if state := store.vocab[model.VocabSK(model.VocabContextPersonal, model.VocabLangKo)]; state.ActiveName != "ttobak-vocab-rev2" {
				t.Fatalf("vocabulary state must follow revision 2: %+v", state)
			}
		})
	}
}

// TestMigrationReconcilesAnEarlierVocabularyState covers a VOCAB# row created
// by an attempt whose mark lost to a blob change.
func TestMigrationReconcilesAnEarlierVocabularyState(t *testing.T) {
	sk := model.VocabSK(model.VocabContextPersonal, model.VocabLangKo)
	store := newFakeDictStore()
	store.blob = legacyBlob("READY")
	svc := newTestDictService(store, nil)
	store.hooks["mark"] = func() {
		store.legacySave(store.blob.Terms[0])
		store.blob.VocabularyName, store.blob.VocabularyStatus = "ttobak-vocab-renamed", "PENDING"
	}
	if _, err := svc.MigrateLegacyDictionary(context.Background(), "u1"); err != nil || store.blob.MigratedAt == "" {
		t.Fatalf("migration must finish on the re-read blob: %v", err)
	}
	state := store.vocab[sk]
	if state.ActiveName != "ttobak-vocab-renamed" || state.Status != model.VocabStatusBuilding || state.ActiveReady || state.TermCount != 1 || state.Version != 2 {
		t.Fatalf("the earlier VOCAB# row must be reconciled under its version guard: %+v", state)
	}

	// Same vocabulary name: status sync may be ahead of the blob, so only the
	// term count is reconciled.
	store = newFakeDictStore()
	store.blob = legacyBlob("PENDING")
	store.vocab[sk] = model.VocabularyState{SK: sk, ActiveName: store.blob.VocabularyName, Status: model.VocabStatusReady, ActiveReady: true, TermCount: 9, Version: 5}
	if _, err := newTestDictService(store, nil).MigrateLegacyDictionary(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	if state := store.vocab[sk]; state.Status != model.VocabStatusReady || !state.ActiveReady || state.TermCount != 2 || state.Version != 6 {
		t.Fatalf("a matching VOCAB# row keeps its status: %+v", state)
	}
}
