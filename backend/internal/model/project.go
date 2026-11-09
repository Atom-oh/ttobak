package model

import (
	"strings"
	"time"
)

// Key prefixes / constants for the Project partition.
// Project CONFIG:       PK: PROJECT#{projectId}, SK: CONFIG
// Project owner index:  PK: USER#{ownerUserId}, SK: PROJECT#{projectId}  (ListProjectsForUser's owner half)
// Project member:       PK: PROJECT#{projectId}, SK: MEMBER#{userId}    (GSI1PK: USER#{userId}, GSI1SK: PROJECT#{projectId})
// Account project ref:  PK: ACCOUNT#{accountId}, SK: PROJECTREF#{projectId}
// Project meeting ref:  PK: PROJECT#{projectId}, SK: MEETINGREF#{occurredAt}#{meetingId}
// Project research ref: PK: PROJECT#{projectId}, SK: RESEARCHREF#{researchId}
const (
	PrefixProjectInvites       = "PROJECT_INVITES#"
	PrefixProject              = "PROJECT#"
	SKProjectConfig            = "CONFIG"
	PrefixProjectMember        = PrefixMember
	PrefixPendingProject       = "PENDING_PROJECT#"
	PrefixPendingProjectMember = "PENDING_MEMBER#"
	PrefixProjectRef           = "PROJECTREF#"
	PrefixProjectMeetingRef    = PrefixMeetingRef
	PrefixProjectResearchRef   = PrefixResearchRef
	// PrefixProjectStageHistory: PK PROJECT#{id}, SK STAGEHIST#{RFC3339Nano}.
	PrefixProjectStageHistory = "STAGEHIST#"

	EntityTypeProject            = "PROJECT"
	EntityTypeProjectIndex       = "PROJECT_INDEX"
	EntityTypeProjectMember      = "PROJECT_MEMBER"
	EntityTypeProjectRef         = "PROJECT_REF"
	EntityTypeProjectMeetingRef  = "PROJECT_MEETING_REF"
	EntityTypeProjectResearchRef = "PROJECT_RESEARCH_REF"
	EntityTypeProjectStageChange = "PROJECT_STAGE_CHANGE"
)

// ProjectStages is the fixed, SFDC-aligned project stage vocabulary, in
// pipeline order. The empty stage means "unset".
var ProjectStages = []string{"Prospect", "Qualified", "Technical Validation", "Business Validation", "Committed", "Launched", "Closed Lost"}

// NormalizeProjectStage maps a case- and surrounding-space-insensitive exact
// match to its canonical stage. "" is valid (unset) and returns ("", true).
func NormalizeProjectStage(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", true
	}
	for _, stage := range ProjectStages {
		if strings.EqualFold(s, stage) {
			return stage, true
		}
	}
	return "", false
}

// IsTerminalStage reports whether the stage ends the engagement.
func IsTerminalStage(stage string) bool {
	return stage == "Launched" || stage == "Closed Lost"
}

// ProjectStageChange is one immutable stage-history row.
type ProjectStageChange struct {
	PK         string    `dynamodbav:"PK"` // PROJECT#{projectId}
	SK         string    `dynamodbav:"SK"` // STAGEHIST#{RFC3339Nano}
	ProjectID  string    `dynamodbav:"projectId"`
	From       string    `dynamodbav:"from,omitempty"`
	To         string    `dynamodbav:"to,omitempty"`
	ChangedBy  string    `dynamodbav:"changedBy"`
	ChangedAt  time.Time `dynamodbav:"changedAt"`
	EntityType string    `dynamodbav:"entityType"` // "PROJECT_STAGE_CHANGE"
}

