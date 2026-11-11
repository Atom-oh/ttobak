package service

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

var (
	ErrCustomerBriefPublish = errors.New("customer brief request delivery failed")
	// ErrCustomerBriefIncomplete means a paged source still had more rows
	// when its read bound was reached, so an input may be missing.
	ErrCustomerBriefIncomplete = errors.New("customer brief source incomplete")
)

// Leases and budgets mirror the saved re-summary run.
const (
	customerBriefQueuedLease  = 5 * time.Minute
	customerBriefRunningLease = 10 * time.Minute
	customerBriefWorkBudget   = 8 * time.Minute
	// A queued row expires no sooner than a full run could take.
	customerBriefRunFloor = customerBriefQueuedLease + customerBriefRunningLease + customerBriefWorkBudget + 5*time.Minute
)

type customerBriefRepo interface {
	GetAccount(context.Context, string) (*model.Account, error)
	GetMember(context.Context, string, string) (*model.AccountMember, error)
	GetCustomerBrief(context.Context, string, string) (*model.CustomerBriefState, error)
	OpenCustomerBrief(context.Context, *model.CustomerBriefState) ([]byte, error)
	QueueCustomerBrief(context.Context, *model.CustomerBriefState, *model.CustomerBriefState, bool) error
	StartCustomerBrief(context.Context, *model.CustomerBriefState, string, time.Time, int64) error
	FailCustomerBrief(context.Context, *model.CustomerBriefState, string, time.Time) error
	CompleteCustomerBrief(context.Context, *model.CustomerBriefState, []byte, string, string, time.Time) error
}

// CustomerBriefSources are the requester-scoped reads behind a brief. Each
// performs its own authorization except News, which the worker guards with
// the direct-membership check.
type CustomerBriefSources struct {
	Insights func(ctx context.Context, userID, accountID string, from, to time.Time) ([]model.AccountInsightDTO, error)
	Meetings func(ctx context.Context, userID, accountID string) ([]model.MeetingRefDTO, error)
	News     func(ctx context.Context, accountID string, from, to time.Time) ([]model.CrawledDocument, error)
	Events   func(ctx context.Context, accountID, userID string, from time.Time, limit int) ([]model.AccountEvent, error)
	// Calendar returns one page and its continuation ("" when complete).
	Calendar func(ctx context.Context, userID, accountID string, from, to time.Time, limit int32, cursor string) ([]model.CalendarEvent, string, error)
	Terms    func(ctx context.Context, pk string) ([]model.DictTerm, error)
}

// NewCustomerBriefSources binds the existing read paths. Terms reads rows
// directly: DictionaryService.ListLayer may perform a migration write. For
// the personal layer it also reads the legacy blob (GetItem only), so an
// unmigrated dictionary still yields its effective term keys.
func NewCustomerBriefSources(accounts *AccountService, news *AccountNewsService, events *AccountEventsService, calendar *CalendarService, dict *repository.DictionaryRepository) CustomerBriefSources {
	return CustomerBriefSources{
		Insights: func(ctx context.Context, userID, accountID string, from, to time.Time) ([]model.AccountInsightDTO, error) {
			return accounts.ListAccountInsights(ctx, userID, accountID, from, to, nil)
		},
		Meetings: accounts.ListAccountMeetings,
		News:     news.CurrentNews,
		Events: func(ctx context.Context, accountID, userID string, from time.Time, limit int) ([]model.AccountEvent, error) {
			page, err := events.List(ctx, accountID, userID, EventListOptions{From: from.UTC().Format(time.RFC3339), Limit: limit})
			if err != nil {
				return nil, err
			}
			return page.Events, nil
		},
		Calendar: func(ctx context.Context, userID, accountID string, from, to time.Time, limit int32, cursor string) ([]model.CalendarEvent, string, error) {
			page, err := calendar.ListEvents(ctx, userID, model.CalendarQuery{From: from, To: to, AccountID: accountID, Limit: limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			next := ""
			if page.NextCursor != nil {
				next = *page.NextCursor
			}
			return page.Events, next, nil
		},
		Terms: func(ctx context.Context, pk string) ([]model.DictTerm, error) {
			rows, err := dict.ListDictTerms(ctx, pk)
			if err != nil || !strings.HasPrefix(pk, model.PrefixUser) {
				return rows, err
			}
			legacy, err := dict.GetDictionary(ctx, strings.TrimPrefix(pk, model.PrefixUser))
			if err != nil {
				return nil, err
			}
			keys := effectiveTermKeys(legacy, rows)
			terms := make([]model.DictTerm, len(keys))
			for i, key := range keys {
				terms[i] = model.DictTerm{TermKey: key, State: model.TermStateActive}
			}
			return terms, nil
		},
	}
}

// CustomerBriefPublisher publishes one CustomerBriefRequested event, with
// identifiers only, on the default bus.
func CustomerBriefPublisher(client actionEventClient) func(context.Context, model.CustomerBriefRequested) error {
	return func(ctx context.Context, event model.CustomerBriefRequested) error {
		if client == nil || event.AccountID == "" || event.UserID == "" || event.RunID == "" {
			return ErrCustomerBriefPublish
		}
		body, err := json.Marshal(event)
		if err != nil {
			return ErrCustomerBriefPublish
		}
		out, err := client.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: []ebtypes.PutEventsRequestEntry{{
			EventBusName: aws.String("default"), Source: aws.String("ttobak.analysis"),
			DetailType: aws.String("CustomerBriefRequested"), Detail: aws.String(string(body)),
		}}})
		if err != nil || out == nil || out.FailedEntryCount != 0 || len(out.Entries) != 1 || aws.ToString(out.Entries[0].ErrorCode) != "" || aws.ToString(out.Entries[0].EventId) == "" {
			return ErrCustomerBriefPublish
		}
		return nil
	}
}

