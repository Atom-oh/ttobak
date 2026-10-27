package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

// pruneFailStore fails the partition listing that tombstone pruning runs
// after a Common delete has committed.
type pruneFailStore struct{ *layerStore }

func (p pruneFailStore) ListDictTerms(context.Context, string) ([]model.DictTerm, error) {
	return nil, errors.New("list failed")
}

func TestDeleteCommonLayerTermReportsPruneWarning(t *testing.T) {
	store := newLayerStore()
	key := store.seed(model.PrefixDictCommon, "K-culture").TermKey
	svc := NewDictionaryServiceWithStore(pruneFailStore{store}, nil)
	resp, err := svc.DeleteLayerTerm(context.Background(), "u1", model.DictLayerCommon, key, nil)
	if err != nil || resp.State != model.TermStateDeleted || len(resp.Warnings) != 1 || resp.Warnings[0] != "tombstone pruning incomplete" {
		t.Fatalf("a committed delete reports its failed pruning: %+v %v", resp, err)
	}
}

func TestListLayerPersonalReportsLegacyConflict(t *testing.T) {
	store := newFakeDictStore()
	store.blob = &model.UserDictionary{UserID: "u1", Terms: []model.DictionaryTerm{{Phrase: "A.I"}, {Phrase: "AI"}}}
	svc := newTestDictService(store, nil)
	if _, err := svc.ListLayer(context.Background(), "u1", model.DictLayerPersonal); !errors.Is(err, ErrLegacyDictionaryConflict) {
		t.Fatalf("an unmigratable blob must not read as an empty list: %v", err)
	}
}

func TestListLayerPersonalReportsFailedMigration(t *testing.T) {
	store := newFakeDictStore()
	store.blob = legacyBlob("READY")
	store.putErr, store.putErrAfter = errors.New("write failed"), 1
	svc := newTestDictService(store, nil)
	if _, err := svc.ListLayer(context.Background(), "u1", model.DictLayerPersonal); err == nil {
		t.Fatalf("a partial migration must not read as a complete list (rows=%d)", len(store.terms))
	}
	store.putErr = nil
	items, err := svc.ListLayer(context.Background(), "u1", model.DictLayerPersonal)
	if err != nil || len(items) != len(store.blob.Terms) {
		t.Fatalf("the next list retries the migration: %+v %v", items, err)
	}
}

func TestListDeletedCommonListsOnlyTheNewestDeletions(t *testing.T) {
	store := newLayerStore()
	svc := newLayerService(store)
	ctx := context.Background()
	var keys []string
	for i := 0; i <= MaxDeletedCommonListed; i++ {
		key := store.seed(model.PrefixDictCommon, fmt.Sprintf("Term %03d", i)).TermKey
		if err := svc.DeleteCommonTerm(ctx, "u1", key, nil); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	got, err := svc.ListDeletedCommon(ctx)
	if err != nil || len(got) != MaxDeletedCommonListed || got[0].TermID != keys[len(keys)-1] || got[len(got)-1].TermID != keys[1] {
		t.Fatalf("want the newest %d deletions, newest first: %d %v", MaxDeletedCommonListed, len(got), err)
	}
}
