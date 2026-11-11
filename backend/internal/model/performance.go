package model

import "fmt"

// Owner-private SA performance rows live in the caller's USER# partition.
const (
	PrefixPerformanceSource     = "PERFSRC#"
	PrefixPerformanceBatch      = "PERFBATCH#"
	PrefixPerformanceAssignment = "PERFASSIGN#"
	PrefixPerformanceTask       = "PERFTASK#"
	PrefixPerformanceMapping    = "PERFMAP#"
	PrefixPerformanceGoals      = "PERFGOAL#"
	PrefixPerformanceReport     = "PERFREPORT#"
)

// Valid performance sync sources. No other source is accepted.
const (
	PerformanceSourceSFDCAssignments = "sfdc-assignments"
	PerformanceSourceSFDCTasks       = "sfdc-tasks"
)

// PerformanceGoalsMaxBytes bounds the goals text in UTF-8 bytes.
const PerformanceGoalsMaxBytes = 8000

// SFDCAssignment is an account assigned to the owner. Role is the registry
// assignmentSource (for example REGISTRY) or "activity" for accounts derived
// from the owner's SA Tasks. It is stored as a plain string.
type SFDCAssignment struct {
	SFDCAccountID string `json:"sfdcAccountId" dynamodbav:"sfdcAccountId"`
	Name          string `json:"name" dynamodbav:"name"`
	Role          string `json:"role" dynamodbav:"role"`
}

// SFDCTask carries only metadata; descriptions and comments are never stored.
type SFDCTask struct {
	TaskID string `json:"taskId" dynamodbav:"taskId"`
	// SFDCAccountID may be empty: tasks without an account stay unmapped.
	SFDCAccountID string `json:"sfdcAccountId" dynamodbav:"sfdcAccountId,omitempty"`
	// ActivityDate is YYYY-MM-DD.
	ActivityDate string `json:"activityDate" dynamodbav:"activityDate"`
	Type         string `json:"type" dynamodbav:"type"`
	Subject      string `json:"subject" dynamodbav:"subject"`
	Status       string `json:"status" dynamodbav:"status"`
	SpecReqID    string `json:"specReqId,omitempty" dynamodbav:"specReqId,omitempty"`
}

// PerformanceMutation is one upsert or remove. Remove uses ExternalID, the
// SFDC account ID (assignments) or task ID (tasks).
type PerformanceMutation struct {
	Op         string          `json:"op"`
	Assignment *SFDCAssignment `json:"assignment,omitempty"`
	Task       *SFDCTask       `json:"task,omitempty"`
	ExternalID string          `json:"externalId,omitempty"`
}

type PerformanceSyncRequest struct {
	BatchID          string                `json:"batchId"`
	ExpectedRevision int64                 `json:"expectedRevision"`
	Complete         bool                  `json:"complete"`
	Mutations        []PerformanceMutation `json:"mutations"`
}

type PerformanceSyncReceipt struct {
	Revision  int64 `json:"revision" dynamodbav:"revision"`
	Completed bool  `json:"completed" dynamodbav:"completed"`
	Upserted  int   `json:"upserted" dynamodbav:"upserted"`
	Removed   int   `json:"removed" dynamodbav:"removed"`
	// Replayed marks a stored receipt returned for an identical retry. It is
	// never serialized or persisted.
	Replayed bool `json:"-" dynamodbav:"-"`
}

// AccountMapping links an SFDC account to a TTOBAK account. A manual mapping
// always wins over an automatic one.
type AccountMapping struct {
	SFDCAccountID string `json:"sfdcAccountId" dynamodbav:"sfdcAccountId"`
	AccountID     string `json:"accountId" dynamodbav:"accountId"`
	Manual        bool   `json:"manual" dynamodbav:"manual"`
	UpdatedAt     string `json:"updatedAt" dynamodbav:"updatedAt"`
}

type PerformanceGoals struct {
	Year      int    `json:"year" dynamodbav:"year"`
	Text      string `json:"text" dynamodbav:"text"`
	UpdatedAt string `json:"updatedAt" dynamodbav:"updatedAt"`
}

func PerformanceSourceSortKey(source string) string { return PrefixPerformanceSource + source }

