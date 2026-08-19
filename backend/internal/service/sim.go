package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// simRequirementBound describes the server-side allowlist for one
// SimRequirement key: whether it's numeric (with a sane range) or an enum
// (with a fixed set of allowed values). This -- not the confirm form -- is
// the actual trust boundary between the untrusted meeting transcript and
// the codegen prompt: only keys/values that pass this allowlist ever reach
// ExtractSimRequirements's output or a POST /sim body, so free text from a
// transcript can never shape the generated Python (see ADR-031).
type simRequirementBound struct {
	label   string
	numeric bool
	min     float64
	max     float64
	enum    []string // for non-numeric keys
}

// AllowedSimRequirementKeys is deliberately scoped to 또박's own stack
// (v1 service range decision in the design doc) -- Lambda/API Gateway/
// DynamoDB/S3/CloudFront/Bedrock -- so unit-price lookups stay simple SKU
// filters instead of the fiddly multi-attribute matching EC2/RDS/ALB
// pricing needs. Anything outside this allowlist is rejected outright
// rather than guessed at.
var AllowedSimRequirementKeys = map[string]simRequirementBound{
	"monthlyActiveUsers":    {label: "월간 활성 사용자", numeric: true, min: 0, max: 1e9},
	"peakRequestsPerSecond": {label: "피크 초당 요청 수(TPS)", numeric: true, min: 0, max: 1e7},
	"avgRequestsPerSecond":  {label: "평균 초당 요청 수(TPS)", numeric: true, min: 0, max: 1e7},
	"dataVolumeGbPerDay":    {label: "일간 데이터량(GB)", numeric: true, min: 0, max: 1e7},
	"storageVolumeGb":       {label: "총 저장 데이터량(GB)", numeric: true, min: 0, max: 1e9},
	"avgPayloadSizeKb":      {label: "평균 요청 페이로드(KB)", numeric: true, min: 0, max: 1e6},
	"targetLatencyMs":       {label: "목표 응답 지연(ms)", numeric: true, min: 0, max: 1e6},
	"availabilitySlo":       {label: "가용성 SLO(%)", numeric: true, min: 90, max: 100},
	"retentionDays":         {label: "데이터 보존 기간(일)", numeric: true, min: 0, max: 36500},
	"region": {
		label: "리전", enum: []string{"ap-northeast-2", "us-east-1", "us-west-2", "eu-west-1", "ap-northeast-1"},
	},
}

const (
	simLabelMaxLen  = 80
	simOptionMinLen = 2
	simOptionMaxLen = 3
	// simReportMaxBytes caps the generated report before it's written to
	// DynamoDB -- defense against a runaway generated report ballooning an
	// item past DynamoDB's 400KB limit; the full report always also lands
	// in S3 regardless of this cap.
	simReportMaxBytes = 100_000
)

// SimRun status set that PutSimRunIfNotRunning's condition also encodes --
// kept as a Go-side helper for callers that want to short-circuit before a
// round-trip to DynamoDB (e.g. reporting a friendlier "이미 실행 중" without
// waiting for a ConditionalCheckFailed).
func isSimRunActive(status string) bool {
	return status == model.SimStatusQueued || status == model.SimStatusRunning
}

// labelCharsetRe restricts free-text SimRequirement labels and SimOption
// names to characters that can't smuggle prompt-injection-shaped control
// text past the length cap -- Korean/English/digits/basic punctuation only.
var labelCharsetRe = regexpMustCompileSimLabel()

func regexpMustCompileSimLabel() *simCharsetChecker {
	return &simCharsetChecker{}
}

// simCharsetChecker avoids importing "regexp" for a check simple enough to
// do by hand: reject control characters and the two prompt-injection
// delimiters most likely to appear verbatim in model-visible text ("```",
// "<|") rather than trying to enumerate every allowed Unicode script. Does
// NOT reject a bare "system:" substring -- legitimate text ("distributed
// system: microservices") can contain it, and this is explicitly
// defense-in-depth, not the actual trust boundary (that's the empty Code
// Interpreter execution role + SANDBOX network mode, see ADR-031).
type simCharsetChecker struct{}

