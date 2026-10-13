package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"sort"

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
			StartTime: segment.StartTime, EndTime: segment.EndTime}
	}
}

func parsedTranscriptQuality(result *TranscribeResult, segments []TranscriptSegmentOut) *model.TranscriptQuality {
	quality := &model.TranscriptQuality{Refinement: "not_requested", SpeakerProvenance: "unknown"}
	if len(segments) > 0 {
		quality.SpeakerProvenance = "acoustic"
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
		result.Refinement = "partial"
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

func updateMeetingTranscript(ctx context.Context, snapshot *model.SummarySnapshot, parsed *parsedTranscript, isNova bool) error {
	fields, err := transcriptUpdateFields(parsed, isNova)
	if err != nil {
		return err
	}
	if err := checkTranscriptObjects(ctx, parsed.Objects); err != nil {
		return err
	}
	return repo.SaveTranscriptUpdate(ctx, snapshot, fields)
}

func failTranscriptSnapshot(ctx context.Context, snapshot *model.SummarySnapshot) error {
	err := repo.SaveTranscriptUpdate(ctx, snapshot, map[string]interface{}{"status": model.StatusError})
	if errors.Is(err, repository.ErrConditionFailed) {
		return nil
	}
	return err
}

// A competing transcript/audio run owns a superseded delivery. Unrelated edits
// (for example notes or selection) need a fresh retry rather than a silent ACK
// that would leave this meeting transcribing forever. Never reuse old output.
func resolveTranscriptConflict(ctx context.Context, snapshot *model.SummarySnapshot, isNova bool) error {
	current, err := repo.CaptureTranscriptUpdate(ctx, snapshot.Meeting.UserID, snapshot.Meeting.MeetingID)
	if err != nil {
		return err
	}
	if current == nil || current.Meeting.Status != snapshot.Meeting.Status {
		return nil
	}
	variant := "transcriptA"
	if isNova {
		variant = "transcriptB"
	}
	for _, field := range []string{variant, "audioKey", "audioKeys", "audioCrop", "diarizationSpeakerHint",
		"audioPartCount", "summarizeRetryClaimedAt"} {
		if !reflect.DeepEqual(snapshot.Checks[0].Fields[field], current.Checks[0].Fields[field]) {
			return nil
		}
	}
	return repository.ErrConditionFailed
}