func PerformanceBatchSortKey(source, batchID string) string {
	return PrefixPerformanceBatch + source + "#" + batchID
}

func PerformanceAssignmentSortKey(sfdcAccountID string) string {
	return PrefixPerformanceAssignment + sfdcAccountID
}

func PerformanceTaskSortKey(taskID string) string { return PrefixPerformanceTask + taskID }

func PerformanceMappingSortKey(sfdcAccountID string) string {
	return PrefixPerformanceMapping + sfdcAccountID
}

func PerformanceGoalsSortKey(year int) string {
	return fmt.Sprintf("%s%04d", PrefixPerformanceGoals, year)
}

// PerformanceReportSortKey takes a period key already checked by
// PerformancePeriodBounds.
func PerformanceReportSortKey(periodKey string) string { return PrefixPerformanceReport + periodKey }

// PerformanceSourceStatus is the owner's sync state for one source. A source
// that has never synced reports revision 0 and no timestamps.
type PerformanceSourceStatus struct {
	Source          string `json:"source" dynamodbav:"source"`
	Revision        int64  `json:"revision" dynamodbav:"revision"`
	LastAttemptAt   string `json:"lastAttemptAt,omitempty" dynamodbav:"lastAttemptAt,omitempty"`
	LastCompletedAt string `json:"lastCompletedAt,omitempty" dynamodbav:"lastCompletedAt,omitempty"`
}

type PerformanceSourceList struct {
	Sources []PerformanceSourceStatus `json:"sources"`
}

// PerformanceMappingItem is one assignment joined with its mapping, if any.
// Manual tells a manual mapping from an automatic one. A mapping whose target
// the owner can no longer read is reported unmapped with Reason "unreadable"
// and no AccountID.
type PerformanceMappingItem struct {
	SFDCAccountID string `json:"sfdcAccountId"`
	Name          string `json:"name"`
	Role          string `json:"role"`
	Mapped        bool   `json:"mapped"`
	AccountID     string `json:"accountId,omitempty"`
	Manual        bool   `json:"manual"`
	Reason        string `json:"reason,omitempty"`
	UpdatedAt     string `json:"updatedAt,omitempty"`
}

// PerformanceMappingUnreadable is the Reason of a mapping whose target account
// the owner can no longer read.
const PerformanceMappingUnreadable = "unreadable"

type PerformanceMappingList struct {
	Items []PerformanceMappingItem `json:"items"`
	// Truncated is true when more assignments exist than Items holds.
	Truncated bool `json:"truncated"`
}

type PerformanceGoalsInput struct {
	Text *string `json:"text"`
}

type PerformanceMappingInput struct {
	AccountID string `json:"accountId"`
}

// PerformanceMissedItem is one logging gap or silent customer. RefKind and
// RefID name the touchpoint (calendar event, meeting or SFDC task) behind it.
// At is RFC3339 UTC. DaysSince counts Seoul calendar days from At to the
// evaluated period end; a silent customer with no touchpoint in the lookback
// window has an empty At and DaysSince -1.
type PerformanceMissedItem struct {
	Kind          string `json:"kind"`
	AccountID     string `json:"accountId"`
	SFDCAccountID string `json:"sfdcAccountId,omitempty"`
	RefKind       string `json:"refKind,omitempty"`
	RefID         string `json:"refId,omitempty"`
	At            string `json:"at,omitempty"`
	DaysSince     int    `json:"daysSince"`
}

// PerformanceSourceFreshness reports when a source last completed a sync.
type PerformanceSourceFreshness struct {
	Source          string `json:"source"`
	LastCompletedAt string `json:"lastCompletedAt,omitempty"`
}

// CalendarCoverageStart is the earliest start (RFC3339 UTC) among the calendar
// events read for the request, or empty when none were read. Calendar-derived
// gaps cover only events from that time on.
type PerformanceMissedList struct {
	Items                 []PerformanceMissedItem      `json:"items"`
	UnmappedCount         int                          `json:"unmappedCount"`
	SourceFreshness       []PerformanceSourceFreshness `json:"sourceFreshness"`
	CalendarCoverageStart string                       `json:"calendarCoverageStart"`
}
