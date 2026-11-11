package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func (store *performanceMemoryStore) PutPerformanceReport(_ context.Context, userID string, report model.PerformanceReport) error {
	if store.reports[userID] == nil {
		store.reports[userID] = map[string]model.PerformanceReport{}
	}
	store.reports[userID][report.PeriodKey] = report
	return nil
}

func (store *performanceMemoryStore) GetPerformanceReport(_ context.Context, userID, periodKey string) (*model.PerformanceReport, error) {
	report, found := store.reports[userID][periodKey]
	if !found {
		return nil, nil
	}
	return &report, nil
}

func (store *performanceMemoryStore) ListPerformanceReports(_ context.Context, userID string) ([]model.PerformanceReportSummary, bool, error) {
	var out []model.PerformanceReportSummary
	for _, report := range store.reports[userID] {
		out = append(out, model.PerformanceReportSummary{PeriodKey: report.PeriodKey, Label: report.Period.Label, GeneratedAt: report.GeneratedAt, Partial: report.Partial})
	}
	return out, false, nil
}

func (store *performanceMemoryStore) DeletePerformanceReport(_ context.Context, userID, periodKey string) error {
	if _, found := store.reports[userID][periodKey]; !found {
		return repository.ErrConditionFailed
	}
	delete(store.reports[userID], periodKey)
	return nil
}

const performanceReportBody = `{"schemaVersion":1,"periodKey":"Q2026-3","period":{"start":"2026-07-01","end":"2026-09-30","label":"2026년 3분기"},` +
	`"alias":"owner","generatedAt":"2026-10-02T01:02:03Z","model":"claude","partial":false,"missingSources":[],` +
	`"highlights":{"projects":["이관"],"initiatives":[],"channels":[],"programs":[]},"summary":"요약",` +
	`"ledger":[{"label":"기회","value":"1200000","unit":"USD"}],` +
	`"sections":[{"id":"customers","title":"고객","lede":"l","paragraphs":["p"],"tables":[{"columns":["a","b"],"rows":[["1","2"]]}],"cards":[{"title":"c","body":"b"}]}],` +
	`"actions":[{"severity":"mid","title":"t","detail":"d","source":"sfdc"}],"dataBasis":[{"source":"sfdc","query":"q","exclusions":"e"}]}`

func TestReportRoutesOwnerOnly(test *testing.T) {
	store := newPerformanceMemoryStore()
	api := newPerformanceHTTPService(store, nil)
	put := performanceHTTPDo(api, "user-a", http.MethodPut, "/api/performance/reports/Q2026-3", performanceReportBody)
	if put.Code != http.StatusOK || strings.TrimSpace(put.Body.String()) != `{"periodKey":"Q2026-3","generatedAt":"2026-10-02T01:02:03Z"}` {
		test.Fatalf("owner put: %d %s", put.Code, put.Body.String())
	}
	owner := performanceHTTPDo(api, "user-a", http.MethodGet, "/api/performance/reports/Q2026-3", "")
	var round map[string]any
	if owner.Code != http.StatusOK || json.Unmarshal(owner.Body.Bytes(), &round) != nil || round["summary"] != "요약" ||
		round["sections"].([]any)[0].(map[string]any)["tables"].([]any)[0].(map[string]any)["rows"].([]any)[0].([]any)[1] != "2" {
		test.Fatalf("owner get: %d %s", owner.Code, owner.Body.String())
	}
	list := performanceHTTPDo(api, "user-a", http.MethodGet, "/api/performance/reports", "")
	if list.Code != http.StatusOK || strings.TrimSpace(list.Body.String()) !=
		`{"items":[{"periodKey":"Q2026-3","label":"2026년 3분기","generatedAt":"2026-10-02T01:02:03Z","partial":false}],"truncated":false}` {
		test.Fatalf("owner list: %d %s", list.Code, list.Body.String())
	}
	other := performanceHTTPDo(api, "user-b", http.MethodGet, "/api/performance/reports", "")
	if other.Code != http.StatusOK || strings.TrimSpace(other.Body.String()) != `{"items":[],"truncated":false}` {
		test.Fatalf("user B list: %d %s", other.Code, other.Body.String())
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		response := performanceHTTPDo(api, "user-b", method, "/api/performance/reports/Q2026-3", "")
		if response.Code != http.StatusNotFound || performanceErrorCode(test, response) != model.ErrCodeNotFound {
			test.Fatalf("user B %s: %d %s", method, response.Code, response.Body.String())
		}
	}
	if _, found := store.reports["user-a"]["Q2026-3"]; !found {
		test.Fatalf("user B must not delete user A's report")
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		if response := performanceHTTPDo(api, "", method, "/api/performance/reports/Q2026-3", performanceReportBody); response.Code != http.StatusUnauthorized {
			test.Fatalf("unauthenticated %s: %d", method, response.Code)
		}
	}
	if response := performanceHTTPDo(api, "user-a", http.MethodGet, "/api/performance/reports?userId=user-b", ""); response.Code != http.StatusBadRequest {
		test.Fatalf("query identity must be rejected: %d", response.Code)
	}
	if response := performanceHTTPDo(api, "user-a", http.MethodPut, "/api/performance/reports/Q2026-4", performanceReportBody); response.Code != http.StatusBadRequest {
		test.Fatalf("a body for another period key: %d", response.Code)
	}
	deleted := performanceHTTPDo(api, "user-a", http.MethodDelete, "/api/performance/reports/Q2026-3", "")
	if deleted.Code != http.StatusNoContent {
		test.Fatalf("owner delete: %d %s", deleted.Code, deleted.Body.String())
	}
	if response := performanceHTTPDo(api, "user-a", http.MethodGet, "/api/performance/reports/Q2026-3", ""); response.Code != http.StatusNotFound {
		test.Fatalf("deleted report: %d", response.Code)
	}
}

