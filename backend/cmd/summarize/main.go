package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagent"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/transcribe"
	"github.com/ttobak/backend/internal/duration"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/sealing"
	"github.com/ttobak/backend/internal/service"
)

var (
	bedrockService     *service.BedrockService
	actionItemsService *service.ActionItemsAnalysisService
	resummaryService   *service.ResummaryService
	kbExportService    *service.KBExportService
	repo               *repository.DynamoDBRepository
	s3Client           *s3.Client
	ebClient           *eventbridge.Client
	bucketName         string

	// Automatic term extraction (design A2.3); package variables so the
	// dispatch branch and triggers are testable without AWS.
	processTermsRequested = func(context.Context, model.TermsRequested) error { return nil }
	enqueueTerms          = func(context.Context, string, string, string) {}

	// Automatic news→meeting links (design G8) for a meeting published to
	// its account; best-effort, bounded by autoNewsLinkTimeout.
	linkAccountNews = func(context.Context, *model.Meeting) {}

	// Deterministic people resolution for a meeting published to its account
	// (inline after the summary, bounded by peopleResolveTimeout), and the
	// PeopleResolveRequested worker for speaker map and contact changes.
	resolveMeetingPeople          = func(context.Context, *model.Meeting) {}
	processPeopleResolveRequested = func(context.Context, model.PeopleResolveRequested) error { return nil }

	// AccountMergeRequested worker: one bounded run of the source's merge job.
	processAccountMergeRequested = func(context.Context, model.AccountMergeRequested) error { return nil }

	// KB/research term sources: Object Created on the KB bucket is mapped to
	// TermsSourceRequested, which runs in its own invocation.
	termsSourceBucket  string
	requestTermsSource = func(context.Context, string, string) error { return nil }
	processTermsSource = func(context.Context, model.TermsSourceRequested) error { return nil }

	// Daily news terms into Common: the schedule's {"kind":"news-daily"}
	// input runs the day ending at the last 21:00 UTC cutoff.
	processNewsDaily = func(context.Context, string) error { return nil }
	newsNow          = time.Now
)

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("failed to load AWS config: %v", err)
	}

	// Bedrock may require a specific region
	bedrockRegion := os.Getenv("BEDROCK_REGION")
	if bedrockRegion == "" {
		bedrockRegion = "us-west-2"
	}

	bedrockCfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion(bedrockRegion))
	if err != nil {
		log.Fatalf("failed to load Bedrock config: %v", err)
	}

	dynamoClient := dynamodb.NewFromConfig(cfg)
	s3Client = s3.NewFromConfig(cfg)
	bedrockClient := bedrockruntime.NewFromConfig(bedrockCfg)
	ebClient = eventbridge.NewFromConfig(cfg)

	tableName := os.Getenv("TABLE_NAME")
	if tableName == "" {
		tableName = "ttobak-main"
	}
	bucketName = os.Getenv("BUCKET_NAME")
	if bucketName == "" {
		bucketName = "ttobak-assets"
	}

	repo = repository.NewDynamoDBRepositoryWithS3(dynamoClient, tableName, s3Client, bucketName)
	bedrockService = service.NewBedrockService(bedrockClient, s3Client, repo)
	metadata := repo.MetadataView()
	attachmentTextService := service.NewAttachmentTextService(metadata, service.NewMeetingService(metadata), s3Client, bucketName, nil)
	bedrockService.SetAttachmentTextService(attachmentTextService)
	resummaryService = service.NewResummaryService(metadata, service.NewMeetingService(metadata), bedrockService, attachmentTextService, nil)
	actionItemsService = service.NewMetadataActionItemsAnalysisService(repo, bedrockService, nil)
	dictionaryRepo := repository.NewDictionaryRepository(dynamoClient, tableName)
	// The vocabulary state row is version-guarded, so this builder and the API
	// Lambda's can both advance it; running it here gets new auto terms into
	// batch Transcribe without a dictionary page visit.
	dictService := service.NewDictionaryService(dictionaryRepo, transcribe.NewFromConfig(cfg))
	dictService.SetVocabularyBuilder(s3Client, bucketName, service.ParseVocabRebuildMinInterval(os.Getenv("VOCAB_REBUILD_MIN_INTERVAL")))
	// The vocabulary plan re-checks each Account's membership (consistent
	// read) before adding its terms, in meeting-recency order.
	dictService.SetMembershipReader(func(ctx context.Context, accountID, userID string) (bool, error) {
		member, err := repo.GetMember(ctx, accountID, userID)
		return member != nil, err
	})
	dictService.SetMembershipBatchReader(service.BatchMembershipReader(repo))
	dictService.SetAccountOrder(service.AccountRecencyOrder(repo))
	// Silent transcript correction (TRANSCRIPT_CORRECTION_MODE): the meeting
	// glossary for refinement, and heardAs feedback from repeated fixes.
	dictService.SetCorrectionStore(dictionaryRepo)
	loadMeetingGlossary = dictService.GlossaryForMeeting
	learnFromCorrections = func(ctx context.Context, meeting *model.Meeting, glossary []service.GlossaryTerm, corrections []model.TranscriptCorrection) {
		dictService.LearnFromCorrections(ctx, meeting.UserID, meeting.AccountID, meeting.MeetingID, glossary, corrections)
	}
	termsService := service.NewTermsAnalysisService(repo, service.NewTermsMerger(dictionaryRepo),
		dictService, bedrockService, attachmentTextService, service.TermsPublisher(ebClient), service.TermsConfigFromEnv())
	termsService.SetVocabularyRefresher(func(ctx context.Context, ownerID string) {
		if _, warnings := dictService.EnsureVocabularies(ctx, ownerID, model.VocabContextPersonal, false); len(warnings) > 0 {
			log.Printf("terms vocabulary refresh: owner=%s warnings=%d", ownerID, len(warnings))
		}
	})
	termsService.SetAccountMemberLister(func(ctx context.Context, accountID string) ([]string, error) {
		members, err := repo.ListAccountMembers(ctx, accountID)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(members))
		for _, m := range members {
			ids = append(ids, m.UserID)
		}
		return ids, nil
	})
	processTermsRequested = termsService.Process
	enqueueTerms = func(ctx context.Context, ownerID, meetingID, reason string) {
		termsService.Enqueue(ctx, ownerID, meetingID, reason) // non-fatal; logs its outcome code
	}
	resummaryService.SetTermsEnqueuer(func(ctx context.Context, ownerID, meetingID string) {
		enqueueTerms(ctx, ownerID, meetingID, "resummary")
	})
	kbBucketName := os.Getenv("KB_BUCKET_NAME")
	termsService.SetSourceReader(service.TermsSourceDeps{
		Store: repo, Objects: s3Client, Bucket: kbBucketName, Publish: service.TermsSourcePublisher(ebClient),
	})
	termsSourceBucket = kbBucketName
	requestTermsSource = termsService.RequestSourceFromObject
	processTermsSource = termsService.ProcessSource
	// News documents are read through GSI4 by crawl time; the candidate
	// history and run rows live under DICT#COMMON. A news run does not
	// refresh per-user vocabularies (it would fan out to every user); they
	// take Common terms at their next scheduled ensure.
	termsService.SetNewsReader(service.TermsNewsDeps{Documents: repository.NewCrawlerRepository(dynamoClient, tableName), Store: repo})
	processNewsDaily = termsService.ProcessNewsDaily

	// Automatic news links read the account's current news rows and write
	// link rows in the same table (DynamoDB grant on the summarize role).
	crawlerRepo := repository.NewCrawlerRepository(dynamoClient, tableName)
	accountNewsService := service.NewAccountNewsService(repo, crawlerRepo,
		service.NewInsightsService(crawlerRepo, s3Client, kbBucketName, bucketName))
	autoNewsLinker := service.NewAutoNewsLinker(accountNewsService, bedrockService,
		service.NewAccountLinksService(repo, crawlerRepo, accountNewsService))
	linkAccountNews = func(ctx context.Context, meeting *model.Meeting) {
		ctx, cancel := context.WithTimeout(ctx, autoNewsLinkTimeout)
		defer cancel()
		result, err := autoNewsLinker.LinkMeeting(ctx, meeting)
		// Meeting ID, counts and a code only: never quotes, titles or content.
		if err != nil {
			log.Printf("account news links failed (non-fatal): meeting=%s code=%s %s", meeting.MeetingID, service.AutoNewsLinkErrorCode(err), result)
		} else if result.Candidates > 0 {
			log.Printf("account news links: meeting=%s %s", meeting.MeetingID, result)
		}
	}

	// The people resolver reads meeting metadata and opens sealed contacts,
	// and the account merge worker re-seals moved contacts and events, so
	// only this repository view carries the record sealer.
	peopleRepo := repo.MetadataView()
	if sealer := sealing.NewEnvelope(kms.NewFromConfig(cfg), os.Getenv("KMS_KEY_ID")); sealer != nil {
		peopleRepo.SetRecordSealer(sealer)
	}
	peopleService := service.NewAccountPeopleService(peopleRepo)
	resolveMeetingPeople = func(ctx context.Context, meeting *model.Meeting) {
		ctx, cancel := context.WithTimeout(ctx, peopleResolveTimeout)
		defer cancel()
		result, err := peopleService.ResolveMeeting(ctx, meeting.UserID, meeting.MeetingID, service.PeopleRunOptions{})
		// Meeting ID, counts and a code only: never names, mentions or contacts.
		if err != nil {
			log.Printf("account people resolve failed (non-fatal): meeting=%s code=%s %s", meeting.MeetingID, service.PeopleRunErrorCode(err), result)
		} else {
			log.Printf("account people resolve: meeting=%s %s", meeting.MeetingID, result)
		}
	}
	processPeopleResolveRequested = func(ctx context.Context, event model.PeopleResolveRequested) error {
		result, err := peopleService.ProcessPeopleResolveRequested(ctx, event)
		if err != nil {
			log.Printf("people resolve request failed: meeting=%s code=%s %s", event.MeetingID, service.PeopleRunErrorCode(err), result)
			return err
		}
		log.Printf("people resolve request: meeting=%s reason=%s %s", event.MeetingID, event.Reason, result)
		return nil
	}

	// KB export service — gracefully skips if not configured
	// The merge worker re-seals moved contacts and events with the target's
	// binding, so it uses the sealing repository view.
	mergeService := service.NewAccountMergeService(peopleRepo, nil)
	mergeService.SetJobRepo(peopleRepo)
	mergeService.SetMoverRepo(peopleRepo)
	mergeService.SetWorkerRepo(peopleRepo)
	mergeService.SetMergePublisher(service.AccountMergePublisher(ebClient))
	mergeService.SetPeopleResolvePublisher(service.PeopleResolvePublisher(ebClient))
	processAccountMergeRequested = func(ctx context.Context, event model.AccountMergeRequested) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			deadline = time.Now().Add(15 * time.Minute)
		}
		return mergeService.ProcessAccountMergeRequested(ctx, event, deadline)
	}

	kbID := os.Getenv("KB_ID")
	dataSourceID := os.Getenv("DATA_SOURCE_ID")
	var bedrockAgentClient *bedrockagent.Client
	if kbBucketName != "" {
		bedrockAgentClient = bedrockagent.NewFromConfig(cfg)
	}
	kbExportService = service.NewKBExportService(s3Client, bedrockAgentClient, kbBucketName, kbID, dataSourceID)
}

