package model

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// validPerformanceReport returns a small report for periodKey Q2026-3.
func validPerformanceReport() *PerformanceReport {
	return &PerformanceReport{
		SchemaVersion:  1,
		PeriodKey:      "Q2026-3",
		Period:         PerformanceReportPeriod{Start: "2026-07-01", End: "2026-09-30", Label: "2026년 3분기"},
		Alias:          "owner",
		GeneratedAt:    "2026-10-02T01:02:03Z",
		Model:          "claude-opus",
		Partial:        true,
		MissingSources: []string{"slack"},
		Highlights:     PerformanceReportHighlights{Projects: []string{"이관"}, Initiatives: []string{}, Channels: []string{}, Programs: []string{}},
		Summary:        "고객 대응 요약입니다. 1 < 2 and 3 > 2.",
		Ledger:         []PerformanceReportLedgerItem{{Label: "기회", Value: "1200000", Unit: "USD", Note: "정확한 금액"}},
		Sections: []PerformanceReportSection{{
			ID: "customers", Title: "고객 대응", Lede: "요약",
			Paragraphs: []string{"질문 주제 요약"},
			Tables:     []PerformanceReportTable{{Columns: []string{"고객", "건수"}, Rows: [][]string{{"A사", "3"}}}},
			Cards:      []PerformanceReportCard{{Title: "카드", Body: "본문"}},
		}},
		Actions:   []PerformanceReportAction{{Severity: "hi", Title: "SFDC 로깅", Detail: "누락 2건", Source: "sfdc"}},
		DataBasis: []PerformanceReportDataBasis{{Source: "sfdc", Query: "tasks in period", Exclusions: "none"}},
	}
}

func TestPerformanceReportKeyPatterns(test *testing.T) {
	day := func(value string) time.Time {
		parsed, err := time.Parse("2006-01-02", value)
		if err != nil {
			test.Fatal(err)
		}
		return parsed
	}
	valid := map[string][2]string{
		"W2026-01":               {"2025-12-29", "2026-01-04"},
		"W2026-53":               {"2026-12-28", "2027-01-03"},
		"W2020-53":               {"2020-12-28", "2021-01-03"},
		"Q2026-1":                {"2026-01-01", "2026-03-31"},
		"Q2026-4":                {"2026-10-01", "2026-12-31"},
		"Y2026":                  {"2026-01-01", "2026-12-31"},
		"C2026-01-01_2026-01-01": {"2026-01-01", "2026-01-01"},
		"C2026-01-01_2027-02-04": {"2026-01-01", "2027-02-04"}, // 400 days, inclusive
	}
	for key, bounds := range valid {
		start, end, err := PerformancePeriodBounds(key)
		if err != nil || !start.Equal(day(bounds[0])) || !end.Equal(day(bounds[1])) {
			test.Fatalf("%s: %v %v %v", key, start, end, err)
		}
		if PerformanceReportSortKey(key) != PrefixPerformanceReport+key || PrefixPerformanceReport != "PERFREPORT#" {
			test.Fatalf("sort key for %s", key)
		}
	}
	for _, key := range []string{
		"", "W2026-00", "W2026-54", "W2025-53", "W26-01", "w2026-01", "Q2026-0", "Q2026-5", "Y26", "Y2026 ",
		"C2026-02-01_2026-01-31", // reversed
		"C2026-01-01_2027-02-05", // 401 days, inclusive
		"C2026-02-30_2026-03-01", // not a date
		"C2026-1-01_2026-01-02",  // bad shape
		"Q1999-1", "Y2101",       // outside the supported years
		"Y2026#x", "../Y2026", "Y2026\n",
	} {
		if _, _, err := PerformancePeriodBounds(key); !errors.Is(err, ErrInvalidPerformanceReport) {
			test.Fatalf("%q must be rejected: %v", key, err)
		}
	}
}

func TestValidateReportAcceptsPlainKoreanText(test *testing.T) {
	if err := ValidatePerformanceReport("Q2026-3", validPerformanceReport()); err != nil {
		test.Fatal(err)
	}
	custom := validPerformanceReport()
	custom.PeriodKey, custom.Period.Start, custom.Period.End = "C2026-02-03_2026-02-10", "2026-02-03", "2026-02-10"
	if err := ValidatePerformanceReport(custom.PeriodKey, custom); err != nil {
		test.Fatal(err)
	}
}

