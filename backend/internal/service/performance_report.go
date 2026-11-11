package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// MaxPerformanceReportList caps the report listing, newest generatedAt first.
const MaxPerformanceReportList = 200

// performanceReportEncodedSize is the compact JSON size of the report without
// HTML escaping, so "&" or "<" count as one byte, matching the stored text.
func performanceReportEncodedSize(report *model.PerformanceReport) (int, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(report); err != nil {
		return 0, err
	}
	return buffer.Len() - 1, nil // Encode appends a newline.
}

// PutReport validates the owner's model-composed report against its period key
// and replaces the stored report. Validation failures store nothing, so the
// previous report stays visible.
func (svc *PerformanceService) PutReport(ctx context.Context, userID, periodKey string, report model.PerformanceReport) (*model.PerformanceReportReceipt, error) {
	if !performanceUserPattern.MatchString(userID) {
		return nil, ErrForbidden
	}
	if err := model.ValidatePerformanceReport(periodKey, &report); err != nil {
		return nil, errors.Join(ErrInvalidInput, err)
	}
	model.NormalizePerformanceReport(&report)
	size, err := performanceReportEncodedSize(&report)
	if err != nil || size > model.PerformanceReportMaxBytes {
		return nil, ErrInvalidInput
	}
	if err := svc.store.PutPerformanceReport(ctx, userID, report); err != nil {
		return nil, err
	}
	return &model.PerformanceReportReceipt{PeriodKey: report.PeriodKey, GeneratedAt: report.GeneratedAt}, nil
}

func (svc *PerformanceService) GetReport(ctx context.Context, userID, periodKey string) (*model.PerformanceReport, error) {
	if !performanceUserPattern.MatchString(userID) {
		return nil, ErrForbidden
	}
	if _, _, err := model.PerformancePeriodBounds(periodKey); err != nil {
		return nil, errors.Join(ErrInvalidInput, err)
	}
	report, err := svc.store.GetPerformanceReport(ctx, userID, periodKey)
	if err != nil {
		return nil, err
	}
	if report == nil {
		return nil, ErrNotFound
	}
	model.NormalizePerformanceReport(report)
	return report, nil
}

// ListReports returns at most MaxPerformanceReportList summaries, newest
// generatedAt first (period key breaks ties), and truncated=true when more
// exist, including past the repository read limit.
func (svc *PerformanceService) ListReports(ctx context.Context, userID string) (*model.PerformanceReportList, error) {
	if !performanceUserPattern.MatchString(userID) {
		return nil, ErrForbidden
	}
	summaries, readTruncated, err := svc.store.ListPerformanceReports(ctx, userID)
	if err != nil {
		return nil, err
	}
	generated := func(value string) time.Time {
		parsed, _ := time.Parse(time.RFC3339, value) // an unparsable value sorts last
		return parsed
	}
	sorted := append([]model.PerformanceReportSummary{}, summaries...)
	sort.SliceStable(sorted, func(i, j int) bool {
		left, right := generated(sorted[i].GeneratedAt), generated(sorted[j].GeneratedAt)
		if !left.Equal(right) {
			return left.After(right)
		}
		return sorted[i].PeriodKey < sorted[j].PeriodKey
	})
	list := &model.PerformanceReportList{Items: sorted, Truncated: readTruncated || len(sorted) > MaxPerformanceReportList}
	if len(sorted) > MaxPerformanceReportList {
		list.Items = sorted[:MaxPerformanceReportList]
	}
	return list, nil
}

func (svc *PerformanceService) DeleteReport(ctx context.Context, userID, periodKey string) error {
	if !performanceUserPattern.MatchString(userID) {
		return ErrForbidden
	}
	if _, _, err := model.PerformancePeriodBounds(periodKey); err != nil {
		return errors.Join(ErrInvalidInput, err)
	}
	if err := svc.store.DeletePerformanceReport(ctx, userID, periodKey); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return ErrNotFound
		}
		return err
	}
	return nil
}
