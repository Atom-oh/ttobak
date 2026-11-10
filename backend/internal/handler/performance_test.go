package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

const performanceHTTPBatch = "90ec2d25-a32e-43a5-96a9-e203859b7d19"

type performanceSavedBatch struct {
	digest  string
	receipt model.PerformanceSyncReceipt
}

// performanceMemoryStore keeps rows per user, mirroring the owner partition.
type performanceMemoryStore struct {
	batches     map[string]performanceSavedBatch
	revisions   map[string]int64
	assignments map[string]map[string]model.SFDCAssignment
	tasks       map[string]map[string]model.SFDCTask
	mappings    map[string]map[string]model.AccountMapping
	goals       map[string]model.PerformanceGoals
}

func newPerformanceMemoryStore() *performanceMemoryStore {
	return &performanceMemoryStore{
		batches: map[string]performanceSavedBatch{}, revisions: map[string]int64{},
		assignments: map[string]map[string]model.SFDCAssignment{}, tasks: map[string]map[string]model.SFDCTask{},
		mappings: map[string]map[string]model.AccountMapping{}, goals: map[string]model.PerformanceGoals{},
	}
}

func (store *performanceMemoryStore) ApplyPerformanceBatch(_ context.Context, userID, source string, req model.PerformanceSyncRequest, digest string) (*model.PerformanceSyncReceipt, error) {
	key := userID + "|" + source + "|" + req.BatchID
	if saved, found := store.batches[key]; found {
		if saved.digest != digest {
			return nil, repository.ErrConditionFailed
		}
		receipt := saved.receipt
		return &receipt, nil
	}
	if store.revisions[userID+"|"+source] != req.ExpectedRevision {
		return nil, repository.ErrConditionFailed
	}
	receipt := model.PerformanceSyncReceipt{Revision: req.ExpectedRevision + 1, Completed: req.Complete}
	for _, mutation := range req.Mutations {
		switch {
		case mutation.Assignment != nil:
			if store.assignments[userID] == nil {
				store.assignments[userID] = map[string]model.SFDCAssignment{}
			}
			store.assignments[userID][mutation.Assignment.SFDCAccountID] = *mutation.Assignment
			receipt.Upserted++
		case mutation.Task != nil:
			if store.tasks[userID] == nil {
				store.tasks[userID] = map[string]model.SFDCTask{}
			}
			store.tasks[userID][mutation.Task.TaskID] = *mutation.Task
			receipt.Upserted++
		default:
			receipt.Removed++
		}
	}
	store.revisions[userID+"|"+source] = receipt.Revision
	store.batches[key] = performanceSavedBatch{digest: digest, receipt: receipt}
	return &receipt, nil
}

func (store *performanceMemoryStore) ListPerformanceSources(_ context.Context, userID string) ([]model.PerformanceSourceStatus, error) {
	var out []model.PerformanceSourceStatus
	for _, source := range []string{model.PerformanceSourceSFDCAssignments, model.PerformanceSourceSFDCTasks} {
		if revision := store.revisions[userID+"|"+source]; revision > 0 {
			out = append(out, model.PerformanceSourceStatus{Source: source, Revision: revision, LastCompletedAt: "2026-11-10T00:00:00Z"})
		}
	}
	return out, nil
}

func (store *performanceMemoryStore) ListPerformanceAssignments(_ context.Context, userID string) ([]model.SFDCAssignment, error) {
	var out []model.SFDCAssignment
	for _, assignment := range store.assignments[userID] {
		out = append(out, assignment)
	}
	return out, nil
}

func (store *performanceMemoryStore) ListAccountMappings(_ context.Context, userID string) ([]model.AccountMapping, error) {
	var out []model.AccountMapping
	for _, mapping := range store.mappings[userID] {
		out = append(out, mapping)
	}
	return out, nil
}

func (store *performanceMemoryStore) PutAccountMapping(_ context.Context, userID string, mapping model.AccountMapping, _ bool) error {
	if store.mappings[userID] == nil {
		store.mappings[userID] = map[string]model.AccountMapping{}
	}
	store.mappings[userID][mapping.SFDCAccountID] = mapping
	return nil
}

func (store *performanceMemoryStore) DeleteAccountMappingIfAutomatic(_ context.Context, userID, sfdcAccountID string) error {
	if mapping, found := store.mappings[userID][sfdcAccountID]; found && !mapping.Manual {
		delete(store.mappings[userID], sfdcAccountID)
	}
	return nil
}

