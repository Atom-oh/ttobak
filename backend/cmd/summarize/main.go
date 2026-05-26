package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

var (
	bedrockService  *service.BedrockService
	kbExportService *service.KBExportService
	repo            *repository.DynamoDBRepository
	s3Client        *s3.Client
	ebClient        *eventbridge.Client
	bucketName      string
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

	// KB export service — gracefully skips if not configured
	kbBucketName := os.Getenv("KB_BUCKET_NAME")
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
		segs[i].ID = fmt.Sprintf("seg-%d", int64(segs[i].StartTime*1000))
	}
}

// Handler processes EventBridge events: S3 transcript uploads and custom AllPartsTranscribed events
func Handler(ctx context.Context, raw json.RawMessage) error {
	// Two-phase dispatch: detect event type first, then route
	var envelope model.EventEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("failed to unmarshal event envelope: %w", err)
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
	return handleSingleTranscript(ctx, bucket, key)
}

// handleSingleTranscript processes a single (non-part) transcript: parse, refine, save, summarize
func handleSingleTranscript(ctx context.Context, bucket, key string) error {
	meetingID := extractMeetingIDFromTranscriptKey(key)
	if meetingID == "" {
		log.Printf("Could not extract meeting ID from key: %s", key)
		return nil
	}

	// Status guard: skip if already processed (prevents re-trigger after merged transcript write)
	meeting, err := repo.GetMeetingByID(ctx, meetingID)
	if err == nil && meeting != nil {
		if meeting.Status == model.StatusDone || meeting.Status == model.StatusSummarizing {
			log.Printf("Skipping transcript for meeting %s (status=%s)", meetingID, meeting.Status)
			return nil
		}
	}

	isNova := strings.Contains(key, "-nova.json")

	transcript, segments, whisperSegments, err := downloadAndParseTranscript(ctx, bucket, key)
	if err != nil {
		log.Printf("Failed to parse transcript: %v", err)
		setMeetingError(ctx, meetingID)
		return nil
	}

	// Refine Whisper transcript using Sonnet if Whisper segments are available
	if len(whisperSegments) > 0 && len(segments) == 0 {
		log.Printf("Refining %d Whisper segments for meeting %s", len(whisperSegments), meetingID)
		refinedText, refinedSegs, refineErr := bedrockService.RefineTranscript(ctx, whisperSegments)
		if refineErr != nil {
			log.Printf("Failed to refine transcript (using raw): %v", refineErr)
		} else {
			transcript = refinedText
			segments = make([]TranscriptSegmentOut, len(refinedSegs))
			for i, rs := range refinedSegs {
				segments[i] = TranscriptSegmentOut{
					Speaker:   rs.Speaker,
					Text:      rs.Text,
					StartTime: rs.StartTime,
					EndTime:   rs.EndTime,
				}
			}
			log.Printf("Refined transcript: %d segments -> %d clean segments", len(whisperSegments), len(refinedSegs))
		}
	}

	err = updateMeetingTranscript(ctx, meetingID, transcript, segments, isNova)
	if err != nil {
		log.Printf("Failed to update meeting with transcript: %v", err)
		setMeetingError(ctx, meetingID)
		return nil
	}

	log.Printf("Updated meeting %s with transcript (nova=%v)", meetingID, isNova)

	// Re-fetch meeting for summary generation. The earlier `meeting` from the
	// status guard at the top of this function may be nil (GSI eventual
	// consistency) — we shadow it here and the explicit nil/error returns
	// below guarantee `meeting` is non-nil before any field access.
	meeting, err = repo.GetMeetingByID(ctx, meetingID)
	if err != nil || meeting == nil {
		log.Printf("Failed to get meeting via GSI, retrying after 1s: %v", err)
		time.Sleep(1 * time.Second)
		meeting, err = repo.GetMeetingByID(ctx, meetingID)
		if err != nil || meeting == nil {
			log.Printf("Still failed to get meeting: %v", err)
			return nil
		}
	}

	if transcript == "" {
		return nil
	}

	// `meeting` is guaranteed non-nil here by the retry block above.
	priorContext := buildLinkedMeetingContext(ctx, meeting)
	return generateSummary(ctx, meeting, priorContext)
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
		log.Printf("Failed to increment parts ready for meeting %s: %v", meetingID, err)
		return nil
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
			log.Printf("Failed to claim all-parts emit for meeting %s: %v", meetingID, claimErr)
			return nil
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

	meeting, err := repo.GetMeeting(ctx, userID, meetingID)
	if err != nil || meeting == nil {
		log.Printf("Failed to get meeting %s: %v", meetingID, err)
		return nil
	}

	// Whitelist guard — only process when the meeting is still in the
	// `transcribing` state. `EventBridge` is at-least-once: if the same
	// `AllPartsTranscribed` event re-invokes after the first call has
	// flipped the status to `summarizing`, the second invoke would
	// otherwise run a duplicate Bedrock summary + KB export + DynamoDB
	// write. Matches `handlePartTranscript`'s guard for consistency.
	if meeting.Status != model.StatusTranscribing {
		log.Printf("Skipping merge for meeting %s (status=%s, expected=transcribing)", meetingID, meeting.Status)
		return nil
	}

	transcript, segments, err := mergePartTranscripts(ctx, detail.Bucket, meetingID, detail.PartCount)
	if err != nil {
		log.Printf("Failed to merge transcripts for meeting %s: %v", meetingID, err)
		setMeetingError(ctx, meetingID)
		return nil
	}

	err = updateMeetingTranscript(ctx, meetingID, transcript, segments, false)
	if err != nil {
		log.Printf("Failed to save merged transcript for meeting %s: %v", meetingID, err)
		setMeetingError(ctx, meetingID)
		return nil
	}

	log.Printf("Saved merged transcript for meeting %s: %d chars, %d segments", meetingID, len(transcript), len(segments))

	// Re-fetch meeting to get updated state
	meeting, err = repo.GetMeeting(ctx, userID, meetingID)
	if err != nil || meeting == nil {
		log.Printf("Failed to re-fetch meeting %s after merge: %v", meetingID, err)
		return nil
	}

	priorContext := buildLinkedMeetingContext(ctx, meeting)
	return generateSummary(ctx, meeting, priorContext)
}

