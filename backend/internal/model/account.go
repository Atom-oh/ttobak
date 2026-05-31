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