// TranscribeResult represents the AWS Transcribe output JSON structure
type TranscribeResult struct {
	Results struct {
		Transcripts []struct {
			Transcript string `json:"transcript"`
		} `json:"transcripts"`
		SpeakerLabels *SpeakerLabels   `json:"speaker_labels,omitempty"`
		Items         []TranscribeItem `json:"items,omitempty"`
	} `json:"results"`
	Status          string           `json:"status"`
	WhisperMetadata *WhisperMetadata `json:"whisper_metadata,omitempty"`
}

// WhisperMetadata represents the Whisper-specific metadata in transcript JSON
type WhisperMetadata struct {
	Engine          string                   `json:"engine"`
	Language        string                   `json:"language"`
	DurationSeconds float64                  `json:"duration_seconds"`
	Segments        []service.WhisperSegment `json:"segments"`
	Diarization     *WhisperDiarization      `json:"diarization,omitempty"`
}

type WhisperDiarization struct {
	Enabled          bool `json:"enabled"`
	DetectedSpeakers int  `json:"num_speakers_detected"`
}

// SpeakerLabels represents the speaker diarization results
type SpeakerLabels struct {
	Speakers int              `json:"speakers"`
	Segments []SpeakerSegment `json:"segments"`
}

// SpeakerSegment represents a contiguous speech segment by one speaker
type SpeakerSegment struct {
	StartTime    string        `json:"start_time"`
	EndTime      string        `json:"end_time"`
	SpeakerLabel string        `json:"speaker_label"`
	Items        []SpeakerItem `json:"items"`
}

