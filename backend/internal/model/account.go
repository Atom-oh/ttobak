package model

import "time"

// Key prefixes / constants for the Account partition.
// Account META:   PK: ACCOUNT#{accountId}, SK: META
// Account member: PK: ACCOUNT#{accountId}, SK: MEMBER#{userId}
//
//	(GSI1PK: USER#{userId}, GSI1SK: ACCOUNT#{accountId} for reverse lookup)
const (
	PrefixAccount = "ACCOUNT#"
	SKAccountMeta = "META"
	PrefixMember  = "MEMBER#"

	EntityTypeAccount       = "ACCOUNT"
	EntityTypeAccountMember = "ACCOUNT_MEMBER"
)

// Account member roles. owner is assigned only to the creator.
const (
	RoleOwner     = "owner"
	RoleAM        = "AM"
	RoleTAM       = "TAM"
	RoleSSA       = "SSA"
	RoleSA        = "SA"
	RoleSAManager = "SA Manager"
	RoleAMManager = "AM Manager"
)

// AssignableRoles is the allowlist for AddMember / UpdateMemberRole.
// owner is deliberately absent — it is server-assigned to the creator only.
var AssignableRoles = []string{RoleAM, RoleTAM, RoleSSA, RoleSA, RoleSAManager, RoleAMManager}

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

