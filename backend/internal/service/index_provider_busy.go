package service

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagent"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrockagent/types"
)

// Every coordinator sync adds a COMPLETE job to the data source's history, so
// walking that whole history on each idle tick grows without bound. Busy
// instead asks the provider for active jobs directly, checks the newest jobs
// for unknown statuses on every call, and repeats the full-history walk at a
// bounded interval as an audit of the filtered answer.
const (
	indexBusyRecentJobs   int32 = 100
	indexBusyPageSize     int32 = 1000
	indexJobAuditInterval       = time.Hour
)

// The status filter matches exactly one value per request; a multi-value EQ
// filter does not mean "any of". One request per active status is required.
var indexActiveIngestionStatuses = []string{"STARTING", "IN_PROGRESS", "STOPPING"}

// indexJobAudit records when this provider instance last completed a full,
// unfiltered walk without finding an active or unknown job.
type indexJobAudit struct {
	mu          sync.Mutex
	completedAt time.Time
}

// indexIngestionActive fails closed: a status outside the documented set is
// neither active nor terminal and stops the coordinator.
func indexIngestionActive(status bedrocktypes.IngestionJobStatus) (bool, error) {
	switch string(status) {
	case "STARTING", "IN_PROGRESS", "STOPPING":
		return true, nil
	case "COMPLETE", "FAILED", "STOPPED":
		return false, nil
	default:
		return false, ErrIndexInvalid
	}
}

func (p *IndexAWSProvider) listIngestionJobs() *bedrockagent.ListIngestionJobsInput {
	return &bedrockagent.ListIngestionJobsInput{KnowledgeBaseId: aws.String(p.kbID), DataSourceId: aws.String(p.dataSourceID)}
}

func (p *IndexAWSProvider) Busy(ctx context.Context) (bool, error) {
	recent := p.listIngestionJobs()
	recent.MaxResults = aws.Int32(indexBusyRecentJobs)
	recent.SortBy = &bedrocktypes.IngestionJobSortBy{
		Attribute: bedrocktypes.IngestionJobSortByAttributeStartedAt, Order: bedrocktypes.SortOrderDescending}
	page, err := p.bedrock.ListIngestionJobs(ctx, recent)
	if err != nil {
		return false, err
	}
	for _, job := range page.IngestionJobSummaries {
		if active, err := indexIngestionActive(job.Status); err != nil || active {
			return active, err
		}
	}
	for _, status := range indexActiveIngestionStatuses {
		input := p.listIngestionJobs()
		input.MaxResults = aws.Int32(indexBusyPageSize)
		input.Filters = []bedrocktypes.IngestionJobFilter{{Attribute: bedrocktypes.IngestionJobFilterAttributeStatus,
			Operator: bedrocktypes.IngestionJobFilterOperatorEq, Values: []string{status}}}
		pages := bedrockagent.NewListIngestionJobsPaginator(p.bedrock, input)
		for pages.HasMorePages() {
			page, err := pages.NextPage(ctx)
			if err != nil {
				return false, err
			}
			for _, job := range page.IngestionJobSummaries {
				// A reply outside the requested status means the filter was not
				// applied as expected; its answer cannot prove the source idle.
				if string(job.Status) != status {
					return false, ErrIndexInvalid
				}
				return true, nil
			}
		}
	}
	return p.auditIngestionJobs(ctx)
}

// auditIngestionJobs is the original full-history walk with the largest page
// size. It runs on this instance's first idle check and then at most once per
// indexJobAuditInterval; only a walk that saw every job as terminal counts.
func (p *IndexAWSProvider) auditIngestionJobs(ctx context.Context) (bool, error) {
	p.audit.mu.Lock()
	defer p.audit.mu.Unlock()
	started := p.now()
	if elapsed := started.Sub(p.audit.completedAt); !p.audit.completedAt.IsZero() && elapsed >= 0 && elapsed < indexJobAuditInterval {
		return false, nil
	}
	input := p.listIngestionJobs()
	input.MaxResults = aws.Int32(indexBusyPageSize)
	pages := bedrockagent.NewListIngestionJobsPaginator(p.bedrock, input)
	jobs, requests := 0, 0
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return false, err
		}
		requests++
		for _, job := range page.IngestionJobSummaries {
			jobs++
			if active, err := indexIngestionActive(job.Status); err != nil || active {
				return active, err
			}
		}
	}
	p.audit.completedAt = started
	log.Printf("Index provider job audit complete jobs=%d requests=%d", jobs, requests)
	return false, nil
}