// SpeakerItem represents a word within a speaker segment
type SpeakerItem struct {
	StartTime    string `json:"start_time"`
	EndTime      string `json:"end_time"`
	SpeakerLabel string `json:"speaker_label"`
}

// TranscribeItem represents a word/punctuation in the transcribe output
type TranscribeItem struct {
	StartTime    string `json:"start_time,omitempty"`
	EndTime      string `json:"end_time,omitempty"`
	Type         string `json:"type"` // "pronunciation" or "punctuation"
	Alternatives []struct {
		Confidence string `json:"confidence"`
		Content    string `json:"content"`
	} `json:"alternatives"`
}

// TranscriptSegmentOut represents a speaker-labeled transcript segment for the API response.
// `ID` is assigned at save time (see `assignSegmentIDs`) so frontend deep links
// from the AI summary (ADR-013 `transcript://{id}`) can resolve to a stable
// scroll target.
type TranscriptSegmentOut struct {
	ID        string  `json:"id,omitempty"`
	Speaker   string  `json:"speaker"`
	Text      string  `json:"text"`
	StartTime float64 `json:"startTime"`
	EndTime   float64 `json:"endTime"`
	Untimed   bool    `json:"untimed,omitempty"`
}

// assignSegmentIDs gives every segment a deterministic, stable identifier the
// frontend uses as the scroll-target DOM id (`ts-{ID}`). Per ADR-013 we use
// the rounded-millisecond start time as the ID — unique within a meeting
// because transcript segments do not overlap, and stable across re-renders
// since the value derives entirely from the segment data itself.
func assignSegmentIDs(segs []TranscriptSegmentOut) {
	for i := range segs {
		if segs[i].ID != "" {
			continue
		}
		if segs[i].Untimed {
			segs[i].ID = fmt.Sprintf("untimed-%d", i)
		} else {
			segs[i].ID = fmt.Sprintf("seg-%d", int64(segs[i].StartTime*1000))
		}
	}
}

// processSummaryRequested is a variable so dispatch tests avoid AWS clients.
var processSummaryRequested = func(ctx context.Context, event model.SummaryRequested) error {
	return resummaryService.Process(ctx, event)
}

// Handler processes EventBridge events: S3 transcript uploads and custom AllPartsTranscribed events
func Handler(ctx context.Context, raw json.RawMessage) error {
	// Two-phase dispatch: detect event type first, then route
	var envelope model.EventEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("failed to unmarshal event envelope: %w", err)
	}

	// The daily news schedule sends a bare {"kind":"news-daily"} object.
	if envelope.Source == "" && envelope.DetailType == "" {
		var scheduled struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(raw, &scheduled) == nil && scheduled.Kind == "news-daily" {
			return processNewsDaily(ctx, service.NewsDayFor(newsNow()))
		}
	}

	if envelope.Source == "ttobak.analysis" && envelope.DetailType == "SummaryRequested" {
		var event struct {
			Detail model.SummaryRequested `json:"detail"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			return fmt.Errorf("decode summary request")
		}
		// Detail.Target selects the Korean ("") or English ("en") run; an
		// unknown target is rejected as invalid input by the service.
		return processSummaryRequested(ctx, event.Detail)
	}

	if envelope.Source == "ttobak.analysis" && envelope.DetailType == "ActionItemsRequested" {
		var event struct {
			Detail model.ActionItemsRequested `json:"detail"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			return fmt.Errorf("decode action items event: %w", err)
		}
		return actionItemsService.Process(ctx, event.Detail)
	}

	// Automatic term extraction runs in its own invocation, never on the S3 path.
	if envelope.Source == "ttobak.analysis" && envelope.DetailType == "TermsRequested" {
		var event struct {
			Detail model.TermsRequested `json:"detail"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			return fmt.Errorf("decode terms event: %w", err)
		}
		return processTermsRequested(ctx, event.Detail)
	}

	// People resolution for speaker map and contact changes (spec E3).
	if envelope.Source == "ttobak.analysis" && envelope.DetailType == "PeopleResolveRequested" {
		var event struct {
			Detail model.PeopleResolveRequested `json:"detail"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			return fmt.Errorf("decode people resolve event")
		}
		return processPeopleResolveRequested(ctx, event.Detail)
	}

	// Account merge jobs run in their own invocations; each run continues
	// from the job's saved step.
	if envelope.Source == "ttobak.analysis" && envelope.DetailType == "AccountMergeRequested" {
		var event struct {
			Detail model.AccountMergeRequested `json:"detail"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			return fmt.Errorf("decode account merge event")
		}
		return processAccountMergeRequested(ctx, event.Detail)
	}

	// KB/research term extraction likewise runs in its own invocation.
	if envelope.Source == "ttobak.analysis" && envelope.DetailType == "TermsSourceRequested" {
		var event struct {
			Detail model.TermsSourceRequested `json:"detail"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			return fmt.Errorf("decode terms source event: %w", err)
		}
		return processTermsSource(ctx, event.Detail)
	}

	// Custom event: all parts of a multi-file meeting have been transcribed
	if envelope.Source == "ttobak.transcribe" && envelope.DetailType == "AllPartsTranscribed" {
		var event model.AllPartsTranscribedEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return fmt.Errorf("failed to unmarshal AllPartsTranscribed event: %w", err)
		}
		return handleAllPartsTranscribed(ctx, &event.Detail)
	}

	// S3 event: transcript file uploaded
	var event model.EventBridgeS3Event
	if err := json.Unmarshal(raw, &event); err != nil {
		return fmt.Errorf("failed to unmarshal EventBridge event: %w", err)
	}

	bucket := event.Detail.Bucket.Name
	key := event.Detail.Object.Key

	// KB bucket objects (kb/, shared/research/) only map to a term source
	// request; they never reach the transcript path. The key goes as
	// delivered: the mapper tries it first and the unescaped form only when
	// the delivered one is missing, so raw and encoded keys both resolve.
	if termsSourceBucket != "" && bucket == termsSourceBucket {
		return requestTermsSource(ctx, bucket, key)
	}

	if decoded, err := url.QueryUnescape(key); err == nil {
		key = decoded
	}

	log.Printf("Processing transcript: bucket=%s, key=%s", bucket, key)

	if !strings.HasPrefix(key, "transcripts/") {
		log.Printf("Skipping non-transcript file: %s", key)
		return nil
	}

	// Part transcript: just increment counter (no summary on individual parts)
	if strings.Contains(key, "_part_") {
		return handlePartTranscript(ctx, bucket, key)
	}

	// Single/legacy transcript: full processing + summary
	return handleSingleTranscript(ctx, bucket, key, event.Detail.Object.ETag)
}

