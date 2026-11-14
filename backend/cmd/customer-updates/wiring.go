package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcore"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/sealing"
	"github.com/ttobak/backend/internal/service"
)

const (
	maxInvokeResponseBytes = 64 * 1024
	maxMemberNames         = 500
	invokeTimeout          = 30 * time.Second
	runSessionPrefix       = "ttobak-cu-"
)

// runsConfig is the run phases' environment.
type runsConfig struct {
	RuntimeARN   string
	EndpointName string
	KMSKeyID     string
	DataBucket   string
	AssetBucket  string
	KBBucket     string
	// ResearchPairsPerDay is CUSTOMER_UPDATE_RESEARCH_PAIRS_PER_DAY: unset or
	// unparseable (or negative) is the default 30; 0 starts no runs.
	ResearchPairsPerDay int
}

func runsConfigFromEnv(getenv func(string) string) runsConfig {
	c := runsConfig{
		RuntimeARN:   strings.TrimSpace(getenv("CUSTOMER_UPDATE_RUNTIME_ARN")),
		EndpointName: strings.TrimSpace(getenv("CUSTOMER_UPDATE_ENDPOINT_NAME")),
		KMSKeyID:     strings.TrimSpace(getenv("KMS_KEY_ID")),
		DataBucket:   strings.TrimSpace(getenv("BILLING_DATA_BUCKET")),
		AssetBucket:  strings.TrimSpace(getenv("BUCKET_NAME")),
		KBBucket:     strings.TrimSpace(getenv("KB_BUCKET_NAME")),
	}
	if c.EndpointName == "" {
		c.EndpointName = "DEFAULT"
	}
	c.ResearchPairsPerDay = service.DefaultResearchPairsPerDay
	if n, err := strconv.Atoi(strings.TrimSpace(getenv("CUSTOMER_UPDATE_RESEARCH_PAIRS_PER_DAY"))); err == nil && n >= 0 {
		c.ResearchPairsPerDay = n
	}
	return c
}

// runsEnabled reports whether the run phases may run: they need the runtime
// ARN and the record key (run rows, decisions and drafts are sealed).
func (c runsConfig) runsEnabled() (bool, string) {
	switch {
	case c.RuntimeARN == "":
		return false, ""
	case c.KMSKeyID == "":
		return false, "customer update runs are off: CUSTOMER_UPDATE_RUNTIME_ARN is set but KMS_KEY_ID is not"
	}
	return true, ""
}

// newRuns wires the run phases with the same services the API uses for
// accounts, projects, news, membership and meeting notes (read-only use).
// With the phases off it returns runs whose Tick does nothing.
func newRuns(cfg aws.Config, tableName string, c runsConfig) *service.CustomerUpdateRuns {
	dynamoClient := dynamodb.NewFromConfig(cfg)
	cuRepo := repository.NewCustomerUpdateRepository(dynamoClient, tableName)
	ok, warning := c.runsEnabled()
	if warning != "" {
		log.Print(warning)
	}
	if !ok {
		return service.NewCustomerUpdateRuns(service.CustomerUpdateRunsDeps{Repo: cuRepo}, time.Now)
	}
	s3Client := s3.NewFromConfig(cfg)
	sealer := sealing.NewEnvelope(kms.NewFromConfig(cfg), c.KMSKeyID)
	cuRepo.SetRecordSealer(sealer)
	// No pack read opens a sealed record of the shared repository (contacts,
	// events, briefs), so it gets no sealer.
	repo := repository.NewDynamoDBRepositoryWithS3(dynamoClient, tableName, s3Client, c.AssetBucket)
	members := service.NewDirectMemberChecker(repo)
	crawlerRepo := repository.NewCrawlerRepository(dynamoClient, tableName)
	insights := service.NewInsightsService(crawlerRepo, s3Client, c.KBBucket, c.AssetBucket)
	accounts := service.NewAccountService(repo, s3Client, c.AssetBucket)
	updateContext := service.NewCustomerUpdateContext(service.CustomerUpdateContextDeps{
		Accounts:      accounts,
		Members:       members,
		Settings:      cuRepo,
		Projects:      service.NewProjectService(repo),
		Insights:      accounts,
		Meetings:      accounts,
		MeetingReader: service.NewMeetingNotesReader(repo),
		News:          service.NewAccountNewsService(repo, crawlerRepo, insights),
		MemberNames:   &accountMemberNames{members: members, repo: repo},
		Services:      service.NewS3ServicesInUseReader(s3Client, c.DataBucket),
	}, time.Now)
	runs := service.NewCustomerUpdateRuns(service.CustomerUpdateRunsDeps{
		Repo: cuRepo, Context: updateContext,
		Invoker: &agentInvoker{client: bedrockagentcore.NewFromConfig(cfg, agentCoreOptions), arn: c.RuntimeARN, qualifier: c.EndpointName},
	}, time.Now)
	runs.SetResearchPairsPerDay(c.ResearchPairsPerDay)
	return runs
}

// ---- agent invoker ----

