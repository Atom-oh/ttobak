package model

import "time"

// Account merge job and contact-map rows live in the source account partition.
// Job: PK ACCOUNT#{sourceId}, SK MERGE#job.
// Contact map: PK ACCOUNT#{sourceId}, SK MERGE#contact#{sourceContactId}.
const (
	SKAccountMergeJob     = "MERGE#job"
	PrefixMergeContactMap = "MERGE#contact#"

	EntityTypeAccountMergeJob        = "ACCOUNT_MERGE_JOB"
	EntityTypeAccountMergeContactMap = "ACCOUNT_MERGE_CONTACT_MAP"
)

const (
	MergeStateRunning = "running"
	MergeStateDone    = "done"
	MergeStateFailed  = "failed"
)

// MergeSteps is the ordered list of merge job steps.
var MergeSteps = []string{"members", "children", "projects", "research", "contacts", "events", "meetings", "metadata", "finish"}

// AccountMergeJob is the resumable state of one source-into-target merge.
type AccountMergeJob struct {
	PK              string         `dynamodbav:"PK"`
	SK              string         `dynamodbav:"SK"`
	SourceAccountID string         `dynamodbav:"sourceAccountId"`
	TargetAccountID string         `dynamodbav:"targetAccountId"`
	RequesterUserID string         `dynamodbav:"requesterUserId"`
	State           string         `dynamodbav:"state"`
	Step            string         `dynamodbav:"step"`
	Cursor          string         `dynamodbav:"cursor,omitempty"`
	ErrorCode       string         `dynamodbav:"errorCode,omitempty"`
	LeaseOwner      string         `dynamodbav:"leaseOwner,omitempty"`
	LeaseUntil      int64          `dynamodbav:"leaseUntil,omitempty"`
	AcceptUnmoved   bool           `dynamodbav:"acceptUnmoved,omitempty"`
	Counts          map[string]int `dynamodbav:"counts,omitempty"`
	StartedAt       time.Time      `dynamodbav:"startedAt"`
	UpdatedAt       time.Time      `dynamodbav:"updatedAt"`
	FinishedAt      *time.Time     `dynamodbav:"finishedAt,omitempty"`
	EntityType      string         `dynamodbav:"entityType"` // "ACCOUNT_MERGE_JOB"
}

// AccountMergeContactMap records which target contact a source contact became.
type AccountMergeContactMap struct {
	PK              string `dynamodbav:"PK"`
	SK              string `dynamodbav:"SK"`
	SourceContactID string `dynamodbav:"sourceContactId"`
	TargetContactID string `dynamodbav:"targetContactId"`
	EntityType      string `dynamodbav:"entityType"` // "ACCOUNT_MERGE_CONTACT_MAP"
}