// handleSingleTranscript processes a single (non-part) transcript: parse, refine, save, summarize
func handleSingleTranscript(ctx context.Context, bucket, key string, eventETags ...string) error {
	meetingID := extractMeetingIDFromTranscriptKey(key)
	if meetingID == "" {
		log.Printf("Could not extract meeting ID from key: %s", key)
		return nil
	}

	// Status guard: skip if already processed (prevents re-trigger after merged transcript write)
	meeting, err := repo.MetadataView().GetMeetingByID(ctx, meetingID)
	if err == nil && meeting != nil {
		meeting, err = repo.MetadataView().GetMeeting(ctx, meeting.UserID, meetingID)
		if err != nil || meeting == nil {
			return err
		}
		if handled, retryErr := resumeSummaryRetry(ctx, meeting, repo.MetadataView(), generateResumedSummary); handled {
			return retryErr
		}
		// Whitelist guard — process when status is `transcribing`, or when
		// it's `summarizing` but eligible for retry (a previous attempt
		// refined/saved the transcript and then died mid-summarize, e.g. a
		// Lambda timeout, without ever reaching `done`/`error`). Without
		// this carve-out, a dead attempt sits here until *something* redelivers
		// or manually re-triggers the transcript event, since nothing here
		// generates a new delivery on its own -- see IsSummarizeRetryEligible.
		// A `summarizing` meeting that is NOT retry-eligible is still being
		// actively processed by another invocation and must not be raced.
		if meeting.Status != model.StatusTranscribing {
			if meeting.Status != model.StatusSummarizing || !service.IsSummarizeRetryEligible(meeting.UpdatedAt) {
				log.Printf("Skipping transcript for meeting %s (status=%s, expected=transcribing)", meetingID, meeting.Status)
				return nil
			}
			// Atomic claim: GetMeetingByID above and this redelivery are not
			// the only possible concurrent reader of this stale meeting --
			// a second redelivery (or a manual re-trigger racing a genuine
			// one) could pass the same eligibility check off the same read.
			// Only the winner of this conditional write actually reprocesses;
			// everyone else skips instead of double-running Bedrock summarize
			// + KB export against the same meeting.
			claimed, claimErr := repo.ClaimSummarizeRetry(ctx, meeting.UserID, meetingID)
			if claimErr != nil {
				// The claim is a conditional write, so returning the error
				// (triggering a Lambda retry) can't cause a duplicate
				// reprocess -- unlike swallowing it here, which would burn
				// this rare stale-recovery redelivery on what's likely a
				// transient DynamoDB error.
				log.Printf("Failed to claim summarize retry for meeting %s: %v", meetingID, claimErr)
				return fmt.Errorf("claim summarize retry failed (retrying): %w", claimErr)
			}
			if !claimed {
				log.Printf("Skipping transcript for meeting %s (summarize retry already claimed)", meetingID)
				return nil
			}
			log.Printf("Reprocessing stuck meeting %s (status=summarizing since %s)", meetingID, meeting.UpdatedAt)
		}
	}

	if err != nil || meeting == nil {
		return errors.New("meeting metadata unavailable before transcription")
	}
	source := "A"
	if strings.Contains(key, "-nova.json") {
		source = "B"
	}
	snapshot, err := repo.CaptureTranscriptUpdate(ctx, meeting.UserID, meetingID, source)
	if err != nil || snapshot == nil {
		return errors.Join(err, errors.New("transcript snapshot unavailable"))
	}
	if snapshot.Meeting.Status != meeting.Status {
		return nil
	}
	isNova := source == "B"
	parsed, err := downloadAndParseTranscript(ctx, bucket, key)
	if err != nil {
		return errors.Join(err, failTranscriptSnapshot(ctx, snapshot))
	}
	if len(eventETags) > 0 && eventETags[0] != "" && strings.Trim(eventETags[0], "\"") != strings.Trim(parsed.Objects[0].ETag, "\"") {
		return nil // superseded S3 delivery
	}
	if !isNova {
		if handled, err := handleNoSpeech(ctx, snapshot, parsed); handled {
			return err
		}
	}
	glossary := meetingGlossary(ctx, meeting)
	refineParsedTranscript(ctx, parsed, glossary)
	logCorrectionOutcome(meetingID, parsed, glossary)
	segments, whisperSegments, audioDuration := parsed.Segments, parsed.Whisper, parsed.Duration

	// Same fallback chain as the multi-part merge path (merge.go): the raw
	// audioDuration comes only from whisper_metadata.duration_seconds, which is
	// absent for AWS-Transcribe-fallback, Nova Sonic and pre-diarization
	// transcripts -- passing it straight through silently persisted 0 for all
	// of those (updateMeetingTranscript drops durationSeconds <= 0).
	// Computed AFTER the refinement block so it sees the reassigned `segments`.
	var segmentsLastEnd, whisperSegmentsLastEnd float64
	if len(segments) > 0 {
		segmentsLastEnd = segments[len(segments)-1].EndTime
	}
	if len(whisperSegments) > 0 {
		whisperSegmentsLastEnd = whisperSegments[len(whisperSegments)-1].End
	}
	resolvedDuration, durationSource := duration.Resolve(audioDuration, segmentsLastEnd, whisperSegmentsLastEnd)
	switch durationSource {
	case duration.SourceSegmentEnd:
		log.Printf(
			"Meeting %s: whisper_metadata.duration_seconds missing; falling back to last segment EndTime=%.2fs (may drift)",
			meetingID, resolvedDuration,
		)
	case duration.SourceWhisperEnd:
		log.Printf(
			"Meeting %s had no refined segments and no duration; using raw Whisper end=%.2fs as duration",
			meetingID, resolvedDuration,
		)
	case duration.SourceUnknown:
		log.Printf("Meeting %s: no usable duration source; using 0 (duration will not be persisted)", meetingID)
	}

	parsed.Duration = resolvedDuration
	current, err := beginTranscriptSummary(ctx, snapshot, parsed, isNova)
	if err != nil || current == nil {
		return err
	}
	learnFromCorrections(ctx, current, glossary, parsed.Corrections)
	return generateClaimedSummary(ctx, current, "", false)
}

