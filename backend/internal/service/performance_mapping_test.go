package service

import (
	"context"
	"errors"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func TestMatchSFDCAccountsUniqueNameOrAlias(test *testing.T) {
	assignments := []model.SFDCAssignment{
		{SFDCAccountID: "S1", Name: "Acme Corp", Role: "REGISTRY"},
		{SFDCAccountID: "S2", Name: "  GLOBEX  ", Role: "activity"},
		{SFDCAccountID: "S3", Name: "Initech", Role: "REGISTRY"},
		{SFDCAccountID: "S4", Name: "", Role: "REGISTRY"},
	}
	accounts := []NamedCandidate{
		{Kind: "account", ID: "a1", Name: "Something Else", Aliases: []string{"acme corp"}},
		{Kind: "account", ID: "a2", Name: "Globex"},
		{Kind: "account", ID: "a3", Name: "Initech Holdings"},
		{Kind: "account", ID: "a4", Name: ""},
	}
	got := MatchSFDCAccounts(assignments, accounts)
	if len(got) != 2 || got["S1"] != "a1" || got["S2"] != "a2" {
		test.Fatalf("got %v", got)
	}
}

func TestMatchSFDCAccountsAmbiguousStaysUnmapped(test *testing.T) {
	assignments := []model.SFDCAssignment{{SFDCAccountID: "S1", Name: "Acme", Role: "REGISTRY"}}
	for name, accounts := range map[string][]NamedCandidate{
		"two names":      {{ID: "a1", Name: "Acme"}, {ID: "a2", Name: "ACME"}},
		"name and alias": {{ID: "a1", Name: "Acme"}, {ID: "a2", Name: "Other", Aliases: []string{"acme"}}},
	} {
		if got := MatchSFDCAccounts(assignments, accounts); len(got) != 0 {
			test.Fatalf("%s: got %v", name, got)
		}
	}
	// One account matching through both its name and an alias is still unique.
	same := []NamedCandidate{{ID: "a1", Name: "Acme", Aliases: []string{"acme"}}}
	if got := MatchSFDCAccounts(assignments, same); got["S1"] != "a1" {
		test.Fatalf("got %v", got)
	}
}

type performanceCandidatesFake struct {
	candidates []NamedCandidate
	err        error
	calls      int
}

func (fake *performanceCandidatesFake) GetAccount(context.Context, string, string) (*model.AccountResponse, error) {
	return &model.AccountResponse{}, nil
}

func (fake *performanceCandidatesFake) ListReadableAccountCandidates(context.Context, string) ([]NamedCandidate, error) {
	fake.calls++
	return fake.candidates, fake.err
}

func performanceAssignmentSync(complete bool) model.PerformanceSyncRequest {
	return model.PerformanceSyncRequest{BatchID: performanceServiceBatch, Complete: complete, Mutations: []model.PerformanceMutation{
		{Op: "upsert", Assignment: &model.SFDCAssignment{SFDCAccountID: "S1", Name: "Acme", Role: "REGISTRY"}},
	}}
}

func TestManualMappingWins(test *testing.T) {
	store := &performanceStoreFake{assignments: map[string][]model.SFDCAssignment{
		"owner": {{SFDCAccountID: "S1", Name: "Acme", Role: "REGISTRY"}},
	}, putErr: repository.ErrConditionFailed}
	accounts := &performanceCandidatesFake{candidates: []NamedCandidate{{ID: "a1", Name: "Acme"}}}
	svc := performanceTestService(store, accounts)
	if _, err := svc.Sync(test.Context(), "owner", model.PerformanceSourceSFDCAssignments, performanceAssignmentSync(true)); err != nil {
		test.Fatalf("a kept manual mapping must not fail the sync: %v", err)
	}
	if len(store.put) != 1 || !store.putManual[0] || store.put[0].Manual || store.put[0].AccountID != "a1" {
		test.Fatalf("put %v manual-guard %v", store.put, store.putManual)
	}
}

func TestSyncAutoMapsOnlyCompleteAssignmentBatches(test *testing.T) {
	assigned := map[string][]model.SFDCAssignment{"owner": {{SFDCAccountID: "S1", Name: "Acme", Role: "activity"}}}
	candidates := []NamedCandidate{{ID: "a1", Name: "Acme"}}
	cases := []struct {
		name     string
		source   string
		request  model.PerformanceSyncRequest
		replay   bool
		wantPuts int
	}{
		{"complete assignments", model.PerformanceSourceSFDCAssignments, performanceAssignmentSync(true), false, 1},
		{"incomplete assignments", model.PerformanceSourceSFDCAssignments, performanceAssignmentSync(false), false, 0},
		{"replay", model.PerformanceSourceSFDCAssignments, performanceAssignmentSync(true), true, 0},
		{"tasks", model.PerformanceSourceSFDCTasks, performanceTaskSync("2026-03-04"), false, 0},
	}
	for _, item := range cases {
		store := &performanceStoreFake{assignments: assigned, replay: item.replay}
		svc := performanceTestService(store, &performanceCandidatesFake{candidates: candidates})
		if _, err := svc.Sync(test.Context(), "owner", item.source, item.request); err != nil {
			test.Fatalf("%s: %v", item.name, err)
		}
		if len(store.put) != item.wantPuts {
			test.Fatalf("%s: puts %v", item.name, store.put)
		}
	}
}

func TestSyncSurvivesAutoMappingFailure(test *testing.T) {
	store := &performanceStoreFake{assignments: map[string][]model.SFDCAssignment{"owner": {{SFDCAccountID: "S1", Name: "Acme", Role: "REGISTRY"}}}}
	accounts := &performanceCandidatesFake{err: errors.New("boom")}
	svc := performanceTestService(store, accounts)
	receipt, err := svc.Sync(test.Context(), "owner", model.PerformanceSourceSFDCAssignments, performanceAssignmentSync(true))
	if err != nil || receipt == nil || !receipt.Completed {
		test.Fatalf("receipt %v err %v", receipt, err)
	}
	if accounts.calls != 1 || len(store.put) != 0 {
		test.Fatalf("calls %d puts %v", accounts.calls, store.put)
	}
}

func staleMappingStore(mappings ...model.AccountMapping) *performanceStoreFake {
	return &performanceStoreFake{
		assignments: map[string][]model.SFDCAssignment{"owner": {
			{SFDCAccountID: "S1", Name: "Acme", Role: "REGISTRY"},
			{SFDCAccountID: "S2", Name: "Gone", Role: "REGISTRY"},
		}},
		mappings: map[string][]model.AccountMapping{"owner": mappings},
	}
}

func TestAutoMappingRemovesStaleAutomatic(test *testing.T) {
	store := staleMappingStore(
		model.AccountMapping{SFDCAccountID: "S1", AccountID: "a1"},
		model.AccountMapping{SFDCAccountID: "S2", AccountID: "a2"},
		model.AccountMapping{SFDCAccountID: "S9", AccountID: "a9"},
	)
	// S1 is now ambiguous, S2 has no readable match, S9 is unassigned by this
	// sync (mappings are read by key, so the removal names S9).
	accounts := &performanceCandidatesFake{candidates: []NamedCandidate{{ID: "a1", Name: "Acme"}, {ID: "a3", Name: "acme"}}}
	svc := performanceTestService(store, accounts)
	sync := performanceAssignmentSync(true)
	sync.Mutations = append(sync.Mutations, model.PerformanceMutation{Op: "remove", ExternalID: "S9"})
	if _, err := svc.Sync(test.Context(), "owner", model.PerformanceSourceSFDCAssignments, sync); err != nil {
		test.Fatal(err)
	}
	if len(store.deleted) != 3 || store.deleted[0] != "S1" || store.deleted[1] != "S2" || store.deleted[2] != "S9" || len(store.put) != 0 {
		test.Fatalf("deleted %v put %v", store.deleted, store.put)
	}
}

func TestAutoMappingKeepsManualWhenStale(test *testing.T) {
	store := staleMappingStore(
		model.AccountMapping{SFDCAccountID: "S1", AccountID: "a1", Manual: true},
		model.AccountMapping{SFDCAccountID: "S9", AccountID: "a9", Manual: true},
	)
	svc := performanceTestService(store, &performanceCandidatesFake{})
	if _, err := svc.Sync(test.Context(), "owner", model.PerformanceSourceSFDCAssignments, performanceAssignmentSync(true)); err != nil {
		test.Fatal(err)
	}
	if len(store.deleted) != 0 {
		test.Fatalf("manual rows must not be deleted: %v", store.deleted)
	}
}

func TestAutoMappingSkipsUnchangedAutomatic(test *testing.T) {
	store := staleMappingStore(model.AccountMapping{SFDCAccountID: "S1", AccountID: "a1", UpdatedAt: "2026-01-01T00:00:00Z"})
	accounts := &performanceCandidatesFake{candidates: []NamedCandidate{{ID: "a1", Name: "Acme"}}}
	svc := performanceTestService(store, accounts)
	if _, err := svc.Sync(test.Context(), "owner", model.PerformanceSourceSFDCAssignments, performanceAssignmentSync(true)); err != nil {
		test.Fatal(err)
	}
	if len(store.put) != 0 || len(store.deleted) != 0 {
		test.Fatalf("put %v deleted %v", store.put, store.deleted)
	}
}
