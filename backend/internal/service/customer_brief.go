package service

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

var ErrCustomerBriefPublish = errors.New("customer brief request delivery failed")

// Leases and budgets mirror the saved re-summary run.
const (
	customerBriefQueuedLease  = 5 * time.Minute
	customerBriefRunningLease = 10 * time.Minute
	customerBriefWorkBudget   = 8 * time.Minute
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
	Calendar func(ctx context.Context, userID, accountID string, from, to time.Time, limit int32) ([]model.CalendarEvent, error)
	Terms    func(ctx context.Context, pk string) ([]model.DictTerm, error)
}

// NewCustomerBriefSources binds the existing read paths. Terms reads rows
// directly: DictionaryService.ListLayer may perform a migration write.
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
		Calendar: func(ctx context.Context, userID, accountID string, from, to time.Time, limit int32) ([]model.CalendarEvent, error) {
			page, err := calendar.ListEvents(ctx, userID, model.CalendarQuery{From: from, To: to, AccountID: accountID, Limit: limit})
			if err != nil {
				return nil, err
			}
			return page.Events, nil
		},
		Terms: dict.ListDictTerms,
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

func CustomerBriefPublisher(client actionEventClient) func(context.Context, model.CustomerBriefRequested) error {
	return func(ctx context.Context, event model.CustomerBriefRequested) error {
		if client == nil {
			return ErrCustomerBriefPublish
		}
		body, err := json.Marshal(event)
		if err != nil {
			return ErrCustomerBriefPublish
		}
		out, err := client.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: []ebtypes.PutEventsRequestEntry{{
			Source: aws.String("ttobak.analysis"), DetailType: aws.String("CustomerBriefRequested"), Detail: aws.String(string(body)),
		}}})
		if err != nil || out == nil || out.FailedEntryCount != 0 || len(out.Entries) != 1 || aws.ToString(out.Entries[0].ErrorCode) != "" || aws.ToString(out.Entries[0].EventId) == "" {
			return ErrCustomerBriefPublish
		}
		return nil
	}
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

func customerBriefStatus(state *model.CustomerBriefState) *model.CustomerBriefResponse {
	return &model.CustomerBriefResponse{Status: state.Status, RunID: state.RunID, ErrorCode: state.ErrorCode, LeaseUntil: state.LeaseUntil}
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
		return customerBriefStatus(prior), nil
	}
	next := &model.CustomerBriefState{AccountID: accountID, UserID: userID, RunID: uuid.NewString(), Status: model.AnalysisQueued,
		RequestedAt: now, LeaseUntil: now.Add(customerBriefQueuedLease).UnixMilli(), UpdatedAt: now,
		ExpiresAt: now.Add(model.CustomerBriefTTL).Unix()}
	if prior != nil && !expired && prior.Sealed != "" {
		// Keep the last good brief's own expiry; only success extends it.
		next.ExpiresAt = prior.ExpiresAt
	}
	if err := s.repo.QueueCustomerBrief(ctx, prior, next, prior != nil && expired); err != nil {
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
	return customerBriefStatus(next), nil
}

// Get returns the run status and the last good brief to a current direct
// member. An expired row reads as absent even before the sweep removes it.
func (s *CustomerBriefService) Get(ctx context.Context, userID, accountID string) (*model.CustomerBriefResponse, error) {
	if err := s.requireDirectMember(ctx, userID, accountID); err != nil {
		return nil, err
	}
	state, err := s.repo.GetCustomerBrief(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if state == nil || state.Expired(now) {
		return &model.CustomerBriefResponse{Status: model.AnalysisUnknown}, nil
	}
	result := customerBriefStatus(state)
	if state.Pending() && state.LeaseUntil <= now.UnixMilli() {
		result.Status, result.ErrorCode, result.LeaseUntil = model.AnalysisFailed, model.AnalysisInterrupted, 0
	}
	if state.Sealed == "" {
		return result, nil
	}
	plain, err := s.repo.OpenCustomerBrief(ctx, state)
	if err != nil {
		return nil, err
	}
	var brief model.CustomerBrief
	if err := json.Unmarshal(plain, &brief); err != nil {
		return nil, repository.ErrCustomerBriefUnreadable
	}
	generated := state.GeneratedAt
	result.Brief, result.GeneratedAt = &brief, &generated
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
		}
		return errors.Join(err, failed(code))
	}
	// Counts and byte lengths only: never brief, meeting or news text.
	log.Printf("customer brief published: account=%s run=%s promptBytes=%d outputBytes=%d briefBytes=%d %s",
		accountID, runID, len(prompt), len(output), len(body), stats)
	return nil
}
