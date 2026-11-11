package repository

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func performanceTestReport(periodKey, start, end, generatedAt string) model.PerformanceReport {
	return model.PerformanceReport{
		SchemaVersion: 1, PeriodKey: periodKey,
		Period: model.PerformanceReportPeriod{Start: start, End: end, Label: "label " + periodKey},
		Alias:  "owner", GeneratedAt: generatedAt, Model: "m", Summary: "요약",
		Ledger: []model.PerformanceReportLedgerItem{}, Sections: []model.PerformanceReportSection{},
		Actions: []model.PerformanceReportAction{}, DataBasis: []model.PerformanceReportDataBasis{},
	}
}

func TestPutReportReplaces(test *testing.T) {
	pinPerformanceNow(test)
	var puts []map[string]any
	repo := summarySDKRepo(func(request *http.Request) (*http.Response, error) {
		body := performanceBody(test, request)
		if performanceTarget(request) != "PutItem" {
			test.Fatalf("unexpected call %s", performanceTarget(request))
		}
		if _, conditional := body["ConditionExpression"]; conditional {
			test.Fatalf("an owner singleton overwrite must be unconditional: %v", body)
		}
		puts = append(puts, body["Item"].(map[string]any))
		return summaryHTTPResponse(200, `{}`, nil), nil
	})
	first := performanceTestReport("Q2026-4", "2026-10-01", "2026-12-31", "2026-11-01T00:00:00Z")
	second := first
	second.GeneratedAt, second.Summary = "2026-11-09T00:00:00Z", "새 요약"
	for _, report := range []model.PerformanceReport{first, second} {
		if err := repo.PutPerformanceReport(test.Context(), "owner", report); err != nil {
			test.Fatal(err)
		}
	}
	want := mustJSONInt(time.Date(2029, time.January, 1, 0, 0, 0, 0, time.UTC).Unix())
	for _, item := range puts {
		if item["PK"].(map[string]any)["S"] != "USER#owner" || item["SK"].(map[string]any)["S"] != "PERFREPORT#Q2026-4" ||
			item["userId"].(map[string]any)["S"] != "owner" || item["pendingShareExpiresAt"].(map[string]any)["N"] != want {
			test.Fatalf("report row identity or TTL: %v", item)
		}
	}
	if puts[1]["summary"].(map[string]any)["S"] != "새 요약" {
		test.Fatalf("the second put must carry the replacement: %v", puts[1])
	}
	// A week whose Sunday falls in the next year expires by that end year.
	week := performanceTestReport("W2026-53", "2026-12-28", "2027-01-03", "2027-01-04T00:00:00Z")
	if err := repo.PutPerformanceReport(test.Context(), "owner", week); err != nil {
		test.Fatal(err)
	}
	if got := puts[2]["pendingShareExpiresAt"].(map[string]any)["N"]; got != mustJSONInt(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).Unix()) {
		test.Fatalf("week TTL follows the period end year: %v", got)
	}
	expired := performanceTestReport("Y2023", "2023-01-01", "2023-12-31", "2024-01-02T00:00:00Z")
	if err := repo.PutPerformanceReport(test.Context(), "owner", expired); !errors.Is(err, ErrInvalidPerformanceInput) {
		test.Fatalf("an already-expired report must not be written: %v", err)
	}
	for name, call := range map[string]func() error{
		"bad user": func() error { return repo.PutPerformanceReport(test.Context(), "a/b", first) },
		"bad key": func() error {
			bad := first
			bad.PeriodKey = "Q2026-9"
			return repo.PutPerformanceReport(test.Context(), "owner", bad)
		},
	} {
		if err := call(); !errors.Is(err, ErrInvalidPerformanceInput) {
			test.Fatalf("%s: %v", name, err)
		}
	}
	if len(puts) != 3 {
		test.Fatalf("rejected writes must not reach DynamoDB: %d", len(puts))
	}
}

func performanceReportItem(user, key string, ttl int64) string {
	return `{"PK":{"S":"USER#` + user + `"},"SK":{"S":"PERFREPORT#` + key + `"},"userId":{"S":"` + user + `"},"schemaVersion":{"N":"1"},` +
		`"periodKey":{"S":"` + key + `"},"period":{"M":{"label":{"S":"L ` + key + `"}}},"generatedAt":{"S":"2026-11-01T00:00:00Z"},` +
		`"partial":{"BOOL":true},"summary":{"S":"s"},"pendingShareExpiresAt":{"N":"` + mustJSONInt(ttl) + `"}}`
}

func TestGetReportHidesExpired(test *testing.T) {
	pinPerformanceNow(test)
	ttl := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC).Unix()
	item := func() string { return `{"Item":` + performanceReportItem("owner", "Y2023", ttl) + `}` }
	repo := summarySDKRepo(func(request *http.Request) (*http.Response, error) {
		body := performanceBody(test, request)
		if performanceTarget(request) != "GetItem" || body["Key"].(map[string]any)["SK"].(map[string]any)["S"] != "PERFREPORT#Y2023" {
			test.Fatalf("unexpected call %s %v", performanceTarget(request), body)
		}
		return summaryHTTPResponse(200, item(), nil), nil
	})
	if report, err := repo.GetPerformanceReport(test.Context(), "owner", "Y2023"); report != nil || err != nil {
		test.Fatalf("expired report must read as not found: %+v %v", report, err)
	}
	ttl = time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC).Unix()
	report, err := repo.GetPerformanceReport(test.Context(), "owner", "Y2023")
	if err != nil || report == nil || report.PeriodKey != "Y2023" || !report.Partial {
		test.Fatalf("live report must be returned: %+v %v", report, err)
	}
	if _, err := repo.GetPerformanceReport(test.Context(), "owner", "Y2023#x"); !errors.Is(err, ErrInvalidPerformanceInput) {
		test.Fatalf("bad key: %v", err)
	}
	foreign := summarySDKRepo(func(*http.Request) (*http.Response, error) {
		return summaryHTTPResponse(200, `{"Item":`+performanceReportItem("other", "Y2023", ttl)+`}`, nil), nil
	})
	if _, err := foreign.GetPerformanceReport(test.Context(), "owner", "Y2023"); !errors.Is(err, ErrPerformanceCorruptData) {
		test.Fatalf("a row with another identity must not be returned: %v", err)
	}
}