func TestValidateReportRejectsMarkupAndSlackLinks(test *testing.T) {
	bad := []string{"<script>alert(1)</script>", "a</b", "<img src=x>", "[x](y)", "see slack.com", "SLACK.COM/archives", "https://acme.Slack.Com/archives/C1/p1"}
	setters := map[string]func(*PerformanceReport, string){
		"summary":        func(r *PerformanceReport, v string) { r.Summary = v },
		"alias":          func(r *PerformanceReport, v string) { r.Alias = v },
		"model":          func(r *PerformanceReport, v string) { r.Model = v },
		"label":          func(r *PerformanceReport, v string) { r.Period.Label = v },
		"missingSources": func(r *PerformanceReport, v string) { r.MissingSources[0] = v },
		"highlight":      func(r *PerformanceReport, v string) { r.Highlights.Projects[0] = v },
		"ledger":         func(r *PerformanceReport, v string) { r.Ledger[0].Note = v },
		"title":          func(r *PerformanceReport, v string) { r.Sections[0].Title = v },
		"lede":           func(r *PerformanceReport, v string) { r.Sections[0].Lede = v },
		"paragraph":      func(r *PerformanceReport, v string) { r.Sections[0].Paragraphs[0] = v },
		"column":         func(r *PerformanceReport, v string) { r.Sections[0].Tables[0].Columns[0] = v },
		"cell":           func(r *PerformanceReport, v string) { r.Sections[0].Tables[0].Rows[0][1] = v },
		"card":           func(r *PerformanceReport, v string) { r.Sections[0].Cards[0].Body = v },
		"action":         func(r *PerformanceReport, v string) { r.Actions[0].Detail = v },
		"actionSource":   func(r *PerformanceReport, v string) { r.Actions[0].Source = v },
		"dataBasis":      func(r *PerformanceReport, v string) { r.DataBasis[0].Estimates = v },
	}
	for field, set := range setters {
		for _, value := range bad {
			report := validPerformanceReport()
			set(report, value)
			if err := ValidatePerformanceReport("Q2026-3", report); !errors.Is(err, ErrInvalidPerformanceReport) {
				test.Fatalf("%s=%q must be rejected: %v", field, value, err)
			}
		}
	}
	for _, value := range []string{"1 < 2", "a <= b", "<3", "[참고] (요약)", "slack 채널", "줄\n바꿈"} {
		report := validPerformanceReport()
		report.Summary = value
		if err := ValidatePerformanceReport("Q2026-3", report); err != nil {
			test.Fatalf("plain text %q must pass: %v", value, err)
		}
	}
	for _, value := range []string{"nul\x00", "bell\x07", string([]byte{0xff})} {
		report := validPerformanceReport()
		report.Summary = value
		if err := ValidatePerformanceReport("Q2026-3", report); !errors.Is(err, ErrInvalidPerformanceReport) {
			test.Fatalf("control or invalid UTF-8 %q must be rejected: %v", value, err)
		}
	}
}

func TestValidateReportCaps(test *testing.T) {
	cases := map[string]func(*PerformanceReport){
		"schema version":    func(r *PerformanceReport) { r.SchemaVersion = 2 },
		"key mismatch":      func(r *PerformanceReport) { r.PeriodKey = "Q2026-2" },
		"period mismatch":   func(r *PerformanceReport) { r.Period.End = "2026-09-29" },
		"empty label":       func(r *PerformanceReport) { r.Period.Label = "" },
		"bad generatedAt":   func(r *PerformanceReport) { r.GeneratedAt = "2026-10-02" },
		"summary 4001":      func(r *PerformanceReport) { r.Summary = strings.Repeat("가", 4001) },
		"paragraph 4001":    func(r *PerformanceReport) { r.Sections[0].Paragraphs[0] = strings.Repeat("a", 4001) },
		"title 201":         func(r *PerformanceReport) { r.Sections[0].Title = strings.Repeat("제", 201) },
		"action title 201":  func(r *PerformanceReport) { r.Actions[0].Title = strings.Repeat("a", 201) },
		"card title 201":    func(r *PerformanceReport) { r.Sections[0].Cards[0].Title = strings.Repeat("a", 201) },
		"51 columns":        func(r *PerformanceReport) { r.Sections[0].Tables[0] = table(51, 1) },
		"501 rows":          func(r *PerformanceReport) { r.Sections[0].Tables[0] = table(2, 501) },
		"ragged row":        func(r *PerformanceReport) { r.Sections[0].Tables[0].Rows[0] = []string{"only one"} },
		"21 highlights":     func(r *PerformanceReport) { r.Highlights.Channels = make([]string, 21) },
		"41 sections":       func(r *PerformanceReport) { r.Sections = sections(41) },
		"201 actions":       func(r *PerformanceReport) { r.Actions = actions(201) },
		"bad severity":      func(r *PerformanceReport) { r.Actions[0].Severity = "critical" },
		"bad section id":    func(r *PerformanceReport) { r.Sections[0].ID = "a b" },
		"duplicate section": func(r *PerformanceReport) { r.Sections = append(r.Sections, r.Sections[0]) },
		"nil report":        nil,
	}
	for name, mutate := range cases {
		var report *PerformanceReport
		if mutate != nil {
			report = validPerformanceReport()
			mutate(report)
		}
		if err := ValidatePerformanceReport("Q2026-3", report); !errors.Is(err, ErrInvalidPerformanceReport) {
			test.Fatalf("%s must be rejected: %v", name, err)
		}
	}
	atCaps := validPerformanceReport()
	atCaps.Summary = strings.Repeat("가", 4000)
	atCaps.Sections = sections(40)
	atCaps.Sections[0].Tables = []PerformanceReportTable{table(50, 2), table(1, 500)}
	atCaps.Actions = actions(200)
	atCaps.Highlights.Programs = make([]string, 20)
	if err := ValidatePerformanceReport("Q2026-3", atCaps); err != nil {
		test.Fatalf("a report at every cap must pass: %v", err)
	}
}

func table(columns, rows int) PerformanceReportTable {
	result := PerformanceReportTable{Columns: make([]string, columns), Rows: make([][]string, rows)}
	for i := range result.Rows {
		result.Rows[i] = make([]string, columns)
	}
	return result
}

func sections(n int) []PerformanceReportSection {
	result := make([]PerformanceReportSection, n)
	for i := range result {
		result[i] = PerformanceReportSection{ID: "s" + strings.Repeat("x", i), Title: "t", Paragraphs: []string{}}
	}
	return result
}

func actions(n int) []PerformanceReportAction {
	result := make([]PerformanceReportAction, n)
	for i := range result {
		result[i] = PerformanceReportAction{Severity: "lo", Title: "t", Detail: "d", Source: "s"}
	}
	return result
}