func (simCharsetChecker) valid(s string) bool {
	if strings.Contains(s, "```") || strings.Contains(s, "<|") {
		return false
	}
	for _, r := range s {
		if r < 0x20 && r != '\t' {
			return false
		}
	}
	return true
}

// validateSimRequirements is the pure decision core for POST
// /api/meetings/{id}/sim: every requirement key must be in
// AllowedSimRequirementKeys, every numeric value must parse within that
// key's bounds, every enum value must be in that key's allowlist, every
// free-text field is length- and charset-capped, and every Required
// requirement must have a non-empty value. Side-effect-free so it can be
// table-tested without mocking DynamoDB or Bedrock -- this is the security
// boundary (see AllowedSimRequirementKeys' doc comment), so it needs to be
// exhaustively tested, not just exercised end-to-end.
func validateSimRequirements(meeting *model.Meeting, userID string, reqs []model.SimRequirement, opts []model.SimOption, existingStatus string) error {
	if meeting == nil {
		return ErrNotFound
	}
	if meeting.UserID != userID {
		return ErrForbidden
	}
	if meeting.Status != model.StatusDone {
		return fmt.Errorf("meeting must be done before simulating (status: %s): %w", meeting.Status, ErrInvalidInput)
	}
	if isSimRunActive(existingStatus) {
		return fmt.Errorf("a simulation is already running for this meeting: %w", ErrInvalidInput)
	}
	if len(opts) < simOptionMinLen || len(opts) > simOptionMaxLen {
		return fmt.Errorf("must compare between %d and %d architecture options: %w", simOptionMinLen, simOptionMaxLen, ErrInvalidInput)
	}
	for _, o := range opts {
		if strings.TrimSpace(o.Name) == "" {
			return fmt.Errorf("option name is required: %w", ErrInvalidInput)
		}
		if len(o.Name) > simLabelMaxLen || len(o.Description) > simLabelMaxLen*4 {
			return fmt.Errorf("option name/description too long: %w", ErrInvalidInput)
		}
		if !labelCharsetRe.valid(o.Name) || !labelCharsetRe.valid(o.Description) {
			return fmt.Errorf("option name/description contains disallowed characters: %w", ErrInvalidInput)
		}
	}

	if len(reqs) == 0 {
		return fmt.Errorf("at least one requirement is needed: %w", ErrInvalidInput)
	}
	// Bound the array itself: without this, a POST body naming the same key
	// (or an unbounded number of distinct keys) thousands of times still
	// passes every per-item check below and reaches the ttobak-sim invoke
	// payload and codegen prompt as-is. len(AllowedSimRequirementKeys) is
	// the real ceiling on distinct keys; a client can never legitimately
	// need more items than that.
	if len(reqs) > len(AllowedSimRequirementKeys) {
		return fmt.Errorf("too many requirements (max %d): %w", len(AllowedSimRequirementKeys), ErrInvalidInput)
	}
	seenKeys := make(map[string]bool, len(reqs))
	for _, req := range reqs {
		bound, ok := AllowedSimRequirementKeys[req.Key]
		if !ok {
			return fmt.Errorf("unknown requirement key %q: %w", req.Key, ErrInvalidInput)
		}
		if seenKeys[req.Key] {
			return fmt.Errorf("duplicate requirement key %q: %w", req.Key, ErrInvalidInput)
		}
		seenKeys[req.Key] = true
		if len(req.Label) > simLabelMaxLen || !labelCharsetRe.valid(req.Label) {
			return fmt.Errorf("requirement %q label invalid: %w", req.Key, ErrInvalidInput)
		}
		// Unit/Evidence are free text too and reach the ttobak-sim invoke
		// payload unmodified (CreateSimulation marshals reqs as-is) -- the
		// function doc's "every free-text field is length- and
		// charset-capped" claim must actually hold for these two, not just
		// Label/Value.
		if len(req.Unit) > simLabelMaxLen || !labelCharsetRe.valid(req.Unit) {
			return fmt.Errorf("requirement %q unit invalid: %w", req.Key, ErrInvalidInput)
		}
		if len(req.Evidence) > simLabelMaxLen || !labelCharsetRe.valid(req.Evidence) {
			return fmt.Errorf("requirement %q evidence invalid: %w", req.Key, ErrInvalidInput)
		}
		value := strings.TrimSpace(req.Value)
		if req.Required && value == "" {
			return fmt.Errorf("required value missing for %q (%s): %w", req.Key, bound.label, ErrInvalidInput)
		}
		if value == "" {
			continue // optional and unset -- fine
		}
		if bound.numeric {
			n, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return fmt.Errorf("requirement %q must be numeric: %w", req.Key, ErrInvalidInput)
			}
			// ParseFloat accepts "NaN"/"Inf"/"-Inf" as valid floats, and NaN
			// compares false against both bounds below (n < min and n > max
			// are both false), silently bypassing the range check entirely.
			if math.IsNaN(n) || math.IsInf(n, 0) {
				return fmt.Errorf("requirement %q must be a finite number: %w", req.Key, ErrInvalidInput)
			}
			if n < bound.min || n > bound.max {
				return fmt.Errorf("requirement %q out of range [%v, %v]: %w", req.Key, bound.min, bound.max, ErrInvalidInput)
			}
		} else {
			allowed := false
			for _, v := range bound.enum {
				if v == value {
					allowed = true
					break
				}
			}
			if !allowed {
				return fmt.Errorf("requirement %q value %q not in allowed set: %w", req.Key, value, ErrInvalidInput)
			}
		}
	}
	return nil
}

