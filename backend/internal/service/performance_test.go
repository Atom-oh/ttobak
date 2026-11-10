package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const performanceServiceBatch = "90ec2d25-a32e-43a5-96a9-e203859b7d19"

type performanceStoreFake struct {
	applied      int
	digests      []string
	requests     []model.PerformanceSyncRequest
	sources      []string
	assignments  map[string][]model.SFDCAssignment
	mappings     map[string][]model.AccountMapping
	statuses     map[string][]model.PerformanceSourceStatus
	goals        map[string]*model.PerformanceGoals
	put          []model.AccountMapping
	putManual    []bool
	applyErr     error
	receipts     map[string]storedPerformanceReceipt
	keepReceipts bool
}

type storedPerformanceReceipt struct {
	digest  string
	receipt model.PerformanceSyncReceipt
}

func (fake *performanceStoreFake) ReplayPerformanceBatch(_ context.Context, userID, source, batchID, digest string) (*model.PerformanceSyncReceipt, error) {
	saved, ok := fake.receipts[userID+"/"+source+"/"+batchID]
	if !ok {
		return nil, nil
	}
	if saved.digest != digest {
		return nil, repository.ErrConditionFailed
	}
	receipt := saved.receipt
	return &receipt, nil
}

func (fake *performanceStoreFake) ApplyPerformanceBatch(_ context.Context, userID, source string, req model.PerformanceSyncRequest, digest string) (*model.PerformanceSyncReceipt, error) {
	fake.applied++
	fake.digests = append(fake.digests, digest)
	fake.requests = append(fake.requests, req)
	fake.sources = append(fake.sources, source)
	if fake.applyErr != nil {
		return nil, fake.applyErr
	}
	receipt := model.PerformanceSyncReceipt{Revision: req.ExpectedRevision + 1, Completed: req.Complete, Upserted: len(req.Mutations)}
	if fake.keepReceipts {
		if fake.receipts == nil {
			fake.receipts = make(map[string]storedPerformanceReceipt)
		}
		fake.receipts[userID+"/"+source+"/"+req.BatchID] = storedPerformanceReceipt{digest: digest, receipt: receipt}
	}
	return &receipt, nil
}

func (fake *performanceStoreFake) ListPerformanceSources(_ context.Context, userID string) ([]model.PerformanceSourceStatus, error) {
	return fake.statuses[userID], nil
}

func (fake *performanceStoreFake) ListPerformanceAssignments(_ context.Context, userID string) ([]model.SFDCAssignment, error) {
	return fake.assignments[userID], nil
}

func (fake *performanceStoreFake) ListAccountMappings(_ context.Context, userID string) ([]model.AccountMapping, error) {
	return fake.mappings[userID], nil
}

func (fake *performanceStoreFake) PutAccountMapping(_ context.Context, userID string, mapping model.AccountMapping, onlyIfNotManual bool) error {
	fake.put = append(fake.put, mapping)
	fake.putManual = append(fake.putManual, onlyIfNotManual)
	return nil
}

func (fake *performanceStoreFake) GetPerformanceGoals(_ context.Context, userID string, year int) (*model.PerformanceGoals, error) {
	return fake.goals[userID], nil
}

func (fake *performanceStoreFake) PutPerformanceGoals(_ context.Context, userID string, goals model.PerformanceGoals) error {
	if fake.goals == nil {
		fake.goals = make(map[string]*model.PerformanceGoals)
	}
	fake.goals[userID] = &goals
	return nil
}

type performanceAccountsFake struct {
	readable map[string]bool
	err      error
}

func (fake performanceAccountsFake) GetAccount(_ context.Context, userID, accountID string) (*model.AccountResponse, error) {
	if fake.err != nil {
		return nil, fake.err
	}
	if !fake.readable[userID+"/"+accountID] {
		return nil, ErrForbidden
	}
	return &model.AccountResponse{}, nil
}

func performanceTestService(store *performanceStoreFake, accounts PerformanceAccountReader) *PerformanceService {
	svc := NewPerformanceService(store, accounts)
	svc.now = func() time.Time { return time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC) }
	return svc
}

func performanceTaskSync(date string) model.PerformanceSyncRequest {
	return model.PerformanceSyncRequest{BatchID: performanceServiceBatch, Complete: true, Mutations: []model.PerformanceMutation{
		{Op: "upsert", Task: &model.SFDCTask{TaskID: "T1", ActivityDate: date, Type: "Call", Subject: "Sync", Status: "Completed"}},
	}}
}

