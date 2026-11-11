package model

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"
)

// SA performance reports are owner-private rows (PK USER#{id}, SK
// PERFREPORT#{periodKey}) written by the owner's Mac from model-composed JSON.
// Every string is plain text; validation here is the server-side safety net
// for content the server cannot otherwise verify (ADR-023).

// ErrInvalidPerformanceReport marks a report or period key that fails
// validation. Services map it to ErrInvalidInput.
var ErrInvalidPerformanceReport = errors.New("invalid performance report")

const (
	PerformanceReportSchemaVersion = 1
	// PerformanceReportMaxBytes bounds the PUT body and the re-encoded report.
	PerformanceReportMaxBytes = 256 * 1024

	performanceReportShortText     = 200
	performanceReportLongText      = 4000
	performanceReportMaxSections   = 40
	performanceReportMaxActions    = 200
	performanceReportMaxHighlights = 20
	performanceReportMaxMissing    = 20
	performanceReportMaxLedger     = 50
	performanceReportMaxParagraphs = 100
	performanceReportMaxTables     = 20
	performanceReportMaxColumns    = 50
	performanceReportMaxRows       = 500
	performanceReportMaxCards      = 50
	performanceReportMaxDataBasis  = 50
	performanceReportMaxCustomDays = 400
	performanceReportMinYear       = 2000
	performanceReportMaxYear       = 2100
	performanceReportDateLayout    = "2006-01-02"
)

var (
	performanceWeekKey    = regexp.MustCompile(`^W(\d{4})-(0[1-9]|[1-4]\d|5[0-3])$`)
	performanceQuarterKey = regexp.MustCompile(`^Q(\d{4})-([1-4])$`)
	performanceYearKey    = regexp.MustCompile(`^Y(\d{4})$`)
	performanceCustomKey  = regexp.MustCompile(`^C(\d{4}-\d{2}-\d{2})_(\d{4}-\d{2}-\d{2})$`)
	performanceSectionID  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	// A tag opener, a Markdown link and any Slack link are rejected in every
	// string field. Other text, including Korean and a bare "<", is allowed.
	performanceReportForbidden = regexp.MustCompile(`<[A-Za-z/]|\]\(|(?i)slack\.com`)
	performanceReportSeverity  = map[string]bool{"hi": true, "mid": true, "lo": true}
)

type PerformanceReportPeriod struct {
	Start string `json:"start" dynamodbav:"start"`
	End   string `json:"end" dynamodbav:"end"`
	Label string `json:"label" dynamodbav:"label"`
}

type PerformanceReportHighlights struct {
	Projects    []string `json:"projects" dynamodbav:"projects"`
	Initiatives []string `json:"initiatives" dynamodbav:"initiatives"`
	Channels    []string `json:"channels" dynamodbav:"channels"`
	Programs    []string `json:"programs" dynamodbav:"programs"`
}

type PerformanceReportLedgerItem struct {
	Label string `json:"label" dynamodbav:"label"`
	Value string `json:"value" dynamodbav:"value"`
	Unit  string `json:"unit" dynamodbav:"unit"`
	Note  string `json:"note" dynamodbav:"note"`
}

// PerformanceReportTable cells are strings; the Mac adapter stringifies numbers.
type PerformanceReportTable struct {
	Columns []string   `json:"columns" dynamodbav:"columns"`
	Rows    [][]string `json:"rows" dynamodbav:"rows"`
}

type PerformanceReportCard struct {
	Title string `json:"title" dynamodbav:"title"`
	Body  string `json:"body" dynamodbav:"body"`
}

type PerformanceReportSection struct {
	ID         string                   `json:"id" dynamodbav:"id"`
	Title      string                   `json:"title" dynamodbav:"title"`
	Lede       string                   `json:"lede" dynamodbav:"lede"`
	Paragraphs []string                 `json:"paragraphs" dynamodbav:"paragraphs"`
	Tables     []PerformanceReportTable `json:"tables" dynamodbav:"tables"`
	Cards      []PerformanceReportCard  `json:"cards" dynamodbav:"cards"`
}

type PerformanceReportAction struct {
	Severity string `json:"severity" dynamodbav:"severity"`
	Title    string `json:"title" dynamodbav:"title"`
	Detail   string `json:"detail" dynamodbav:"detail"`
	Source   string `json:"source" dynamodbav:"source"`
}