// parseSimRequirements parses ExtractSimRequirements's Haiku JSON output
// into a draft []model.SimRequirement, dropping (never fabricating) any
// entry that fails the same allowlist validateSimRequirements enforces --
// an extraction draft is shown to the user for confirmation, but it must
// never contain a value that couldn't survive the real run-time
// validation, or the confirm form would show something the run endpoint
// would then reject.
func parseSimRequirements(raw string, segments []speakerSegment) []model.SimRequirement {
	raw = stripCodeFences(raw)
	// Haiku occasionally appends a trailing sentence after the array
	// ("이상입니다.") despite the system prompt asking for JSON only.
	// json.Unmarshal rejects trailing non-whitespace, so narrow to the
	// outermost [...] span first rather than failing the whole extraction
	// over one stray sentence.
	if start := strings.Index(raw, "["); start >= 0 {
		if end := strings.LastIndex(raw, "]"); end > start {
			raw = raw[start : end+1]
		}
	}
	type rawReq struct {
		Key      string `json:"key"`
		Value    string `json:"value"`
		Required bool   `json:"required"`
		TSMarker string `json:"tsMarker"`
	}
	var items []rawReq
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		// Must not be silent: an empty draft is indistinguishable from "no
		// quantitative values were mentioned in this meeting" for anyone
		// looking at the SIMRUN row afterward, unless the extraction
		// failure itself is visible somewhere (a log line here, since this
		// is a best-effort draft the user reviews and corrects anyway --
		// returning an error would just surface as the same empty form).
		log.Printf("parseSimRequirements: failed to parse Haiku output as JSON: %v", err)
		return []model.SimRequirement{}
	}

	out := make([]model.SimRequirement, 0, len(items))
	seen := map[string]bool{}
	for _, it := range items {
		bound, ok := AllowedSimRequirementKeys[it.Key]
		if !ok || seen[it.Key] {
			continue
		}
		value := strings.TrimSpace(it.Value)
		if value != "" {
			if bound.numeric {
				n, err := strconv.ParseFloat(value, 64)
				// Same finite+range check as validateSimRequirements -- this
				// function's own contract is "draft never shows an
				// unsubmittable value", so a value the run endpoint would
				// reject (NaN/Inf, or out of [min, max]) must not survive
				// into the draft either.
				if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < bound.min || n > bound.max {
					continue // never pass an unparseable/out-of-range number through to the form
				}
			} else {
				allowed := false
				for _, v := range bound.enum {
					if v == value {
						allowed = true
						break
					}
				}
				if !allowed {
					continue
				}
			}
		}
		seen[it.Key] = true

		evidence := ""
		if it.TSMarker != "" {
			if m := tsMarkerSeconds(it.TSMarker); m >= 0 {
				if seg := nearestSegment(segments, float64(m)); seg != nil && seg.ID != "" {
					evidence = "transcript://" + seg.ID
				}
			}
		}
		out = append(out, model.SimRequirement{
			Key:      it.Key,
			Label:    bound.label,
			Value:    value,
			Required: it.Required,
			Source:   model.SimRequirementSourceExtracted,
			Evidence: evidence,
		})
	}
	return out
}