func TestListReportsPaginatesAndProjects(test *testing.T) {
	pinPerformanceNow(test)
	future := performanceNow().AddDate(1, 0, 0).Unix()
	pages := 0
	repo := summarySDKRepo(func(request *http.Request) (*http.Response, error) {
		body := performanceBody(test, request)
		if performanceTarget(request) != "Query" {
			test.Fatalf("unexpected call %s", performanceTarget(request))
		}
		projection, _ := body["ProjectionExpression"].(string)
		if projection == "" || strings.Contains(projection, "sections") {
			test.Fatalf("listing must project summary fields only: %q", projection)
		}
		names := mustJSON(test, body["ExpressionAttributeNames"])
		if strings.Contains(names, "summary") || strings.Contains(names, "sections") || !strings.Contains(names, "PERFREPORT#") && !strings.Contains(mustJSON(test, body["ExpressionAttributeValues"]), "PERFREPORT#") {
			test.Fatalf("listing names/values: %s", names)
		}
		pages++
		if _, again := body["ExclusiveStartKey"]; !again {
			return summaryHTTPResponse(200, `{"Items":[`+performanceReportItem("owner", "Q2026-3", future)+`,`+performanceReportItem("owner", "Y2023", 1)+
				`],"LastEvaluatedKey":{"PK":{"S":"USER#owner"},"SK":{"S":"PERFREPORT#Y2023"}}}`, nil), nil
		}
		return summaryHTTPResponse(200, `{"Items":[`+performanceReportItem("owner", "W2026-45", future)+`,`+performanceReportItem("other", "Y2026", future)+`]}`, nil), nil
	})
	items, truncated, err := repo.ListPerformanceReports(test.Context(), "owner")
	if err != nil || truncated || pages != 2 || len(items) != 2 || items[0].PeriodKey != "Q2026-3" || items[1].PeriodKey != "W2026-45" ||
		items[0].Label != "L Q2026-3" || !items[0].Partial || items[0].GeneratedAt != "2026-11-01T00:00:00Z" {
		test.Fatalf("reports must paginate and drop expired or foreign rows: %+v pages=%d err=%v", items, pages, err)
	}
}

func TestDeleteReportRequiresExistingRow(test *testing.T) {
	missing := false
	repo := summarySDKRepo(func(request *http.Request) (*http.Response, error) {
		body := performanceBody(test, request)
		if performanceTarget(request) != "DeleteItem" || body["Key"].(map[string]any)["SK"].(map[string]any)["S"] != "PERFREPORT#Y2026" ||
			body["ConditionExpression"] == nil {
			test.Fatalf("unexpected call %s %v", performanceTarget(request), body)
		}
		if missing {
			return summaryHTTPResponse(400, `{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","message":"x"}`, nil), nil
		}
		return summaryHTTPResponse(200, `{}`, nil), nil
	})
	if err := repo.DeletePerformanceReport(test.Context(), "owner", "Y2026"); err != nil {
		test.Fatal(err)
	}
	missing = true
	if err := repo.DeletePerformanceReport(test.Context(), "owner", "Y2026"); !errors.Is(err, ErrConditionFailed) {
		test.Fatalf("deleting a missing report: %v", err)
	}
	if err := repo.DeletePerformanceReport(test.Context(), "owner", "nope"); !errors.Is(err, ErrInvalidPerformanceInput) {
		test.Fatalf("bad key: %v", err)
	}
}

func TestListReportsTruncatedPastRowLimit(test *testing.T) {
	pinPerformanceNow(test)
	future := performanceNow().AddDate(1, 0, 0).Unix()
	var limit int32
	build := func(count int) string {
		items := make([]string, count)
		for i := range items {
			items[i] = performanceReportItem("owner", "Q2026-3", future)
		}
		return strings.Join(items, ",")
	}
	count := PerformanceReportListRowLimit + 1
	repo := summarySDKRepo(func(request *http.Request) (*http.Response, error) {
		limit = int32(performanceBody(test, request)["Limit"].(float64))
		return summaryHTTPResponse(200, `{"Items":[`+build(count)+`]}`, nil), nil
	})
	items, truncated, err := repo.ListPerformanceReports(test.Context(), "owner")
	if err != nil || !truncated || len(items) != PerformanceReportListRowLimit || limit != PerformanceReportListRowLimit+1 {
		test.Fatalf("extra row must set truncated: n=%d truncated=%v limit=%d err=%v", len(items), truncated, limit, err)
	}
	count = PerformanceReportListRowLimit
	items, truncated, err = repo.ListPerformanceReports(test.Context(), "owner")
	if err != nil || truncated || len(items) != PerformanceReportListRowLimit {
		test.Fatalf("exactly the limit is not truncated: n=%d truncated=%v err=%v", len(items), truncated, err)
	}
}
