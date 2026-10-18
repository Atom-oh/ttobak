package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

type parsedTranscript struct {
	Text     string
	Segments []TranscriptSegmentOut
	Whisper  []service.WhisperSegment
	Duration float64
	Quality  *model.TranscriptQuality
	Objects  []model.SummaryObject
}

func refineParsedTranscript(ctx context.Context, parsed *parsedTranscript) {
	if len(parsed.Whisper) == 0 || len(parsed.Segments) > 0 {
		return
	}
	text, segments, quality, err := bedrockService.RefineTranscriptWithQuality(ctx, parsed.Whisper)
	quality.DiarizationEnabled, quality.DetectedSpeakers = parsed.Quality.DiarizationEnabled, parsed.Quality.DetectedSpeakers
	parsed.Quality = quality
	if err != nil {
		// Invalid raw timing remains readable as the original text, never as
		// fabricated timestamps. The fixed failure category is in Quality.
		return
	}
	parsed.Text = text
	parsed.Segments = make([]TranscriptSegmentOut, len(segments))
	for i, segment := range segments {
		parsed.Segments[i] = TranscriptSegmentOut{Speaker: segment.Speaker, Text: segment.Text,
			StartTime: segment.StartTime, EndTime: segment.EndTime, Untimed: segment.Untimed}
	}
}

func parsedTranscriptQuality(result *TranscribeResult, segments []TranscriptSegmentOut) *model.TranscriptQuality {
	quality := &model.TranscriptQuality{Refinement: "not_requested", SpeakerProvenance: "unknown", Timing: "unavailable"}
	if len(segments) > 0 {
		quality.SpeakerProvenance, quality.Timing = "acoustic", "available"
	}
	if metadata := result.WhisperMetadata; metadata != nil {
		if metadata.Diarization != nil {
			enabled := metadata.Diarization.Enabled
			quality.DiarizationEnabled = &enabled
			quality.DetectedSpeakers = metadata.Diarization.DetectedSpeakers
		}
	}
	if quality.SpeakerProvenance != "acoustic" {
		quality.Degraded, quality.Reasons = true, []string{"diarization_unavailable"}
	}
	return quality
}

func mergeTranscriptQuality(parts []*model.TranscriptQuality) *model.TranscriptQuality {
	result := &model.TranscriptQuality{Refinement: "not_requested", SpeakerProvenance: "unknown"}
	provenance, reasons, refinements := map[string]bool{}, map[string]bool{}, map[string]bool{}
	allEnabled, knownEnabled := true, true
	for _, part := range parts {
		result.BlankSegments += part.BlankSegments
		result.UntimedSegments += part.UntimedSegments
		result.TotalChunks += part.TotalChunks
		result.FallbackChunks += part.FallbackChunks
		result.Degraded = result.Degraded || part.Degraded
		result.DetectedSpeakers += part.DetectedSpeakers
		provenance[part.SpeakerProvenance], refinements[part.Refinement] = true, true
		if part.DiarizationEnabled == nil {
			knownEnabled = false
		} else {
			allEnabled = allEnabled && *part.DiarizationEnabled
		}
		for _, reason := range part.Reasons {
			reasons[reason] = true
		}
	}
	if knownEnabled && len(parts) > 0 {
		result.DiarizationEnabled = &allEnabled
	}
	if len(provenance) == 1 {
		for source := range provenance {
			result.SpeakerProvenance = source
		}
	} else if len(provenance) > 1 {
		result.SpeakerProvenance = "mixed"
	}
	if len(refinements) == 1 {
		for refinement := range refinements {
			result.Refinement = refinement
		}
	} else {
		result.Refinement = "mixed"
		if result.FallbackChunks > 0 {
			result.Refinement = "partial"
		}
	}
	result.Timing = "available"
	for _, part := range parts {
		if part.Timing != "available" {
			result.Timing = "partial"
		}
	}
	if len(parts) > 0 {
		allUntimed := true
		for _, part := range parts {
			allUntimed = allUntimed && part.Timing == "unavailable"
		}
		if allUntimed {
			result.Timing = "unavailable"
		}
	}
	for reason := range reasons {
		result.Reasons = append(result.Reasons, reason)
	}
	sort.Strings(result.Reasons)
	return result
}

func checkTranscriptObjects(ctx context.Context, objects []model.SummaryObject) error {
	for _, object := range objects {
		if object.ETag == "" {
			return repository.ErrConditionFailed
		}
		current, err := s3Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(object.Bucket), Key: aws.String(object.Key)})
		if err != nil {
			return err
		}
		if aws.ToString(current.ETag) != object.ETag || object.VersionID != "" && aws.ToString(current.VersionId) != object.VersionID {
			return repository.ErrConditionFailed
		}
	}
	return nil
}

func transcriptUpdateFields(parsed *parsedTranscript, isNova bool) (map[string]interface{}, error) {
	variant := "A"
	if isNova {
		variant = "B"
	}
	assignSegmentIDs(parsed.Segments)
	segments := ""
	if len(parsed.Segments) > 0 {
		body, err := json.Marshal(parsed.Segments)
		if err != nil {
			return nil, errors.New("invalid transcript segments")
		}
		segments = string(body)
	}
	quality := *parsed.Quality
	quality.TextSHA256, quality.SegmentsSHA256 = model.TranscriptHash(parsed.Text), model.TranscriptHash(segments)
	fields := map[string]interface{}{"transcript" + variant: parsed.Text, "transcriptQuality" + variant: quality}
	// Segments are shared by both variants: an untimed publication must not blank
	// segments written for the other one. The base writer skipped empty segments too.
	if segments != "" {
		fields["transcriptSegments"] = segments
	}
	if parsed.Duration > 0 && !math.IsInf(parsed.Duration, 0) && !math.IsNaN(parsed.Duration) {
		fields["duration"] = int(math.Round(parsed.Duration))
	}
	return fields, nil
}