func TestPerformanceSyncDigestIsCanonical(test *testing.T) {
	store := &performanceStoreFake{}
	svc := performanceTestService(store, nil)
	first := performanceTaskSync("2026-03-04")
	upper := performanceTaskSync("2026-03-04")
	upper.BatchID = strings.ToUpper(performanceServiceBatch)
	changed := performanceTaskSync("2026-03-05")
	for _, request := range []model.PerformanceSyncRequest{first, upper, changed} {
		if _, err := svc.Sync(test.Context(), "owner", model.PerformanceSourceSFDCTasks, request); err != nil {
			test.Fatal(err)
		}
	}
	if len(store.digests) != 3 || len(store.digests[0]) != 64 || store.digests[0] != store.digests[1] || store.digests[0] == store.digests[2] {
		test.Fatalf("digest must be sha256 over the normalized request: %v", store.digests)
	}
	if store.requests[1].BatchID != performanceServiceBatch || store.requests[0].Mutations == nil {
		test.Fatalf("request must be normalized: %+v", store.requests[1])
	}
	empty := model.PerformanceSyncRequest{BatchID: performanceServiceBatch}
	if _, err := svc.Sync(test.Context(), "owner", model.PerformanceSourceSFDCTasks, empty); err != nil || store.requests[3].Mutations == nil {
		test.Fatalf("nil mutations must normalize to empty: %v", err)
	}
}

func TestPerformanceSyncReplayConflictPropagates(test *testing.T) {
	store := &performanceStoreFake{applyErr: repository.ErrConditionFailed}
	svc := performanceTestService(store, nil)
	if _, err := svc.Sync(test.Context(), "owner", model.PerformanceSourceSFDCTasks, performanceTaskSync("2026-03-04")); !errors.Is(err, repository.ErrConditionFailed) {
		test.Fatalf("conflict must keep the sentinel: %v", err)
	}
}

func TestSyncReplayAcrossYearBoundary(test *testing.T) {
	store := &performanceStoreFake{keepReceipts: true}
	svc := performanceTestService(store, nil)
	svc.now = func() time.Time { return time.Date(2026, 12, 30, 9, 0, 0, 0, time.UTC) }
	request := performanceTaskSync("2025-06-01")
	first, err := svc.Sync(test.Context(), "owner", model.PerformanceSourceSFDCTasks, request)
	if err != nil {
		test.Fatalf("apply in window: %v", err)
	}
	svc.now = func() time.Time { return time.Date(2027, 1, 2, 9, 0, 0, 0, time.UTC) }
	replay, err := svc.Sync(test.Context(), "owner", model.PerformanceSourceSFDCTasks, request)
	if err != nil || *replay != *first || store.applied != 1 {
		test.Fatalf("identical replay must return the stored receipt: %v %+v applied=%d", err, replay, store.applied)
	}
	fresh := performanceTaskSync("2025-06-01")
	fresh.BatchID = "11111111-2222-4333-8444-555555555555"
	if _, err := svc.Sync(test.Context(), "owner", model.PerformanceSourceSFDCTasks, fresh); !errors.Is(err, ErrInvalidInput) || store.applied != 1 {
		test.Fatalf("new batch outside window must be invalid: %v", err)
	}
	changed := performanceTaskSync("2025-06-01")
	changed.Mutations[0].Task.Subject = "Changed"
	if _, err := svc.Sync(test.Context(), "owner", model.PerformanceSourceSFDCTasks, changed); !errors.Is(err, repository.ErrConditionFailed) {
		test.Fatalf("digest mismatch must conflict: %v", err)
	}
}