type CustomerBriefService struct {
	repo     customerBriefRepo
	sources  CustomerBriefSources
	generate func(ctx context.Context, system, prompt string) (string, error)
	modelID  string
	publish  func(context.Context, model.CustomerBriefRequested) error
	now      func() time.Time
}

func NewCustomerBriefService(repo customerBriefRepo, sources CustomerBriefSources, generate func(context.Context, string, string) (string, error), publish func(context.Context, model.CustomerBriefRequested) error) *CustomerBriefService {
	return &CustomerBriefService{repo: repo, sources: sources, generate: generate, modelID: ClaudeOpusModelID, publish: publish, now: time.Now}
}

// GenerateCustomerBrief calls the auxiliary model through the strict
// complete-response gate: truncated output is an error, never a partial brief.
func (s *BedrockService) GenerateCustomerBrief(ctx context.Context, system, prompt string) (string, error) {
	request := ClaudeRequest{AnthropicVersion: "bedrock-2023-05-31", MaxTokens: 4096, System: system,
		Messages: []ClaudeMessage{{Role: "user", Content: []ContentBlock{{Type: "text", Text: prompt}}}}}
	return s.invokeCompleteAnalysisWithID(ctx, PrepareClaudeRequest(request, ClaudeOpusModelID), ClaudeOpusModelID)
}

// requireDirectMember accepts only a current MEMBER row on an existing
// account; inherited (ancestor or billing) read access does not qualify.
func (s *CustomerBriefService) requireDirectMember(ctx context.Context, userID, accountID string) error {
	if !resummaryID.MatchString(userID) || validateAccountHierarchyIDs(accountID, "") != nil {
		return ErrInvalidInput
	}
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	if account == nil {
		return ErrNotFound
	}
	member, err := s.repo.GetMember(ctx, accountID, userID)
	if err != nil {
		return err
	}
	if member == nil || member.UserID != userID {
		return ErrForbidden
	}
	return nil
}

// customerBriefErrorCodes are the worker codes passed through to clients;
// any other stored code reads as FAILED.
var customerBriefErrorCodes = map[string]bool{
	model.AnalysisSourceChanged: true, "SOURCE_UNAVAILABLE": true, model.AnalysisInvalidOutput: true,
	"ACCESS_REVOKED": true, model.AnalysisInterrupted: true, "TIMEOUT": true, model.AnalysisProviderFailed: true,
	model.AnalysisPublishFailed: true, model.AnalysisPersistenceFail: true, "OUTPUT_TOO_LARGE": true,
}

