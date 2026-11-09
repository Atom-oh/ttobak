package service

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

// Synthetic fixtures only. readClock counts DynamoDB-shaped reads and their
// sequential depth: a read's level is one more than the deepest read that had
// finished when it started, so concurrent siblings share a level and the
// request's depth is the longest chain of reads that waited on each other.
type readClock struct {
	mu                     sync.Mutex
	getItem, query, batchN int
	finished, depth        int
	latency                time.Duration
	off                    bool
}

func (c *readClock) read(kind string) {
	c.mu.Lock()
	if c.off {
		c.mu.Unlock()
		return
	}
	switch kind {
	case "GetItem":
		c.getItem++
	case "Query":
		c.query++
	default:
		c.batchN++
	}
	level := c.finished + 1
	c.mu.Unlock()
	time.Sleep(c.latency)
	c.mu.Lock()
	c.finished = max(c.finished, level)
	c.depth = max(c.depth, level)
	c.mu.Unlock()
}

func (c *readClock) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.getItem, c.query, c.batchN, c.finished, c.depth = 0, 0, 0, 0, 0
}

// readCountStore is a read-mostly multi-partition DictionaryStore whose every
// read costs one clock read; it is safe for concurrent use. Unused methods
// panic through the nil embedded interface.
type readCountStore struct {
	DictionaryStore
	clock *readClock
	mu    sync.Mutex
	blob  *model.UserDictionary
	terms map[string][]model.DictTerm
	vocab map[string]model.VocabularyState
}

func (r *readCountStore) GetDictionary(context.Context, string) (*model.UserDictionary, error) {
	r.clock.read("GetItem")
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.blob == nil {
		return nil, nil
	}
	b := *r.blob
	return &b, nil
}

func (r *readCountStore) ListDictTerms(_ context.Context, pk string) ([]model.DictTerm, error) {
	r.clock.read("Query")
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]model.DictTerm(nil), r.terms[pk]...), nil
}

func (r *readCountStore) GetVocabularyState(_ context.Context, _ string, c, l string) (*model.VocabularyState, error) {
	r.clock.read("GetItem")
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.vocab[model.VocabSK(c, l)]
	if !ok {
		return nil, nil
	}
	return &st, nil
}

// readCountRecency is AccountRecencySource with one Query per call.
type readCountRecency struct {
	clock    *readClock
	accounts []model.AccountMember
	recent   []string
}

func (r *readCountRecency) ListAccountsForUser(context.Context, string) ([]model.AccountMember, error) {
	r.clock.read("Query")
	return r.accounts, nil
}

func (r *readCountRecency) ListRecentMeetingAccountIDs(context.Context, string, int) ([]string, error) {
	r.clock.read("Query")
	return r.recent, nil
}

// readCountFixture is a migrated user with n member Accounts (plus one Account
// in the order the user already left) whose ko-KR vocabulary is READY for the
// current plan, so a GET makes one ensure step and no write.
func readCountFixture(t *testing.T, n int, batch bool) (*DictionaryService, *readClock) {
	t.Helper()
	clock := &readClock{latency: 3 * time.Millisecond, off: true}
	store := &readCountStore{clock: clock, terms: map[string][]model.DictTerm{}, vocab: map[string]model.VocabularyState{},
		blob: &model.UserDictionary{UserID: "u1", MigratedAt: "2026-10-17T00:00:00.000000Z"}}
	store.terms["USER#u1"] = []model.DictTerm{layerRow("USER#u1", "Kilo", model.TermOriginManual, 0), layerRow("USER#u1", "Delta", model.TermOriginAuto, 3)}
	store.terms[model.PrefixDictCommon] = []model.DictTerm{layerRow(model.PrefixDictCommon, "Echo", model.TermOriginAuto, 7)}
	src := &readCountRecency{clock: clock}
	member := map[string]bool{}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("a%02d", i)
		member[id] = true
		src.accounts = append(src.accounts, model.AccountMember{AccountID: id})
		store.terms[model.PrefixAccount+id] = []model.DictTerm{layerRow(model.PrefixAccount+id, "Acct-"+letters(i), model.TermOriginAuto, 1)}
	}
	if n > 0 {
		store.terms["ACCOUNT#left"] = []model.DictTerm{layerRow("ACCOUNT#left", "Leaked", model.TermOriginAuto, 99)}
	}
	tr := &fakeTranscribe{vocabs: map[string]*fakeVocabulary{}, now: time.Now}
	svc := NewDictionaryServiceWithStore(store, tr)
	svc.SetVocabularyBuilder(&fakeObjects{puts: map[string]string{}}, "assets", 10*time.Minute)
	svc.SetMembershipReader(func(_ context.Context, accountID, userID string) (bool, error) {
		clock.read("GetItem")
		return userID == "u1" && member[accountID], nil
	})
	if batch {
		svc.SetMembershipBatchReader(func(_ context.Context, ids []string, userID string) (map[string]bool, error) {
			clock.read("BatchGetItem")
			out := map[string]bool{}
			for _, id := range ids {
				out[id] = userID == "u1" && member[id]
			}
			return out, nil
		})
	}
	order := AccountRecencyOrder(src)
	svc.SetAccountOrder(func(ctx context.Context, userID string) ([]string, error) {
		ids, err := order(ctx, userID)
		if n > 0 {
			ids = append([]string{"left"}, ids...) // stale order: a left Account first
		}
		return ids, err
	})
	layers, err := svc.vocabularyLayers(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	plan := planVocabularyLayers(layers, model.VocabLangKo)
	active := "ttobak-vocab-0000000000000001"
	store.vocab[model.VocabSK(model.VocabContextPersonal, model.VocabLangKo)] = model.VocabularyState{
		PK: "USER#u1", SK: model.VocabSK(model.VocabContextPersonal, model.VocabLangKo), Context: model.VocabContextPersonal,
		Language: model.VocabLangKo, Status: model.VocabStatusReady, ActiveName: active, ActiveReady: true,
		ActiveFingerprint: plan.fingerprint, TermCount: plan.count, Version: 3,
	}
	clock.off = false
	return svc, clock
}