// Project groups meetings, research, and accounts under a sales opportunity.
type Project struct {
	PK          string `dynamodbav:"PK"` // PROJECT#{projectId}
	SK          string `dynamodbav:"SK"` // CONFIG
	ProjectID   string `dynamodbav:"projectId"`
	Name        string `dynamodbav:"name"`
	Description string `dynamodbav:"description,omitempty"`
	SfdcOpptyID string `dynamodbav:"sfdcOpptyId,omitempty"`
	SfdcURL     string `dynamodbav:"sfdcUrl,omitempty"`
	Stage       string `dynamodbav:"stage,omitempty"`
	// StageChangedAt is set whenever Stage changes; nil for legacy rows.
	StageChangedAt *time.Time `dynamodbav:"stageChangedAt,omitempty"`
	OwnerUserID    string     `dynamodbav:"ownerUserId"`
	// AccountIDs links this project to zero or more accounts (many-to-many).
	// ProjectRef provides the reverse-lookup item in each account partition.
	//
	// Stored as a DynamoDB String Set (stringset tag), not a List -- this
	// lets link mutations use atomic ADD/DELETE update expressions instead
	// of a read-modify-write on the whole list, which would otherwise lose
	// one side's change when two accounts are (un)linked concurrently.
	AccountIDs []string  `dynamodbav:"accountIds,omitempty,stringset"`
	CreatedAt  time.Time `dynamodbav:"createdAt"`
	UpdatedAt  time.Time `dynamodbav:"updatedAt"`
	EntityType string    `dynamodbav:"entityType"` // "PROJECT"
}

// ProjectMember binds a user directly to a project.
type ProjectMember struct {
	PK         string    `dynamodbav:"PK"` // PROJECT#{projectId}
	SK         string    `dynamodbav:"SK"` // MEMBER#{userId}
	ProjectID  string    `dynamodbav:"projectId"`
	UserID     string    `dynamodbav:"userId"`
	Email      string    `dynamodbav:"email,omitempty"`
	AddedAt    time.Time `dynamodbav:"addedAt"`
	GSI1PK     string    `dynamodbav:"GSI1PK,omitempty"` // USER#{userId}
	GSI1SK     string    `dynamodbav:"GSI1SK,omitempty"` // PROJECT#{projectId}
	EntityType string    `dynamodbav:"entityType"`       // "PROJECT_MEMBER"
}

// ProjectRef is a lightweight reverse reference from an account to a project.
type ProjectRef struct {
	PK          string    `dynamodbav:"PK"` // ACCOUNT#{accountId}
	SK          string    `dynamodbav:"SK"` // PROJECTREF#{projectId}
	AccountID   string    `dynamodbav:"accountId"`
	ProjectID   string    `dynamodbav:"projectId"`
	OwnerUserID string    `dynamodbav:"ownerUserId"`
	Name        string    `dynamodbav:"name,omitempty"`
	CreatedAt   time.Time `dynamodbav:"createdAt"`
	EntityType  string    `dynamodbav:"entityType"` // "PROJECT_REF"
}

// ProjectMeetingRef is a lightweight meeting reference in a project partition.
type ProjectMeetingRef struct {
	PK          string    `dynamodbav:"PK"` // PROJECT#{projectId}
	SK          string    `dynamodbav:"SK"` // MEETINGREF#{occurredAt}#{meetingId}
	ProjectID   string    `dynamodbav:"projectId"`
	MeetingID   string    `dynamodbav:"meetingId"`
	OwnerUserID string    `dynamodbav:"ownerUserId"`
	Title       string    `dynamodbav:"title,omitempty"`
	Date        time.Time `dynamodbav:"date"`
	EntityType  string    `dynamodbav:"entityType"` // "PROJECT_MEETING_REF"
}

// ProjectResearchRef is a lightweight research reference in a project partition.
type ProjectResearchRef struct {
	PK          string    `dynamodbav:"PK"` // PROJECT#{projectId}
	SK          string    `dynamodbav:"SK"` // RESEARCHREF#{researchId}
	ProjectID   string    `dynamodbav:"projectId"`
	ResearchID  string    `dynamodbav:"researchId"`
	OwnerUserID string    `dynamodbav:"ownerUserId"`
	Topic       string    `dynamodbav:"topic,omitempty"`
	CreatedAt   time.Time `dynamodbav:"createdAt"`
	EntityType  string    `dynamodbav:"entityType"` // "PROJECT_RESEARCH_REF"
}

// --- Request / Response DTOs ---

type CreateProjectRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	SfdcOpptyID string `json:"sfdcOpptyId,omitempty"`
	SfdcURL     string `json:"sfdcUrl,omitempty"`
	Stage       string `json:"stage,omitempty"`
}

type UpdateProjectRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	SfdcOpptyID string `json:"sfdcOpptyId,omitempty"`
	SfdcURL     string `json:"sfdcUrl,omitempty"`
	Stage       string `json:"stage,omitempty"`
}

type ProjectResponse struct {
	AccessPermission       string             `json:"accessPermission"`
	InheritedFromAccountID string             `json:"inheritedFromAccountId,omitempty"`
	ProjectID              string             `json:"projectId"`
	Name                   string             `json:"name"`
	Description            string             `json:"description,omitempty"`
	SfdcOpptyID            string             `json:"sfdcOpptyId,omitempty"`
	SfdcURL                string             `json:"sfdcUrl,omitempty"`
	Stage                  string             `json:"stage,omitempty"`
	StageChangedAt         *time.Time         `json:"stageChangedAt,omitempty"`
	StageMapped            bool               `json:"stageMapped"`
	OwnerUserID            string             `json:"ownerUserId"`
	AccountIDs             []string           `json:"accountIds"`
	Members                []ProjectMemberDTO `json:"members"`
	CreatedAt              time.Time          `json:"createdAt"`
	UpdatedAt              time.Time          `json:"updatedAt"`
}

type ProjectSummary struct {
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	Stage     string `json:"stage,omitempty"`
	// StageChangedAt/StageMapped mirror ProjectResponse; an unmapped legacy
	// stage is returned as-is with StageMapped=false.
	StageChangedAt *time.Time `json:"stageChangedAt,omitempty"`
	StageMapped    bool       `json:"stageMapped"`
	SfdcOpptyID    string     `json:"sfdcOpptyId,omitempty"`
}

type ProjectMemberDTO struct {
	Pending       bool   `json:"pending,omitempty"`
	EmailVerified bool   `json:"emailVerified"`
	UserID        string `json:"userId"`
	Email         string `json:"email,omitempty"`
}

type AddProjectMemberRequest struct {
	AllowPending bool   `json:"allowPending,omitempty"`
	Email        string `json:"email"`
}

type LinkProjectAccountRequest struct {
	AccountID string `json:"accountId"`
}

type LinkProjectMeetingRequest struct {
	MeetingID string `json:"meetingId"`
}

type LinkProjectResearchRequest struct {
	ResearchID string `json:"researchId"`
}

type ProjectMeetingRefDTO struct {
	MeetingID   string    `json:"meetingId"`
	OwnerUserID string    `json:"ownerUserId"`
	Title       string    `json:"title"`
	Date        time.Time `json:"date"`
}

type ProjectResearchDTO struct {
	ResearchID  string `json:"researchId"`
	Topic       string `json:"topic"`
	Summary     string `json:"summary,omitempty"`
	Status      string `json:"status"`
	OwnerUserID string `json:"ownerUserId"`
	// CreatedAt is a string (not time.Time, unlike this DTO's siblings) because
	// it's passed through directly from Research.CreatedAt, which is itself a
	// string field (see model/meeting.go's Research struct).
	CreatedAt string `json:"createdAt"`
}

// ProjectInsightDTO deliberately has no Evidence field -- same non-fan-out
// invariant as AccountInsightDTO (see model/account.go).
type ProjectInsightDTO struct {
	Type        string    `json:"type"`
	Text        string    `json:"text"`
	Implication string    `json:"implication,omitempty"`
	NextAction  string    `json:"nextAction,omitempty"`
	SourceID    string    `json:"sourceId"`
	OccurredAt  time.Time `json:"occurredAt"`
	TsMarker    string    `json:"tsMarker,omitempty"`
	Entities    []string  `json:"entities,omitempty"`
}

type ProjectBrief struct {
	Project        *ProjectResponse               `json:"project"`
	InsightsByType map[string][]ProjectInsightDTO `json:"insightsByType"`
	Meetings       []ProjectMeetingRefDTO         `json:"meetings"`
	Research       []ProjectResearchDTO           `json:"research"`
}

// PendingProjectMemberPage is an owner-only, bounded view of canonical pending grants.
type PendingProjectMemberDTO struct {
	Email     string `json:"email"`
	ExpiresAt int64  `json:"expiresAt"`
}
type PendingProjectMemberPage struct {
	Members    []PendingProjectMemberDTO `json:"members"`
	NextCursor string                    `json:"nextCursor,omitempty"`
}