// handlePartTranscript increments the parts-ready counter and emits AllPartsTranscribed when all parts are done
func handlePartTranscript(ctx context.Context, bucket, key string) error {
	meetingID, partIndex, ok := extractPartInfo(key)
	if !ok {
		log.Printf("Could not extract part info from key: %s", key)
		return nil
	}

	log.Printf("Part transcript received: meeting=%s part=%d key=%s", meetingID, partIndex, key)

	meeting, err := repo.GetMeetingByID(ctx, meetingID)
	if err != nil || meeting == nil {
		log.Printf("Failed to get meeting %s: %v", meetingID, err)
		return nil
	}

	// Status guard: skip if already past transcription phase
	if meeting.Status == model.StatusDone || meeting.Status == model.StatusError || meeting.Status == model.StatusSummarizing {
		log.Printf("Skipping part transcript for meeting %s (status=%s)", meetingID, meeting.Status)
		return nil
	}

	partsReady, partCount, err := repo.IncrementAudioPartsReady(ctx, meeting.UserID, meetingID, partIndex)
	if err != nil {
		// Transient DynamoDB error (throttle, timeout). Propagate so the
		// Lambda runtime retries — `IncrementAudioPartsReady` is idempotent
		// (set ADD), so retry is safe. Without this, an ack would lose the
		// part forever and the meeting would sit at `transcribing` until
		// the 30-min auto-expiry.
		log.Printf("Failed to increment parts ready for meeting %s: %v", meetingID, err)
		return fmt.Errorf("increment parts ready failed (retrying): %w", err)
	}

	log.Printf("Meeting %s: parts ready %d/%d", meetingID, partsReady, partCount)

	if partsReady >= partCount && partCount > 0 {
		// Once-only emit lock: S3 part transcripts can be redelivered (S3
		// notifications are at-least-once via EventBridge), and every retry
		// would otherwise re-emit `AllPartsTranscribed` here. The
		// `handleAllPartsTranscribed` whitelist guard catches duplicates
		// downstream, but the spurious events still cost CloudWatch + IAM
		// throttle budget. `ClaimAllPartsEmit` writes
		// `allPartsEmittedAt` only when not already set.
		claimed, claimErr := repo.ClaimAllPartsEmit(ctx, meeting.UserID, meetingID)
		if claimErr != nil {
			// Transient DynamoDB error on the conditional SET. Propagate
			// so Lambda retries — `ClaimAllPartsEmit` is idempotent (only
			// the first conditional check succeeds; later attempts return
			// claimed=false), so retry is safe.
			log.Printf("Failed to claim all-parts emit for meeting %s: %v", meetingID, claimErr)
			return fmt.Errorf("claim all-parts emit failed (retrying): %w", claimErr)
		}
		if !claimed {
			log.Printf("Skipping duplicate AllPartsTranscribed emit for meeting %s (already emitted)", meetingID)
			return nil
		}
		log.Printf("All %d parts transcribed for meeting %s, emitting AllPartsTranscribed", partCount, meetingID)
		if emitErr := emitAllPartsTranscribedEvent(ctx, meetingID, meeting.UserID, partCount, bucket); emitErr != nil {
			// Compensation path: release the lock so the next Lambda retry
			// can re-claim and re-emit. Without this, EventBridge throttle
			// / transient PutEvents failure would leave the meeting
			// permanently stuck in `transcribing` (30-min auto-expiry
			// would eventually flip it to `error`, but that's a poor UX).
			// Return the emit error so the Lambda runtime retries.
			if releaseErr := repo.ReleaseAllPartsEmit(ctx, meeting.UserID, meetingID); releaseErr != nil {
				log.Printf("CRITICAL: lock-release failed after emit failure for meeting %s: %v (emit err: %v)",
					meetingID, releaseErr, emitErr)
			}
			return emitErr
		}
		return nil
	}

	return nil
}

