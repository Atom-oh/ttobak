package model

import "time"

const (
	AttachmentTextUnknown   = "unknown"
	PrefixAttachmentText    = "ATTEXT#"
	AttachmentTextQueued    = "queued"
	AttachmentTextRunning   = "running"
	AttachmentTextSucceeded = "succeeded"
	AttachmentTextPartial   = "partial"
	AttachmentTextFailed    = "failed"
)

// AttachmentTextState is separate from upload/image metadata, so those writers
// cannot erase a running extraction or replace its result with a stale snapshot.
type AttachmentTextState struct {
	RunID      string    `dynamodbav:"runId" json:"runId,omitempty"`
	Status     string    `dynamodbav:"status" json:"status"`
	LeaseUntil int64     `dynamodbav:"leaseUntil" json:"leaseUntil,omitempty"`
	SourceKey  string    `dynamodbav:"sourceKey" json:"-"`
	OwnerID    string    `dynamodbav:"ownerId" json:"-"`
	UploaderID string    `dynamodbav:"uploaderId" json:"-"`
	SourceETag string    `dynamodbav:"sourceETag,omitempty" json:"-"`
	ResultKey  string    `dynamodbav:"resultKey,omitempty" json:"-"`
	ErrorCode  string    `dynamodbav:"errorCode,omitempty" json:"errorCode,omitempty"`
	UnitCount  int       `dynamodbav:"unitCount,omitempty" json:"unitCount,omitempty"`
	Complete   bool      `dynamodbav:"complete" json:"complete"`
	UpdatedAt  time.Time `dynamodbav:"updatedAt" json:"updatedAt"`
}

func (s *AttachmentTextState) Pending() bool {
	return s != nil && (s.Status == AttachmentTextQueued || s.Status == AttachmentTextRunning)
}

type DocumentUploadCompleted struct {
	Bucket       string `json:"bucket"`
	Key          string `json:"key"`
	MeetingID    string `json:"meetingId"`
	OwnerID      string `json:"ownerId"`
	UserID       string `json:"userId"`
	AttachmentID string `json:"attachmentId"`
	RunID        string `json:"runId"`
}

type AttachmentTextUnit struct {
	Text     string                 `json:"text"`
	Location map[string]interface{} `json:"location"`
}

type AttachmentTextResult struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Format        string               `json:"format"`
	Status        string               `json:"status"`
	Complete      bool                 `json:"complete"`
	Scope         string               `json:"scope"`
	Units         []AttachmentTextUnit `json:"units"`
	Warnings      []interface{}        `json:"warnings,omitempty"`
	Source        AttachmentTextSource `json:"source"`
	Metrics       struct {
		Units     int `json:"units"`
		TextBytes int `json:"textBytes"`
	} `json:"metrics"`
}

// Source is the immutable result object's provenance, independent of the
// current attempt's runId (a failed retry can retain an older result).
type AttachmentTextSource struct {
	Bucket       string `json:"bucket"`
	Key          string `json:"key"`
	ETag         string `json:"eTag"`
	MeetingID    string `json:"meetingId"`
	OwnerID      string `json:"ownerId"`
	UploaderID   string `json:"uploaderId"`
	AttachmentID string `json:"attachmentId"`
	RunID        string `json:"runId"`
}

// Complete/UnitCount describe this attempt only. HasResult can be true when a
// failed or pending attempt retains an older result, available on the text route.
type AttachmentTextStatus struct {
	Status           string     `json:"status"`
	RunID            string     `json:"runId,omitempty"`
	ErrorCode        string     `json:"errorCode,omitempty"`
	LeaseUntil       int64      `json:"leaseUntil,omitempty"`
	UpdatedAt        *time.Time `json:"updatedAt,omitempty"`
	UnitCount        int        `json:"unitCount"`
	Complete         bool       `json:"complete"`
	HasResult        bool       `json:"hasResult"`
	NeedsResummary   bool       `json:"needsResummary"`
	SummaryExcerpted bool       `json:"summaryExcerpted"`
}
