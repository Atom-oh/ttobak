package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

// Synthetic fixtures only.

// failingListStore lists layerStore rows, failing chosen partitions and
// delaying earlier partitions longest so concurrent completion order is the
// reverse of the sequential order.
type failingListStore struct {
	*layerStore
	mu    sync.Mutex
	fail  map[string]error
	delay map[string]time.Duration
	lists []string
}

func (f *failingListStore) ListDictTerms(ctx context.Context, pk string) ([]model.DictTerm, error) {
	time.Sleep(f.delay[pk])
	f.mu.Lock()
	f.lists = append(f.lists, pk)
	err := f.fail[pk]
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return f.layerStore.ListDictTerms(ctx, pk)
}

func (f *failingListStore) listed(pk string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.lists {
		if p == pk {
			return true
		}
	}
	return false
}

func TestListVocabularyLayersOrderAndErrors(t *testing.T) {
	ids := []string{"a1", "a2", "a3", "a4", "left", "a5"}
	errA2, errA4, errCommon, errPersonal := errors.New("a2 list"), errors.New("a4 list"), errors.New("common list"), errors.New("personal list")
	errMember := errors.New("membership read")
	cases := []struct {
		name       string
		batch      bool
		fail       map[string]error
		memberFail string // per-item: this Account's check fails; batch: the batch fails
		wantErr    error
		wantMember bool // the error is the wrapped membership error
		notListed  []string
		wantLayers string
	}{
		{name: "all ok per-item", wantLayers: "personal-manual,account:a1,account:a2,account:a3,account:a4,account:a5,personal-auto,common"},
		{name: "all ok batched", batch: true, wantLayers: "personal-manual,account:a1,account:a2,account:a3,account:a4,account:a5,personal-auto,common"},
		{name: "personal error wins", fail: map[string]error{"USER#u1": errPersonal, "ACCOUNT#a2": errA2}, memberFail: "a1", wantErr: errPersonal},
		{name: "earliest account list error", fail: map[string]error{"ACCOUNT#a2": errA2, "ACCOUNT#a4": errA4, model.PrefixDictCommon: errCommon}, wantErr: errA2},
		{name: "list before failing membership", fail: map[string]error{"ACCOUNT#a2": errA2}, memberFail: "a3", wantErr: errA2},
		{name: "membership before later list", fail: map[string]error{"ACCOUNT#a4": errA4}, memberFail: "a3", wantErr: errMember, wantMember: true,
			notListed: []string{"ACCOUNT#a4", "ACCOUNT#a5", model.PrefixDictCommon}},
		{name: "batch membership error", batch: true, memberFail: "*", wantErr: errMember, wantMember: true,
			notListed: []string{"ACCOUNT#a1", "ACCOUNT#a5", model.PrefixDictCommon}},
		{name: "common error last", fail: map[string]error{model.PrefixDictCommon: errCommon}, wantErr: errCommon},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := newLayerStore()
			base.put(layerRow("USER#u1", "Kilo", model.TermOriginManual, 0))
			base.put(layerRow("USER#u1", "Delta", model.TermOriginAuto, 2))
			base.put(layerRow(model.PrefixDictCommon, "Echo", model.TermOriginAuto, 1))
			delay := map[string]time.Duration{}
			for i, id := range ids {
				base.put(layerRow(model.PrefixAccount+id, "Acct-"+letters(i), model.TermOriginAuto, 1))
				delay[model.PrefixAccount+id] = time.Duration(len(ids)-i) * 2 * time.Millisecond
				if id != "left" {
					base.members[id+"/u1"] = true
				}
			}
			store := &failingListStore{layerStore: base, fail: tc.fail, delay: delay}
			svc := NewDictionaryServiceWithStore(store, nil)
			svc.SetMembershipReader(func(_ context.Context, id, user string) (bool, error) {
				if !tc.batch && id == tc.memberFail {
					return false, errMember
				}
				return base.members[id+"/"+user], nil
			})
			if tc.batch {
				svc.SetMembershipBatchReader(func(_ context.Context, want []string, user string) (map[string]bool, error) {
					if tc.memberFail != "" {
						return nil, errMember
					}
					out := map[string]bool{}
					for _, id := range want {
						out[id] = base.members[id+"/"+user]
					}
					return out, nil
				})
			}
			layers, err := svc.listVocabularyLayers(context.Background(), "u1", ids, svc.newMembershipMemo("u1"))
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) || layers != nil {
					t.Fatalf("error %v, want %v", err, tc.wantErr)
				}
				if tc.wantMember != strings.HasPrefix(err.Error(), "check account membership: ") {
					t.Fatalf("membership wrapping: %v", err)
				}
				for _, pk := range tc.notListed {
					if store.listed(pk) {
						t.Fatalf("%s was listed after the first failure", pk)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, l := range layers {
				names = append(names, l.name)
			}
			if got := strings.Join(names, ","); got != tc.wantLayers {
				t.Fatalf("layers %s, want %s", got, tc.wantLayers)
			}
			if store.listed("ACCOUNT#left") {
				t.Fatal("a non-member Account's rows are never read")
			}
		})
	}
}