// handleAllPartsTranscribed merges all part transcripts and generates the summary directly
func handleAllPartsTranscribed(ctx context.Context, detail *model.AllPartsTranscribedDetail) error {
	meetingID := detail.MeetingID
	userID := detail.UserID

	log.Printf("AllPartsTranscribed: merging %d parts for meeting %s", detail.PartCount, meetingID)

	meeting, err := repo.MetadataView().GetMeeting(ctx, userID, meetingID)
	if err != nil || meeting == nil {
		log.Printf("Failed to get meeting %s: %v", meetingID, err)
		return nil
	}
	if handled, retryErr := resumeSummaryRetry(ctx, meeting, repo.MetadataView(), generateResumedSummary); handled {
		return retryErr
	}

	// Whitelist guard — process when the meeting is still `transcribing`,
	// or when it's `summarizing` but eligible for retry (see
	// handleSingleTranscript's matching comment: a prior attempt died
	// mid-summarize and nothing else re-drives it). `EventBridge` is
	// at-least-once: if the same `AllPartsTranscribed` event re-invokes
	// while the first call is still actively running (`summarizing`, not
	// retry-eligible), this guard still blocks the duplicate Bedrock
	// summary + KB export + DynamoDB write.
	if meeting.Status != model.StatusTranscribing {
		if meeting.Status != model.StatusSummarizing || !service.IsSummarizeRetryEligible(meeting.UpdatedAt) {
			log.Printf("Skipping merge for meeting %s (status=%s, expected=transcribing)", meetingID, meeting.Status)
			return nil
		}
		// Atomic claim — see handleSingleTranscript's matching comment.
		claimed, claimErr := repo.ClaimSummarizeRetry(ctx, meeting.UserID, meetingID)
		if claimErr != nil {
			log.Printf("Failed to claim summarize retry for meeting %s: %v", meetingID, claimErr)
			return fmt.Errorf("claim summarize retry failed (retrying): %w", claimErr)
		}
		if !claimed {
			log.Printf("Skipping merge for meeting %s (summarize retry already claimed)", meetingID)
			return nil
		}
		log.Printf("Reprocessing stuck meeting %s (status=summarizing since %s)", meetingID, meeting.UpdatedAt)
	}

	snapshot, err := repo.CaptureTranscriptUpdate(ctx, userID, meetingID)
	if err != nil || snapshot == nil {
		return errors.Join(err, errors.New("transcript snapshot unavailable"))
	}
	if snapshot.Meeting.Status != meeting.Status {
		return nil
	}
	glossary := meetingGlossary(ctx, meeting)
	parsed, err := mergePartTranscripts(ctx, detail.Bucket, meetingID, detail.PartCount, glossary)
	if err != nil {
		return errors.Join(err, failTranscriptSnapshot(ctx, snapshot))
	}
	logCorrectionOutcome(meetingID, parsed, glossary)
	if handled, err := handleNoSpeech(ctx, snapshot, parsed); handled {
		return err
	}
	current, err := beginTranscriptSummary(ctx, snapshot, parsed, false)
	if err != nil || current == nil {
		return err
	}
	learnFromCorrections(ctx, current, glossary, parsed.Corrections)
	transcript, segments := parsed.Text, parsed.Segments

	// Archive the merged transcript to S3 (multi-file audio design, ADR-014).
	// AudioPlayer uses presigned URLs from `audioKeys` so playback doesn't
	// depend on this object, but the archival copy preserves the merged
	// timeline for KB re-ingest, support debugging, and future re-summary.
	// Non-fatal: a failed archival write must not block the summary pipeline.
	if archiveErr := archiveMergedTranscript(ctx, detail.Bucket, meetingID, transcript, segments); archiveErr != nil {
		log.Printf("Non-fatal: failed to archive merged transcript for meeting %s: %v", meetingID, archiveErr)
	}

	return generateClaimedSummary(ctx, current, "", false)
}

// generateSummary runs the full Bedrock pipeline: summary, action items, tags, sentiment, KB export
func generateSummary(ctx context.Context, meeting *model.Meeting, priorContext string) error {
	return generateSummaryRun(ctx, meeting, priorContext, "", false)
}

func generateResumedSummary(ctx context.Context, meeting *model.Meeting, priorContext string) error {
	return generateSummaryRun(ctx, meeting, priorContext, meeting.SummarizeRetryClaimedAt, true)
}