func TestPerformanceSyncValidation(test *testing.T) {
	task := func(mutate func(*model.SFDCTask)) model.PerformanceSyncRequest {
		request := performanceTaskSync("2026-03-04")
		mutate(request.Mutations[0].Task)
		return request
	}
	assignment := model.PerformanceSyncRequest{BatchID: performanceServiceBatch, Mutations: []model.PerformanceMutation{
		{Op: "upsert", Assignment: &model.SFDCAssignment{SFDCAccountID: "A1", Name: "Customer", Role: "REGISTRY"}},
	}}
	cases := []struct {
		name    string
		source  string
		request model.PerformanceSyncRequest
		ok      bool
	}{
		{"valid task", model.PerformanceSourceSFDCTasks, performanceTaskSync("2026-03-04"), true},
		{"previous year start", model.PerformanceSourceSFDCTasks, performanceTaskSync("2025-01-01"), true},
		{"before previous year", model.PerformanceSourceSFDCTasks, performanceTaskSync("2024-12-31"), false},
		{"one year ahead", model.PerformanceSourceSFDCTasks, performanceTaskSync("2027-11-10"), true},
		{"over one year ahead", model.PerformanceSourceSFDCTasks, performanceTaskSync("2027-11-11"), false},
		{"impossible date", model.PerformanceSourceSFDCTasks, performanceTaskSync("2026-02-30"), false},
		{"datetime", model.PerformanceSourceSFDCTasks, performanceTaskSync("2026-03-04T00:00:00Z"), false},
		{"accountless", model.PerformanceSourceSFDCTasks, task(func(t *model.SFDCTask) { t.SFDCAccountID = "" }), true},
		{"bad account", model.PerformanceSourceSFDCTasks, task(func(t *model.SFDCTask) { t.SFDCAccountID = "A.1" }), false},
		{"long id", model.PerformanceSourceSFDCTasks, task(func(t *model.SFDCTask) { t.TaskID = strings.Repeat("a", 65) }), false},
		{"max id", model.PerformanceSourceSFDCTasks, task(func(t *model.SFDCTask) { t.TaskID = strings.Repeat("a", 64) }), true},
		{"bad specreq", model.PerformanceSourceSFDCTasks, task(func(t *model.SFDCTask) { t.SpecReqID = "SR 1" }), false},
		{"subject 300", model.PerformanceSourceSFDCTasks, task(func(t *model.SFDCTask) { t.Subject = strings.Repeat("a", 300) }), true},
		{"subject 301", model.PerformanceSourceSFDCTasks, task(func(t *model.SFDCTask) { t.Subject = strings.Repeat("a", 301) }), false},
		{"invalid utf8", model.PerformanceSourceSFDCTasks, task(func(t *model.SFDCTask) { t.Subject = "\xff" }), false},
		{"task on assignment source", model.PerformanceSourceSFDCAssignments, performanceTaskSync("2026-03-04"), false},
		{"assignment on task source", model.PerformanceSourceSFDCTasks, assignment, false},
		{"valid assignment", model.PerformanceSourceSFDCAssignments, assignment, true},
		{"unknown source", "mail", performanceTaskSync("2026-03-04"), false},
		{"bad batch", model.PerformanceSourceSFDCTasks, model.PerformanceSyncRequest{BatchID: "batch-1"}, false},
		{"negative revision", model.PerformanceSourceSFDCTasks, model.PerformanceSyncRequest{BatchID: performanceServiceBatch, ExpectedRevision: -1}, false},
		{"remove", model.PerformanceSourceSFDCTasks, model.PerformanceSyncRequest{BatchID: performanceServiceBatch, Mutations: []model.PerformanceMutation{{Op: "remove", ExternalID: "T1"}}}, true},
		{"remove with task", model.PerformanceSourceSFDCTasks, model.PerformanceSyncRequest{BatchID: performanceServiceBatch, Mutations: []model.PerformanceMutation{{Op: "remove", ExternalID: "T1", Task: &model.SFDCTask{}}}}, false},
		{"unknown op", model.PerformanceSourceSFDCTasks, model.PerformanceSyncRequest{BatchID: performanceServiceBatch, Mutations: []model.PerformanceMutation{{Op: "patch", ExternalID: "T1"}}}, false},
	}
	long := assignment
	long.Mutations = []model.PerformanceMutation{{Op: "upsert", Assignment: &model.SFDCAssignment{SFDCAccountID: "A1", Name: strings.Repeat("a", 201)}}}
	cases = append(cases, struct {
		name    string
		source  string
		request model.PerformanceSyncRequest
		ok      bool
	}{"name 201", model.PerformanceSourceSFDCAssignments, long, false})
	for _, scenario := range cases {
		test.Run(scenario.name, func(test *testing.T) {
			store := &performanceStoreFake{}
			_, err := performanceTestService(store, nil).Sync(test.Context(), "owner", scenario.source, scenario.request)
			if scenario.ok && (err != nil || store.applied != 1) {
				test.Fatalf("expected acceptance: %v", err)
			}
			if !scenario.ok && (!errors.Is(err, ErrInvalidInput) || store.applied != 0) {
				test.Fatalf("expected ErrInvalidInput before storage: %v applied=%d", err, store.applied)
			}
		})
	}
	if _, err := performanceTestService(&performanceStoreFake{}, nil).Sync(test.Context(), "", model.PerformanceSourceSFDCTasks, performanceTaskSync("2026-03-04")); !errors.Is(err, ErrForbidden) {
		test.Fatalf("missing user: %v", err)
	}
}

