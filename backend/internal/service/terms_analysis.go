package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// Automatic term extraction job (design A2.3). Enqueue only claims a run and
// publishes ttobak.analysis/TermsRequested; Process runs in a fresh summarize
// invocation. Logs carry meeting/run IDs, codes, counts and token counts only,
// never term text, quotes or prompts.
const (
	AutoTermsModeOff      = "off"
	AutoTermsModePersonal = "personal"

	termsLease          = 5 * time.Minute
	termsRunBudget      = 300 * time.Second
	termsDeadlineMargin = 30 * time.Second
	termsCallTimeout    = 90 * time.Second
	termsMinCallBudget  = 15 * time.Second

	// Enqueue outcomes (codes, safe to log).
	TermsEnqueueQueued    = "QUEUED"
	TermsEnqueueDisabled  = model.TermsErrorDisabled
	TermsEnqueueUnchanged = "UNCHANGED"
	TermsEnqueuePending   = "PENDING"
	TermsEnqueueNotReady  = "NOT_READY"
	TermsEnqueueNoSource  = "NO_SOURCE"
	TermsEnqueueFailed    = "ENQUEUE_FAILED"
)

// TermsConfig: AUTO_TERMS_MODE (off|personal), AUTO_TERMS_USERS (canary
// subs; empty means everyone), AUTO_TERMS_MIN_CONFIDENCE, BEDROCK_TERMS_MODEL_ID.
type TermsConfig struct {
	Mode          string
	Users         map[string]bool
	MinConfidence float64
	ModelID       string
}

// TermsConfigFromEnv parses the flags; unknown modes are off.
func TermsConfigFromEnv() TermsConfig {
	cfg := TermsConfig{Mode: AutoTermsModeOff, MinConfidence: DefaultAutoTermsMinConfidence, ModelID: getEnvOrDefault("BEDROCK_TERMS_MODEL_ID", ClaudeSonnetModelID)}
	if strings.TrimSpace(os.Getenv("AUTO_TERMS_MODE")) == AutoTermsModePersonal {
		cfg.Mode = AutoTermsModePersonal
	}
	for _, user := range strings.Split(os.Getenv("AUTO_TERMS_USERS"), ",") {
		if user = strings.TrimSpace(user); user != "" {
			if cfg.Users == nil {
				cfg.Users = map[string]bool{}
			}
			cfg.Users[user] = true
		}
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv("AUTO_TERMS_MIN_CONFIDENCE")), 64); err == nil {
		cfg.MinConfidence = v // FilterCandidates falls back on out-of-range values
	}
	return cfg
}

// Enabled reports whether extraction runs for ownerID: the mode is on and
// the canary list is empty or contains the owner.
func (c TermsConfig) Enabled(ownerID string) bool {
	return c.Mode == AutoTermsModePersonal && ownerID != "" && (len(c.Users) == 0 || c.Users[ownerID])
}

type termsAnalysisRepo interface {
	GetMeeting(context.Context, string, string) (*model.Meeting, error)
	GetTermsAnalysis(context.Context, string) (*model.TermsAnalysis, error)
	QueueTermsAnalysis(context.Context, string, string, *model.TermsAnalysis, *model.TermsAnalysis) error
	StartTermsAnalysis(context.Context, string, string, string, time.Time, int64) error
	RenewTermsAnalysisLease(context.Context, string, string, string, int64, time.Time, int64) error
	ReserveTermsAccepted(context.Context, string, string, string, int64, time.Time, int64, []model.TermsAcceptedTerm) error
	CompleteTermsAnalysis(context.Context, string, string, string, int64, repository.TermsCompletion, time.Time) error
	FailTermsAnalysis(context.Context, string, *model.TermsAnalysis, string, []model.TermsAcceptedTerm, time.Time) error
}

type termsMergeRunner interface {
	MergeExtractedTerms(context.Context, string, []ExtractedTerm, TermsMeetingRef) (*TermsMergeResult, error)
}