// agentCoreOptions disables SDK retries: a retry would resend the same
// single-use session id and payload, and after an ambiguous timeout the
// runtime would answer RUN_EXISTS for a run that is actually running.
func agentCoreOptions(o *bedrockagentcore.Options) {
	o.Retryer = aws.NopRetryer{}
	o.RetryMaxAttempts = 1
}

type agentRuntimeAPI interface {
	InvokeAgentRuntime(ctx context.Context, in *bedrockagentcore.InvokeAgentRuntimeInput, optFns ...func(*bedrockagentcore.Options)) (*bedrockagentcore.InvokeAgentRuntimeOutput, error)
}

// agentInvoker calls InvokeAgentRuntime once per run id. The session id is
// derived from the run id (42 characters, within AgentCore's 33-256 rule).
type agentInvoker struct {
	client    agentRuntimeAPI
	arn       string
	qualifier string
}

var responseCodePattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,40}$`)

type invokeResponse struct {
	Accepted bool   `json:"accepted"`
	RunID    string `json:"runId"`
	Error    *struct {
		Code string `json:"code"`
	} `json:"error"`
}

// notAccepted is a definite refusal from the runtime. A payload rejection
// (HTTP 400 or 413, or an unknown status with a code other than the runtime's
// refusal codes) is marked Contract: it fails the same way every time.
func notAccepted(status int32, code string) error {
	refusal := code == "BUSY" || code == "RUN_EXISTS" || code == "UNAVAILABLE"
	contract := !refusal && (status == 400 || status == 413 || status == 0)
	return &service.RunNotAcceptedError{Code: code, Contract: contract}
}

func outcomeUnknown(code string) error {
	return fmt.Errorf("%w: %s", service.ErrRunOutcomeUnknown, code)
}

// Invoke sends the payload and accepts only a 200 answer whose bounded body
// says accepted for this run id. A transport error, timeout, unreadable body
// or a 200 that is not a valid acceptance is ErrRunOutcomeUnknown (the run
// may be running); any other runtime answer is a RunNotAcceptedError. The
// body is never logged or returned.
func (a *agentInvoker) Invoke(ctx context.Context, runID string, payload []byte) error {
	ctx, cancel := context.WithTimeout(ctx, invokeTimeout)
	defer cancel()
	out, err := a.client.InvokeAgentRuntime(ctx, &bedrockagentcore.InvokeAgentRuntimeInput{
		AgentRuntimeArn:  aws.String(a.arn),
		Qualifier:        aws.String(a.qualifier),
		Payload:          payload,
		RuntimeSessionId: aws.String(runSessionPrefix + runID),
		ContentType:      aws.String("application/json"),
		Accept:           aws.String("application/json"),
	})
	if err != nil {
		return err
	}
	if out.Response == nil {
		return outcomeUnknown("empty")
	}
	defer out.Response.Close()
	body, err := io.ReadAll(io.LimitReader(out.Response, maxInvokeResponseBytes+1))
	if err != nil {
		return outcomeUnknown("read")
	}
	var status int32
	if out.StatusCode != nil {
		status = *out.StatusCode
	}
	var res invokeResponse
	parsed := len(body) <= maxInvokeResponseBytes && json.Unmarshal(body, &res) == nil
	switch {
	case parsed && res.Error != nil:
		code := res.Error.Code
		if !responseCodePattern.MatchString(code) {
			code = "error"
		}
		return notAccepted(status, code)
	case status != 0 && status != 200:
		return notAccepted(status, fmt.Sprintf("status_%d", status))
	case !parsed:
		return outcomeUnknown("body")
	case !res.Accepted || res.RunID != runID:
		return outcomeUnknown("not_accepted")
	}
	return nil
}

// ---- member names ----

type directMemberChecker interface {
	RequireDirectMember(ctx context.Context, userID, accountID string) error
}

type memberProfileRepo interface {
	ListAccountMembers(ctx context.Context, accountID string) ([]model.AccountMember, error)
	GetUserByID(ctx context.Context, userID string) (*model.User, error)
}

// accountMemberNames implements service.UpdateContextMemberNames: profile
// display names of the account's current direct members (canonical MEMBER
// rows only; ListAccountMembers reads every page), at most 500 names (the
// finalize block-list cap), after checking the caller is a direct member.
type accountMemberNames struct {
	members directMemberChecker
	repo    memberProfileRepo
}

var errMemberNamesUnavailable = errors.New("member names reader is not configured")

func (a *accountMemberNames) AccountMemberNames(ctx context.Context, userID, accountID string) ([]string, error) {
	if a == nil || a.members == nil || a.repo == nil {
		return nil, errMemberNamesUnavailable
	}
	if err := a.members.RequireDirectMember(ctx, userID, accountID); err != nil {
		return nil, err
	}
	rows, err := a.repo.ListAccountMembers(ctx, accountID)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, m := range rows {
		if len(names) == maxMemberNames {
			break
		}
		if m.AccountID != accountID || m.PK != model.PrefixAccount+accountID || m.SK != model.PrefixMember+m.UserID || m.UserID == "" {
			continue
		}
		profile, err := a.repo.GetUserByID(ctx, m.UserID)
		if err != nil {
			return nil, err
		}
		if profile == nil {
			continue
		}
		if name := strings.TrimSpace(profile.Name); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}
