package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func (fake *performanceStoreFake) PutPerformanceReport(_ context.Context, userID string, report model.PerformanceReport) error {
	if fake.reports == nil {
		fake.reports = map[string]map[string]model.PerformanceReport{}
	}
	if fake.reports[userID] == nil {
		fake.reports[userID] = map[string]model.PerformanceReport{}
	}
	fake.reports[userID][report.PeriodKey] = report
	return nil
}

func (fake *performanceStoreFake) GetPerformanceReport(_ context.Context, userID, periodKey string) (*model.PerformanceReport, error) {
	report, found := fake.reports[userID][periodKey]
	if !found {
		return nil, nil
	}
	return &report, nil
}

func (fake *performanceStoreFake) ListPerformanceReports(_ context.Context, userID string) ([]model.PerformanceReportSummary, bool, error) {
	if fake.summaries != nil {
		return fake.summaries, fake.readTruncated, nil
	}
	var out []model.PerformanceReportSummary
	for _, report := range fake.reports[userID] {
		out = append(out, model.PerformanceReportSummary{PeriodKey: report.PeriodKey, Label: report.Period.Label, GeneratedAt: report.GeneratedAt, Partial: report.Partial})
	}
	return out, false, nil
}

func (fake *performanceStoreFake) DeletePerformanceReport(_ context.Context, userID, periodKey string) error {
	if _, found := fake.reports[userID][periodKey]; !found {
		return repository.ErrConditionFailed
	}
	delete(fake.reports[userID], periodKey)
	return nil
}

func serviceTestReport() model.PerformanceReport {
	return model.PerformanceReport{
		SchemaVersion: 1, PeriodKey: "Q2026-3",
		Period: model.PerformanceReportPeriod{Start: "2026-07-01", End: "2026-09-30", Label: "3분기"},
		Alias:  "owner", GeneratedAt: "2026-10-02T01:02:03+09:00", Model: "m", Summary: "요약",
		Sections: []model.PerformanceReportSection{{ID: "s1", Title: "t", Paragraphs: []string{"p"}}},
	}
}

func TestPutReportValidatesAndReturnsReceipt(test *testing.T) {
	store := &performanceStoreFake{}
	svc := performanceTestService(store, nil)
	receipt, err := svc.PutReport(test.Context(), "owner", "Q2026-3", serviceTestReport())
	if err != nil || receipt.PeriodKey != "Q2026-3" || receipt.GeneratedAt != "2026-10-02T01:02:03+09:00" {
		test.Fatalf("receipt: %+v %v", receipt, err)
	}
	stored := store.reports["owner"]["Q2026-3"]
	if stored.Ledger == nil || stored.Actions == nil || stored.DataBasis == nil || stored.MissingSources == nil ||
		stored.Highlights.Projects == nil || stored.Sections[0].Tables == nil || stored.Sections[0].Cards == nil {
		test.Fatalf("absent lists must be stored as empty lists: %+v", stored)
	}
	if _, err := svc.PutReport(test.Context(), "a/b", "Q2026-3", serviceTestReport()); !errors.Is(err, ErrForbidden) {
		test.Fatalf("bad user: %v", err)
	}
	for name, mutate := range map[string]func(*model.PerformanceReport){
		"markup":   func(r *model.PerformanceReport) { r.Summary = "<b>x</b>" },
		"slack":    func(r *model.PerformanceReport) { r.Sections[0].Paragraphs[0] = "https://x.slack.com/archives/C1" },
		"mismatch": func(r *model.PerformanceReport) { r.PeriodKey = "Q2026-4" },
	} {
		report := serviceTestReport()
		mutate(&report)
		if _, err := svc.PutReport(test.Context(), "owner", "Q2026-3", report); !errors.Is(err, ErrInvalidInput) {
			test.Fatalf("%s: %v", name, err)
		}
	}
	// Per-field caps pass, but the encoded report exceeds 256 KiB.
	large := serviceTestReport()
	large.Sections = nil
	for i := range 40 {
		large.Sections = append(large.Sections, model.PerformanceReportSection{ID: fmt.Sprintf("s%d", i), Title: "t", Paragraphs: []string{strings.Repeat("가", 4000)}})
	}
	if _, err := svc.PutReport(test.Context(), "owner", "Q2026-3", large); !errors.Is(err, ErrInvalidInput) {
		test.Fatalf("an encoded report over 256 KiB must be rejected: %v", err)
	}
	// HTML-sensitive characters must not inflate the measured size.
	ampersands := serviceTestReport()
	ampersands.Sections = nil
	for i := range 20 {
		ampersands.Sections = append(ampersands.Sections, model.PerformanceReportSection{ID: fmt.Sprintf("s%d", i), Title: "t", Paragraphs: []string{strings.Repeat("&", 4000)}})
	}
	if _, err := svc.PutReport(test.Context(), "owner", "Q2026-3", ampersands); err != nil {
		test.Fatalf("80 KB of '&' is within the cap: %v", err)
	}
}