func TestMembershipMemo(t *testing.T) {
	ctx := context.Background()
	calls := map[string]int{}
	fail := map[string]bool{"a2": true}
	svc := NewDictionaryServiceWithStore(newLayerStore(), nil)
	var mu sync.Mutex
	svc.SetMembershipReader(func(_ context.Context, id, _ string) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		calls[id]++
		if fail[id] {
			return false, errors.New("synthetic")
		}
		return id != "a3", nil
	})
	memo := svc.newMembershipMemo("u1")
	members, failAt, err := memo.check(ctx, []string{"a1", "a2", "a3"})
	if err == nil || failAt != 1 || len(members) != 1 || !members[0] {
		t.Fatalf("first failure at a2: %v %d %v", members, failAt, err)
	}
	// The error is not cached: a2 is read again, a1 is not.
	fail["a2"] = false
	members, failAt, err = memo.check(ctx, []string{"a1", "a2", "a3"})
	if err != nil || failAt != 3 || !members[0] || !members[1] || members[2] {
		t.Fatalf("retry: %v %d %v", members, failAt, err)
	}
	if calls["a1"] != 1 || calls["a2"] != 2 {
		t.Fatalf("a1 read once, a2 re-read after its error: %v", calls)
	}
	// A new memo (another request) reads again.
	if _, _, err := svc.newMembershipMemo("u1").check(ctx, []string{"a1"}); err != nil || calls["a1"] != 2 {
		t.Fatalf("memos are never shared: %v %v", calls, err)
	}
	// A memo is bound to its user.
	if other := svc.newMembershipMemo("u2"); other.userID != "u2" || len(other.known) != 0 {
		t.Fatal("a fresh memo starts empty for its own user")
	}
}

// A multi-step ensure checks each Account's membership once per call, while
// every step still re-reads the state and the rows; the next call reads again.
func TestEnsureVocabulariesMembershipOncePerCall(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	fx.add("Alpha Grid", "Alpha-Grid")
	checks := 0
	fx.svc.SetMembershipReader(func(context.Context, string, string) (bool, error) { checks++; return true, nil })
	fx.svc.SetAccountOrder(func(context.Context, string) ([]string, error) { return []string{"a1"}, nil })
	if _, warnings := fx.ensure(true); len(warnings) != 0 || fx.tr.count("create:") != 1 {
		t.Fatalf("a claim and submit take several steps: %v %v", warnings, fx.tr.calls)
	}
	if checks != 1 {
		t.Fatalf("membership read %d times in one call", checks)
	}
	fx.ensure(false)
	if checks != 2 {
		t.Fatalf("each call reads membership afresh: %d", checks)
	}
}