// tsMarkerSeconds extracts NNN from a "[TS:NNN]" marker, or -1 if malformed.
func tsMarkerSeconds(marker string) int {
	marker = strings.TrimPrefix(marker, "[TS:")
	marker = strings.TrimSuffix(marker, "]")
	n, err := strconv.Atoi(strings.TrimSpace(marker))
	if err != nil {
		return -1
	}
	return n
}

// nearestSegment mirrors resolveTranscriptAnchors' snap-to-nearest logic
// for evidence links found outside the markdown-anchor path.
func nearestSegment(segments []speakerSegment, targetSeconds float64) *speakerSegment {
	if len(segments) == 0 {
		return nil
	}
	bestIdx := -1
	bestDiff := -1.0
	for i, seg := range segments {
		diff := seg.StartTime - targetSeconds
		if diff < 0 {
			diff = -diff
		}
		if bestDiff < 0 || diff < bestDiff {
			bestDiff = diff
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		return nil
	}
	return &segments[bestIdx]
}

// simRepo defines the repository methods SimService needs -- kept minimal
// and interface-based so tests can substitute a fake without a real
// DynamoDB client, matching this codebase's meetingRepo convention.
type simRepo interface {
	GetMeeting(ctx context.Context, userID, meetingID string) (*model.Meeting, error)
	GetSimRun(ctx context.Context, meetingID string) (*model.SimRun, error)
	PutSimRunIfNotRunning(ctx context.Context, run *model.SimRun) error
	UpdateSimRunFieldsIfMatch(ctx context.Context, meetingID, simRunID string, fields map[string]interface{}) error
}

// SimService owns the eligibility/validation core and the async hand-off
// to ttobak-sim (ADR-031). Extraction (which needs the transcript-anchor
// resolver, Go-only per ADR-013) lives on BedrockService; SimService only
// validates the user-confirmed requirements and kicks off the run.
type SimService struct {
	repo            simRepo
	bedrock         *BedrockService
	lambdaClient    *lambda.Client
	simFunctionName string
}

// NewSimService constructs a SimService. lambdaClient/simFunctionName may
// be zero-valued in tests that only exercise Extract/validate paths.
func NewSimService(repo simRepo, bedrock *BedrockService, lambdaClient *lambda.Client, simFunctionName string) *SimService {
	return &SimService{repo: repo, bedrock: bedrock, lambdaClient: lambdaClient, simFunctionName: simFunctionName}
}

// ExtractRequirements produces a draft requirement set for the user to
// confirm/correct, and persists it as the meeting's current SimRun in
// "extracted" state (overwriting any prior draft/result -- re-extracting is
// how a user retries a bad first draft). It must NOT be allowed to overwrite
// a genuinely active (queued/running) run -- that would reset the row to
// "extracted" out from under a live Code Interpreter session, and a
// subsequent POST /sim would then pass PutSimRunIfNotRunning's check and
// start a second, concurrent run for the same meeting. PutSimRunIfNotRunning
// (not the unconditional PutSimRun) is reused here for exactly that guard;
// its condition already encodes "not currently queued/running".
func (s *SimService) ExtractRequirements(ctx context.Context, userID, meetingID string) (*model.SimRun, error) {
	meeting, err := s.repo.GetMeeting(ctx, userID, meetingID)
	if err != nil {
		return nil, fmt.Errorf("failed to get meeting: %w", err)
	}
	if meeting == nil {
		return nil, ErrNotFound
	}
	if meeting.UserID != userID {
		return nil, ErrForbidden
	}
	if meeting.Status != model.StatusDone {
		return nil, fmt.Errorf("meeting must be done before simulating (status: %s): %w", meeting.Status, ErrInvalidInput)
	}

	// Reconcile-and-persist a stuck run to "error" before the write below --
	// otherwise a stale queued/running row (dead worker) would still read as
	// active and PutSimRunIfNotRunning would reject this extraction too,
	// even though the meeting has no live run left to protect.
	if _, err := s.GetSimRun(ctx, meetingID); err != nil {
		return nil, fmt.Errorf("failed to check existing sim run: %w", err)
	}

	reqs, err := s.bedrock.ExtractSimRequirements(ctx, meeting)
	if err != nil {
		return nil, fmt.Errorf("failed to extract simulation requirements: %w", err)
	}
	reqsJSON, err := json.Marshal(reqs)
	if err != nil {
		return nil, fmt.Errorf("failed to encode requirements: %w", err)
	}

	run := &model.SimRun{
		SimRunID:     uuid.New().String(),
		MeetingID:    meetingID,
		UserID:       userID,
		Status:       model.SimStatusExtracted,
		Requirements: string(reqsJSON),
	}
	if err := s.repo.PutSimRunIfNotRunning(ctx, run); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return nil, fmt.Errorf("a simulation is already running for this meeting: %w", ErrInvalidInput)
		}
		return nil, fmt.Errorf("failed to save extracted requirements: %w", err)
	}
	return run, nil
}

