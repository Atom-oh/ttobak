package model

import "time"

// Customer update agent runs. One lease row per user (USER#{u} / CURUN) names
// the run in flight; day counters (USER#{u} / CUDAY#{yyyymmdd}, KST day) bound
// drafts per day. The agent runtime writes result rows under CURUN#{runId}
// (contracts/customer-updates/run-v1.json resultRows).
const (
	SKUpdateRun        = "CURUN"
	PrefixUpdateDay    = "CUDAY#"
	PrefixUpdateRunPK  = "CURUN#"
	SKUpdateRunClaim   = "CLAIM"
	SKUpdateRunDone    = "DONE"
	PrefixRunItemSK    = "ITEM#"
	PrefixRunDecSK     = "DEC#"
	PrefixRunDraftSK   = "DRAFT#"
	UpdateRunLeaseTail = 7 * 24 * time.Hour
	UpdateDayTTL       = 3 * 24 * time.Hour

	// SKUpdateItemCursor holds a user's item scan position (attribute
	// itemCursor, the last evaluated item sort key).
	SKUpdateItemCursor  = "CUCURSOR"
	UpdateItemCursorTTL = 30 * 24 * time.Hour

	MaxUpdateRunItems    = 30
	MaxUpdateRunAccounts = 40
	// MaxUpdateRunRows bounds the result rows read or deleted for one run.
	MaxUpdateRunRows = 2000
)

func UpdateDaySK(day string) string   { return PrefixUpdateDay + day }
func UpdateRunPK(runID string) string { return PrefixUpdateRunPK + runID }

// UpdateRun is a user's run lease. The row's TTL is LeaseUntil + 7 days; an
// expired lease is removed by the tick's expiry path, not overwritten.
type UpdateRun struct {
	RunID      string    `json:"runId" dynamodbav:"runId"`
	StartedAt  time.Time `json:"startedAt" dynamodbav:"startedAt"`
	LeaseUntil time.Time `json:"leaseUntil" dynamodbav:"leaseUntil"`
	ItemHashes []string  `json:"itemHashes" dynamodbav:"itemHashes"`
	AccountIDs []string  `json:"accountIds" dynamodbav:"accountIds"`
}

// UpdateDay counts the drafts accepted for a user on one KST day, and the
// (item, account) pairs researched that day (accepted DEC rows whose outcome
// was drafted or discarded).
type UpdateDay struct {
	Total      int            `json:"total"`
	PerAccount map[string]int `json:"perAccount"`
	Researched int            `json:"researched"`
}

// UpdatePendingMark is one pending-work marker (PK CUPENDING, SK userId).
// MarkedAt is the stored marker value verbatim ("" for a legacy marker
// without one); clearing is conditional on it.
type UpdatePendingMark struct {
	UserID   string
	MarkedAt string
}

// RunItemRow is an ITEM#{urlHash} result row.
type RunItemRow struct {
	URLHash string
	State   string
	Reason  string
}

// RunDecisionRow is an opened DEC#{urlHash}#{accountId} result row.
type RunDecisionRow struct {
	URLHash       string
	AccountID     string
	Choice        string
	Score         float64
	Backend       string
	Outcome       string
	DiscardReason string
	Reason        string
	// GateInput is set only for a needed choice.
	GateInput *UpdateGateInput
}

// RunDraftRow is an opened DRAFT#{accountId}#{urlHash} result row. Text is raw
// and unescaped.
type RunDraftRow struct {
	AccountID      string
	URLHash        string
	SourceURL      string
	SourceTitle    string
	Why            string
	Body           string
	Links          []string
	OpportunityIDs []string
	FetchedURLs    []string
}

// RunRows is the classified content of one run's result partition. Malformed
// counts rows with a bad key, wrong plaintext types or an over-bound payload;
// Unreadable counts rows whose sealed payload does not open or parse.
type RunRows struct {
	Done       bool
	FinishedAt time.Time
	Items      []RunItemRow
	Decisions  []RunDecisionRow
	Drafts     []RunDraftRow
	Malformed  int
	Unreadable int
}