// termsLegacyMigrator makes the owner's term rows authoritative before a
// merge, as every manual v2 write does (ensureMigrated). Until migratedAt is
// set, readers union the legacy blob under the rows and migration copies only
// keys without a row, so an automatic row written first would hide or replace
// a blob term now and drop it permanently at migration.
// *DictionaryService implements it.
type termsLegacyMigrator interface {
	MigrateLegacyDictionary(context.Context, string) (LegacyMigrationResult, error)
}

var _ termsLegacyMigrator = (*DictionaryService)(nil)

type termsAttachmentSource interface {
	summaryAttachments(context.Context, string, string) ([]model.Attachment, error)
}

// TermsAnalysisService queues and runs automatic term extraction.
type TermsAnalysisService struct {
	repo        termsAnalysisRepo
	merger      termsMergeRunner
	migrator    termsLegacyMigrator
	model       termsModel
	attachments termsAttachmentSource
	publish     func(context.Context, model.TermsRequested) error
	cfg         TermsConfig
	now         func() time.Time
}

// NewTermsAnalysisService wires the job; attachments may be nil. A nil
// migrator fails every run before any dictionary write.
func NewTermsAnalysisService(repo termsAnalysisRepo, merger termsMergeRunner, migrator termsLegacyMigrator, llm termsModel, attachments termsAttachmentSource, publish func(context.Context, model.TermsRequested) error, cfg TermsConfig) *TermsAnalysisService {
	return &TermsAnalysisService{repo: repo, merger: merger, migrator: migrator, model: llm, attachments: attachments, publish: publish, cfg: cfg, now: time.Now}
}

// TermsPublisher publishes ttobak.analysis/TermsRequested on the default bus.
func TermsPublisher(client actionEventClient) func(context.Context, model.TermsRequested) error {
	return func(ctx context.Context, event model.TermsRequested) error {
		detail, _ := json.Marshal(event)
		out, err := client.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: []ebtypes.PutEventsRequestEntry{{
			EventBusName: aws.String("default"), Source: aws.String("ttobak.analysis"),
			DetailType: aws.String("TermsRequested"), Detail: aws.String(string(detail)),
		}}})
		if err == nil && (out == nil || out.FailedEntryCount != 0 || len(out.Entries) != 1 || aws.ToString(out.Entries[0].EventId) == "") {
			err = errors.New("terms event was not accepted")
		}
		return err
	}
}

// termsSource is the immutable evidence one run reads.
type termsSource struct {
	notes      string
	transcript string
	tainted    bool
	notesSafe  bool
	hash       string
}

func newTermsSource(meeting *model.Meeting) termsSource {
	transcript, variant := selectMeetingTranscript(meeting)
	revision := meeting.TranscriptRevisionA
	if variant == "B" {
		revision = meeting.TranscriptRevisionB
	}
	tainted := meeting.SourceEditedByNonOwnerAt != ""
	identity, _ := json.Marshal([]string{
		actionSourceHash(meeting.Content), variant, revision, actionSourceHash(transcript),
		meeting.AttachmentSummarySources, strconv.FormatBool(tainted),
	})
	return termsSource{
		notes: meeting.Content, transcript: transcript, tainted: tainted,
		notesSafe: meeting.SummaryPrivacyState() == model.SummaryPrivacySafe, hash: actionSourceHash(string(identity)),
	}
}

// Enqueue claims a run for a done meeting and publishes TermsRequested. It
// does no model work and returns an outcome code; callers treat every
// outcome as non-fatal.
func (s *TermsAnalysisService) Enqueue(ctx context.Context, ownerID, meetingID, reason string) string {
	outcome := s.enqueue(ctx, ownerID, meetingID)
	if outcome != TermsEnqueueDisabled {
		log.Printf("terms enqueue: meeting=%s reason=%s outcome=%s", meetingID, reason, outcome)
	}
	return outcome
}

