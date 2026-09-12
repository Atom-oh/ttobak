package model

import "time"

const (
	ActionAnalysisSK        = "ANALYSIS#actionItems"
	AnalysisUnknown         = "unknown"
	AnalysisQueued          = "queued"
	AnalysisRunning         = "running"
	AnalysisSucceeded       = "succeeded"
	AnalysisFailed          = "failed"
	AnalysisInvalidOutput   = "INVALID_OUTPUT"
	AnalysisProviderFailed  = "PROVIDER_FAILED"
	AnalysisPublishFailed   = "PUBLISH_FAILED"
	AnalysisInterrupted     = "INTERRUPTED"
	AnalysisSourceChanged   = "SOURCE_CHANGED"
	AnalysisPersistenceFail = "PERSISTENCE_FAILED"
)

// ActionItemsAnalysis is separate from Meeting so unrelated whole-item writes
// cannot erase an active claim. No source text or generated output is copied here.
type ActionItemsAnalysis struct {
	RunID      string    `dynamodbav:"runId" json:"runId,omitempty"`
	Status     string    `dynamodbav:"status" json:"status"`
	SourceHash string    `dynamodbav:"sourceHash" json:"-"`
	StartedAt  time.Time `dynamodbav:"startedAt" json:"startedAt,omitempty"`
	UpdatedAt  time.Time `dynamodbav:"updatedAt" json:"updatedAt,omitempty"`
	LeaseUntil int64     `dynamodbav:"leaseUntil" json:"leaseUntil,omitempty"`
	ErrorCode  string    `dynamodbav:"errorCode" json:"errorCode,omitempty"`
}

type ActionItemsRequested struct {
	OwnerID   string `json:"ownerId"`
	MeetingID string `json:"meetingId"`
	RunID     string `json:"runId"`
}

func (a *ActionItemsAnalysis) Pending() bool {
	return a != nil && (a.Status == AnalysisQueued || a.Status == AnalysisRunning)
}
