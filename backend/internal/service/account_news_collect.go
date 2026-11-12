package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// newsCollectInterval is the per-source minimum gap between accepted
// "collect now" requests.
const newsCollectInterval = time.Hour

// newsCollectReleaseTimeout bounds the claim release after a failed publish.
const newsCollectReleaseTimeout = 5 * time.Second

// The crawler's NewsCollectRequested contract (news_crawler._manual_request):
// accountId and requestId match newsCollectIDPattern and sourceId is
// "acct_" + accountId + "_" + 8 lowercase hex. Other sources cannot be
// collected on request.
var newsCollectIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
var newsCollectSuffixPattern = regexp.MustCompile(`^[0-9a-f]{8}$`)

// ErrNewsCollectUnavailable means the request was not handed to the crawler
// (no publisher, or the event was not accepted); the caller may retry.
var ErrNewsCollectUnavailable = errors.New("news collection is unavailable")

// ErrNewsCollectTooSoon is the hourly limit; errors.As a
// *NewsCollectLimitError for the time it lifts.
var ErrNewsCollectTooSoon = errors.New("news collection was requested recently")

// NewsCollectLimitError reports when the next request is allowed.
type NewsCollectLimitError struct{ NextAllowedAt time.Time }

func (e *NewsCollectLimitError) Error() string { return ErrNewsCollectTooSoon.Error() }
func (e *NewsCollectLimitError) Is(target error) bool {
	return target == ErrNewsCollectTooSoon
}

// NewsCollectPublisher publishes one NewsCollectRequested event, with
// identifiers only, on the default bus.
func NewsCollectPublisher(client actionEventClient) func(context.Context, model.NewsCollectRequested) error {
	return func(ctx context.Context, event model.NewsCollectRequested) error {
		if client == nil || event.SourceID == "" || event.AccountID == "" || event.RequestID == "" {
			return ErrNewsCollectUnavailable
		}
		body, err := json.Marshal(event)
		if err != nil {
			return ErrNewsCollectUnavailable
		}
		out, err := client.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: []ebtypes.PutEventsRequestEntry{{
			EventBusName: aws.String("default"), Source: aws.String("ttobak.crawler"),
			DetailType: aws.String("NewsCollectRequested"), Detail: aws.String(string(body)),
		}}})
		// HTTP 200 alone is insufficient; per-entry failures are in the body.
		if err != nil || out == nil || out.FailedEntryCount != 0 || len(out.Entries) != 1 ||
			aws.ToString(out.Entries[0].ErrorCode) != "" || aws.ToString(out.Entries[0].EventId) == "" {
			return ErrNewsCollectUnavailable
		}
		return nil
	}
}

// SetCollectPublisher injects the NewsCollectRequested publisher; without one
// CollectNow is ErrNewsCollectUnavailable.
func (s *AccountNewsService) SetCollectPublisher(publish func(context.Context, model.NewsCollectRequested) error) {
	s.publishCollect = publish
}

// nextCollectAllowedAt is when a source with this claim may be collected
// again, or "" when it may be collected at now. An unreadable claim is
// treated as absent; the claim condition still decides.
func nextCollectAllowedAt(lastRequestedAt string, now time.Time) string {
	last, err := time.Parse(time.RFC3339, lastRequestedAt)
	if lastRequestedAt == "" || err != nil {
		return ""
	}
	next := last.UTC().Add(newsCollectInterval)
	if !next.After(now) {
		return ""
	}
	return next.Format(time.RFC3339)
}

// collectableAccountSource reports whether src is the account's own enabled,
// user-created source.
func collectableAccountSource(src *model.CrawlerSource, accountID, sourceID string) bool {
	return src != nil && src.SourceID == sourceID && src.AccountID == accountID && src.Status != "disabled" && !src.GlobalNewsSource
}

// CollectNow asks the crawler to collect one account-owned source now. Any
// current direct member may ask, at most once per source per hour. The
// source must belong to the account by its ref and CONFIG (else ErrNotFound,
// as for Update). The claim is one fenced transaction
// (repository.ClaimAccountNewsCollect); the run itself is asynchronous and
// bounded by the crawler.
func (s *AccountNewsService) CollectNow(ctx context.Context, userID, accountID, sourceID string) (*model.NewsCollectResponse, error) {
	if !validNewsSourceID(sourceID) {
		return nil, ErrInvalidInput
	}
	if _, err := s.authorize(ctx, accountID, userID); err != nil {
		return nil, err
	}
	prefix := accountNewsSourcePrefix + accountID + "_"
	if !newsCollectIDPattern.MatchString(accountID) || len(sourceID) <= len(prefix) || sourceID[:len(prefix)] != prefix ||
		!newsCollectSuffixPattern.MatchString(sourceID[len(prefix):]) {
		return nil, ErrNotFound
	}
	refs, err := s.repo.ListAccountNewsSources(ctx, accountID)
	if err != nil {
		return nil, err
	}
	linked := false
	for _, ref := range refs {
		linked = linked || ref.SourceID == sourceID
	}
	if !linked {
		return nil, ErrNotFound
	}
	src, err := s.crawler.GetSource(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if !collectableAccountSource(src, accountID, sourceID) {
		return nil, ErrNotFound
	}
	now := s.now().UTC().Truncate(time.Second)
	if err := collectLimit(src, now); err != nil {
		return nil, err
	}
	if s.publishCollect == nil {
		return nil, ErrNewsCollectUnavailable
	}
	requestID := uuid.NewString()
	err = s.repo.ClaimAccountNewsCollect(ctx, accountID, userID, sourceID, requestID, now, now.Add(-newsCollectInterval))
	switch {
	case errors.Is(err, repository.ErrAccountNewsAccess):
		return nil, ErrForbidden // membership revoked before the commit
	case errors.Is(err, repository.ErrConditionFailed):
		// Gone/disabled (404) or claimed by a concurrent request (429).
		current, readErr := s.crawler.GetSource(ctx, sourceID)
		if readErr != nil {
			return nil, readErr
		}
		if !collectableAccountSource(current, accountID, sourceID) {
			return nil, ErrNotFound
		}
		if limited := collectLimit(current, now); limited != nil {
			return nil, limited
		}
		return nil, fmt.Errorf("collect claim refused for an unrecognized reason: %w", err)
	case err != nil:
		return nil, err
	}
	event := model.NewsCollectRequested{SourceID: sourceID, AccountID: accountID, RequestID: requestID}
	if err := s.publishCollect(ctx, event); err != nil {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), newsCollectReleaseTimeout)
		defer cancel()
		// A failed release leaves the claim in place; it simply expires after
		// newsCollectInterval, so the member waits instead of retrying now.
		if releaseErr := s.repo.ReleaseAccountNewsCollect(releaseCtx, sourceID, requestID); releaseErr != nil {
			log.Printf("news collect claim release failed: account=%s requestId=%s: %v", accountID, requestID, releaseErr)
		}
		return nil, ErrNewsCollectUnavailable
	}
	return &model.NewsCollectResponse{
		RequestID: requestID, RequestedAt: now.Format(time.RFC3339), NextAllowedAt: now.Add(newsCollectInterval).Format(time.RFC3339),
	}, nil
}

// collectLimit is the hourly-limit error for src at now, or nil.
func collectLimit(src *model.CrawlerSource, now time.Time) error {
	next := nextCollectAllowedAt(src.LastCollectRequestedAt, now)
	if next == "" {
		return nil
	}
	at, _ := time.Parse(time.RFC3339, next)
	return &NewsCollectLimitError{NextAllowedAt: at}
}