// CreateSimulation validates the user-confirmed requirements/options,
// atomically claims the meeting's SimRun slot (rejecting a second
// concurrent run), and hands off to ttobak-sim asynchronously. The Lambda
// invoke is fire-and-forget (InvocationType Event) -- the caller polls
// GetMeeting for status, matching ResearchDetailClient's existing job-poll
// pattern rather than adding a new WebSocket path for a minutes-long job.
func (s *SimService) CreateSimulation(ctx context.Context, userID, meetingID string, reqs []model.SimRequirement, opts []model.SimOption) (*model.SimRun, error) {
	meeting, err := s.repo.GetMeeting(ctx, userID, meetingID)
	if err != nil {
		return nil, fmt.Errorf("failed to get meeting: %w", err)
	}

	// GetSimRun (the service method, not repo.GetSimRun) reconciles-and-
	// persists a stuck run to "error" first -- without this, a stale
	// queued/running row from a dead worker would keep isSimRunActive true
	// forever and PutSimRunIfNotRunning's condition would keep rejecting
	// every retry, permanently blocking this meeting's simulator.
	existing, err := s.GetSimRun(ctx, meetingID)
	if err != nil {
		return nil, fmt.Errorf("failed to get existing sim run: %w", err)
	}
	existingStatus := ""
	if existing != nil {
		existingStatus = existing.Status
	}

	if err := validateSimRequirements(meeting, userID, reqs, opts, existingStatus); err != nil {
		return nil, err
	}

	reqsJSON, err := json.Marshal(reqs)
	if err != nil {
		return nil, fmt.Errorf("failed to encode requirements: %w", err)
	}
	optsJSON, err := json.Marshal(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to encode options: %w", err)
	}

	run := &model.SimRun{
		SimRunID:     uuid.New().String(),
		MeetingID:    meetingID,
		UserID:       userID,
		Status:       model.SimStatusQueued,
		Requirements: string(reqsJSON),
		Options:      string(optsJSON),
	}
	if err := s.repo.PutSimRunIfNotRunning(ctx, run); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return nil, fmt.Errorf("a simulation is already running for this meeting: %w", ErrInvalidInput)
		}
		return nil, fmt.Errorf("failed to create sim run: %w", err)
	}

	payload, err := json.Marshal(map[string]interface{}{
		"simRunId":     run.SimRunID,
		"meetingId":    meetingID,
		"userId":       userID,
		"requirements": reqs,
		"options":      opts,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to encode invoke payload: %w", err)
	}

	if s.lambdaClient != nil && s.simFunctionName != "" {
		if _, err := s.lambdaClient.Invoke(ctx, &lambda.InvokeInput{
			FunctionName:   aws.String(s.simFunctionName),
			InvocationType: lambdatypes.InvocationTypeEvent,
			Payload:        payload,
		}); err != nil {
			// The run is already recorded as "queued". An IAM/config
			// misroute or throttling here is common enough that leaving
			// this for the 20-minute stuck-run reconciliation would lock
			// out every retry for 20 minutes on what's often a
			// deterministic, immediately-retryable failure -- flip it to
			// "error" now instead. Best-effort: if this update itself fails
			// (or loses to a concurrent claim), the 20-minute reconciliation
			// is still the fallback, so a failure here isn't escalated.
			if uerr := s.repo.UpdateSimRunFieldsIfMatch(ctx, meetingID, run.SimRunID, map[string]interface{}{
				"status":       model.SimStatusError,
				"errorMessage": "시뮬레이션 실행을 시작하지 못했습니다. 다시 시도해주세요.",
			}); uerr != nil {
				log.Printf("CreateSimulation: failed to mark run %s as errored after invoke failure: %v", run.SimRunID, uerr)
			}
			return nil, fmt.Errorf("failed to invoke ttobak-sim: %w", err)
		}
	}

	return run, nil
}

