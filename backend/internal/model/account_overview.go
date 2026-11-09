package model

import "time"

// Account signals state values.
const (
	NewsStateOK           = "OK"
	NewsStateNoSource     = "NO_SOURCE"
	NewsStateNoRecentNews = "NO_RECENT_NEWS"
)

// Stall reason codes.
const (
	StallStageStale = "STAGE_STALE"
	StallOpenAction = "OPEN_ACTIONS"
	StallNoUpcoming = "NO_UPCOMING"
)

// Default stall thresholds (spec "Default stall signals").
const (
	StallStageAfter  = 21 * 24 * time.Hour
	StallActionAfter = 14 * 24 * time.Hour
)

// AccountSignals is the account page's AWS signals section. It is derived
// read-only data; nothing here is written back as a link.
type AccountSignals struct {
	NewsState       string              `json:"newsState"`     // NewsState*
	News            []SignalNews        `json:"news"`          // at most 10, last 30 days
	Opportunities   []SignalOppty       `json:"opportunities"` // at most 20
	Insights        []AccountInsightDTO `json:"insights"`      // at most 10, need|opportunity|risk
	RestrictedCount int                 `json:"restrictedCount"`
	// LinksTruncated reports that a per-endpoint link read hit its bound, so
	// linkedProjectIds, candidate and NO_UPCOMING may be incomplete.
	LinksTruncated bool `json:"linksTruncated"`
}

// SignalNews is one recent news item of the account's own news sources. ID
// is the link form {sourceId}:{docHash}. Candidate marks unlinked news that
// looks like a new opportunity; it is a display flag only.
type SignalNews struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	URL              string   `json:"url,omitempty"`
	PubDate          string   `json:"pubDate,omitempty"`
	AWSRelevance     string   `json:"awsRelevance,omitempty"`
	QualityScore     float64  `json:"qualityScore"`
	LinkedProjectIDs []string `json:"linkedProjectIds"`
	Candidate        bool     `json:"candidate"`
}

// SignalOppty is one project linked to the account. DaysInStage is nil when
// the stage change time is unknown (legacy rows).
type SignalOppty struct {
	ProjectID   string        `json:"projectId"`
	Name        string        `json:"name"`
	Stage       string        `json:"stage"`
	StageMapped bool          `json:"stageMapped"`
	DaysInStage *int          `json:"daysInStage,omitempty"`
	Terminal    bool          `json:"terminal"`
	Stall       []StallReason `json:"stall"`
}

// StallReason explains why an opportunity looks stalled.
type StallReason struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// Flow result states.
const (
	FlowStateTerminal = "TERMINAL"
	FlowStateStalled  = "STALLED"
	FlowStateActive   = "ACTIVE"
	// FlowStateUnknown: the stage is unmapped, or its change time is unknown
	// and no stage history exists.
	FlowStateUnknown = "UNKNOWN"
)

// Flow item kinds.
const (
	FlowItemEvent   = "event"
	FlowItemMeeting = "meeting"
)

// AccountFlow is the account page's per-opportunity engagement flow. Rows are
// built only from stored links (event→project links and meeting projectIds);
// nothing is inferred or written back.
type AccountFlow struct {
	Rows       []FlowRow `json:"rows"` // at most 20 projects, nonterminal first
	Unassigned FlowRow   `json:"unassigned"`
	// Truncated reports that a read or size bound was hit, so some rows or
	// items may be missing and unassigned items may be linked.
	Truncated bool `json:"truncated"`
}

// FlowRow is one opportunity's flow (ProjectID empty for the unassigned row).
type FlowRow struct {
	ProjectID string               `json:"projectId,omitempty"`
	Activity  []FlowItem           `json:"activity"` // account events, at most 5, earliest first
	Response  []FlowItem           `json:"response"` // published meetings, at most 5, earliest first
	Oppty     *SignalOppty         `json:"oppty,omitempty"`
	History   []ProjectStageChange `json:"history"` // at most 10, newest first
	Result    FlowResult           `json:"result"`
}

// FlowItem is one activity or response. ID is the event ID, or
// {ownerUserId}:{meetingId} for meetings. Origin and Evidence come from the
// event→project link row (manual links have no evidence). Need is the newest
// readable need-type insight of a meeting.
type FlowItem struct {
	Kind     string `json:"kind"` // FlowItem*
	ID       string `json:"id"`
	Title    string `json:"title"`
	Date     string `json:"date,omitempty"` // RFC 3339
	Origin   string `json:"origin,omitempty"`
	Evidence string `json:"evidence,omitempty"`
	Need     string `json:"need,omitempty"`
}

// FlowResult is the opportunity's current state.
type FlowResult struct {
	State string        `json:"state"` // FlowState*
	Stall []StallReason `json:"stall"`
}

// Graph node kinds.
const (
	GraphNodeAccount = "account"
	GraphNodeProject = "project"
	GraphNodeMeeting = "meeting"
	GraphNodeEvent   = "event"
	GraphNodeNews    = "news"
	GraphNodeTech    = "tech"
	GraphNodeMember  = "member"
	GraphNodeContact = "contact"
)

// Graph edge kinds.
const (
	GraphEdgeProjectAccount = "project_account"
	GraphEdgeMeetingProject = "meeting_project"
	GraphEdgeMeetingAccount = "meeting_account"
	GraphEdgeLink           = "link"
	GraphEdgeAttends        = "attends"
	GraphEdgeMemberOf       = "member_of"
)

// AccountGraph is the account's relationship graph. Every node and edge comes
// from a revalidated stored relation; nothing is inferred or written back.
type AccountGraph struct {
	Nodes []GraphNode `json:"nodes"` // at most 150
	Edges []GraphEdge `json:"edges"` // at most 300
	// Truncated reports that a node, edge, read or size bound was hit.
	Truncated bool `json:"truncated"`
}

// GraphNode ID is {kind}:{id}, where id is the link-form ID of the item
// (meetings {ownerUserId}:{meetingId}, news and tech {sourceId}:{docHash}).
// Label is at most 60 code points.
type GraphNode struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"` // GraphNode*
	Label string `json:"label"`
}

// GraphEdge joins two node IDs. Origin is set only for link edges.
type GraphEdge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Kind   string `json:"kind"` // GraphEdge*
	Origin string `json:"origin,omitempty"`
}