// Transcript publication and summary ownership share one immutable CAS.
// Conflicts discard old output; a human edit is summarized from saved current text.
func beginTranscriptSummary(ctx context.Context, snapshot *model.SummarySnapshot, parsed *parsedTranscript, isNova bool) (*model.Meeting, error) {
	fields, err := transcriptUpdateFields(parsed, isNova)
	if err != nil {
		return nil, err
	}
	if err := checkTranscriptObjects(ctx, parsed.Objects); err != nil {
		return nil, err
	}
	if same, err := repo.TranscriptTargetsMatch(ctx, snapshot); err != nil || !same {
		if err == nil {
			err = repository.ErrConditionFailed
		}
		return nil, err
	}
	contentHash := repository.SummaryContentHash(snapshot)
	claim, err := repo.StartTranscriptSummary(ctx, snapshot, fields, contentHash)
	if errors.Is(err, repository.ErrConditionFailed) {
		current, captureErr := repo.CaptureTranscriptUpdate(ctx, snapshot.Meeting.UserID, snapshot.Meeting.MeetingID)
		if captureErr != nil {
			return nil, captureErr
		}
		if current == nil || current.Meeting.Status != snapshot.Meeting.Status {
			return nil, nil
		}
		for _, field := range []string{"audioKey", "audioKeys", "audioCrop", "diarizationSpeakerHint", "audioPartCount", "summarizeRetryClaimedAt"} {
			if !reflect.DeepEqual(snapshot.Checks[0].Fields[field], current.Checks[0].Fields[field]) {
				return nil, nil
			}
		}
		variant := "A"
		if isNova {
			variant = "B"
		}
		changed := !reflect.DeepEqual(snapshot.Checks[0].Fields["transcript"+variant], current.Checks[0].Fields["transcript"+variant]) ||
			!reflect.DeepEqual(snapshot.Checks[0].Fields["transcriptRevision"+variant], current.Checks[0].Fields["transcriptRevision"+variant])
		if !changed {
			return nil, repository.ErrConditionFailed
		}
		// Empty fields never rebind the discarded worker output to a fresh source.
		claim, err = repo.StartTranscriptSummary(ctx, current, nil, contentHash)
	}
	if err != nil {
		return nil, err
	}
	release := func(cause error) (*model.Meeting, error) {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return nil, errors.Join(cause, repo.ReleaseSummaryRetryClaim(cleanup, snapshot.Meeting.UserID, snapshot.Meeting.MeetingID, claim))
	}
	current, err := repo.MetadataView().GetMeeting(ctx, snapshot.Meeting.UserID, snapshot.Meeting.MeetingID)
	if err != nil {
		return release(err)
	}
	if current == nil || current.Status != model.StatusSummarizing || !current.SummaryRetryPending || current.SummarizeRetryClaimedAt != claim {
		return release(repository.ErrConditionFailed)
	}
	return current, nil
}

func failTranscriptSnapshot(ctx context.Context, snapshot *model.SummarySnapshot) error {
	err := repo.SaveTranscriptUpdate(ctx, snapshot, map[string]interface{}{"status": model.StatusError})
	if errors.Is(err, repository.ErrConditionFailed) {
		return nil
	}
	return err
}

// transcriptHasNoSpeech reports a decodable transcript that carries no text at
// all: blank text, no speaker segments and only blank Whisper segments. Such a
// run is terminal (NO_SPEECH), not a summary to retry three times.
func transcriptHasNoSpeech(parsed *parsedTranscript) bool {
	if parsed == nil || strings.TrimSpace(parsed.Text) != "" || len(parsed.Segments) > 0 {
		return false
	}
	for _, segment := range parsed.Whisper {
		if strings.TrimSpace(segment.Text) != "" {
			return false
		}
	}
	return true
}

// markNoSpeech records the terminal NO_SPEECH code under the transcript
// snapshot fence. It publishes no transcript field and makes no model call.
var markNoSpeech = func(ctx context.Context, snapshot *model.SummarySnapshot) error {
	return repo.MarkSttNoSpeech(ctx, snapshot)
}

// handleNoSpeech marks an empty transcript instead of summarizing it. Like
// publication, the mark first revalidates the downloaded objects, so a
// replaced transcript is never marked from stale bytes. A lost fence means
// another writer progressed; that is not an error to retry.
func handleNoSpeech(ctx context.Context, snapshot *model.SummarySnapshot, parsed *parsedTranscript) (bool, error) {
	if !transcriptHasNoSpeech(parsed) {
		return false, nil
	}
	err := checkTranscriptObjects(ctx, parsed.Objects)
	if err == nil {
		err = markNoSpeech(ctx, snapshot)
	}
	switch {
	case errors.Is(err, repository.ErrConditionFailed):
		log.Printf("Meeting %s: empty transcript superseded before the %s mark", snapshot.Meeting.MeetingID, model.SttFailureNoSpeech)
		return true, nil
	case err == nil:
		log.Printf("Meeting %s: transcript has no speech; marked %s", snapshot.Meeting.MeetingID, model.SttFailureNoSpeech)
	}
	return true, err
}