func (s *TermsAnalysisService) enqueue(ctx context.Context, ownerID, meetingID string) string {
	if s == nil || !s.cfg.Enabled(ownerID) {
		return TermsEnqueueDisabled
	}
	meeting, err := s.repo.GetMeeting(ctx, ownerID, meetingID)
	if err != nil {
		return TermsEnqueueFailed
	}
	if meeting == nil || meeting.Status != model.StatusDone {
		return TermsEnqueueNotReady
	}
	source := newTermsSource(meeting)
	if strings.TrimSpace(source.transcript) == "" && strings.TrimSpace(source.notes) == "" {
		return TermsEnqueueNoSource
	}
	prior, err := s.repo.GetTermsAnalysis(ctx, meetingID)
	if err != nil {
		return TermsEnqueueFailed
	}
	now := s.now()
	if prior.Pending() && prior.LeaseUntil > now.UnixMilli() {
		return TermsEnqueuePending
	}
	if prior != nil && prior.SucceededSourceHash == source.hash {
		return TermsEnqueueUnchanged
	}
	next := &model.TermsAnalysis{
		RunID: uuid.NewString(), Status: model.AnalysisQueued, SourceHash: source.hash,
		StartedAt: now, UpdatedAt: now, LeaseUntil: now.Add(termsLease).UnixMilli(),
	}
	if err := s.repo.QueueTermsAnalysis(ctx, ownerID, meetingID, prior, next); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return TermsEnqueueUnchanged // another run, an unchanged source or a deletion won
		}
		return TermsEnqueueFailed
	}
	if s.publish == nil || s.publish(ctx, model.TermsRequested{OwnerID: ownerID, MeetingID: meetingID, RunID: next.RunID}) != nil {
		_ = s.fail(ctx, meetingID, next, model.AnalysisPublishFailed, nil)
		return model.AnalysisPublishFailed
	}
	return TermsEnqueueQueued
}

func (s *TermsAnalysisService) fail(ctx context.Context, meetingID string, state *model.TermsAnalysis, code string, accepted []model.TermsAcceptedTerm) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	merged := append([]model.TermsAcceptedTerm(nil), state.AcceptedTerms...)
	for _, a := range accepted {
		if acceptedIndex(merged, a.TermKey) < 0 && len(merged) < model.MaxTermsAccepted {
			merged = append(merged, a)
		}
	}
	err := s.repo.FailTermsAnalysis(cleanup, meetingID, state, code, merged, s.now())
	log.Printf("terms run: meeting=%s run=%s status=failed code=%s accepted=%d", meetingID, state.RunID, code, len(merged))
	if errors.Is(err, repository.ErrConditionFailed) {
		return nil // a replacement run, deletion, or another terminal write won
	}
	return err
}

// termsBudget is min(300s, deadline-30s); a non-positive result means no
// time for a run.
func termsBudget(untilDeadline time.Duration, hasDeadline bool) time.Duration {
	budget := termsRunBudget
	if hasDeadline && untilDeadline-termsDeadlineMargin < budget {
		budget = untilDeadline - termsDeadlineMargin
	}
	return budget
}

type termsRunStats struct {
	calls, inputTokens, outputTokens int
}