func TestPerformanceMappingAuthorization(test *testing.T) {
	store := &performanceStoreFake{assignments: map[string][]model.SFDCAssignment{"owner": {{SFDCAccountID: "A1", Name: "Customer"}}}}
	accounts := performanceAccountsFake{readable: map[string]bool{"owner/acct-1": true}}
	svc := performanceTestService(store, accounts)
	if _, err := svc.PutMapping(test.Context(), "owner", "A1", "acct-2"); !errors.Is(err, ErrForbidden) || len(store.put) != 0 {
		test.Fatalf("unreadable account must be forbidden: %v", err)
	}
	if _, err := svc.PutMapping(test.Context(), "owner", "A9", "acct-1"); !errors.Is(err, ErrNotFound) || len(store.put) != 0 {
		test.Fatalf("unassigned SFDC account must be not found: %v", err)
	}
	if _, err := svc.PutMapping(test.Context(), "owner", "A 1", "acct-1"); !errors.Is(err, ErrInvalidInput) {
		test.Fatalf("bad SFDC id: %v", err)
	}
	mapping, err := svc.PutMapping(test.Context(), "owner", "A1", "acct-1")
	if err != nil || len(store.put) != 1 || !store.put[0].Manual || store.putManual[0] || mapping.AccountID != "acct-1" {
		test.Fatalf("manual mapping must be written unconditionally: %+v %v", store.put, err)
	}
	failing := performanceTestService(store, performanceAccountsFake{err: errors.New("dynamo down")})
	if _, err := failing.PutMapping(test.Context(), "owner", "A1", "acct-1"); err == nil || errors.Is(err, ErrForbidden) {
		test.Fatalf("lookup failure must surface: %v", err)
	}
	if _, err := performanceTestService(store, nil).PutMapping(test.Context(), "owner", "A1", "acct-1"); !errors.Is(err, ErrForbidden) {
		test.Fatalf("missing account reader must fail closed: %v", err)
	}
}

func TestPerformanceListMappingsMarksUnmapped(test *testing.T) {
	store := &performanceStoreFake{
		assignments: map[string][]model.SFDCAssignment{"owner": {{SFDCAccountID: "B2", Name: "Beta"}, {SFDCAccountID: "A1", Name: "Alpha", Role: "REGISTRY"}}},
		mappings:    map[string][]model.AccountMapping{"owner": {{SFDCAccountID: "A1", AccountID: "acct-1", Manual: true}, {SFDCAccountID: "Z9", AccountID: "acct-9"}}},
	}
	list, err := performanceTestService(store, nil).ListMappings(test.Context(), "owner")
	if err != nil || len(list.Items) != 2 {
		test.Fatalf("mappings: %+v %v", list, err)
	}
	first, second := list.Items[0], list.Items[1]
	if first.SFDCAccountID != "A1" || !first.Mapped || first.AccountID != "acct-1" || !first.Manual || first.Role != "REGISTRY" ||
		second.SFDCAccountID != "B2" || second.Mapped || second.AccountID != "" {
		test.Fatalf("assignments must be joined and sorted, unmapped kept: %+v", list.Items)
	}
}

func TestPerformanceGoalsValidation(test *testing.T) {
	store := &performanceStoreFake{}
	svc := performanceTestService(store, nil)
	if _, err := svc.PutGoals(test.Context(), "owner", 2026, strings.Repeat("a", 8001)); !errors.Is(err, ErrInvalidInput) {
		test.Fatalf("oversized goals: %v", err)
	}
	for _, year := range []int{2024, 2028} {
		if _, err := svc.PutGoals(test.Context(), "owner", year, "x"); !errors.Is(err, ErrInvalidInput) {
			test.Fatalf("year %d outside retention: %v", year, err)
		}
		if _, err := svc.GetGoals(test.Context(), "owner", year); !errors.Is(err, ErrInvalidInput) {
			test.Fatalf("get year %d: %v", year, err)
		}
	}
	empty, err := svc.GetGoals(test.Context(), "owner", 2026)
	if err != nil || empty.Year != 2026 || empty.Text != "" {
		test.Fatalf("missing goals return empty text: %+v %v", empty, err)
	}
	saved, err := svc.PutGoals(test.Context(), "owner", 2026, strings.Repeat("a", 8000))
	if err != nil || saved.Year != 2026 || saved.UpdatedAt == "" || store.goals["owner"].Year != 2026 {
		test.Fatalf("goals save: %+v %v", saved, err)
	}
}

