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
	PK              string `dynamodbav:"PK"`
	SK              string `dynamodbav:"SK"`
	SourceAccountID string `dynamodbav:"sourceAccountId"`
	TargetAccountID string `dynamodbav:"targetAccountId"`
	RequesterUserID string `dynamodbav:"requesterUserId"`
	State           string `dynamodbav:"state"`
	Step            string `dynamodbav:"step"`
	Cursor          string `dynamodbav:"cursor,omitempty"`
	ErrorCode       string `dynamodbav:"errorCode,omitempty"`
	LeaseOwner      string `dynamodbav:"leaseOwner,omitempty"`
	// LeaseUntil is the lease expiry in Unix seconds. Another worker may take
	// the lease over only after it passes, and a takeover always replaces
	// LeaseOwner, so the previous holder's fenced writes fail.
	LeaseUntil    int64 `dynamodbav:"leaseUntil,omitempty"`
	AcceptUnmoved bool  `dynamodbav:"acceptUnmoved,omitempty"`
	// Counts holds per-step totals keyed "{step}.{moved|skipped|resumed|unreadable|keptSeparate}".
	Counts     map[string]int `dynamodbav:"counts,omitempty"`
	StartedAt  time.Time      `dynamodbav:"startedAt"`
	UpdatedAt  time.Time      `dynamodbav:"updatedAt"`
	FinishedAt *time.Time     `dynamodbav:"finishedAt,omitempty"`
	EntityType string         `dynamodbav:"entityType"` // "ACCOUNT_MERGE_JOB"
}

// AccountMergeContactMap records which target contact a source contact became.
type AccountMergeContactMap struct {
	PK              string `dynamodbav:"PK"`
	SK              string `dynamodbav:"SK"`
	SourceContactID string `dynamodbav:"sourceContactId"`
	TargetContactID string `dynamodbav:"targetContactId"`
	EntityType      string `dynamodbav:"entityType"` // "ACCOUNT_MERGE_CONTACT_MAP"
}

// Preview unmoved-category and CLI-only names, in response order.
var (
	MergeUnmovedCategories = []string{"newsSources", "kb", "documents", "calendarShares", "dictionary", "billing"}
	MergeCLIOnlyCategories = []string{"specreqTasks", "calendarEvents", "linkOnlyMeetings"}
)

// MergeUnmovedCategory names a category v1 does not move and whether the
// source account holds any of it.
type MergeUnmovedCategory struct {
	Category string `json:"category"`
	Present  bool   `json:"present"`
	// Unverifiable marks a category the API cannot probe; it is reported
	// present so the caller must accept it.
	Unverifiable bool `json:"unverifiable,omitempty"`
}

// MergeAudience describes who gains access to the source's moved content.
type MergeAudience struct {
	IntoMembersNotInFrom int  `json:"intoMembersNotInFrom"`
	IntoHasAncestors     bool `json:"intoHasAncestors"`
}

// AccountMergePreview is the read-only inventory of a proposed merge. A count
// of -1 means the category exceeded its bounded read cap.
type AccountMergePreview struct {
	FromAccountID         string                 `json:"fromAccountId"`
	IntoAccountID         string                 `json:"intoAccountId"`
	Counts                map[string]int         `json:"counts"`
	Unmoved               []MergeUnmovedCategory `json:"unmoved"`
	CLIOnly               []string               `json:"cliOnly"`
	NewAudience           MergeAudience          `json:"newAudience"`
	RequiresAcceptUnmoved bool                   `json:"requiresAcceptUnmoved"`
}

// StartAccountMergeRequest is the body of POST /api/accounts/{accountId}/merge.
type StartAccountMergeRequest struct {
	IntoAccountID string `json:"intoAccountId"`
	AcceptUnmoved bool   `json:"acceptUnmoved"`
}

// AccountMergeJobDTO is the progress view of a merge job. Lease and cursor
// details stay internal.
type AccountMergeJobDTO struct {
	FromAccountID string         `json:"fromAccountId"`
	IntoAccountID string         `json:"intoAccountId"`
	State         string         `json:"state"`
	Step          string         `json:"step"`
	ErrorCode     string         `json:"errorCode,omitempty"`
	Counts        map[string]int `json:"counts"`
	StartedAt     time.Time      `json:"startedAt"`
	UpdatedAt     time.Time      `json:"updatedAt"`
	FinishedAt    *time.Time     `json:"finishedAt,omitempty"`
}

// DTO returns the public progress view of the job.
func (j *AccountMergeJob) DTO() *AccountMergeJobDTO {
	counts := make(map[string]int, len(j.Counts))
	for k, v := range j.Counts {
		counts[k] = v
	}
	return &AccountMergeJobDTO{FromAccountID: j.SourceAccountID, IntoAccountID: j.TargetAccountID, State: j.State,
		Step: j.Step, ErrorCode: j.ErrorCode, Counts: counts, StartedAt: j.StartedAt, UpdatedAt: j.UpdatedAt, FinishedAt: j.FinishedAt}
}

// MergeStepIndex returns the position of step in MergeSteps, or -1.
func MergeStepIndex(step string) int {
	for i, s := range MergeSteps {
		if s == step {
			return i
		}
	}
	return -1
}