type PerformanceReportDataBasis struct {
	Source     string `json:"source" dynamodbav:"source"`
	Query      string `json:"query" dynamodbav:"query"`
	Exclusions string `json:"exclusions" dynamodbav:"exclusions"`
	Estimates  string `json:"estimates" dynamodbav:"estimates"`
}

type PerformanceReport struct {
	SchemaVersion  int                           `json:"schemaVersion" dynamodbav:"schemaVersion"`
	PeriodKey      string                        `json:"periodKey" dynamodbav:"periodKey"`
	Period         PerformanceReportPeriod       `json:"period" dynamodbav:"period"`
	Alias          string                        `json:"alias" dynamodbav:"alias"`
	GeneratedAt    string                        `json:"generatedAt" dynamodbav:"generatedAt"`
	Model          string                        `json:"model" dynamodbav:"model"`
	Partial        bool                          `json:"partial" dynamodbav:"partial"`
	MissingSources []string                      `json:"missingSources" dynamodbav:"missingSources"`
	Highlights     PerformanceReportHighlights   `json:"highlights" dynamodbav:"highlights"`
	Summary        string                        `json:"summary" dynamodbav:"summary"`
	Ledger         []PerformanceReportLedgerItem `json:"ledger" dynamodbav:"ledger"`
	Sections       []PerformanceReportSection    `json:"sections" dynamodbav:"sections"`
	Actions        []PerformanceReportAction     `json:"actions" dynamodbav:"actions"`
	DataBasis      []PerformanceReportDataBasis  `json:"dataBasis" dynamodbav:"dataBasis"`
}

// PerformanceReportSummary is one list item; the list never carries content.
type PerformanceReportSummary struct {
	PeriodKey   string `json:"periodKey"`
	Label       string `json:"label"`
	GeneratedAt string `json:"generatedAt"`
	Partial     bool   `json:"partial"`
}

type PerformanceReportList struct {
	Items []PerformanceReportSummary `json:"items"`
	// Truncated is true when more reports exist than Items holds.
	Truncated bool `json:"truncated"`
}

type PerformanceReportReceipt struct {
	PeriodKey   string `json:"periodKey"`
	GeneratedAt string `json:"generatedAt"`
}

func invalidPerformanceReport(field string) error {
	return fmt.Errorf("%w: %s", ErrInvalidPerformanceReport, field)
}

func performanceReportDate(value string) (time.Time, bool) {
	parsed, err := time.Parse(performanceReportDateLayout, value)
	return parsed, err == nil && parsed.Format(performanceReportDateLayout) == value &&
		parsed.Year() >= performanceReportMinYear && parsed.Year() <= performanceReportMaxYear
}

func performanceReportYear(value string) (int, bool) {
	year, err := strconv.Atoi(value)
	return year, err == nil && year >= performanceReportMinYear && year <= performanceReportMaxYear
}

// PerformancePeriodBounds returns the inclusive first and last day (UTC
// midnight) of a period key: W{ISO year}-{ISO week} (Monday to Sunday),
// Q{year}-{1..4}, Y{year}, or C{start}_{end} spanning at most 400 days
// inclusive. Years run from 2000 to 2100.
func PerformancePeriodBounds(key string) (time.Time, time.Time, error) {
	if match := performanceWeekKey.FindStringSubmatch(key); match != nil {
		year, ok := performanceReportYear(match[1])
		week, _ := strconv.Atoi(match[2])
		if !ok {
			return time.Time{}, time.Time{}, invalidPerformanceReport("period key")
		}
		january4 := time.Date(year, time.January, 4, 0, 0, 0, 0, time.UTC)
		monday := january4.AddDate(0, 0, -((int(january4.Weekday())+6)%7)+7*(week-1))
		if isoYear, _ := monday.ISOWeek(); isoYear != year { // week 53 of a 52-week year
			return time.Time{}, time.Time{}, invalidPerformanceReport("period key")
		}
		return monday, monday.AddDate(0, 0, 6), nil
	}
	if match := performanceQuarterKey.FindStringSubmatch(key); match != nil {
		year, ok := performanceReportYear(match[1])
		if !ok {
			return time.Time{}, time.Time{}, invalidPerformanceReport("period key")
		}
		quarter, _ := strconv.Atoi(match[2])
		start := time.Date(year, time.Month(3*(quarter-1)+1), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 3, -1), nil
	}
	if match := performanceYearKey.FindStringSubmatch(key); match != nil {
		year, ok := performanceReportYear(match[1])
		if !ok {
			return time.Time{}, time.Time{}, invalidPerformanceReport("period key")
		}
		return time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC), time.Date(year, time.December, 31, 0, 0, 0, 0, time.UTC), nil
	}
	if match := performanceCustomKey.FindStringSubmatch(key); match != nil {
		start, startOK := performanceReportDate(match[1])
		end, endOK := performanceReportDate(match[2])
		if !startOK || !endOK || end.Before(start) || end.Sub(start) >= performanceReportMaxCustomDays*24*time.Hour {
			return time.Time{}, time.Time{}, invalidPerformanceReport("period key")
		}
		return start, end, nil
	}
	return time.Time{}, time.Time{}, invalidPerformanceReport("period key")
}