// generateSummary runs the full Bedrock pipeline: summary, action items, tags, sentiment, KB export
func generateSummary(ctx context.Context, meeting *model.Meeting, priorContext string) error {
	meetingID := meeting.MeetingID
	userID := meeting.UserID

	if meeting.Status == model.StatusError {
		return nil
	}

	repo.UpdateMeetingFields(ctx, userID, meetingID, map[string]interface{}{
		"status": model.StatusSummarizing,
	})

	content, err := bedrockService.SummarizeTranscript(ctx, meetingID, userID, priorContext)
	if err != nil {
		log.Printf("Failed to generate summary: %v", err)
		repo.UpdateMeetingFields(ctx, userID, meetingID, map[string]interface{}{
			"status": model.StatusError,
		})
		return nil
	}

	log.Printf("Generated content for meeting %s: %d characters", meetingID, len(content))

	actionItems, err := bedrockService.ExtractActionItems(ctx, meetingID, userID)
	if err != nil {
		log.Printf("Failed to extract action items (non-fatal): %v", err)
	} else {
		if err := repo.UpdateMeetingFields(ctx, userID, meetingID, map[string]interface{}{
			"actionItems": actionItems,
		}); err != nil {
			log.Printf("Failed to save action items: %v", err)
		} else {
			log.Printf("Extracted action items for meeting %s: %s", meetingID, actionItems)
		}
	}

	tags, err := bedrockService.ExtractTags(ctx, meetingID, userID)
	if err != nil {
		log.Printf("Failed to extract tags (non-fatal): %v", err)
	} else if len(tags) > 0 {
		if err := repo.UpdateMeetingFields(ctx, userID, meetingID, map[string]interface{}{
			"tags": tags,
		}); err != nil {
			log.Printf("Failed to save tags: %v", err)
		} else {
			log.Printf("Extracted tags for meeting %s: %v", meetingID, tags)
		}
	}

	sentiment, err := bedrockService.ExtractSentiment(ctx, meetingID, userID)
	if err != nil {
		log.Printf("Failed to extract sentiment (non-fatal): %v", err)
	} else if sentiment != "" {
		if err := repo.UpdateMeetingFields(ctx, userID, meetingID, map[string]interface{}{
			"sentiment": sentiment,
		}); err != nil {
			log.Printf("Failed to save sentiment: %v", err)
		} else {
			log.Printf("Extracted sentiment for meeting %s: %s", meetingID, sentiment)
		}
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

// truncateRunes returns at most `n` runes of `s` plus an ellipsis when truncated.
// Used instead of byte slicing for Korean / mixed-script content where naive
// `s[:n]` can cut mid-rune and produce invalid UTF-8.
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

// buildLinkedMeetingContext fetches summaries from linked predecessor meetings
func buildLinkedMeetingContext(ctx context.Context, meeting *model.Meeting) string {
	if len(meeting.LinkedMeetingIDs) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("아래는 이전 연관 회의의 요약입니다. 이전 회의에서의 액션 아이템 이행 여부와 연속성을 반영해주세요:\n\n")

	count := 0
	for _, linkedID := range meeting.LinkedMeetingIDs {
		if count >= 3 {
			break
		}
		linked, err := repo.GetMeetingByID(ctx, linkedID)
		if err != nil || linked == nil || linked.Content == "" {
			continue
		}
		// Defense-in-depth: `LinkMeetings` handler validates ownership at
		// link time, but if a future code path (direct DDB write, admin
		// import, migration) ever planted another user's id into
		// LinkedMeetingIDs we don't want to embed their summary into this
		// meeting's prompt. Skip silently so the prompt remains valid but
		// drops the orphaned reference.
		if linked.UserID != meeting.UserID {
			log.Printf("Skipping linked meeting %s — owner mismatch (%s vs %s)",
				linkedID, linked.UserID, meeting.UserID)
			continue
		}

		summary := truncateRunes(linked.Content, 2000)

		title := linked.Title
		if title == "" {
			title = linked.MeetingID
		}

		sb.WriteString(fmt.Sprintf("### 이전 회의: %s\n%s\n\n", title, summary))

		if linked.ActionItems != "" {
			sb.WriteString(fmt.Sprintf("#### 액션 아이템\n%s\n\n", truncateRunes(linked.ActionItems, 500)))
		}

		count++
	}

	if count == 0 {
		return ""
	}

	return sb.String()
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

func downloadAndParseTranscript(ctx context.Context, bucket, key string) (string, []TranscriptSegmentOut, []service.WhisperSegment, error) {
	result, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to download transcript: %w", err)
	}
	defer result.Body.Close()

	data, err := io.ReadAll(result.Body)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to read transcript: %w", err)
	}

	var transcribeResult TranscribeResult
	if err := json.Unmarshal(data, &transcribeResult); err != nil {
		return "", nil, nil, fmt.Errorf("failed to parse transcript JSON: %w", err)
	}

	if len(transcribeResult.Results.Transcripts) == 0 {
		return "", nil, nil, fmt.Errorf("no transcript found in result")
	}

	transcript := transcribeResult.Results.Transcripts[0].Transcript
	segments := extractSpeakerSegments(&transcribeResult)

	var whisperSegments []service.WhisperSegment
	if transcribeResult.WhisperMetadata != nil && len(transcribeResult.WhisperMetadata.Segments) > 0 {
		whisperSegments = transcribeResult.WhisperMetadata.Segments
	}

	return transcript, segments, whisperSegments, nil
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

		startTime, _ := strconv.ParseFloat(seg.StartTime, 64)
		endTime, _ := strconv.ParseFloat(seg.EndTime, 64)

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

func extractMeetingIDFromTranscriptKey(key string) string {
	key = strings.TrimPrefix(key, "transcripts/")
	key = strings.TrimSuffix(key, ".json")
	key = strings.TrimSuffix(key, "-nova")
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
	partStr := key[idx+6:] // skip "_part_"
	partIndex, err := strconv.Atoi(partStr)
	if err != nil {
		return "", 0, false
	}
	return meetingID, partIndex, true
}

func updateMeetingTranscript(ctx context.Context, meetingID, transcript string, segments []TranscriptSegmentOut, isNova bool) error {
	meeting, err := repo.GetMeetingByID(ctx, meetingID)
	if err != nil {
		return err
	}
	if meeting == nil {
		return fmt.Errorf("meeting not found")
	}

	fields := map[string]interface{}{}
	if isNova {
		fields["transcriptB"] = transcript
	} else {
		fields["transcriptA"] = transcript
	}

	if len(segments) > 0 {
		// ADR-013: ensure every segment carries a stable ID so the AI summary
		// can deep-link into the transcript via `transcript://{id}`.
		assignSegmentIDs(segments)
		segJSON, err := json.Marshal(segments)
		if err == nil {
			fields["transcriptSegments"] = string(segJSON)
			log.Printf("Saved %d speaker segments for meeting %s", len(segments), meetingID)
		}
	}

	return repo.UpdateMeetingFields(ctx, meeting.UserID, meetingID, fields)
}

func main() {
	lambda.Start(Handler)
}