func generateClaimedSummary(ctx context.Context, meeting *model.Meeting, priorContext string, resumed bool) error {
	err := generateSummaryRun(ctx, meeting, priorContext, meeting.SummarizeRetryClaimedAt, resumed)
	if err == nil {
		return nil
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return errors.Join(err, repo.ReleaseSummaryRetryClaim(cleanup, meeting.UserID, meeting.MeetingID, meeting.SummarizeRetryClaimedAt))
}

const (
	autoNewsLinkTimeout  = 20 * time.Second
	peopleResolveTimeout = 30 * time.Second
)

// finishSummaryAnalysis ends the post-summary analysis sequence: it queues term
// extraction and, for a meeting published to its account, adds automatic news
// links and resolves its people edges. All run before the privacy early return. It reports whether the
// legacy KB export should run.
func finishSummaryAnalysis(ctx context.Context, userID, meetingID string, current *model.Meeting) bool {
	enqueueTerms(ctx, userID, meetingID, "summary")
	if current != nil && current.SharedToAccount && current.AccountID != "" {
		linkAccountNews(ctx, current)
		resolveMeetingPeople(ctx, current)
	}
	return !repository.SummaryPrivacyWritesEnabled()
}

func generateSummaryRun(ctx context.Context, meeting *model.Meeting, priorContext, claim string, resumed bool) error {
	retry := meeting.SummaryRetryPending && meeting.Status == model.StatusSummarizing
	meetingID := meeting.MeetingID
	userID := meeting.UserID

	if meeting.Status == model.StatusError {
		return nil
	}

	// Flip status to `summarizing` before Bedrock work. If this write fails we
	// must abort: otherwise the downstream summary save would overwrite the
	// content while status is still `transcribing`, which the frontend gates
	// off (loading spinner stays visible while content appears blank).
	fields := map[string]interface{}{"status": model.StatusSummarizing}
	if !retry {
		fields["summaryRetryAttempts"], fields["summaryRetryPending"], fields["summaryConflictCode"] = 0, false, ""
	}
	if claim == "" {
		if statusErr := repo.UpdateMeetingFields(ctx, userID, meetingID, fields); statusErr != nil {
			log.Printf("Failed to set status=summarizing for meeting %s: %v", meetingID, statusErr)
			return fmt.Errorf("set summarizing status failed (retrying): %w", statusErr)
		}

	}
	var content string
	var err error
	if claim != "" {
		content, err = bedrockService.SummarizeClaimedTranscript(ctx, meetingID, userID, priorContext, claim, resumed)
	} else {
		content, err = bedrockService.SummarizeTranscript(ctx, meetingID, userID, priorContext)
	}
	if humanContentPreserved(ctx, err, userID, meetingID) {
		return nil
	}
	if err != nil {
		if retry {
			return err // The owning retry claim decides pending versus terminal.
		}
		if errors.Is(err, service.ErrSummaryConflict) {
			log.Printf("Summary source conflict for meeting %s; retry pending", meetingID)
			return err // Lambda redelivery claims the marker and generates afresh.
		}
		log.Printf("Failed to generate summary: %v", err)
		repo.UpdateMeetingFields(ctx, userID, meetingID, map[string]interface{}{
			"status": model.StatusError,
		})
		return nil
	}

	log.Printf("Generated content for meeting %s: %d characters", meetingID, len(content))

	if err := actionItemsService.RunInline(ctx, userID, meetingID); err != nil {
		log.Printf("Action item analysis could not finish: %v", err)
	}

	for _, field := range []string{"tags", "sentiment", "insights"} {
		if err := bedrockService.DeriveSummary(ctx, userID, meetingID, field); err != nil {
			log.Printf("Failed to publish current %s (non-fatal): %v", field, err)
		}
	}
	// Copy saved insights; the transaction verifies canonical publication/generation/revision.
	current, err := repo.MetadataView().GetMeeting(ctx, userID, meetingID)
	if err != nil {
		log.Printf("Failed to read current insights for fanout (non-fatal): %v", err)
	} else if current != nil && current.SharedToAccount && current.AccountID != "" {
		if items, err := service.BuildAccountInsights(current.AccountID, current); err != nil {
			log.Printf("Failed to build account insights (non-fatal): %v", err)
		} else if len(items) == 0 {
			// A validated empty result must not leave the previous account copy readable.
			if err := repo.ClearAccountInsights(ctx, userID, meetingID, current.AccountID,
				current.SummaryGeneration(), current.InsightsRevision); err != nil {
				log.Printf("Failed to clear stale account insights (non-fatal): %v", err)
			}
		} else if err := repo.PutAccountInsights(ctx, items); err != nil {
			log.Printf("Failed to fan out current insights (non-fatal): %v", err)
		}
	}
	if err != nil {
		current = nil
	}
	if !finishSummaryAnalysis(ctx, userID, meetingID, current) {
		// Canonical stream/reconciliation owns immutable revision-checked exports.
		// Drain legacy fixed objects; they can revive stale derivation after resets.
		return nil
	}

	// KB Export: re-fetch meeting with all saved fields
	updatedMeeting, err := repo.GetMeeting(ctx, userID, meetingID)
	if err != nil {
		log.Printf("Failed to re-fetch meeting for KB export (non-fatal): %v", err)
	} else if updatedMeeting != nil {
		attachments, _ := repo.ListAttachments(ctx, meetingID)
		doc := service.GenerateMeetingDocument(updatedMeeting, attachments)
		if err := kbExportService.ExportToKB(ctx, updatedMeeting.UserID, meetingID, doc); err != nil {
			log.Printf("Failed to export to KB (non-fatal): %v", err)
		}
		if err := kbExportService.TriggerIngestion(ctx); err != nil {
			log.Printf("Failed to trigger KB ingestion (non-fatal): %v", err)
		}
	}

	return nil
}

// humanContentPreserved reports the preserved-human-content early return.
// The meeting is done with owner or editor text, so term extraction is still
// queued (the taint flag covers editor-authored text).
func humanContentPreserved(ctx context.Context, err error, userID, meetingID string) bool {
	if !errors.Is(err, service.ErrSummaryHumanContentPreserved) {
		return false
	}
	enqueueTerms(ctx, userID, meetingID, "human-content-preserved")
	return true
}

// emitAllPartsTranscribedEvent publishes a custom EventBridge event when all parts are transcribed
func emitAllPartsTranscribedEvent(ctx context.Context, meetingID, userID string, partCount int, bucket string) error {
	if ebClient == nil {
		log.Printf("EventBridge client not configured, cannot emit AllPartsTranscribed")
		return fmt.Errorf("EventBridge client not configured")
	}

	detail, _ := json.Marshal(model.AllPartsTranscribedDetail{
		MeetingID: meetingID,
		UserID:    userID,
		PartCount: partCount,
		Bucket:    bucket,
	})

	_, err := ebClient.PutEvents(ctx, &eventbridge.PutEventsInput{
		Entries: []ebtypes.PutEventsRequestEntry{{
			Source:     aws.String("ttobak.transcribe"),
			DetailType: aws.String("AllPartsTranscribed"),
			Detail:     aws.String(string(detail)),
		}},
	})
	if err != nil {
		return fmt.Errorf("failed to emit AllPartsTranscribed event: %w", err)
	}

	log.Printf("Emitted AllPartsTranscribed event for meeting %s", meetingID)
	return nil
}

// archiveMergedTranscript writes the merged multi-part transcript to
// `transcripts/{meetingID}.json` so the archival shape mirrors a
// single-file transcript. This is debug/replay scaffolding for the
// multi-file audio design (ADR-014); the live playback path uses presigned URLs from the
// `audioKeys` slice, so a failed write here must NOT block summarization.
func archiveMergedTranscript(ctx context.Context, bucket, meetingID, transcript string, segments []TranscriptSegmentOut) error {
	payload := struct {
		Source     string                 `json:"source"`
		MeetingID  string                 `json:"meetingId"`
		Transcript string                 `json:"transcript"`
		Segments   []TranscriptSegmentOut `json:"segments"`
		MergedAt   string                 `json:"mergedAt"`
	}{
		Source:     "merged-parts",
		MeetingID:  meetingID,
		Transcript: transcript,
		Segments:   segments,
		MergedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal merged transcript: %w", err)
	}
	key := fmt.Sprintf("transcripts/%s.json", meetingID)
	_, err = s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        strings.NewReader(string(body)),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("put merged transcript to s3: %w", err)
	}
	return nil
}

