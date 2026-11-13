package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	sfntypes "github.com/aws/aws-sdk-go-v2/service/sfn/types"
	"github.com/aws/smithy-go"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// newsCollectInterval is the per-source minimum gap between accepted
// "collect now" requests.
const newsCollectInterval = time.Hour

// newsCollectReleaseTimeout bounds the claim release after a failed publish.
const newsCollectReleaseTimeout = 5 * time.Second

// The starter's NewsCollectRequested contract (news_manual.parse_request):
// accountId and requestId match newsCollectIDPattern and sourceId is
// "acct_" + accountId + "_" + 8 lowercase hex. Other sources cannot be
// collected on request.
var newsCollectIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
var newsCollectSuffixPattern = regexp.MustCompile(`^[0-9a-f]{8}$`)

// ErrNewsCollectUnavailable means the request was not handed to the crawler
// (no publisher, or the event was not accepted); the caller may retry.
var ErrNewsCollectUnavailable = errors.New("news collection is unavailable")

// ErrNewsCollectDailyWindow refuses a request near the daily schedule;
// errors.As a *NewsCollectWindowError for the window's end.
var ErrNewsCollectDailyWindow = errors.New("news collection is paused around the daily run")

// ErrNewsCollectBusy refuses a request while a daily or publication run is
// in progress.
var ErrNewsCollectBusy = errors.New("another news collection is in progress")

// NewsCollectWindowError reports when the daily window ends.
type NewsCollectWindowError struct{ NextAllowedAt time.Time }

func (e *NewsCollectWindowError) Error() string { return ErrNewsCollectDailyWindow.Error() }
func (e *NewsCollectWindowError) Is(target error) bool {
	return target == ErrNewsCollectDailyWindow
}

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

// newsCollectAdmissionTimeout bounds both execution lookups together.
const newsCollectAdmissionTimeout = 5 * time.Second

// NewsRunChecker reports whether a state machine has a RUNNING execution.
type NewsRunChecker interface {
	Running(ctx context.Context, stateMachineARN string) (bool, error)
}

// sfnExecutionLister is the Step Functions call NewsRunChecker needs.
type sfnExecutionLister interface {
	ListExecutions(ctx context.Context, in *sfn.ListExecutionsInput, optFns ...func(*sfn.Options)) (*sfn.ListExecutionsOutput, error)
}

type sfnRunChecker struct{ client sfnExecutionLister }

// NewSFNRunChecker checks for a RUNNING execution exactly as the starter's
// _running does (status RUNNING, max results 1). A nil client always errors.
func NewSFNRunChecker(client sfnExecutionLister) NewsRunChecker {
	return sfnRunChecker{client: client}
}

func (c sfnRunChecker) Running(ctx context.Context, stateMachineARN string) (bool, error) {
	if c.client == nil {
		return false, ErrNewsCollectUnavailable
	}
	out, err := c.client.ListExecutions(ctx, &sfn.ListExecutionsInput{
		StateMachineArn: aws.String(stateMachineARN), StatusFilter: sfntypes.ExecutionStatusRunning, MaxResults: 1,
	})
	if err != nil {
		return false, err
	}
	if out == nil {
		return false, ErrNewsCollectUnavailable
	}
	return len(out.Executions) > 0, nil
}

// newsDailyWindow is the starter's daily window (news_manual.daily_window):
// from lead before the daily schedule until grace after it, wrapping at
// midnight. Its four values come from the same CDK constants.
type newsDailyWindow struct {
	scheduleSeconds, leadSeconds, graceSeconds int64
}

var newsWindowValuePattern = regexp.MustCompile(`^[0-9]{1,5}$`)

// parseNewsDailyWindow validates the configuration as daily_window does.
func parseNewsDailyWindow(hour, minute, leadSeconds, graceSeconds string) (newsDailyWindow, error) {
	raw := []string{hour, minute, leadSeconds, graceSeconds}
	values := make([]int64, len(raw))
	for i, v := range raw {
		if !newsWindowValuePattern.MatchString(v) {
			return newsDailyWindow{}, ErrNewsCollectUnavailable
		}
		values[i], _ = strconv.ParseInt(v, 10, 64)
	}
	h, m, lead, grace := values[0], values[1], values[2], values[3]
	if h > 23 || m > 59 || lead <= 0 || lead >= 43200 || grace <= 0 || grace >= 43200 {
		return newsDailyWindow{}, ErrNewsCollectUnavailable
	}
	return newsDailyWindow{scheduleSeconds: h*3600 + m*60, leadSeconds: lead, graceSeconds: grace}, nil
}

