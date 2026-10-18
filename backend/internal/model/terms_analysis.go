package model

import (
	"fmt"
	"time"
)

// Automatic term extraction run state, MEETING#{mid}/ANALYSIS#terms. Like
// ANALYSIS#actionItems it is separate from the meeting item so unrelated
// whole-item writes cannot erase an active claim. It stores hashes, enums and
// counts only, never term text, quotes or prompts.
const (
	TermsAnalysisSK         = "ANALYSIS#terms"
	EntityTypeTermsAnalysis = "TERMS_ANALYSIS"

	// Error codes beyond the shared Analysis* ones. TAINTED_SUGGEST_ONLY marks
	// a succeeded run whose survivors were only suggested (A2.1).
	TermsErrorDisabled           = "DISABLED"
	TermsErrorTaintedSuggestOnly = "TAINTED_SUGGEST_ONLY"

	// Evidence source types stored on terms, observations and accepted terms.
	TermSourceTranscript = "transcript"
	TermSourceNotes      = "notes"
	TermSourceDocument   = "document"

	// MaxTermsAccepted bounds acceptedTerms. Every observation a meeting
	// creates must be listed there, because deletion cleanup finds the
	// meeting's DICTOBS rows only through it.
	MaxTermsAccepted = 40
)

// TermsAcceptedTerm identifies one term this meeting has an observation for.
type TermsAcceptedTerm struct {
	TermKey     string   `dynamodbav:"termKey" json:"termKey"`
	SourceTypes []string `dynamodbav:"sourceTypes,omitempty" json:"sourceTypes,omitempty"`
}

// TermsCounters summarizes one merge; counts only.
type TermsCounters struct {
	Candidates       int `dynamodbav:"candidates" json:"candidates"`
	Created          int `dynamodbav:"created" json:"created"`
	Suggested        int `dynamodbav:"suggested" json:"suggested"`
	Merged           int `dynamodbav:"merged" json:"merged"`
	Promoted         int `dynamodbav:"promoted" json:"promoted"`
	AlreadyCounted   int `dynamodbav:"alreadyCounted" json:"alreadyCounted"`
	SkippedTombstone int `dynamodbav:"skippedTombstone" json:"skippedTombstone"`
	SkippedManual    int `dynamodbav:"skippedManual" json:"skippedManual"`
	SkippedTainted   int `dynamodbav:"skippedTainted" json:"skippedTainted"`
	Invalid          int `dynamodbav:"invalid" json:"invalid"`
	Capped           int `dynamodbav:"capped" json:"capped"`
	AliasesDropped   int `dynamodbav:"aliasesDropped" json:"aliasesDropped"`
	Conflicts        int `dynamodbav:"conflicts" json:"conflicts"`
	Evicted          int `dynamodbav:"evicted" json:"evicted"`
	ObsPruned        int `dynamodbav:"obsPruned" json:"obsPruned"`
}

// TermsAnalysis is the per-meeting extraction run. SucceededSourceHash is the
// sourceHash of the last succeeded run; a new run is queued only for a
// different source. AcceptedTerms accumulates across runs for the meeting.
type TermsAnalysis struct {
	OwnerID             string              `dynamodbav:"ownerId,omitempty" json:"-"` // meeting owner; binds deletion sweeps
	RunID               string              `dynamodbav:"runId" json:"runId,omitempty"`
	Status              string              `dynamodbav:"status" json:"status"`
	SourceHash          string              `dynamodbav:"sourceHash" json:"-"`
	SucceededSourceHash string              `dynamodbav:"succeededSourceHash,omitempty" json:"-"`
	SummaryGeneration   int64               `dynamodbav:"summaryGeneration" json:"-"`
	StartedAt           time.Time           `dynamodbav:"startedAt" json:"startedAt,omitempty"`
	UpdatedAt           time.Time           `dynamodbav:"updatedAt" json:"updatedAt,omitempty"`
	LeaseUntil          int64               `dynamodbav:"leaseUntil" json:"leaseUntil,omitempty"`
	ErrorCode           string              `dynamodbav:"errorCode" json:"errorCode,omitempty"`
	Counters            TermsCounters       `dynamodbav:"counters" json:"counters"`
	AcceptedTerms       []TermsAcceptedTerm `dynamodbav:"acceptedTerms,omitempty" json:"-"`
}

// Pending reports a queued or running run.
func (a *TermsAnalysis) Pending() bool {
	return a != nil && (a.Status == AnalysisQueued || a.Status == AnalysisRunning)
}

// ValidTermsErrorCode reports whether code belongs to the run error enum.
func ValidTermsErrorCode(code string) bool {
	switch code {
	case AnalysisInvalidOutput, AnalysisProviderFailed, AnalysisSourceChanged, AnalysisInterrupted,
		AnalysisPersistenceFail, TermsErrorDisabled, TermsErrorTaintedSuggestOnly:
		return true
	}
	return false
}

// ValidTermSourceType reports whether s is an evidence source type.
func ValidTermSourceType(s string) bool {
	return s == TermSourceTranscript || s == TermSourceNotes || s == TermSourceDocument
}

// ValidTermKey reports a 32-lowercase-hex term key.
func ValidTermKey(key string) bool {
	if len(key) != 32 {
		return false
	}
	for _, c := range key {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ValidateTermsAccepted enforces the acceptedTerms bound: at most 40 unique
// valid keys with known source types, so no text can be stored there.
func ValidateTermsAccepted(terms []TermsAcceptedTerm) error {
	if len(terms) > MaxTermsAccepted {
		return fmt.Errorf("acceptedTerms has %d entries, limit %d", len(terms), MaxTermsAccepted)
	}
	seen := make(map[string]bool, len(terms))
	for _, t := range terms {
		if !ValidTermKey(t.TermKey) || seen[t.TermKey] {
			return fmt.Errorf("acceptedTerms has an invalid or duplicate key")
		}
		seen[t.TermKey] = true
		for _, s := range t.SourceTypes {
			if !ValidTermSourceType(s) {
				return fmt.Errorf("acceptedTerms has an unknown source type")
			}
		}
	}
	return nil
}