// Process runs one queued extraction with the run-state CAS: Start with a
// five-minute lease, renewal between model calls, and Complete or Fail
// (detached 3 s context) against the observed run and lease.
func (s *TermsAnalysisService) Process(ctx context.Context, event model.TermsRequested) error {
	if event.OwnerID == "" || event.MeetingID == "" || event.RunID == "" {
		return ErrInvalidInput
	}
	state, err := s.repo.GetTermsAnalysis(ctx, event.MeetingID)
	if err != nil {
		return err
	}
	if state == nil || state.RunID != event.RunID || state.Status != model.AnalysisQueued {
		return nil
	}
	if !s.cfg.Enabled(event.OwnerID) {
		return s.fail(ctx, event.MeetingID, state, model.TermsErrorDisabled, nil)
	}
	meeting, err := s.repo.GetMeeting(ctx, event.OwnerID, event.MeetingID)
	if err != nil {
		return err
	}
	var source termsSource
	if meeting != nil {
		source = newTermsSource(meeting)
	}
	if meeting == nil || meeting.Status != model.StatusDone || source.hash != state.SourceHash {
		return s.failSourceChanged(ctx, event, state)
	}
	now := s.now()
	if state.LeaseUntil <= now.UnixMilli() {
		return s.fail(ctx, event.MeetingID, state, model.AnalysisInterrupted, nil)
	}
	deadline, hasDeadline := ctx.Deadline()
	budget := termsBudget(time.Until(deadline), hasDeadline)
	if budget < termsMinCallBudget {
		return s.fail(ctx, event.MeetingID, state, model.AnalysisInterrupted, nil)
	}
	lease := now.Add(termsLease).UnixMilli()
	if err := s.repo.StartTermsAnalysis(ctx, event.OwnerID, event.MeetingID, event.RunID, now, lease); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return nil
		}
		return err
	}
	running := *state
	running.Status, running.LeaseUntil = model.AnalysisRunning, lease
	runCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	documents := s.ownerDocuments(runCtx, event.OwnerID, event.MeetingID)
	candidates, stats, code := s.extract(runCtx, &running, event, source, documents)
	if code != "" {
		return s.fail(ctx, event.MeetingID, &running, code, nil)
	}
	fresh, err := s.repo.GetMeeting(runCtx, event.OwnerID, event.MeetingID)
	if err != nil || fresh == nil || fresh.Status != model.StatusDone || newTermsSource(fresh).hash != state.SourceHash {
		return s.failSourceChanged(ctx, event, &running)
	}
	sources := TermSources{Transcript: source.transcript, Notes: source.notes}
	for _, doc := range documents {
		sources.Documents = append(sources.Documents, TermSourceDocumentText{ID: doc.ID, Text: doc.Text})
	}
	filtered, filterCounts := FilterCandidates(candidates, sources, s.cfg.MinConfidence)
	// Reserve every key this run may observe before writing, so deletion
	// cleanup finds the observations even if the meeting is deleted mid-run.
	// A key that does not fit in acceptedTerms is not merged.
	planned := append([]model.TermsAcceptedTerm(nil), state.AcceptedTerms...)
	terms := make([]ExtractedTerm, 0, len(filtered))
	for _, f := range filtered {
		if acceptedIndex(planned, f.TermKey) < 0 {
			if len(planned) >= model.MaxTermsAccepted {
				continue
			}
			planned = append(planned, model.TermsAcceptedTerm{TermKey: f.TermKey, SourceTypes: f.SourceTypes})
		}
		terms = append(terms, ExtractedTerm{
			DisplayAs: f.DisplayAs, Phrase: f.Phrase, Type: f.Type, HeardAs: f.HeardAs, SourceTypes: f.SourceTypes,
			Confidence: f.Confidence, TranscribeKo: f.TranscribeKo, TranscribeEn: f.TranscribeEn,
		})
	}
	// Migrate the owner's legacy blob first (a no-op once migratedAt is set),
	// before reserving keys, so no automatic row can claim a blob term's key.
	if err := s.migrateLegacy(runCtx, event.OwnerID); err != nil {
		code := model.AnalysisPersistenceFail
		if errors.Is(err, ErrLegacyDictionaryConflict) {
			// Permanent until the owner resolves it; do not ask for redelivery.
			return s.fail(ctx, event.MeetingID, &running, code, nil)
		}
		return errors.Join(err, s.fail(ctx, event.MeetingID, &running, code, nil))
	}
	reserveAt := s.now()
	reserveLease := reserveAt.Add(termsLease).UnixMilli()
	if err := s.repo.ReserveTermsAccepted(runCtx, event.OwnerID, event.MeetingID, event.RunID, running.LeaseUntil, reserveAt, reserveLease, planned); err != nil {
		return s.fail(ctx, event.MeetingID, &running, leaseErrorCode(err), nil)
	}
	running.LeaseUntil, running.AcceptedTerms = reserveLease, planned
	merged, err := s.merger.MergeExtractedTerms(runCtx, model.PrefixUser+event.OwnerID, terms, TermsMeetingRef{
		OwnerID: event.OwnerID, MeetingID: event.MeetingID, NotesSafe: source.notesSafe,
		Tainted: source.tainted, Accepted: state.AcceptedTerms,
	})
	var accepted []model.TermsAcceptedTerm
	if merged != nil {
		accepted = merged.Accepted
		if merged.CleanupErr != nil {
			log.Printf("terms run: meeting=%s run=%s cleanup=failed", event.MeetingID, event.RunID)
		}
	}
	if err != nil {
		return errors.Join(err, s.fail(ctx, event.MeetingID, &running, model.AnalysisPersistenceFail, accepted))
	}
	completion := repository.TermsCompletion{Counters: merged.Counters, AcceptedTerms: merged.Accepted}
	if source.tainted {
		completion.ErrorCode = model.TermsErrorTaintedSuggestOnly
	}
	err = s.repo.CompleteTermsAnalysis(ctx, event.OwnerID, event.MeetingID, event.RunID, running.LeaseUntil, completion, s.now())
	if errors.Is(err, repository.ErrConditionFailed) {
		return s.fail(ctx, event.MeetingID, &running, model.AnalysisInterrupted, accepted)
	}
	if err != nil {
		return errors.Join(err, s.fail(ctx, event.MeetingID, &running, model.AnalysisPersistenceFail, accepted))
	}
	counts, _ := json.Marshal(filterCounts)
	c := merged.Counters
	log.Printf("terms run: meeting=%s run=%s status=succeeded code=%s calls=%d inputTokens=%d outputTokens=%d filter=%s created=%d suggested=%d merged=%d promoted=%d skipped=%d",
		event.MeetingID, event.RunID, completion.ErrorCode, stats.calls, stats.inputTokens, stats.outputTokens, counts,
		c.Created, c.Suggested, c.Merged, c.Promoted, c.SkippedTombstone+c.SkippedManual+c.SkippedTainted)
	// An Enqueue during this run returned PENDING and queued nothing; a source
	// that changed after the freshness check gets its own run now.
	s.Enqueue(ctx, event.OwnerID, event.MeetingID, "post-run")
	return nil
}

