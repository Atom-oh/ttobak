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

// Present distinguishes an absent attribute from its explicit empty value.
// Values retain DynamoDB's original strings, including timestamp formatting.
type SummaryValue struct {
	Present bool        `json:"present"`
	Value   interface{} `json:"value,omitempty"`
}
type SummaryCheck struct {
	PK     string                  `json:"pk"`
	SK     string                  `json:"sk"`
	Exists bool                    `json:"exists"`
	Fields map[string]SummaryValue `json:"fields,omitempty"`
}
type SummarySnapshot struct {
	Meeting     *Meeting
	Attachments []Attachment
	TextStates  map[string]*AttachmentTextState
	Checks      []SummaryCheck // first check is the canonical meeting
	Stored      map[string]interface{}
}
type SummaryObject struct {
	Bucket    string `json:"bucket"`
	Key       string `json:"key"`
	ETag      string `json:"etag"`
	VersionID string `json:"versionId,omitempty"`
}