// validPerformanceReportText accepts valid UTF-8 plain text of at most
// maxRunes code points with no control characters other than tab and newlines,
// no tag opener, no Markdown link and no Slack link.
func validPerformanceReportText(value string, maxRunes int) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxRunes || performanceReportForbidden.MatchString(value) {
		return false
	}
	for _, r := range value {
		if (r < 0x20 && r != '\t' && r != '\n' && r != '\r') || r == 0x7f {
			return false
		}
	}
	return true
}

func validPerformanceReportList(values []string, maxItems, maxRunes int) bool {
	if len(values) > maxItems {
		return false
	}
	for _, value := range values {
		if !validPerformanceReportText(value, maxRunes) {
			return false
		}
	}
	return true
}

// ValidatePerformanceReport checks a report against its URL period key: the
// schema version, the period bounds the key implies, counts and per-field
// limits, and the plain-text rules on every string.
func ValidatePerformanceReport(key string, r *PerformanceReport) error {
	if r == nil {
		return invalidPerformanceReport("report")
	}
	start, end, err := PerformancePeriodBounds(key)
	if err != nil {
		return err
	}
	if r.SchemaVersion != PerformanceReportSchemaVersion {
		return invalidPerformanceReport("schemaVersion")
	}
	if r.PeriodKey != key || r.Period.Start != start.Format(performanceReportDateLayout) || r.Period.End != end.Format(performanceReportDateLayout) {
		return invalidPerformanceReport("period")
	}
	if r.Period.Label == "" || !validPerformanceReportText(r.Period.Label, performanceReportShortText) {
		return invalidPerformanceReport("period.label")
	}
	if _, err := time.Parse(time.RFC3339, r.GeneratedAt); err != nil {
		return invalidPerformanceReport("generatedAt")
	}
	if !validPerformanceReportText(r.Alias, performanceReportShortText) || !validPerformanceReportText(r.Model, performanceReportShortText) {
		return invalidPerformanceReport("alias or model")
	}
	if !validPerformanceReportList(r.MissingSources, performanceReportMaxMissing, performanceReportShortText) {
		return invalidPerformanceReport("missingSources")
	}
	for _, list := range [][]string{r.Highlights.Projects, r.Highlights.Initiatives, r.Highlights.Channels, r.Highlights.Programs} {
		if !validPerformanceReportList(list, performanceReportMaxHighlights, performanceReportShortText) {
			return invalidPerformanceReport("highlights")
		}
	}
	if !validPerformanceReportText(r.Summary, performanceReportLongText) {
		return invalidPerformanceReport("summary")
	}
	if len(r.Ledger) > performanceReportMaxLedger {
		return invalidPerformanceReport("ledger")
	}
	for _, item := range r.Ledger {
		if item.Label == "" || !validPerformanceReportList([]string{item.Label, item.Value, item.Unit}, 3, performanceReportShortText) ||
			!validPerformanceReportText(item.Note, performanceReportLongText) {
			return invalidPerformanceReport("ledger")
		}
	}
	if err := validatePerformanceReportSections(r.Sections); err != nil {
		return err
	}
	if len(r.Actions) > performanceReportMaxActions {
		return invalidPerformanceReport("actions")
	}
	for _, action := range r.Actions {
		if !performanceReportSeverity[action.Severity] || action.Title == "" ||
			!validPerformanceReportList([]string{action.Title, action.Source}, 2, performanceReportShortText) ||
			!validPerformanceReportText(action.Detail, performanceReportLongText) {
			return invalidPerformanceReport("actions")
		}
	}
	if len(r.DataBasis) > performanceReportMaxDataBasis {
		return invalidPerformanceReport("dataBasis")
	}
	for _, basis := range r.DataBasis {
		if basis.Source == "" || !validPerformanceReportText(basis.Source, performanceReportShortText) ||
			!validPerformanceReportList([]string{basis.Query, basis.Exclusions, basis.Estimates}, 3, performanceReportLongText) {
			return invalidPerformanceReport("dataBasis")
		}
	}
	return nil
}