// customerBriefRun maps a stored row to the client run state. A pending run
// whose lease passed reads as failed/INTERRUPTED.
func customerBriefRun(state *model.CustomerBriefState, now time.Time) model.CustomerBriefRun {
	if state == nil {
		return model.CustomerBriefRun{Status: "none"}
	}
	run := model.CustomerBriefRun{Status: state.Status, RunID: state.RunID}
	if !state.RequestedAt.IsZero() {
		requested := state.RequestedAt
		run.RequestedAt = &requested
	}
	switch {
	case state.Pending() && state.LeaseUntil <= now.UnixMilli():
		run.Status, run.ErrorCode = model.AnalysisFailed, model.AnalysisInterrupted
	case state.Pending(), state.Status == model.AnalysisSucceeded:
	case state.Status == model.AnalysisFailed && customerBriefErrorCodes[state.ErrorCode]:
		run.ErrorCode = state.ErrorCode
	default:
		run.Status, run.ErrorCode = model.AnalysisFailed, "FAILED"
	}
	return run
}

// Request claims a run for the member and publishes CustomerBriefRequested.
// A live queued/running run is returned as is, without a second event.
func (s *CustomerBriefService) Request(ctx context.Context, userID, accountID string) (*model.CustomerBriefResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := s.requireDirectMember(ctx, userID, accountID); err != nil {
		return nil, err
	}
	prior, err := s.repo.GetCustomerBrief(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	expired := prior.Expired(now)
	if prior.Pending() && !expired && prior.LeaseUntil > now.UnixMilli() {
		return &model.CustomerBriefResponse{Run: customerBriefRun(prior, now)}, nil
	}
	next := &model.CustomerBriefState{AccountID: accountID, UserID: userID, RunID: uuid.NewString(), Status: model.AnalysisQueued,
		RequestedAt: now, LeaseUntil: now.Add(customerBriefQueuedLease).UnixMilli(), UpdatedAt: now,
		ExpiresAt: now.Add(model.CustomerBriefTTL).Unix()}
	keep := prior.BriefReadable(now)
	if keep {
		// The brief keeps its own expiry (pinned on legacy rows too); only the
		// row TTL is floored so the sweep cannot remove a working run.
		next.BriefExpiresAt = prior.BriefExpiry()
		next.ExpiresAt = max(next.BriefExpiresAt, now.Add(customerBriefRunFloor).Unix())
	}
	// A brief past its own expiry is cleared in the same write.
	if err := s.repo.QueueCustomerBrief(ctx, prior, next, prior != nil && !keep); err != nil {
		return nil, err
	}
	err = ErrCustomerBriefPublish
	if s.publish != nil {
		err = s.publish(ctx, model.CustomerBriefRequested{AccountID: accountID, UserID: userID, RunID: next.RunID})
	}
	if err != nil {
		if failErr := s.fail(ctx, next, model.AnalysisPublishFailed); failErr != nil {
			return nil, failErr
		}
		return nil, ErrCustomerBriefPublish
	}
	// The response does not open the retained brief; GET returns it.
	return &model.CustomerBriefResponse{Run: customerBriefRun(next, now)}, nil
}

// Get returns the run status and the last good brief to a current direct
// member. An expired row reads as absent even before the sweep removes it,
// and the brief is returned only before its own expiry.
func (s *CustomerBriefService) Get(ctx context.Context, userID, accountID string) (*model.CustomerBriefResponse, error) {
	if err := s.requireDirectMember(ctx, userID, accountID); err != nil {
		return nil, err
	}
	state, err := s.repo.GetCustomerBrief(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if state.Expired(now) {
		state = nil
	}
	result := &model.CustomerBriefResponse{Run: customerBriefRun(state, now)}
	if !state.BriefReadable(now) {
		return result, nil
	}
	plain, err := s.repo.OpenCustomerBrief(ctx, state)
	if err != nil {
		return nil, err
	}
	var brief model.CustomerBrief
	// The size check keeps the response within CustomerBriefResponseMaxBytes.
	if len(plain) > model.CustomerBriefMaxBytes || json.Unmarshal(plain, &brief) != nil {
		return nil, repository.ErrCustomerBriefUnreadable
	}
	result.Brief = &model.CustomerBriefView{GeneratedAt: state.GeneratedAt, Content: &brief}
	return result, nil
}

func (s *CustomerBriefService) fail(ctx context.Context, state *model.CustomerBriefState, code string) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := s.repo.FailCustomerBrief(cleanup, state, code, s.now().UTC())
	if errors.Is(err, repository.ErrConditionFailed) {
		return nil
	}
	return err
}

func customerBriefErrorCode(work context.Context, err error) string {
	switch {
	case work.Err() != nil, errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "TIMEOUT"
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrNotFound):
		return "ACCESS_REVOKED"
	case errors.Is(err, repository.ErrConditionFailed):
		return model.AnalysisSourceChanged
	case errors.Is(err, ErrInvalidAnalysisResponse):
		return model.AnalysisInvalidOutput
	case errors.Is(err, ErrCustomerBriefIncomplete):
		return "SOURCE_UNAVAILABLE"
	default:
		return "SOURCE_UNAVAILABLE"
	}
}