// The dictionary GET's reads for a migrated user with READY vocabulary. Before
// the reads were batched and fanned out, this fixture measured every read
// waiting on the previous one: no accounts 6 GetItem/4 Query (depth 10), one
// account 8/6 (14), thirty accounts 37/35 (72).
func TestDictionaryGetReadCounts(t *testing.T) {
	cases := []struct {
		name                       string
		accounts                   int
		batch                      bool
		getItem, query, batchItems int
		maxDepth                   int
	}{
		// GetItem: migration blob, first state, final blob/ko/en, plus
		// per-Account membership unless batched. Query: account list,
		// recent meetings (only with memberships), Personal, each member
		// Account, Common, final Personal.
		{"no accounts", 0, false, 5, 4, 0, 5},
		{"one account per-item", 1, false, 7, 6, 0, 6},
		{"one account batched", 1, true, 5, 6, 1, 6},
		// Depth: blob, order(2)||state, Personal||membership, ceil(31/8)
		// lists, final reads.
		{"thirty accounts batched", 30, true, 5, 35, 1, 1 + 2 + 1 + 4 + 1},
		// Per-item membership: 31 checks at 8 in flight take 4 waves.
		{"thirty accounts per-item", 30, false, 36, 35, 0, 1 + 2 + 4 + 4 + 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, clock := readCountFixture(t, tc.accounts, tc.batch)
			resp, err := svc.GetDictionaryWithTerms(context.Background(), "u1")
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("GetItem %d Query %d BatchGetItem %d depth %d", clock.getItem, clock.query, clock.batchN, clock.depth)
			if clock.getItem != tc.getItem || clock.query != tc.query || clock.batchN != tc.batchItems {
				t.Fatalf("reads: GetItem %d Query %d BatchGetItem %d, want %d %d %d", clock.getItem, clock.query, clock.batchN, tc.getItem, tc.query, tc.batchItems)
			}
			if clock.depth > tc.maxDepth {
				t.Fatalf("sequential depth %d, want at most %d", clock.depth, tc.maxDepth)
			}
			if len(resp.Warnings) != 0 || resp.Vocabularies.Ko.Status != model.VocabStatusReady || !resp.Vocabularies.Ko.ActiveReady || resp.Status != "READY" {
				t.Fatalf("steady READY response: %+v %v", resp.Vocabularies, resp.Warnings)
			}
		})
	}
}

// Batched and per-item membership give the same response and the same plan.
func TestDictionaryGetSameResponseEitherMembershipRead(t *testing.T) {
	per, _ := readCountFixture(t, 12, false)
	bat, _ := readCountFixture(t, 12, true)
	a, errA := per.GetDictionaryWithTerms(context.Background(), "u1")
	b, errB := bat.GetDictionaryWithTerms(context.Background(), "u1")
	if errA != nil || errB != nil || !reflect.DeepEqual(a, b) {
		t.Fatalf("responses differ:\n%+v\n%+v (%v %v)", a, b, errA, errB)
	}
	for _, svc := range []*DictionaryService{per, bat} {
		layers, err := svc.vocabularyLayers(context.Background(), "u1")
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range layers {
			if l.name == model.DictLayerAccountPrefix+"left" {
				t.Fatal("an Account the user left never contributes a layer")
			}
		}
	}
}