type UpdateMemberRequest struct {
	Role string `json:"role"`
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

const (
	PrefixResearchRef     = "RESEARCHREF#"
	EntityTypeResearchRef = "RESEARCH_REF"
)

// ResearchRef is a lightweight reference to a research report linked to an
// account, stored in the account partition so members can list linked
// research without a cross-partition scan (mirrors MeetingRef).
// PK: ACCOUNT#{accountId}, SK: RESEARCHREF#{researchId}.
type ResearchRef struct {
	PK          string    `dynamodbav:"PK"`
	SK          string    `dynamodbav:"SK"`
	AccountID   string    `dynamodbav:"accountId"`
	ResearchID  string    `dynamodbav:"researchId"`
	OwnerUserID string    `dynamodbav:"ownerUserId"`
	Topic       string    `dynamodbav:"topic,omitempty"`
	CreatedAt   time.Time `dynamodbav:"createdAt"`
	EntityType  string    `dynamodbav:"entityType"` // "RESEARCH_REF"
}

// AccountResearchDTO is the list-view item for "research linked to this account".
type AccountResearchDTO struct {
	ResearchID  string `json:"researchId"`
	Topic       string `json:"topic"`
	Summary     string `json:"summary,omitempty"`
	Status      string `json:"status"`
	OwnerUserID string `json:"ownerUserId"`
	CreatedAt   string `json:"createdAt"`
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
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Text        string   `json:"text"`
	Evidence    string   `json:"evidence,omitempty"`
	Implication string   `json:"implication,omitempty"`
	NextAction  string   `json:"nextAction,omitempty"`
	TsMarker    string   `json:"tsMarker,omitempty"` // [TS:NNN] transcript deep link
	Entities    []string `json:"entities,omitempty"`
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
	Evidence     string    `dynamodbav:"evidence,omitempty"`
	Implication  string    `dynamodbav:"implication,omitempty"`
	NextAction   string    `dynamodbav:"nextAction,omitempty"`
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
	Type        string    `json:"type"`
	Text        string    `json:"text"`
	Evidence    string    `json:"evidence,omitempty"`
	Implication string    `json:"implication,omitempty"`
	NextAction  string    `json:"nextAction,omitempty"`
	SourceType  string    `json:"sourceType"`
	SourceID    string    `json:"sourceId"`
	OccurredAt  time.Time `json:"occurredAt"`
	TsMarker    string    `json:"tsMarker,omitempty"`
	Entities    []string  `json:"entities,omitempty"`
}

// AccountBrief bundles an account's raw material for one-shot consumption by
// the personal-side agent (SFDC/SIFT/2by2/Player Card prep). Insights are
// grouped by type for convenience.
type AccountBrief struct {
	Account        *AccountResponse               `json:"account"`
	InsightsByType map[string][]AccountInsightDTO `json:"insightsByType"`
	Meetings       []MeetingRefDTO                `json:"meetings"`
}

const (
	EntityTypeAccountDoc = "ACCOUNT_DOC" // SK uses existing model.PrefixDoc ("DOC#"), PK: ACCOUNT#{id}
	EntityTypeUserDoc    = "USER_DOC"    // personal (account-less) doc, PK: USER#{userId}
)

// AccountDocument is a locally-authored (email/calendar/prep/note/blog/slide)
// doc, either shared to an account (PK: ACCOUNT#{id}, EntityType
// EntityTypeAccountDoc) or personal (PK: USER#{userId}, EntityType
// EntityTypeUserDoc). SK: DOC#{docId} either way. Content is inline markdown
// (<=300KB); a slide instead carries FileKey (S3, Content empty). Loop guard:
// only local-origin docs (no ttobak_id frontmatter) are accepted, so
// TtobakOrigin=false. Links holds normalized [[wikilink]] targets parsed out
// of Content on every put/update -- a future graph view's data source.
type AccountDocument struct {
	PK           string   `dynamodbav:"PK"`
	SK           string   `dynamodbav:"SK"`
	AccountID    string   `dynamodbav:"accountId,omitempty"`
	DocID        string   `dynamodbav:"docId"`
	Title        string   `dynamodbav:"title"`
	DocType      string   `dynamodbav:"docType,omitempty"` // "prep" | "reference" | "note" | "blog" | "slide" | ...
	Path         string   `dynamodbav:"path,omitempty"`    // original vault path
	Content      string   `dynamodbav:"content"`           // inline markdown; empty for a slide
	Links        []string `dynamodbav:"links,omitempty"`   // normalized [[wikilink]] targets found in Content
	FileKey      string   `dynamodbav:"fileKey,omitempty"` // S3 key under docs/{userId}/... for a slide upload
	FileName     string   `dynamodbav:"fileName,omitempty"`
	MimeType     string   `dynamodbav:"mimeType,omitempty"`
	FileSize     int64    `dynamodbav:"fileSize,omitempty"`
	SourceUserID string   `dynamodbav:"sourceUserId"`
	TtobakOrigin bool     `dynamodbav:"ttobakOrigin"`
	// PublicShareToken is set once CreateUserDocPublicShare mints an
	// unauthenticated share link for this (personal, slide) document; see
	// PublicShare for the token->doc pointer item this references.
	PublicShareToken string    `dynamodbav:"publicShareToken,omitempty"`
	CreatedAt        time.Time `dynamodbav:"createdAt"`
	UpdatedAt        time.Time `dynamodbav:"updatedAt"`
	EntityType       string    `dynamodbav:"entityType"`
}

type PutDocumentRequest struct {
	Title   string `json:"title"`
	DocType string `json:"docType,omitempty"`
	Path    string `json:"path,omitempty"`
	// Markdown is a pointer so "key omitted from the JSON body" (nil --
	// don't touch the existing body, an update-only concept) is
	// distinguishable from "explicitly set to empty string" (non-nil
	// pointer to "" -- e.g. the user selected-all-and-deleted in the
	// editor and wants that saved). A plain string can't represent this:
	// omitted and explicit-empty both unmarshal to "", so update would
	// silently discard the user's intent to clear the note.
	Markdown *string `json:"markdown"`
	FileKey  string  `json:"fileKey,omitempty"`
	FileName string  `json:"fileName,omitempty"`
	MimeType string  `json:"mimeType,omitempty"`
	FileSize int64   `json:"fileSize,omitempty"`
}

type AccountDocumentDTO struct {
	DocID        string    `json:"docId"`
	Title        string    `json:"title"`
	DocType      string    `json:"docType,omitempty"`
	Path         string    `json:"path,omitempty"`
	Links        []string  `json:"links,omitempty"`
	FileName     string    `json:"fileName,omitempty"`
	SourceUserID string    `json:"sourceUserId"`
	// SharedBy is set only on a document the caller received via a per-user
	// share (see PrefixDocShare) -- it carries the owner's email and doubles
	// as the frontend's "this is read-only, not mine" marker.
	SharedBy string `json:"sharedBy,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type AccountDocumentDetail struct {
	AccountDocumentDTO
	Content     string `json:"content"`
	FileKey     string `json:"-"` // internal only; handler presigns this into DownloadURL
	DownloadURL string `json:"downloadUrl,omitempty"`
	// PreviewURL is set only for a PPTX/PPT slide whose PDF sidecar
	// conversion (convert-doc Lambda) has finished; see UploadService.GeneratePreviewPDFURL.
	PreviewURL string `json:"previewUrl,omitempty"`
	// PublicShareToken, if set, means GET /api/public/docs/{token} serves
	// this document unauthenticated. See AccountService.CreateUserDocPublicShare.
	PublicShareToken string `json:"publicShareToken,omitempty"`
}

const (
	PrefixPubShare     = "PUBSHARE#"
	SKPubShare         = "PUBSHARE"
	EntityTypePubShare = "PUB_SHARE"
)

// Per-user document sharing (reference, not copy -- contrast
// ShareUserDocumentToAccount, which copies). Reuses model.Share as the item
// shape (MeetingID carries the docId) but with its own SK prefixes: the
// recipient-side row must NOT be "SHARED#" or a shared document would be
// picked up by ListSharesForUser / the meetings "shared" tab, which both
// key off that prefix alone.
const (
	PrefixDocShare     = "SHAREDDOC#" // PK: USER#{sharedToUserId}, SK: SHAREDDOC#{docId}
	PrefixDocShareTo   = "DOCSHARE_TO#"
	EntityTypeDocShare = "DOC_SHARE"
	PrefixDocSharePart = "USERDOC#" // PK for the doc-side share list: USERDOC#{docId}
)

// ShareDocumentRequest is the body of POST /api/documents/{docId}/share.
// Permission is deliberately absent: a document share is read-only (the
// owner alone edits/deletes/shares), unlike a meeting share's read|edit.
type ShareDocumentRequest struct {
	Email string `json:"email"`
}

// PublicShare is a token -> document pointer enabling one unauthenticated
// GET route (see handler.DocumentHandler.PublicGetDoc) to resolve a share
// link without a table scan. PK: PUBSHARE#{token}, SK: PUBSHARE.
type PublicShare struct {
	PK         string    `dynamodbav:"PK"`
	SK         string    `dynamodbav:"SK"`
	Token      string    `dynamodbav:"token"`
	DocPK      string    `dynamodbav:"docPK"` // USER#{userId} -- personal docs only for now
	DocID      string    `dynamodbav:"docId"`
	CreatedAt  time.Time `dynamodbav:"createdAt"`
	EntityType string    `dynamodbav:"entityType"` // "PUB_SHARE"
}

// VaultFile is one Obsidian note in an export bundle.
type VaultFile struct {
	Path     string `json:"path"`
	Markdown string `json:"markdown"`
}