// Process runs one CustomerBriefRequested event. Only the queued run named by
// the event proceeds; every failure records the error state alone, so the
// previous brief stays readable.
func (s *CustomerBriefService) Process(ctx context.Context, accountID, userID, runID string) error {
	if !resummaryID.MatchString(runID) || !resummaryID.MatchString(userID) || validateAccountHierarchyIDs(accountID, "") != nil {
		return ErrInvalidInput
	}
	state, err := s.repo.GetCustomerBrief(ctx, accountID, userID)
	if err != nil {
		return err
	}
	if state == nil || state.Expired(s.now()) || state.RunID != runID || state.Status != model.AnalysisQueued {
		return nil
	}
	if state.LeaseUntil <= s.now().UnixMilli() {
		return s.fail(ctx, state, model.AnalysisInterrupted)
	}
	inputs, err := s.gather(ctx, state)
	if err != nil {
		return s.fail(ctx, state, customerBriefErrorCode(ctx, err))
	}
	hash := inputs.hash()
	now := s.now().UTC()
	lease := now.Add(customerBriefRunningLease).UnixMilli()
	if err := s.repo.StartCustomerBrief(ctx, state, hash, now, lease); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return s.fail(ctx, state, model.AnalysisSourceChanged)
		}
		return err
	}
	running := *state
	running.Status, running.LeaseUntil, running.InputHash = model.AnalysisRunning, lease, hash
	work, cancel := context.WithTimeout(ctx, customerBriefWorkBudget)
	defer cancel()
	failed := func(code string) error {
		log.Printf("customer brief failed: account=%s run=%s code=%s", accountID, runID, code)
		return s.fail(ctx, &running, code)
	}
	if s.generate == nil {
		return failed(model.AnalysisProviderFailed)
	}
	prompt := inputs.prompt()
	output, err := s.generate(work, customerBriefSystemPrompt, prompt)
	if err != nil {
		code := model.AnalysisProviderFailed
		if work.Err() != nil {
			code = "TIMEOUT"
		} else if errors.Is(err, ErrInvalidAnalysisResponse) {
			code = model.AnalysisInvalidOutput
		}
		return failed(code)
	}
	brief, stats, err := inputs.validate(output)
	if err != nil {
		return failed(model.AnalysisInvalidOutput)
	}
	body, err := json.Marshal(brief)
	if err != nil || len(body) > model.CustomerBriefMaxBytes {
		return failed("OUTPUT_TOO_LARGE")
	}
	// gather re-checks access as the requester, then re-reads the inputs,
	// immediately before publishing; the completion transaction also
	// requires the MEMBER row.
	fresh, err := s.gather(work, state)
	if err != nil {
		return failed(customerBriefErrorCode(work, err))
	}
	if fresh.hash() != hash {
		return failed(model.AnalysisSourceChanged)
	}
	if err := s.repo.CompleteCustomerBrief(work, &running, body, actionSourceHash(string(body)), s.modelID, s.now().UTC()); err != nil {
		code := model.AnalysisPersistenceFail
		if errors.Is(err, repository.ErrConditionFailed) {
			code = model.AnalysisSourceChanged
			// The transaction also requires the MEMBER row: name a revoked
			// membership distinctly from a run or input change.
			if access := s.requireDirectMember(work, userID, accountID); errors.Is(access, ErrForbidden) || errors.Is(access, ErrNotFound) {
				code = "ACCESS_REVOKED"
			}
		}
		return errors.Join(err, failed(code))
	}
	// Counts and byte lengths only: never brief, meeting or news text.
	log.Printf("customer brief published: account=%s run=%s promptBytes=%d outputBytes=%d briefBytes=%d %s",
		accountID, runID, len(prompt), len(output), len(body), stats)
	return nil
}
