package model

import "time"

// Key prefixes / constants for the Project partition.
// Project CONFIG:       PK: PROJECT#{projectId}, SK: CONFIG
// Project owner index:  PK: USER#{ownerUserId}, SK: PROJECT#{projectId}  (ListProjectsForUser's owner half)
// Project member:       PK: PROJECT#{projectId}, SK: MEMBER#{userId}    (GSI1PK: USER#{userId}, GSI1SK: PROJECT#{projectId})
// Account project ref:  PK: ACCOUNT#{accountId}, SK: PROJECTREF#{projectId}
// Project meeting ref:  PK: PROJECT#{projectId}, SK: MEETINGREF#{occurredAt}#{meetingId}
// Project research ref: PK: PROJECT#{projectId}, SK: RESEARCHREF#{researchId}
const (
	PrefixProject            = "PROJECT#"
	SKProjectConfig          = "CONFIG"
	PrefixProjectMember      = PrefixMember
	PrefixProjectRef         = "PROJECTREF#"
	PrefixProjectMeetingRef  = PrefixMeetingRef
	PrefixProjectResearchRef = PrefixResearchRef

	EntityTypeProject            = "PROJECT"
	EntityTypeProjectIndex       = "PROJECT_INDEX"
	EntityTypeProjectMember      = "PROJECT_MEMBER"
	EntityTypeProjectRef         = "PROJECT_REF"
	EntityTypeProjectMeetingRef  = "PROJECT_MEETING_REF"
	EntityTypeProjectResearchRef = "PROJECT_RESEARCH_REF"
)

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
	OwnerUserID string `dynamodbav:"ownerUserId"`
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
	ProjectID   string             `json:"projectId"`
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	SfdcOpptyID string             `json:"sfdcOpptyId,omitempty"`
	SfdcURL     string             `json:"sfdcUrl,omitempty"`
	Stage       string             `json:"stage,omitempty"`
	OwnerUserID string             `json:"ownerUserId"`
	AccountIDs  []string           `json:"accountIds"`
	Members     []ProjectMemberDTO `json:"members"`
	CreatedAt   time.Time          `json:"createdAt"`
	UpdatedAt   time.Time          `json:"updatedAt"`
}

type ProjectSummary struct {
	ProjectID   string `json:"projectId"`
	Name        string `json:"name"`
	Stage       string `json:"stage,omitempty"`
	SfdcOpptyID string `json:"sfdcOpptyId,omitempty"`
}

type ProjectMemberDTO struct {
	UserID string `json:"userId"`
	Email  string `json:"email,omitempty"`
}

type AddProjectMemberRequest struct {
	Email string `json:"email"`
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

type ProjectInsightDTO struct {
	Type       string    `json:"type"`
	Text       string    `json:"text"`
	SourceID   string    `json:"sourceId"`
	OccurredAt time.Time `json:"occurredAt"`
	TsMarker   string    `json:"tsMarker,omitempty"`
	Entities   []string  `json:"entities,omitempty"`
}

type ProjectBrief struct {
	Project        *ProjectResponse               `json:"project"`
	InsightsByType map[string][]ProjectInsightDTO `json:"insightsByType"`
	Meetings       []ProjectMeetingRefDTO         `json:"meetings"`
	Research       []ProjectResearchDTO           `json:"research"`
}