func TestPerformanceSourcesListsEveryKnownSource(test *testing.T) {
	store := &performanceStoreFake{statuses: map[string][]model.PerformanceSourceStatus{
		"owner": {{Source: model.PerformanceSourceSFDCTasks, Revision: 4, LastCompletedAt: "2026-11-09T00:00:00Z"}},
	}}
	list, err := performanceTestService(store, nil).ListSources(test.Context(), "owner")
	if err != nil || len(list.Sources) != 2 || list.Sources[0].Source != model.PerformanceSourceSFDCAssignments ||
		list.Sources[0].Revision != 0 || list.Sources[1].Revision != 4 {
		test.Fatalf("sources: %+v %v", list, err)
	}
}

func TestPerformanceMissedPlaceholder(test *testing.T) {
	svc := performanceTestService(&performanceStoreFake{}, nil)
	list, err := svc.ListMissed(test.Context(), "owner", "week")
	if err != nil || list.Items == nil || len(list.Items) != 0 {
		test.Fatalf("missed: %+v %v", list, err)
	}
	if _, err := svc.ListMissed(test.Context(), "owner", "month"); !errors.Is(err, ErrInvalidInput) {
		test.Fatalf("bad period: %v", err)
	}
}

func performanceAssignmentRows(n int) []model.SFDCAssignment {
	rows := make([]model.SFDCAssignment, n)
	for i := range rows {
		rows[i] = model.SFDCAssignment{SFDCAccountID: fmt.Sprintf("A%04d", i), Name: "n"}
	}
	return rows
}

func TestSyncRejectsAssignmentsOverCap(test *testing.T) {
	upsert := func(id string) model.PerformanceMutation {
		return model.PerformanceMutation{Op: "upsert", Assignment: &model.SFDCAssignment{SFDCAccountID: id, Name: "n"}}
	}
	for _, tc := range []struct {
		name      string
		stored    int
		mutations []model.PerformanceMutation
		ok        bool
	}{
		{"new id at cap", MaxPerformanceAssignments, []model.PerformanceMutation{upsert("NEW")}, false},
		{"new id below cap", MaxPerformanceAssignments - 1, []model.PerformanceMutation{upsert("NEW")}, true},
		{"update existing at cap", MaxPerformanceAssignments, []model.PerformanceMutation{upsert("A0001")}, true},
		{"remove at cap", MaxPerformanceAssignments, []model.PerformanceMutation{{Op: "remove", ExternalID: "A0001"}}, true},
		{"remove offsets new", MaxPerformanceAssignments, []model.PerformanceMutation{{Op: "remove", ExternalID: "A0001"}, upsert("NEW")}, true},
		{"remove of unknown id offsets nothing", MaxPerformanceAssignments, []model.PerformanceMutation{{Op: "remove", ExternalID: "GONE"}, upsert("NEW")}, false},
		{"over cap removal still allowed", MaxPerformanceAssignments + 5, []model.PerformanceMutation{{Op: "remove", ExternalID: "A0001"}}, true},
	} {
		test.Run(tc.name, func(test *testing.T) {
			store := &performanceStoreFake{assignments: map[string][]model.SFDCAssignment{"owner": performanceAssignmentRows(tc.stored)}}
			request := model.PerformanceSyncRequest{BatchID: performanceServiceBatch, Complete: true, Mutations: tc.mutations}
			_, err := performanceTestService(store, nil).Sync(test.Context(), "owner", model.PerformanceSourceSFDCAssignments, request)
			if tc.ok && err != nil || !tc.ok && !errors.Is(err, ErrInvalidInput) {
				test.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if !tc.ok && store.applied != 0 {
				test.Fatal("rejected batch must not be applied")
			}
		})
	}
}

func TestListMappingsBoundedAndTruncated(test *testing.T) {
	for _, tc := range []struct {
		n         int
		truncated bool
	}{{MaxPerformanceAssignments, false}, {MaxPerformanceAssignments + 1, true}, {MaxPerformanceAssignments + 50, true}} {
		rows := performanceAssignmentRows(tc.n)
		rows[0], rows[len(rows)-1] = rows[len(rows)-1], rows[0] // unsorted input
		store := &performanceStoreFake{assignments: map[string][]model.SFDCAssignment{"owner": rows}}
		list, err := performanceTestService(store, nil).ListMappings(test.Context(), "owner")
		if err != nil || len(list.Items) > MaxPerformanceAssignments || list.Truncated != tc.truncated ||
			list.Items[0].SFDCAccountID != "A0000" {
			test.Fatalf("n=%d: items=%d truncated=%v err=%v", tc.n, len(list.Items), list.Truncated, err)
		}
	}
}
