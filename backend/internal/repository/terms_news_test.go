package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

// A news merge records the system actor as updatedBy; other merges leave it.
func TestMergeUpdatedByIsWrittenOnlyWhenSet(t *testing.T) {
	key := "0123456789abcdef0123456789abcdef"
	obs := &model.DictObservation{PK: model.PrefixDictCommon, SK: model.DictObsSK(key, model.DictObsSourceID(model.DictSourceNews, "aaaa1111")), TermKey: key,
		SourceKind: model.DictSourceNews, SourceRef: "aaaa1111", OwnerID: model.DictNewsActor}
	repo, calls := dictWire(t, nil)
	merge := DictMergeInput{PK: model.PrefixDictCommon, TermKey: key, Observation: obs, SeenAt: "now", ExpectedVersion: 1, UpdatedBy: model.DictNewsActor}
	if err := repo.MergeAutoObservation(context.Background(), merge); err != nil {
		t.Fatal(err)
	}
	merge.UpdatedBy = ""
	if err := repo.MergeAutoObservation(context.Background(), merge); err != nil {
		t.Fatal(err)
	}
	withActor := resolved(txItems(t, (*calls)[0])[1]["Update"].(map[string]any), "UpdateExpression")
	requireAll(t, "news merge", withActor, "updatedBy")
	if plain := resolved(txItems(t, (*calls)[1])[1]["Update"].(map[string]any), "UpdateExpression"); strings.Contains(plain, "updatedBy") {
		t.Fatalf("a merge without an actor must not touch updatedBy: %s", plain)
	}
}

func TestTermsNewsRowsNeedTheirKeys(t *testing.T) {
	repo := &DynamoDBRepository{}
	if err := repo.PutTermsNewsHistory(context.Background(), &model.TermsNewsHistory{TermKey: "k", ExpiresAt: 1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("history without days: %v", err)
	}
	if err := repo.PutTermsNewsRun(context.Background(), &model.TermsNewsRun{Day: "2026-10-18", ExpiresAt: 1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("run without a hash: %v", err)
	}
}