// GetSimRun fetches the meeting's current simulation state and, if it finds
// a stuck run, persists the "error" transition before returning -- mirrors
// MeetingService.GetMeetingDetail's isStuck pattern (read-triggered write),
// rather than only reporting staleness in-memory for this one response. This
// is what actually closes the permanent-block failure mode: once persisted,
// isSimRunActive/PutSimRunIfNotRunning's condition see "error", not a stale
// "queued"/"running", so the next extract/run attempt is no longer rejected.
// The persist uses UpdateSimRunFieldsIfMatch (simRunId-conditioned) so a
// concurrent newer run that has already claimed the row is never clobbered
// by this reconciliation of an older one.
func (s *SimService) GetSimRun(ctx context.Context, meetingID string) (*model.SimRun, error) {
	run, err := s.repo.GetSimRun(ctx, meetingID)
	if err != nil {
		return nil, err
	}
	if ReconcileStuckSimRun(run) {
		const stuckMessage = "시뮬레이션이 응답하지 않아 시간 초과로 처리되었습니다"
		if err := s.repo.UpdateSimRunFieldsIfMatch(ctx, meetingID, run.SimRunID, map[string]interface{}{
			"status":       model.SimStatusError,
			"errorMessage": stuckMessage,
		}); err != nil && !errors.Is(err, repository.ErrConditionFailed) {
			// A condition failure just means a newer run already took over
			// this row -- not an error worth surfacing. Anything else is
			// logged by the caller's own error handling; this reconciliation
			// is best-effort, so fall through and still return the in-memory
			// view below rather than failing the whole read.
			log.Printf("GetSimRun: failed to persist stuck-run reconciliation for meeting %s: %v", meetingID, err)
		} else if err == nil {
			run.Status = model.SimStatusError
			run.ErrorMessage = stuckMessage
		}
	}
	return run, nil
}

// simRunStuckThreshold mirrors meeting.go's 30-minute isStuck window, but
// shorter: a sim run has no long-running external process analogous to
// Whisper ECS, so 20 minutes past Lambda's own 15-minute timeout is already
// generous slack for a Lambda that died without writing "error".
const simRunStuckThreshold = 20 * time.Minute

// ReconcileStuckSimRun reports whether run should be treated as errored due
// to age, without mutating it -- callers decide whether/how to persist that.
func ReconcileStuckSimRun(run *model.SimRun) bool {
	if run == nil {
		return false
	}
	if run.Status != model.SimStatusQueued && run.Status != model.SimStatusRunning {
		return false
	}
	return time.Since(run.UpdatedAt) > simRunStuckThreshold
}