// migrateLegacy runs the legacy dictionary migration for ownerID; a missing
// migrator is a wiring error, never a reason to merge unmigrated.
func (s *TermsAnalysisService) migrateLegacy(ctx context.Context, ownerID string) error {
	if s.migrator == nil {
		return errors.New("terms run: legacy dictionary migrator is not configured")
	}
	if _, err := s.migrator.MigrateLegacyDictionary(ctx, ownerID); err != nil {
		log.Printf("terms run: legacy dictionary migration failed: conflict=%t", errors.Is(err, ErrLegacyDictionaryConflict))
		return fmt.Errorf("failed to migrate legacy dictionary: %w", err)
	}
	return nil
}

// failSourceChanged ends a run whose source moved on and queues the current
// source: an Enqueue that saw this run pending recorded nothing.
func (s *TermsAnalysisService) failSourceChanged(ctx context.Context, event model.TermsRequested, state *model.TermsAnalysis) error {
	if err := s.fail(ctx, event.MeetingID, state, model.AnalysisSourceChanged, nil); err != nil {
		return err
	}
	s.Enqueue(context.WithoutCancel(ctx), event.OwnerID, event.MeetingID, "source-changed")
	return nil
}

// renew extends the lease; a lost lease ends the run as interrupted.
func (s *TermsAnalysisService) renew(ctx context.Context, running *model.TermsAnalysis, event model.TermsRequested) string {
	now := s.now()
	lease := now.Add(termsLease).UnixMilli()
	if err := s.repo.RenewTermsAnalysisLease(ctx, event.OwnerID, event.MeetingID, event.RunID, running.LeaseUntil, now, lease); err != nil {
		return leaseErrorCode(err)
	}
	running.LeaseUntil = lease
	return ""
}

