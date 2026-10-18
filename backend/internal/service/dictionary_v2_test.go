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
}

func newFakeDictStore() *fakeDictStore {
	return &fakeDictStore{terms: map[string]model.DictTerm{}, vocab: map[string]model.VocabularyState{}}
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
func (f *fakeDictStore) MarkDictionaryMigrated(_ context.Context, _ string, now string) error {
	if f.blob == nil || f.blob.MigratedAt != "" {
		return cond()
	}
	f.blob.MigratedAt = now
	return nil
}
func (f *fakeDictStore) ListDictTerms(context.Context, string) ([]model.DictTerm, error) {
	out := []model.DictTerm{}
	for _, t := range f.terms {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SK < out[j].SK })
	return out, nil
}
func (f *fakeDictStore) PutDictTermsIfAbsent(_ context.Context, terms []model.DictTerm) (int, error) {
	if f.putErr != nil {
		return 0, f.putErr
	}
	n := 0
	for _, t := range terms {
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
func (f *fakeDictStore) CreateVocabularyStateIfAbsent(_ context.Context, s *model.VocabularyState) error {
	if _, ok := f.vocab[s.SK]; ok {
		return cond()
	}
	f.vocab[s.SK] = *s
	return nil
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
	if len(resp.Terms) != 2 || resp.Terms[0].SoundsLike != "또박 싱크" || resp.Terms[1].DisplayAs != "베타 노드" {
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