// end is the window's end when now is inside it, else the zero time.
func (w newsDailyWindow) end(now time.Time) time.Time {
	unix := now.Unix()
	// Signed seconds since the nearest schedule time, in [-43200, 43200).
	shifted := (unix - w.scheduleSeconds + 43200) % 86400
	if shifted < 0 {
		shifted += 86400
	}
	offset := shifted - 43200
	if offset < -w.leadSeconds || offset >= w.graceSeconds {
		return time.Time{}
	}
	return time.Unix(unix-offset+w.graceSeconds, 0).UTC()
}

// NewsCollectAdmission is the starter's admission configuration as the API
// sees it: the daily window and the two state machines whose RUNNING
// executions make the starter refuse.
type NewsCollectAdmission struct {
	window                   newsDailyWindow
	dailyARN, publicationARN string
	runs                     NewsRunChecker
	invalid                  bool
}

// NewNewsCollectAdmission builds the admission check from the API's
// environment values. Invalid or missing values, or no checker, make every
// request unavailable rather than unchecked.
func NewNewsCollectAdmission(hour, minute, leadSeconds, graceSeconds, dailyARN, publicationARN string, runs NewsRunChecker) *NewsCollectAdmission {
	window, err := parseNewsDailyWindow(hour, minute, leadSeconds, graceSeconds)
	return &NewsCollectAdmission{
		window: window, dailyARN: dailyARN, publicationARN: publicationARN, runs: runs,
		invalid: err != nil || runs == nil || !validStateMachineARN(dailyARN) || !validStateMachineARN(publicationARN),
	}
}

func validStateMachineARN(arn string) bool {
	return strings.HasPrefix(arn, "arn:") && strings.Contains(arn, ":states:") && strings.Contains(arn, ":stateMachine:")
}

// check mirrors the starter's refusals before its claim, in its order. It is
// advisory (check-then-publish): a run can begin after it, so the starter
// stays the enforcement point and may still refuse after a 202, releasing the
// claim. A lookup error is unavailable, never accepted.
func (a *NewsCollectAdmission) check(ctx context.Context, now time.Time) error {
	if a == nil || a.invalid {
		return ErrNewsCollectUnavailable
	}
	if end := a.window.end(now); !end.IsZero() {
		return &NewsCollectWindowError{NextAllowedAt: end}
	}
	ctx, cancel := context.WithTimeout(ctx, newsCollectAdmissionTimeout)
	defer cancel()
	for _, arn := range []string{a.dailyARN, a.publicationARN} {
		running, err := a.runs.Running(ctx, arn)
		if err != nil {
			// Only the error code: no names, queries or URLs.
			code := "UNKNOWN"
			var apiErr smithy.APIError
			if errors.As(err, &apiErr) {
				code = apiErr.ErrorCode()
			} else if errors.Is(err, context.DeadlineExceeded) {
				code = "TIMEOUT"
			}
			log.Printf("news collect admission lookup failed: code=%s", code)
			return ErrNewsCollectUnavailable
		}
		if running {
			return ErrNewsCollectBusy
		}
	}
	return nil
}

// SetCollectAdmission injects the admission check; without one CollectNow is
// ErrNewsCollectUnavailable.
func (s *AccountNewsService) SetCollectAdmission(admission *NewsCollectAdmission) {
	s.collectAdmission = admission
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
// bounded by the crawler. Between ownership and the hourly limit it checks
// the starter's admission conditions (daily window, running daily or
// publication run), so a non-member never learns the pipeline state and a
// refusal claims and publishes nothing.
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
	if s.publishCollect == nil {
		return nil, ErrNewsCollectUnavailable
	}
	now := s.now().UTC().Truncate(time.Second)
	if err := s.collectAdmission.check(ctx, now); err != nil {
		return nil, err
	}
	if err := collectLimit(src, now); err != nil {
		return nil, err
	}
	requestID := uuid.NewString()
	err = s.repo.ClaimAccountNewsCollect(ctx, accountID, userID, sourceID, requestID, now, now.Add(-newsCollectInterval))
	switch {
	case errors.Is(err, repository.ErrAccountNewsAccess):
		return nil, ErrForbidden // membership revoked before the commit
	case errors.Is(err, repository.ErrConditionFailed):
		// Gone/disabled (404) or claimed by a concurrent request (429). The
		// re-read is strongly consistent so it sees the refusing write.
		current, readErr := s.crawler.GetSourceConsistent(ctx, sourceID)
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
