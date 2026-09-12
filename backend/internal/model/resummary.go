package model

import "time"

const ResummarySK = "ANALYSIS#summary"

// ResummaryState never stores note/transcript text. Results and success are
// published atomically; failures retain the meeting's previous content.
type ResummaryState struct {
	RunID       string    `dynamodbav:"runId"`
	Status      string    `dynamodbav:"status"`
	OwnerID     string    `dynamodbav:"ownerId"`
	RequestedBy string    `dynamodbav:"requestedBy"`
	SourceHash  string    `dynamodbav:"sourceHash"`
	LeaseUntil  int64     `dynamodbav:"leaseUntil"`
	UpdatedAt   time.Time `dynamodbav:"updatedAt"`
	ErrorCode   string    `dynamodbav:"errorCode"`
	ResultHash  string    `dynamodbav:"resultHash,omitempty"`
}

func (s *ResummaryState) Pending() bool {
	return s != nil && (s.Status == AnalysisQueued || s.Status == AnalysisRunning)
}

type SummaryRequested struct {
	MeetingID string `json:"meetingId"`
	RunID     string `json:"runId"`
}
