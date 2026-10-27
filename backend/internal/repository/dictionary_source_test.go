package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

// A KB or research observation has no source meeting: it is keyed by
// DictObsSourceID and its transactions carry no MEETING# ConditionCheck.
func TestDocumentSourceObservationHasNoMeetingCheck(t *testing.T) {
	key := "0123456789abcdef0123456789abcdef"
	ref := "kb/u1/1_a.md"
	obs := &model.DictObservation{PK: "USER#u1", SK: model.DictObsSK(key, model.DictObsSourceID(model.DictSourceKB, ref)), TermKey: key,
		SourceKind: model.DictSourceKB, SourceRef: ref, OwnerID: "u1"}
	term := &model.DictTerm{PK: "USER#u1", SK: model.DictTermSK(key), TermKey: key, State: model.TermStateActive, Origin: model.TermOriginAuto, DisplayAs: "AlphaGrid", Hits: 1, Version: 1}

	repo, calls := dictWire(t, nil)
	if err := repo.CreateAutoTermChecked(context.Background(), term, obs, nil); err != nil {
		t.Fatal(err)
	}
	merge := DictMergeInput{PK: "USER#u1", TermKey: key, Observation: obs, SeenAt: "now", ExpectedVersion: 1, SourceTypes: []string{model.TermSourceDocument}}
	if err := repo.MergeAutoObservation(context.Background(), merge); err != nil {
		t.Fatal(err)
	}
	if err := repo.PromoteSuggestedTerm(context.Background(), merge, DictPromotion{DisplayAs: "AlphaGrid"}); err != nil {
		t.Fatal(err)
	}
	for i, call := range *calls {
		if items := txItems(t, call); len(items) != 2 || strings.Contains(fmt.Sprint(items), model.PrefixMeeting) {
			t.Fatalf("call %d must hold only the term and observation writes: %v", i, items)
		}
	}
	promotion := resolved(txItems(t, (*calls)[2])[1]["Update"].(map[string]any), "UpdateExpression")
	requireAll(t, "promotion", promotion, "REMOVE", "firstSourceMeetingId")

	bad := *obs
	bad.SK = model.DictObsSK(key, ref)
	merge.Observation = &bad
	if err := repo.MergeAutoObservation(context.Background(), merge); err == nil {
		t.Fatal("an observation keyed by the raw ref must be rejected")
	}
}