func (store *performanceMemoryStore) GetPerformanceGoals(_ context.Context, userID string, year int) (*model.PerformanceGoals, error) {
	goals, found := store.goals[userID]
	if !found || goals.Year != year {
		return nil, nil
	}
	return &goals, nil
}

func (store *performanceMemoryStore) PutPerformanceGoals(_ context.Context, userID string, goals model.PerformanceGoals) error {
	store.goals[userID] = goals
	return nil
}

type performanceHTTPAccounts map[string]bool

func (readable performanceHTTPAccounts) GetAccount(_ context.Context, userID, accountID string) (*model.AccountResponse, error) {
	if !readable[userID+"/"+accountID] {
		return nil, service.ErrForbidden
	}
	return &model.AccountResponse{}, nil
}

func performanceHTTPRouter(api PerformanceAPI, userID string) http.Handler {
	handler := NewPerformanceHandler(api)
	router := chi.NewRouter()
	if userID != "" {
		router.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), middleware.UserIDKey, userID)))
			})
		})
	}
	router.Post("/api/performance/sources/{source}/sync", handler.Sync)
	router.Get("/api/performance/sources", handler.ListSources)
	router.Get("/api/performance/goals", handler.GetGoals)
	router.Put("/api/performance/goals", handler.PutGoals)
	router.Get("/api/performance/mappings", handler.ListMappings)
	router.Put("/api/performance/mappings/{sfdcAccountId}", handler.PutMapping)
	router.Get("/api/performance/missed", handler.ListMissed)
	return router
}

func performanceHTTPDo(api PerformanceAPI, userID, method, target, body string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	performanceHTTPRouter(api, userID).ServeHTTP(response, httptest.NewRequest(method, target, strings.NewReader(body)))
	return response
}

func performanceRecentDate() string { return time.Now().UTC().Format("2006-01-02") }

func performanceTaskBody(subject string) string {
	return `{"batchId":"` + performanceHTTPBatch + `","expectedRevision":0,"complete":true,"mutations":[{"op":"upsert","task":{"taskId":"T1","sfdcAccountId":"A1","activityDate":"` +
		performanceRecentDate() + `","type":"Call","subject":"` + subject + `","status":"Completed"}}]}`
}

const performanceAssignmentBody = `{"batchId":"` + performanceHTTPBatch + `","expectedRevision":0,"complete":true,"mutations":[{"op":"upsert","assignment":{"sfdcAccountId":"A1","name":"Customer","role":"REGISTRY"}}]}`

func performanceErrorCode(test *testing.T, response *httptest.ResponseRecorder) string {
	test.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		test.Fatalf("error body: %s", response.Body.String())
	}
	return body.Error.Code
}

func newPerformanceHTTPService(store *performanceMemoryStore, readable performanceHTTPAccounts) *service.PerformanceService {
	return service.NewPerformanceService(store, readable)
}

func TestSyncRejectsUnknownFields(test *testing.T) {
	store := newPerformanceMemoryStore()
	api := newPerformanceHTTPService(store, nil)
	sync := "/api/performance/sources/sfdc-tasks/sync"
	bodies := map[string]string{
		"description":      strings.Replace(performanceTaskBody("Sync"), `"status":"Completed"`, `"status":"Completed","description":"PRIVATE"`, 1),
		"comments":         strings.Replace(performanceTaskBody("Sync"), `"status":"Completed"`, `"status":"Completed","comments":"PRIVATE"`, 1),
		"top-level userId": strings.Replace(performanceTaskBody("Sync"), `"complete":true`, `"complete":true,"userId":"other"`, 1),
		"missing revision": strings.Replace(performanceTaskBody("Sync"), `"expectedRevision":0,`, "", 1),
		"null mutations":   `{"batchId":"` + performanceHTTPBatch + `","expectedRevision":0,"complete":true,"mutations":null}`,
		"duplicate key":    strings.Replace(performanceTaskBody("Sync"), `"complete":true`, `"complete":true,"complete":false`, 1),
		"trailing":         performanceTaskBody("Sync") + "{}",
		"invalid utf8":     performanceTaskBody("\xff"),
	}
	for name, body := range bodies {
		response := performanceHTTPDo(api, "owner", http.MethodPost, sync, body)
		if response.Code != http.StatusBadRequest || performanceErrorCode(test, response) != model.ErrCodeBadRequest {
			test.Fatalf("%s: %d %s", name, response.Code, response.Body.String())
		}
	}
	if len(store.tasks) != 0 {
		test.Fatalf("rejected bodies must not be stored: %v", store.tasks)
	}
	padded := performanceTaskBody("Sync")
	if response := performanceHTTPDo(api, "owner", http.MethodPost, sync, padded+strings.Repeat(" ", 65537-len(padded))); response.Code != http.StatusRequestEntityTooLarge {
		test.Fatalf("64 KiB cap: %d", response.Code)
	}
	if response := performanceHTTPDo(api, "owner", http.MethodPost, sync, padded+strings.Repeat(" ", 65536-len(padded))); response.Code != http.StatusOK {
		test.Fatalf("64 KiB exactly: %d %s", response.Code, response.Body.String())
	}
}

