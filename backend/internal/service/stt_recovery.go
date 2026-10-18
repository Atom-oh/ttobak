package service

import (
	"math"
	"regexp"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/ttobak/backend/internal/model"
)

// SttRecoveryAction is the scheduled tick's per-meeting decision (ADR-031 amendment).
type SttRecoveryAction string

const (
	SttRecoverySkip                 SttRecoveryAction = "skip"
	SttRecoveryReport               SttRecoveryAction = "report"
	SttRecoveryReportCheckpointOnly SttRecoveryAction = "report_checkpoint_only"
	SttRecoveryReportMultipart      SttRecoveryAction = "report_multipart"
	SttRecoveryReportUnsupported    SttRecoveryAction = "report_unsupported"
	SttRecoveryTerminal             SttRecoveryAction = "terminal"
	SttRecoveryRedriveSTT           SttRecoveryAction = "redrive_stt"
	SttRecoveryRedriveSummary       SttRecoveryAction = "redrive_summary"
	SttRecoveryMarkExhausted        SttRecoveryAction = "mark_exhausted"
)

// Recovery timing. The STT deadline is generous on purpose: about 2.4x the
// observed 12.5 s/MB processing rate plus the 60-minute stuck window and a
// 15-minute margin, so a healthy long run is never duplicated.
const (
	SttRecoveryBaseWindow       = 60 * time.Minute
	SttRecoveryMargin           = 15 * time.Minute
	SttRecoveryPerMB            = 30 * time.Second
	SttRecoveryRetryDelay       = 15 * time.Minute
	SttSummaryRedriveMinAge     = 20 * time.Minute
	SttFailureDeletionGrace     = 7 * 24 * time.Hour
	SttRecoveryMinAudioBytes    = 1024
	sttRecoveryBytesPerMegabyte = 1_000_000
)

// SttRecoveryFacts are the S3 observations for one candidate row.
type SttRecoveryFacts struct {
	AudioFound    bool
	AudioSize     int64
	AudioModified time.Time
	// NewestAudioModified is the latest LastModified of any object under the
	// meeting's audio prefix, including rediarize_ copies that restart Whisper
	// without changing audioKey.
	NewestAudioModified time.Time
	CheckpointFound     bool
	TranscriptFound     bool
	TranscriptModified  time.Time
	TranscriptETag      string
	TranscriptType      string
}

// SttRecoveryDecision carries the action and a fixed, content-free reason.
type SttRecoveryDecision struct {
	Action  SttRecoveryAction
	Reason  string
	Key     string
	Attempt int
}

var sttMeetingUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// SafeSttIdentifier reports a non-empty path segment without separators.
func SafeSttIdentifier(id string) bool {
	return id != "" && len(id) <= 128 && !strings.ContainsAny(id, "/\\") && id != "." && id != ".." && norm.NFC.IsNormalString(id)
}

// SttRecoveryAudioKeyAllowed accepts only a bound final audio object of this
// meeting: never checkpoints, progress files, realtime or crop outputs.
func SttRecoveryAudioKeyAllowed(userID, meetingID, key string) bool {
	if !SafeSttIdentifier(userID) || !SafeSttIdentifier(meetingID) || !norm.NFC.IsNormalString(key) {
		return false
	}
	name, ok := strings.CutPrefix(key, "audio/"+userID+"/"+meetingID+"/")
	if !ok || name == "" || strings.Contains(name, "/") || strings.Contains(key, "..") {
		return false
	}
	for _, marker := range []string{"checkpoint_", "recording_progress", "realtime_", "crop_result_"} {
		if strings.Contains(name, marker) {
			return false
		}
	}
	return true
}

// IsSttCheckpointObjectName matches only the exact allowlisted checkpoint
// basenames (ADR-048); it is not a suffix match.
func IsSttCheckpointObjectName(name string) bool {
	return isCheckpointFileName(name)
}

func parseSttTime(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	return t, err == nil
}

// SttFailureMarkCurrent reports whether a code is still in force: status error
// and no write of any kind since the mark.
func SttFailureMarkCurrent(row model.SttRecoveryRow) bool {
	return row.SttFailureCode != "" && row.Status == model.StatusError && row.SttFailedAt != "" && row.UpdatedAt == row.SttFailedAt
}

