package model

// Machine-readable STT failure codes (ADR-031 amendment). A code is honored only while
// the meeting is still status=error and its raw updatedAt equals sttFailedAt:
// any later write bumps updatedAt and permanently cancels the mark's effect.
const (
	// SttFailureAudioCorrupt: the Whisper worker could not decode the bound
	// audio (or it decoded to effectively nothing). Terminal; delete-eligible.
	SttFailureAudioCorrupt = "AUDIO_CORRUPT"
	// SttFailureNoSpeech: summarize received a decodable transcript with no
	// text at all. Terminal; delete-eligible.
	SttFailureNoSpeech = "NO_SPEECH"
	// SttFailureWorkerFailed: any other Whisper worker exception. Retryable
	// within the automatic attempt budget.
	SttFailureWorkerFailed = "WORKER_FAILED"
	// SttFailureRunTaskFailed / SttFailureRedriveFailed: the recovery tick
	// could not start its claimed re-run. Retryable.
	SttFailureRunTaskFailed = "RUNTASK_FAILED"
	SttFailureRedriveFailed = "REDRIVE_FAILED"
	// SttFailureRecoveryExhausted: the automatic budget is spent. Terminal,
	// never delete-eligible.
	SttFailureRecoveryExhausted = "RECOVERY_EXHAUSTED"
)

// MaxSttRecoveryAttempts bounds automatic STT and summary re-drives per audio key.
const MaxSttRecoveryAttempts = 2

// IsRetryableSttFailure reports whether the recovery tick may re-run a code.
func IsRetryableSttFailure(code string) bool {
	switch code {
	case SttFailureWorkerFailed, SttFailureRunTaskFailed, SttFailureRedriveFailed:
		return true
	}
	return false
}

// IsTerminalSttFailure reports codes that stop automatic re-runs.
func IsTerminalSttFailure(code string) bool {
	switch code {
	case SttFailureAudioCorrupt, SttFailureNoSpeech, SttFailureRecoveryExhausted:
		return true
	}
	return false
}

// IsDeletableSttFailure reports codes the 7-day grace delete may act on.
func IsDeletableSttFailure(code string) bool {
	return code == SttFailureAudioCorrupt || code == SttFailureNoSpeech
}

// SttRecoveryRow is the ID/state-only projection the recovery tick scans.
// Timestamps stay raw strings so conditions compare exactly what is stored.
// It deliberately has no title, notes, content, transcript or summary field.
type SttRecoveryRow struct {
	MeetingID                 string      `dynamodbav:"meetingId"`
	UserID                    string      `dynamodbav:"userId"`
	Status                    string      `dynamodbav:"status"`
	UpdatedAt                 string      `dynamodbav:"updatedAt"`
	CreatedAt                 string      `dynamodbav:"createdAt"`
	AudioKey                  string      `dynamodbav:"audioKey"`
	AudioKeys                 []string    `dynamodbav:"audioKeys"`
	AudioPartCount            int         `dynamodbav:"audioPartCount"`
	AudioCrop                 interface{} `dynamodbav:"audioCrop"`
	SttProvider               string      `dynamodbav:"sttProvider"`
	SummarizeRetryClaimedAt   string      `dynamodbav:"summarizeRetryClaimedAt"`
	SummaryRetryPending       bool        `dynamodbav:"summaryRetryPending"`
	SummaryConflictCode       string      `dynamodbav:"summaryConflictCode"`
	SttFailureCode            string      `dynamodbav:"sttFailureCode"`
	SttFailedAt               string      `dynamodbav:"sttFailedAt"`
	SttRecoveryAttempts       int         `dynamodbav:"sttRecoveryAttempts"`
	SttRecoveryAudioKey       string      `dynamodbav:"sttRecoveryAudioKey"`
	SttRecoveryClaimedAt      string      `dynamodbav:"sttRecoveryClaimedAt"`
	SttSummaryRedriveAttempts int         `dynamodbav:"sttSummaryRedriveAttempts"`
	DiarizationSpeakerHint    int         `dynamodbav:"diarizationSpeakerHint"`
	// Present records which projected attributes existed, so claims can pin
	// "absent" separately from a stored zero value.
	Present map[string]bool `dynamodbav:"-"`
}

// SttRecoveryProjection lists exactly the attributes SttRecoveryRow reads.
var SttRecoveryProjection = []string{
	"meetingId", "userId", "status", "updatedAt", "createdAt", "audioKey", "audioKeys",
	"audioPartCount", "audioCrop", "sttProvider", "summarizeRetryClaimedAt", "summaryRetryPending",
	"summaryConflictCode", "sttFailureCode", "sttFailedAt", "sttRecoveryAttempts", "sttRecoveryAudioKey",
	"sttRecoveryClaimedAt", "sttSummaryRedriveAttempts", "diarizationSpeakerHint",
}

// SttFailureMark identifies the exact mark a grace deletion is conditioned on.
type SttFailureMark struct {
	Code     string
	FailedAt string
}