func TestSyncRejectsUnknownSource(test *testing.T) {
	api := newPerformanceHTTPService(newPerformanceMemoryStore(), nil)
	for _, source := range []string{"mail", "calendar", "SFDC-TASKS"} {
		response := performanceHTTPDo(api, "owner", http.MethodPost, "/api/performance/sources/"+source+"/sync", performanceTaskBody("Sync"))
		if response.Code != http.StatusBadRequest {
			test.Fatalf("%s: %d", source, response.Code)
		}
	}
	if response := performanceHTTPDo(api, "owner", http.MethodPost, "/api/performance/sources/sfdc-assignments/sync", performanceTaskBody("Sync")); response.Code != http.StatusBadRequest {
		test.Fatalf("task on the assignment source: %d", response.Code)
	}
}

func TestSyncReplay(test *testing.T) {
	api := newPerformanceHTTPService(newPerformanceMemoryStore(), nil)
	sync := "/api/performance/sources/sfdc-tasks/sync"
	first := performanceHTTPDo(api, "owner", http.MethodPost, sync, performanceTaskBody("Sync"))
	second := performanceHTTPDo(api, "owner", http.MethodPost, sync, performanceTaskBody("Sync"))
	if first.Code != http.StatusOK || second.Code != http.StatusOK || first.Body.String() != second.Body.String() {
		test.Fatalf("replay must return the same receipt: %d %s / %d %s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	var receipt model.PerformanceSyncReceipt
	if json.Unmarshal(first.Body.Bytes(), &receipt) != nil || receipt.Revision != 1 || !receipt.Completed || receipt.Upserted != 1 {
		test.Fatalf("receipt: %s", first.Body.String())
	}
	conflict := performanceHTTPDo(api, "owner", http.MethodPost, sync, performanceTaskBody("Changed"))
	if conflict.Code != http.StatusConflict || performanceErrorCode(test, conflict) != model.ErrCodeConflict {
		test.Fatalf("same batch, different digest: %d %s", conflict.Code, conflict.Body.String())
	}
	if response := performanceHTTPDo(api, "owner", http.MethodPost, sync, performanceTaskBody("Sync")); response.Body.String() != first.Body.String() {
		test.Fatalf("conflict must not overwrite the receipt: %s", response.Body.String())
	}
}

func TestPerformanceRoutesAreOwnerScoped(test *testing.T) {
	store := newPerformanceMemoryStore()
	api := newPerformanceHTTPService(store, performanceHTTPAccounts{"user-a/acct-a": true})
	year := time.Now().UTC().Format("2006")
	steps := []struct{ method, target, body string }{
		{http.MethodPost, "/api/performance/sources/sfdc-assignments/sync", performanceAssignmentBody},
		{http.MethodPost, "/api/performance/sources/sfdc-tasks/sync", performanceTaskBody("Sync")},
		{http.MethodPut, "/api/performance/goals?year=" + year, `{"text":"A's private goals"}`},
		{http.MethodPut, "/api/performance/mappings/A1", `{"accountId":"acct-a"}`},
	}
	for _, step := range steps {
		if response := performanceHTTPDo(api, "user-a", step.method, step.target, step.body); response.Code != http.StatusOK {
			test.Fatalf("user A %s %s: %d %s", step.method, step.target, response.Code, response.Body.String())
		}
	}
	if mapping := store.mappings["user-a"]["A1"]; !mapping.Manual || mapping.AccountID != "acct-a" {
		test.Fatalf("manual mapping: %+v", mapping)
	}
	owner := performanceHTTPDo(api, "user-a", http.MethodGet, "/api/performance/mappings", "")
	if !strings.Contains(owner.Body.String(), `"mapped":true`) || !strings.Contains(owner.Body.String(), `"acct-a"`) {
		test.Fatalf("owner mappings: %s", owner.Body.String())
	}
	reads := map[string]string{
		"/api/performance/mappings":           `{"items":[]}`,
		"/api/performance/goals?year=" + year: `{"year":` + year + `,"text":"","updatedAt":""}`,
		"/api/performance/missed?period=week": `{"items":[]}`,
		"/api/performance/missed?period=year": `{"items":[]}`,
	}
	for target, expected := range reads {
		response := performanceHTTPDo(api, "user-b", http.MethodGet, target, "")
		if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != expected {
			test.Fatalf("user B %s: %d %s", target, response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "private, no-store" {
			test.Fatalf("performance reads must not be cached: %s", target)
		}
	}
	sources := performanceHTTPDo(api, "user-b", http.MethodGet, "/api/performance/sources", "")
	if sources.Code != http.StatusOK || strings.Contains(sources.Body.String(), "lastCompletedAt") || strings.Contains(sources.Body.String(), `"revision":1`) {
		test.Fatalf("user B sources: %s", sources.Body.String())
	}
	forbidden := performanceHTTPDo(api, "user-b", http.MethodPut, "/api/performance/mappings/A1", `{"accountId":"acct-a"}`)
	if forbidden.Code != http.StatusForbidden || performanceErrorCode(test, forbidden) != model.ErrCodeForbidden {
		test.Fatalf("mapping to a non-member account: %d %s", forbidden.Code, forbidden.Body.String())
	}
	unassigned := performanceHTTPDo(api, "user-a", http.MethodPut, "/api/performance/mappings/A9", `{"accountId":"acct-a"}`)
	if unassigned.Code != http.StatusNotFound {
		test.Fatalf("mapping an unassigned SFDC account: %d", unassigned.Code)
	}
	if len(store.mappings["user-b"]) != 0 || len(store.tasks["user-b"]) != 0 {
		test.Fatalf("user B must have no rows")
	}
	for _, target := range []string{"/api/performance/sources", "/api/performance/mappings", "/api/performance/missed", "/api/performance/goals?year=" + year} {
		if response := performanceHTTPDo(api, "", http.MethodGet, target, ""); response.Code != http.StatusUnauthorized {
			test.Fatalf("unauthenticated %s: %d", target, response.Code)
		}
	}
	if response := performanceHTTPDo(api, "user-a", http.MethodGet, "/api/performance/mappings?userId=user-b", ""); response.Code != http.StatusBadRequest {
		test.Fatalf("query identity must be rejected: %d", response.Code)
	}
	if response := performanceHTTPDo(api, "user-a", http.MethodGet, "/api/performance/missed?period=month", ""); response.Code != http.StatusBadRequest {
		test.Fatalf("bad period: %d", response.Code)
	}
}

func TestGoalsTooLarge(test *testing.T) {
	store := newPerformanceMemoryStore()
	api := newPerformanceHTTPService(store, nil)
	year := time.Now().UTC().Format("2006")
	target := "/api/performance/goals?year=" + year
	if response := performanceHTTPDo(api, "owner", http.MethodPut, target, `{"text":"`+strings.Repeat("a", 8001)+`"}`); response.Code != http.StatusBadRequest {
		test.Fatalf("8,001 bytes: %d", response.Code)
	}
	if response := performanceHTTPDo(api, "owner", http.MethodPut, target, `{"text":"`+strings.Repeat("가", 2667)+`"}`); response.Code != http.StatusBadRequest {
		test.Fatalf("8,001 UTF-8 bytes: %d", response.Code)
	}
	if _, found := store.goals["owner"]; found {
		test.Fatalf("oversized goals stored")
	}
	if response := performanceHTTPDo(api, "owner", http.MethodPut, target, `{"text":"`+strings.Repeat("a", 8000)+`"}`); response.Code != http.StatusOK {
		test.Fatalf("8,000 bytes: %d %s", response.Code, response.Body.String())
	}
	for name, request := range map[string][2]string{
		"unknown field": {target, `{"text":"x","year":2020}`},
		"missing year":  {"/api/performance/goals", `{"text":"x"}`},
		"bad year":      {"/api/performance/goals?year=26", `{"text":"x"}`},
		"old year":      {"/api/performance/goals?year=2000", `{"text":"x"}`},
		"missing text":  {target, `{}`},
	} {
		if response := performanceHTTPDo(api, "owner", http.MethodPut, request[0], request[1]); response.Code != http.StatusBadRequest {
			test.Fatalf("%s: %d", name, response.Code)
		}
	}
}