func leaseErrorCode(err error) string {
	if errors.Is(err, repository.ErrConditionFailed) {
		return model.AnalysisInterrupted
	}
	return model.AnalysisPersistenceFail
}

// extract runs up to three model calls: call 1 sends notes, owner documents
// and the first transcript chunk; calls 2-3 send later (sampled) chunks with
// the candidate names so far. A returned code fails the run.
func (s *TermsAnalysisService) extract(ctx context.Context, running *model.TermsAnalysis, event model.TermsRequested, source termsSource, documents []termsDocumentText) ([]TermCandidate, termsRunStats, string) {
	var stats termsRunStats
	chunks := sampleChunks(chunkTranscript(source.transcript, termsChunkMaxBytes), termsMaxCalls)
	if len(chunks) == 0 {
		chunks = []string{""}
	}
	var candidates []TermCandidate
	for i, chunk := range chunks {
		input := termsCallInput{Task: "extract_terms", Call: i + 1, Calls: len(chunks), TranscriptChunk: chunk}
		if i == 0 {
			input.Notes, input.Documents = truncateUTF8(source.notes, termsNotesMaxBytes), documents
		} else {
			deadline, _ := ctx.Deadline()
			if time.Until(deadline) < termsMinCallBudget {
				break // keep the candidates found so far
			}
			if code := s.renew(ctx, running, event); code != "" {
				return nil, stats, code
			}
			input.KnownCandidates = knownCandidateNames(candidates)
		}
		request, err := buildTermsRequest(input)
		if err != nil {
			return nil, stats, model.AnalysisInvalidOutput
		}
		callCtx, cancel := context.WithTimeout(ctx, termsCallTimeout)
		text, in, out, err := s.model.InvokeTermsModel(callCtx, request, s.cfg.ModelID)
		cancel()
		stats.calls++
		stats.inputTokens += in
		stats.outputTokens += out
		if err != nil {
			if errors.Is(err, ErrInvalidAnalysisResponse) {
				return nil, stats, model.AnalysisInvalidOutput
			}
			if ctx.Err() != nil {
				return nil, stats, model.AnalysisInterrupted
			}
			return nil, stats, model.AnalysisProviderFailed
		}
		found, err := decodeTermsOutput(text)
		if err != nil {
			return nil, stats, model.AnalysisInvalidOutput
		}
		candidates = append(candidates, found...)
	}
	return candidates, stats, ""
}

// ownerDocuments returns verified document text the owner uploaded: current
// succeeded/partial results bound to the source ETag (summaryAttachments),
// at most 16 KiB each and 64 KiB in total. Failures yield no documents.
func (s *TermsAnalysisService) ownerDocuments(ctx context.Context, ownerID, meetingID string) []termsDocumentText {
	if s.attachments == nil {
		return nil
	}
	attachments, err := s.attachments.summaryAttachments(ctx, ownerID, meetingID)
	if err != nil {
		return nil
	}
	var docs []termsDocumentText
	total := 0
	for _, att := range attachments {
		state := att.SummaryTextState
		if att.Type != model.AttachTypeDocument || att.ExtractedText == nil || att.SummaryOmitted || state == nil ||
			state.UploaderID != ownerID || (att.UserID != "" && att.UserID != ownerID) ||
			(state.Status != model.AttachmentTextSucceeded && state.Status != model.AttachmentTextPartial) {
			continue
		}
		var text strings.Builder
		for _, unit := range att.ExtractedText.Units {
			text.WriteString(unit.Text)
			text.WriteString("\n")
		}
		body := truncateUTF8(text.String(), min(termsDocumentMaxBytes, termsDocumentsMaxBytes-total))
		if strings.TrimSpace(body) == "" {
			continue
		}
		total += len(body)
		docs = append(docs, termsDocumentText{ID: att.AttachmentID, Text: body})
		if total >= termsDocumentsMaxBytes {
			break
		}
	}
	return docs
}