// downloadAndParseTranscript fetches a transcript JSON from S3 and returns its
// transcript text, refined speaker segments, raw Whisper segments, and the
// authoritative audio duration in seconds from `whisper_metadata.duration_seconds`
// (0 when absent — caller handles fallback). The duration is required by
// `mergePartTranscripts` to avoid timestamp drift caused by trailing silence
// that the last segment's EndTime omits.
//
// `duration_seconds` is the actual decoded audio length (faster_whisper's
// `info.duration`), not processing time -- see ADR-009. A transcript written
// by a pre-diarization container image instead has Whisper's wall-clock
// transcription time in this field, since the field's meaning changed
// without a rename; there is no reliable way to detect that from the JSON
// alone, so such historical transcripts may report an inaccurate value here.
func downloadAndParseTranscript(ctx context.Context, bucket, key string) (*parsedTranscript, error) {
	result, err := s3Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	defer result.Body.Close()
	const maxTranscriptBytes = 16 * 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(result.Body, maxTranscriptBytes+1))
	if err != nil || len(data) > maxTranscriptBytes {
		return nil, errors.New("transcript read failed or exceeded limit")
	}
	var source TranscribeResult
	if json.Unmarshal(data, &source) != nil || len(source.Results.Transcripts) == 0 {
		return nil, errors.New("invalid transcript source")
	}
	parsed := &parsedTranscript{Text: source.Results.Transcripts[0].Transcript, Segments: extractSpeakerSegments(&source),
		Objects: []model.SummaryObject{{Bucket: bucket, Key: key, ETag: aws.ToString(result.ETag), VersionID: aws.ToString(result.VersionId)}}}
	parsed.Quality = parsedTranscriptQuality(&source, parsed.Segments)
	if source.WhisperMetadata != nil {
		parsed.Whisper, parsed.Duration = source.WhisperMetadata.Segments, source.WhisperMetadata.DurationSeconds
	}
	return parsed, nil
}

func extractSpeakerSegments(result *TranscribeResult) []TranscriptSegmentOut {
	if result.Results.SpeakerLabels == nil || len(result.Results.SpeakerLabels.Segments) == 0 {
		return nil
	}

	itemContent := make(map[string]string)
	for _, item := range result.Results.Items {
		if len(item.Alternatives) > 0 {
			if item.Type == "pronunciation" && item.StartTime != "" {
				itemContent[item.StartTime] = item.Alternatives[0].Content
			}
		}
	}

	var segments []TranscriptSegmentOut
	for _, seg := range result.Results.SpeakerLabels.Segments {
		var words []string
		for _, item := range seg.Items {
			if content, ok := itemContent[item.StartTime]; ok {
				words = append(words, content)
			}
		}
		if len(words) == 0 {
			continue
		}

		startTime, startErr := strconv.ParseFloat(seg.StartTime, 64)
		endTime, endErr := strconv.ParseFloat(seg.EndTime, 64)
		if startErr != nil || endErr != nil || math.IsNaN(startTime) || math.IsNaN(endTime) ||
			math.IsInf(startTime, 0) || math.IsInf(endTime, 0) || startTime < 0 || endTime <= startTime ||
			len(segments) > 0 && startTime < segments[len(segments)-1].StartTime {
			return nil // Keep readable source text, without fabricated timing.
		}

		segments = append(segments, TranscriptSegmentOut{
			Speaker:   seg.SpeakerLabel,
			Text:      strings.Join(words, " "),
			StartTime: startTime,
			EndTime:   endTime,
		})
	}

	return segments
}

func setMeetingError(ctx context.Context, meetingID string) {
	meeting, err := repo.GetMeetingByID(ctx, meetingID)
	if err != nil || meeting == nil {
		return
	}
	repo.UpdateMeetingFields(ctx, meeting.UserID, meetingID, map[string]interface{}{
		"status": model.StatusError,
	})
}

// extractMeetingIDFromTranscriptKey returns the meetingID embedded in a
// single (non-part) transcript S3 key like `transcripts/{id}.json` or
// `transcripts/{id}-nova.json`. Returns "" for part-transcript keys
// (`transcripts/{id}_part_NNN.json`) — those are handled by
// `extractPartInfo` and would otherwise produce an id of `{id}_part_NNN`
// which is not a real meeting id. The dispatch in `handler` checks
// `_part_` first so this defensive guard is belt-and-suspenders.
func extractMeetingIDFromTranscriptKey(key string) string {
	if strings.Contains(key, "_part_") {
		return ""
	}
	key = strings.TrimPrefix(key, "transcripts/")
	key = strings.TrimSuffix(key, ".json")
	key = strings.TrimSuffix(key, "-nova")
	// The repository's transcript overflow spill writes
	// transcripts/{meetingId}/{field}.txt (see repository.storeTranscript),
	// and the EventBridge rule matches the whole transcripts/ prefix — so
	// every spill upload lands here too. A nested key is never a meeting's
	// own transcript JSON; returning "{id}/transcriptA.txt" as a "meeting
	// id" would skip the status guard (GetMeetingByID misses) and then fail
	// parsing the .txt as JSON, erroring the invocation on every long
	// meeting's normal pipeline run. Reject anything with a path separator.
	if strings.Contains(key, "/") {
		return ""
	}
	return key
}

// extractPartInfo parses meetingID and partIndex from a part transcript key
// Expected: transcripts/{meetingID}_part_{NNN}.json
func extractPartInfo(key string) (meetingID string, partIndex int, ok bool) {
	key = strings.TrimPrefix(key, "transcripts/")
	key = strings.TrimSuffix(key, ".json")

	idx := strings.LastIndex(key, "_part_")
	if idx < 0 {
		return "", 0, false
	}

	meetingID = key[:idx]
	// Reject malformed keys where the meeting id is empty (e.g.
	// `transcripts/_part_001.json`). Without this guard, callers reach
	// `repo.GetMeetingByID("")` which returns nil and the call is silently
	// dropped — the malformed event vanishes from the retry queue.
	if meetingID == "" {
		return "", 0, false
	}
	partStr := key[idx+6:] // skip "_part_"
	partIndex, err := strconv.Atoi(partStr)
	if err != nil {
		return "", 0, false
	}
	return meetingID, partIndex, true
}

func main() {
	lambda.Start(Handler)
}