func TestListReportsNewestFirstAndBounded(test *testing.T) {
	store := &performanceStoreFake{summaries: []model.PerformanceReportSummary{
		{PeriodKey: "Y2025", GeneratedAt: "2026-01-02T00:00:00Z"},
		{PeriodKey: "Q2026-3", GeneratedAt: "2026-10-02T08:00:00+09:00"}, // 2026-10-01T23:00Z
		{PeriodKey: "W2026-40", GeneratedAt: "2026-10-02T00:00:00Z"},
	}}
	svc := performanceTestService(store, nil)
	list, err := svc.ListReports(test.Context(), "owner")
	if err != nil || len(list.Items) != 3 || list.Items[0].PeriodKey != "W2026-40" || list.Items[1].PeriodKey != "Q2026-3" || list.Items[2].PeriodKey != "Y2025" || list.Truncated {
		test.Fatalf("newest generatedAt first: %+v %v", list, err)
	}
	store.summaries = nil
	for i := range 205 {
		store.summaries = append(store.summaries, model.PerformanceReportSummary{PeriodKey: fmt.Sprintf("Y%04d", 2000+i%100), GeneratedAt: fmt.Sprintf("2026-01-01T00:%02d:%02dZ", i/60, i%60)})
	}
	list, err = svc.ListReports(test.Context(), "owner")
	if err != nil || len(list.Items) != MaxPerformanceReportList || !list.Truncated || list.Items[0].GeneratedAt != "2026-01-01T00:03:24Z" {
		test.Fatalf("bounded list: %d %v %v", len(list.Items), list.Truncated, err)
	}
	empty, err := svc.ListReports(test.Context(), "nobody")
	store.summaries = nil
	if err != nil || empty.Items == nil {
		test.Fatalf("empty list must serialize as []: %+v %v", empty, err)
	}
}

func TestGetAndDeleteReport(test *testing.T) {
	store := &performanceStoreFake{}
	svc := performanceTestService(store, nil)
	if _, err := svc.GetReport(test.Context(), "owner", "Q2026-3"); !errors.Is(err, ErrNotFound) {
		test.Fatalf("missing report: %v", err)
	}
	if _, err := svc.PutReport(test.Context(), "owner", "Q2026-3", serviceTestReport()); err != nil {
		test.Fatal(err)
	}
	if report, err := svc.GetReport(test.Context(), "owner", "Q2026-3"); err != nil || report.Summary != "요약" {
		test.Fatalf("get: %+v %v", report, err)
	}
	if _, err := svc.GetReport(test.Context(), "other", "Q2026-3"); !errors.Is(err, ErrNotFound) {
		test.Fatalf("another user's report: %v", err)
	}
	if _, err := svc.GetReport(test.Context(), "owner", "Q2026-9"); !errors.Is(err, ErrInvalidInput) {
		test.Fatalf("bad key: %v", err)
	}
	if err := svc.DeleteReport(test.Context(), "other", "Q2026-3"); !errors.Is(err, ErrNotFound) {
		test.Fatalf("delete by another user: %v", err)
	}
	if err := svc.DeleteReport(test.Context(), "owner", "Q2026-3"); err != nil {
		test.Fatal(err)
	}
	if err := svc.DeleteReport(test.Context(), "owner", "Q2026-3"); !errors.Is(err, ErrNotFound) {
		test.Fatalf("second delete: %v", err)
	}
}

func TestListReportsTruncatedWhenReadLimitHit(test *testing.T) {
	store := &performanceStoreFake{summaries: []model.PerformanceReportSummary{{PeriodKey: "Y2025", GeneratedAt: "2026-01-02T00:00:00Z"}}, readTruncated: true}
	list, err := performanceTestService(store, nil).ListReports(test.Context(), "owner")
	if err != nil || len(list.Items) != 1 || !list.Truncated {
		test.Fatalf("a read past the row limit must report truncated: %+v %v", list, err)
	}
}