// SttRecoveryDeadline is when a claimed or stuck STT run is presumed dead. The
// short retry delay applies only to a current retryable mark: a stale code left
// on a row a user has since restarted never shortens a healthy run's window.
func SttRecoveryDeadline(row model.SttRecoveryRow, facts SttRecoveryFacts) time.Time {
	if model.IsRetryableSttFailure(row.SttFailureCode) && SttFailureMarkCurrent(row) {
		if failed, ok := parseSttTime(row.SttFailedAt); ok {
			return failed.Add(SttRecoveryRetryDelay)
		}
	}
	base := facts.AudioModified
	if facts.NewestAudioModified.After(base) {
		base = facts.NewestAudioModified
	}
	if claimed, ok := parseSttTime(row.SttRecoveryClaimedAt); ok && claimed.After(base) {
		base = claimed
	}
	if row.Status == model.StatusTranscribing {
		if updated, ok := parseSttTime(row.UpdatedAt); ok && updated.After(base) {
			base = updated
		}
	}
	megabytes := math.Ceil(float64(facts.AudioSize) / sttRecoveryBytesPerMegabyte)
	return base.Add(SttRecoveryBaseWindow + SttRecoveryMargin + time.Duration(megabytes)*SttRecoveryPerMB)
}

// SttPipelineRestartedAt is the latest observed (re)start of the STT pipeline:
// the newest audio-prefix object, the last STT recovery claim and, while
// transcribing, updatedAt unless it is this tick's own summary re-drive claim
// (that re-emits the transcript; it restarts nothing). A transcript older than this belongs to a superseded
// run (for example a user rediarization still in progress) and must never be
// re-published. It is not truncated: S3 LastModified has second precision, so
// a transcript written in the same second as a restart compares as older and is
// treated as superseded, which errs on the safe side.
func SttPipelineRestartedAt(row model.SttRecoveryRow, facts SttRecoveryFacts) time.Time {
	restart := facts.AudioModified
	if facts.NewestAudioModified.After(restart) {
		restart = facts.NewestAudioModified
	}
	if claimed, ok := parseSttTime(row.SttRecoveryClaimedAt); ok && claimed.After(restart) {
		restart = claimed
	}
	if row.Status == model.StatusTranscribing && row.UpdatedAt != row.SttSummaryRedriveClaimedAt {
		if updated, ok := parseSttTime(row.UpdatedAt); ok && updated.After(restart) {
			restart = updated
		}
	}
	return restart
}

