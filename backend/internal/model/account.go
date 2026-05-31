package model

import "time"

// Key prefixes / constants for the Account partition.
// Account META:   PK: ACCOUNT#{accountId}, SK: META
// Account member: PK: ACCOUNT#{accountId}, SK: MEMBER#{userId}
//                 (GSI1PK: USER#{userId}, GSI1SK: ACCOUNT#{accountId} for reverse lookup)
const (
	PrefixAccount = "ACCOUNT#"
	SKAccountMeta = "META"
	PrefixMember  = "MEMBER#"

	EntityTypeAccount       = "ACCOUNT"
	EntityTypeAccountMember = "ACCOUNT_MEMBER"
)

// Account member roles. owner is assigned only to the creator.
const (
	RoleOwner = "owner"
	RoleAM    = "AM"
	RoleTAM   = "TAM"
	RoleSSA   = "SSA"
)

// Account is a first-class customer/company entity shared across a team.
type Account struct {
	PK              string    `dynamodbav:"PK"` // ACCOUNT#{accountId}
	SK              string    `dynamodbav:"SK"` // META
	AccountID       string    `dynamodbav:"accountId"`
	Name            string    `dynamodbav:"name"`
	Aliases         []string  `dynamodbav:"aliases,omitempty"` // tag mapping e.g. ["하나은행","Hana Bank"]
	Domains         []string  `dynamodbav:"domains,omitempty"`
	Industry        string    `dynamodbav:"industry,omitempty"`
	CrawlerSourceID string    `dynamodbav:"crawlerSourceId,omitempty"` // links CRAWLER#{sourceId} (Plan 3)
	OwnerUserID     string    `dynamodbav:"ownerUserId"`
	CreatedAt       time.Time `dynamodbav:"createdAt"`
	UpdatedAt       time.Time `dynamodbav:"updatedAt"`
	EntityType      string    `dynamodbav:"entityType"` // "ACCOUNT"
}

// AccountMember binds a user (with a role) to an account.
type AccountMember struct {
	PK         string    `dynamodbav:"PK"` // ACCOUNT#{accountId}
	SK         string    `dynamodbav:"SK"` // MEMBER#{userId}
	AccountID  string    `dynamodbav:"accountId"`
	UserID     string    `dynamodbav:"userId"`
	Email      string    `dynamodbav:"email,omitempty"`
	Role       string    `dynamodbav:"role"`
	AddedAt    time.Time `dynamodbav:"addedAt"`
	GSI1PK     string    `dynamodbav:"GSI1PK,omitempty"` // USER#{userId}
	GSI1SK     string    `dynamodbav:"GSI1SK,omitempty"` // ACCOUNT#{accountId}
	EntityType string    `dynamodbav:"entityType"`       // "ACCOUNT_MEMBER"
}

// --- Request / Response DTOs ---

type CreateAccountRequest struct {
	Name     string   `json:"name"`
	Aliases  []string `json:"aliases,omitempty"`
	Domains  []string `json:"domains,omitempty"`
	Industry string   `json:"industry,omitempty"`
}

type AddMemberRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

type AccountMemberDTO struct {
	UserID string `json:"userId"`
	Email  string `json:"email,omitempty"`
	Role   string `json:"role"`
}

type AccountResponse struct {
	AccountID   string             `json:"accountId"`
	Name        string             `json:"name"`
	Aliases     []string           `json:"aliases,omitempty"`
	Domains     []string           `json:"domains,omitempty"`
	Industry    string             `json:"industry,omitempty"`
	OwnerUserID string             `json:"ownerUserId"`
	Members     []AccountMemberDTO `json:"members"`
	CreatedAt   time.Time          `json:"createdAt"`
}

// AccountSummary is the list-view item for "my accounts".
type AccountSummary struct {
	AccountID string `json:"accountId"`
	Name      string `json:"name"`
	Role      string `json:"role"`
}

const (
	PrefixMeetingRef     = "MEETINGREF#"
	EntityTypeMeetingRef = "MEETING_REF"
)

// MeetingRef is a lightweight reference to a meeting shared into an account,
// stored in the account partition so members can list shared meetings without
// a cross-partition scan. PK: ACCOUNT#{accountId}, SK: MEETINGREF#{occurredAt}#{meetingId}.
type MeetingRef struct {
	PK          string    `dynamodbav:"PK"`
	SK          string    `dynamodbav:"SK"`
	AccountID   string    `dynamodbav:"accountId"`
	MeetingID   string    `dynamodbav:"meetingId"`
	OwnerUserID string    `dynamodbav:"ownerUserId"`
	Title       string    `dynamodbav:"title,omitempty"`
	Date        time.Time `dynamodbav:"date"`
	EntityType  string    `dynamodbav:"entityType"` // "MEETING_REF"
}

// --- meeting↔account request/response DTOs ---

type LinkAccountRequest struct {
	AccountID string `json:"accountId"`
}

type ShareToAccountRequest struct {
	AccountID string `json:"accountId"`
}

type ShareToAccountResult struct {
	AccountID  string `json:"accountId"`
	SharedWith int    `json:"sharedWith"` // number of account members granted read
}

type MeetingRefDTO struct {
	MeetingID   string    `json:"meetingId"`
	OwnerUserID string    `json:"ownerUserId"`
	Title       string    `json:"title"`
	Date        time.Time `json:"date"`
}