func validatePerformanceReportSections(sections []PerformanceReportSection) error {
	if len(sections) > performanceReportMaxSections {
		return invalidPerformanceReport("sections")
	}
	seen := make(map[string]bool, len(sections))
	for _, section := range sections {
		if !performanceSectionID.MatchString(section.ID) || seen[section.ID] {
			return invalidPerformanceReport("section id")
		}
		seen[section.ID] = true
		if section.Title == "" || !validPerformanceReportText(section.Title, performanceReportShortText) ||
			!validPerformanceReportText(section.Lede, performanceReportLongText) ||
			!validPerformanceReportList(section.Paragraphs, performanceReportMaxParagraphs, performanceReportLongText) {
			return invalidPerformanceReport("section text")
		}
		if len(section.Tables) > performanceReportMaxTables || len(section.Cards) > performanceReportMaxCards {
			return invalidPerformanceReport("section size")
		}
		for _, table := range section.Tables {
			if len(table.Columns) == 0 || !validPerformanceReportList(table.Columns, performanceReportMaxColumns, performanceReportShortText) ||
				len(table.Rows) > performanceReportMaxRows {
				return invalidPerformanceReport("table")
			}
			for _, row := range table.Rows {
				if len(row) != len(table.Columns) || !validPerformanceReportList(row, performanceReportMaxColumns, performanceReportLongText) {
					return invalidPerformanceReport("table row")
				}
			}
		}
		for _, card := range section.Cards {
			if !validPerformanceReportText(card.Title, performanceReportShortText) || !validPerformanceReportText(card.Body, performanceReportLongText) {
				return invalidPerformanceReport("card")
			}
		}
	}
	return nil
}

// NormalizePerformanceReport replaces absent lists with empty ones so that a
// stored or returned report never carries null lists. Table rows are left as
// validated.
func NormalizePerformanceReport(r *PerformanceReport) {
	emptyStrings := func(values *[]string) {
		if *values == nil {
			*values = []string{}
		}
	}
	emptyStrings(&r.MissingSources)
	emptyStrings(&r.Highlights.Projects)
	emptyStrings(&r.Highlights.Initiatives)
	emptyStrings(&r.Highlights.Channels)
	emptyStrings(&r.Highlights.Programs)
	if r.Ledger == nil {
		r.Ledger = []PerformanceReportLedgerItem{}
	}
	if r.Sections == nil {
		r.Sections = []PerformanceReportSection{}
	}
	for i := range r.Sections {
		section := &r.Sections[i]
		emptyStrings(&section.Paragraphs)
		if section.Tables == nil {
			section.Tables = []PerformanceReportTable{}
		}
		if section.Cards == nil {
			section.Cards = []PerformanceReportCard{}
		}
		for j := range section.Tables {
			emptyStrings(&section.Tables[j].Columns)
			if section.Tables[j].Rows == nil {
				section.Tables[j].Rows = [][]string{}
			}
			for k := range section.Tables[j].Rows {
				emptyStrings(&section.Tables[j].Rows[k])
			}
		}
	}
	if r.Actions == nil {
		r.Actions = []PerformanceReportAction{}
	}
	if r.DataBasis == nil {
		r.DataBasis = []PerformanceReportDataBasis{}
	}
}
