package model

import "time"

// PrefixSpecReqTask prefixes the sort key of a private per-occurrence SpecReq
// task in the user partition (USER#{userId}).
const PrefixSpecReqTask = "SRTASK#"

// SpecReqTaskSortKey returns the sort key of the task for a calendar event ID.
func SpecReqTaskSortKey(eventID string) string {
	return PrefixSpecReqTask + eventID
}

// Task paths: who issues the SpecReq.
const (
	SpecReqPathMine   = "mine"
	SpecReqPathTheirs = "theirs"
)

// Stored task states. "ack_needed" is a runner-local notice and never stored.
const (
	SpecReqStateIssueNeeded   = "issue_needed"
	SpecReqStateIssued        = "issued"
	SpecReqStateRequestNeeded = "request_needed"
	SpecReqStateRequested     = "requested"
	SpecReqStateAcked         = "acked"
	SpecReqStateAckFailed     = "ack_failed"
	SpecReqStateResolved      = "resolved"
	SpecReqStateNeedsAccount  = "needs_account"
	SpecReqStateNeedsOwner    = "needs_owner"
	SpecReqStateClosed        = "closed"
)

// Failure codes recorded with ack_failed.
const (
	SpecReqFailAckRejected    = "ACK_REJECTED"
	SpecReqFailAckUnconfirmed = "ACK_UNCONFIRMED"
	SpecReqFailAckNotEligible = "ACK_NOT_ELIGIBLE"
)

// SpecReq statuses as defined by the provider tool schema.
const (
	SpecReqStatusPending   = "PENDING"
	SpecReqStatusNextUp    = "NEXT_UP"
	SpecReqStatusWorking   = "WORKING"
	SpecReqStatusWaiting   = "WAITING"
	SpecReqStatusCompleted = "COMPLETED"
	SpecReqStatusCancelled = "CANCELLED"
)

// SpecReqStatuses lists every accepted SpecReq status.
var SpecReqStatuses = []string{"PENDING", "NEXT_UP", "WORKING", "WAITING", "COMPLETED", "CANCELLED"}

// Transition operations.
const (
	SpecReqOpClassify       = "classify"
	SpecReqOpPickAccount    = "pickAccount"
	SpecReqOpPickOwner      = "pickOwner"
	SpecReqOpMarkRequested  = "markRequested"
	SpecReqOpLinkSpecReq    = "linkSpecReq"
	SpecReqOpRecordAck      = "recordAck"
	SpecReqOpRefreshSpecReq = "refreshSpecReq"
	SpecReqOpClose          = "close"
)

// SpecReqTask is a private task for one calendar occurrence. It never holds
// attendee addresses, mail, Slack data or SpecReq bodies.
type SpecReqTask struct {
	EventID            string    `json:"eventId"`
	SourceID           string    `json:"sourceId"`
	AccountID          string    `json:"accountId,omitempty"`
	Path               string    `json:"path,omitempty"`
	State              string    `json:"state"`
	CounterpartUserID  string    `json:"counterpartUserId,omitempty"`
	SpecReqID          string    `json:"specReqId,omitempty"`
	SpecReqTitle       string    `json:"specReqTitle,omitempty"`
	SpecReqStatus      string    `json:"specReqStatus,omitempty"`
	FailureCode        string    `json:"failureCode,omitempty"`
	OccurrenceStartsAt time.Time `json:"occurrenceStartsAt"`
	OccurrenceEndsAt   time.Time `json:"occurrenceEndsAt"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
	StateChangedAt     time.Time `json:"stateChangedAt"`
	Version            int64     `json:"version"`
}

// SpecReqTaskEvent is the calendar event summary joined onto a task view.
type SpecReqTaskEvent struct {
	Title     string    `json:"title"`
	StartsAt  time.Time `json:"startsAt"`
	EndsAt    time.Time `json:"endsAt"`
	TimeZone  string    `json:"timeZone"`
	IsAllDay  bool      `json:"isAllDay"`
	StartDate string    `json:"startDate,omitempty"`
	EndDate   string    `json:"endDate,omitempty"`
}

// SpecReqTaskView is a task with its optional event summary.
type SpecReqTaskView struct {
	SpecReqTask
	Event *SpecReqTaskEvent `json:"event,omitempty"`
}

// SpecReqTaskPage is one page of task views.
type SpecReqTaskPage struct {
	Tasks      []SpecReqTaskView `json:"tasks"`
	NextCursor *string           `json:"nextCursor,omitempty"`
}

// CreateSpecReqTaskRequest creates the task for one occurrence.
type CreateSpecReqTaskRequest struct {
	SourceID          string `json:"sourceId"`
	ExternalID        string `json:"externalId"`
	AccountID         string `json:"accountId,omitempty"`
	Path              string `json:"path,omitempty"`
	State             string `json:"state"`
	CounterpartUserID string `json:"counterpartUserId,omitempty"`
}

// SpecReqTaskTransition is one version-conditioned change of a task.
type SpecReqTaskTransition struct {
	Op                string `json:"op"`
	Version           int64  `json:"version"`
	AccountID         string `json:"accountId,omitempty"`
	CounterpartUserID string `json:"counterpartUserId,omitempty"`
	Path              string `json:"path,omitempty"`
	State             string `json:"state,omitempty"`
	SpecReqID         string `json:"specReqId,omitempty"`
	SpecReqTitle      string `json:"specReqTitle,omitempty"`
	SpecReqStatus     string `json:"specReqStatus,omitempty"`
	FailureCode       string `json:"failureCode,omitempty"`
}