// Insight types (Plan 3). Feeds SIFT / 2by2 / Player Card raw material.
const (
	InsightTrend       = "trend"
	InsightNeed        = "need"
	InsightCompetitive = "competitive"
	InsightRisk        = "risk"
	InsightOpportunity = "opportunity"
	InsightTech        = "tech"
	InsightStakeholder = "stakeholder"
	InsightAction      = "action"
)

const (
	PrefixInsight     = "INSIGHT#"
	EntityTypeInsight = "ACCOUNT_INSIGHT"
)

// IsValidInsightType reports whether t is one of the 8 recognized insight types.
func IsValidInsightType(t string) bool {
	switch t {
	case InsightTrend, InsightNeed, InsightCompetitive, InsightRisk,
		InsightOpportunity, InsightTech, InsightStakeholder, InsightAction:
		return true
	default:
		return false
	}
}

// MeetingInsight is one typed insight extracted from a meeting (stored as JSON in Meeting.Insights).
type MeetingInsight struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Text     string   `json:"text"`
	TsMarker string   `json:"tsMarker,omitempty"` // [TS:NNN] transcript deep link
	Entities []string `json:"entities,omitempty"`
}

// AccountInsight is a persisted insight item in the account partition.
// PK: ACCOUNT#{accountId}, SK: INSIGHT#{occurredAt}#{meetingId}#{index}
type AccountInsight struct {
	PK           string    `dynamodbav:"PK"`
	SK           string    `dynamodbav:"SK"`
	AccountID    string    `dynamodbav:"accountId"`
	InsightID    string    `dynamodbav:"insightId"`
	Type         string    `dynamodbav:"type"`
	Text         string    `dynamodbav:"text"`
	SourceType   string    `dynamodbav:"sourceType"` // "meeting" | "news" | "ingest"
	SourceID     string    `dynamodbav:"sourceId"`
	SourceUserID string    `dynamodbav:"sourceUserId,omitempty"`
	OccurredAt   time.Time `dynamodbav:"occurredAt"`
	TsMarker     string    `dynamodbav:"tsMarker,omitempty"`
	Entities     []string  `dynamodbav:"entities,omitempty"`
	CreatedAt    time.Time `dynamodbav:"createdAt"`
	EntityType   string    `dynamodbav:"entityType"` // "ACCOUNT_INSIGHT"
}

type AccountInsightDTO struct {
	Type       string    `json:"type"`
	Text       string    `json:"text"`
	SourceType string    `json:"sourceType"`
	SourceID   string    `json:"sourceId"`
	OccurredAt time.Time `json:"occurredAt"`
	TsMarker   string    `json:"tsMarker,omitempty"`
	Entities   []string  `json:"entities,omitempty"`
}

// AccountBrief bundles an account's raw material for one-shot consumption by
// the personal-side agent (SFDC/SIFT/2by2/Player Card prep). Insights are
// grouped by type for convenience.
type AccountBrief struct {
	Account        *AccountResponse               `json:"account"`
	InsightsByType map[string][]AccountInsightDTO `json:"insightsByType"`
	Meetings       []MeetingRefDTO                `json:"meetings"`
}

const EntityTypeAccountDoc = "ACCOUNT_DOC" // SK uses existing model.PrefixDoc ("DOC#")

// AccountDocument is a locally-authored (email/calendar/prep) doc ingested into
// an account so non-Obsidian teammates can read it in TTOBAK. PK: ACCOUNT#{id},
// SK: DOC#{docId}. Content is inline markdown (<=300KB). Loop guard: only
// local-origin docs (no ttobak_id frontmatter) are accepted, so TtobakOrigin=false.
type AccountDocument struct {
	PK           string    `dynamodbav:"PK"`
	SK           string    `dynamodbav:"SK"`
	AccountID    string    `dynamodbav:"accountId"`
	DocID        string    `dynamodbav:"docId"`
	Title        string    `dynamodbav:"title"`
	DocType      string    `dynamodbav:"docType,omitempty"` // "prep" | "reference" | ...
	Path         string    `dynamodbav:"path,omitempty"`    // original vault path
	Content      string    `dynamodbav:"content"`           // inline markdown
	SourceUserID string    `dynamodbav:"sourceUserId"`
	TtobakOrigin bool      `dynamodbav:"ttobakOrigin"`
	CreatedAt    time.Time `dynamodbav:"createdAt"`
	UpdatedAt    time.Time `dynamodbav:"updatedAt"`
	EntityType   string    `dynamodbav:"entityType"`
}

type PutDocumentRequest struct {
	Title    string `json:"title"`
	DocType  string `json:"docType,omitempty"`
	Path     string `json:"path,omitempty"`
	Markdown string `json:"markdown"`
}

type AccountDocumentDTO struct {
	DocID        string    `json:"docId"`
	Title        string    `json:"title"`
	DocType      string    `json:"docType,omitempty"`
	Path         string    `json:"path,omitempty"`
	SourceUserID string    `json:"sourceUserId"`
	CreatedAt    time.Time `json:"createdAt"`
}

type AccountDocumentDetail struct {
	AccountDocumentDTO
	Content string `json:"content"`
}

// VaultFile is one Obsidian note in an export bundle.
type VaultFile struct {
	Path     string `json:"path"`
	Markdown string `json:"markdown"`
}