func TestReportResponsesNoStore(test *testing.T) {
	store := newPerformanceMemoryStore()
	api := newPerformanceHTTPService(store, nil)
	requests := []struct{ user, method, target, body string }{
		{"owner", http.MethodPut, "/api/performance/reports/Q2026-3", performanceReportBody},
		{"owner", http.MethodGet, "/api/performance/reports", ""},
		{"owner", http.MethodGet, "/api/performance/reports/Q2026-3", ""},
		{"owner", http.MethodGet, "/api/performance/reports/Y1999", ""},
		{"owner", http.MethodPut, "/api/performance/reports/Q2026-3", `{"summary":"<script>"}`},
		{"", http.MethodGet, "/api/performance/reports", ""},
		{"owner", http.MethodDelete, "/api/performance/reports/Q2026-3", ""},
	}
	for _, step := range requests {
		response := performanceHTTPDo(api, step.user, step.method, step.target, step.body)
		if response.Header().Get("Cache-Control") != "private, no-store" {
			test.Fatalf("%s %s (%d) must not be cached", step.method, step.target, response.Code)
		}
	}
}

func TestReportBodyIsStrictAndCapped(test *testing.T) {
	store := newPerformanceMemoryStore()
	api := newPerformanceHTTPService(store, nil)
	target := "/api/performance/reports/Q2026-3"
	// 300 KB of padding inside a valid string: rejected before decoding.
	oversized := strings.Replace(performanceReportBody, `"summary":"요약"`, `"summary":"`+strings.Repeat("a", 300*1024)+`"`, 1)
	if response := performanceHTTPDo(api, "owner", http.MethodPut, target, oversized); response.Code != http.StatusRequestEntityTooLarge {
		test.Fatalf("over 256 KiB: %d", response.Code)
	}
	for name, body := range map[string]string{
		"unknown field":   strings.Replace(performanceReportBody, `"partial":false`, `"partial":false,"html":"x"`, 1),
		"null":            strings.Replace(performanceReportBody, `"missingSources":[]`, `"missingSources":null`, 1),
		"duplicate key":   strings.Replace(performanceReportBody, `"summary":"요약"`, `"summary":"요약","summary":"b"`, 1),
		"missing summary": strings.Replace(performanceReportBody, `"summary":"요약",`, ``, 1),
		"numeric cell":    strings.Replace(performanceReportBody, `["1","2"]`, `[1,2]`, 1),
		"markdown link":   strings.Replace(performanceReportBody, `"detail":"d"`, `"detail":"[x](https://a.b)"`, 1),
		"trailing data":   performanceReportBody + `{}`,
	} {
		if response := performanceHTTPDo(api, "owner", http.MethodPut, target, body); response.Code != http.StatusBadRequest {
			test.Fatalf("%s: %d %s", name, response.Code, response.Body.String())
		}
	}
	if len(store.reports["owner"]) != 0 {
		test.Fatalf("rejected reports must not be stored")
	}
	// Optional nested fields (tables, cards, note, estimates) may be absent.
	minimal := `{"schemaVersion":1,"periodKey":"Y2026","period":{"start":"2026-01-01","end":"2026-12-31","label":"2026"},"alias":"o",` +
		`"generatedAt":"2026-11-11T00:00:00Z","model":"m","partial":true,"summary":"s","ledger":[],"sections":[{"id":"a","title":"t","lede":"","paragraphs":[]}],"actions":[],"dataBasis":[]}`
	if response := performanceHTTPDo(api, "owner", http.MethodPut, "/api/performance/reports/Y2026", minimal); response.Code != http.StatusOK {
		test.Fatalf("minimal report: %d %s", response.Code, response.Body.String())
	}
	got := performanceHTTPDo(api, "owner", http.MethodGet, "/api/performance/reports/Y2026", "")
	if !strings.Contains(got.Body.String(), `"tables":[]`) || !strings.Contains(got.Body.String(), `"missingSources":[]`) || strings.Contains(got.Body.String(), "null") {
		test.Fatalf("absent lists must read back as []: %s", got.Body.String())
	}
}