// ClassifySttRecovery is the pure candidate predicate. Order matters: every
// exclusion runs before any re-drive decision.
func ClassifySttRecovery(row model.SttRecoveryRow, facts SttRecoveryFacts, now time.Time) SttRecoveryDecision {
	skip := func(reason string) SttRecoveryDecision {
		return SttRecoveryDecision{Action: SttRecoverySkip, Reason: reason}
	}
	switch row.Status {
	case model.StatusTranscribing, model.StatusError, model.StatusRecording:
	default:
		return skip("status")
	}
	if !SafeSttIdentifier(row.UserID) || !SafeSttIdentifier(row.MeetingID) {
		return skip("identity")
	}
	if row.AudioCrop != nil {
		return skip("audio_crop")
	}
	if row.SummarizeRetryClaimedAt != "" || row.SummaryRetryPending || row.SummaryConflictCode != "" {
		return skip("summary_state")
	}
	if model.IsTerminalSttFailure(row.SttFailureCode) {
		if SttFailureMarkCurrent(row) {
			return SttRecoveryDecision{Action: SttRecoveryTerminal, Reason: strings.ToLower(row.SttFailureCode)}
		}
		if row.Status != model.StatusTranscribing {
			return skip("terminal_mark_superseded")
		}
		// A later user action restarted the pipeline; the old mark is stale.
	}
	keys := row.AudioKeys
	if len(keys) == 0 && row.AudioKey != "" {
		keys = []string{row.AudioKey}
	}
	if len(keys) == 0 {
		if facts.CheckpointFound {
			return SttRecoveryDecision{Action: SttRecoveryReportCheckpointOnly, Reason: "checkpoint_only"}
		}
		return skip("no_audio")
	}
	if len(keys) > 1 || row.AudioPartCount > 1 {
		return SttRecoveryDecision{Action: SttRecoveryReportMultipart, Reason: "multipart"}
	}
	if len(row.AudioKeys) > 0 {
		// The claim binds only the single-file audioKey attribute.
		return SttRecoveryDecision{Action: SttRecoveryReport, Reason: "audio_keys_list"}
	}
	key := keys[0]
	if row.Status == model.StatusRecording {
		return skip("recording")
	}
	if !SttRecoveryAudioKeyAllowed(row.UserID, row.MeetingID, key) {
		return SttRecoveryDecision{Action: SttRecoveryReport, Reason: "audio_key"}
	}
	if row.SttProvider != "" && row.SttProvider != "whisper" {
		return SttRecoveryDecision{Action: SttRecoveryReportUnsupported, Reason: "provider"}
	}
	if !facts.AudioFound || facts.AudioSize < SttRecoveryMinAudioBytes {
		return SttRecoveryDecision{Action: SttRecoveryReport, Reason: "audio_missing"}
	}
	// Both budgets are bound to the audio key they were spent on.
	attempts, summaryAttempts := 0, 0
	if row.SttRecoveryAudioKey == key {
		attempts, summaryAttempts = row.SttRecoveryAttempts, row.SttSummaryRedriveAttempts
	}
	if model.IsRetryableSttFailure(row.SttFailureCode) && SttFailureMarkCurrent(row) {
		if failed, ok := parseSttTime(row.SttFailedAt); ok && now.Before(failed.Add(SttRecoveryRetryDelay)) {
			return skip("in_flight")
		}
	}
	if facts.TranscriptFound && facts.TranscriptModified.After(SttPipelineRestartedAt(row, facts)) {
		if now.Sub(facts.TranscriptModified) < SttSummaryRedriveMinAge {
			return skip("transcript_fresh")
		}
		// An earlier re-drive may still be summarizing; give it the same window
		// before spending another attempt on a duplicate run.
		if claimed, ok := parseSttTime(row.SttSummaryRedriveClaimedAt); ok && now.Sub(claimed) < SttSummaryRedriveMinAge {
			return skip("summary_redrive_in_flight")
		}
		if summaryAttempts >= model.MaxSttRecoveryAttempts {
			return SttRecoveryDecision{Action: SttRecoveryReport, Reason: "summary_redrive_exhausted"}
		}
		return SttRecoveryDecision{Action: SttRecoveryRedriveSummary, Reason: "transcript_unpublished", Key: key, Attempt: summaryAttempts + 1}
	}
	if now.Before(SttRecoveryDeadline(row, facts)) {
		return skip("in_flight")
	}
	if attempts >= model.MaxSttRecoveryAttempts {
		return SttRecoveryDecision{Action: SttRecoveryMarkExhausted, Reason: "attempts", Key: key}
	}
	return SttRecoveryDecision{Action: SttRecoveryRedriveSTT, Reason: "stuck", Key: key, Attempt: attempts + 1}
}

// SttFailureDeletionDue reports whether a marked failed recording passed its
// grace period with no activity at all. Only UUID meeting IDs of single-file
// recordings without a speaker hint are eligible; this pre-filters rows the
// guarded repository delete would refuse anyway.
func SttFailureDeletionDue(row model.SttRecoveryRow, now time.Time) bool {
	if row.Status != model.StatusError || !model.IsDeletableSttFailure(row.SttFailureCode) || !SttFailureMarkCurrent(row) {
		return false
	}
	if len(row.AudioKeys) > 0 || row.AudioPartCount > 1 || row.DiarizationSpeakerHint != 0 {
		return false
	}
	failed, ok := parseSttTime(row.SttFailedAt)
	if !ok || now.Sub(failed) < SttFailureDeletionGrace {
		return false
	}
	return sttMeetingUUID.MatchString(row.MeetingID) && SafeSttIdentifier(row.UserID) && row.AudioCrop == nil
}
