package model

// AccountPerson is one resolved member or contact of the people panel with
// counts over the meetings the viewer can currently see.
type AccountPerson struct {
	Kind     string         `json:"kind"` // member|contact
	ID       string         `json:"id"`
	Label    string         `json:"label"`
	Meetings int            `json:"meetings"` // distinct visible meetings
	Roles    map[string]int `json:"roles"`    // role → visible edge count
}

type AccountPeoplePage struct {
	People     []AccountPerson `json:"people"`
	NextCursor string          `json:"nextCursor,omitempty"`
	Truncated  bool            `json:"truncated"`
}

// MentionGroup is one normalized unresolved mention across meetings.
type MentionGroup struct {
	Key      string   `json:"key"`      // the 32-hex hash
	Text     string   `json:"text"`     // one visible original mention, ≤120 code points
	Meetings int      `json:"meetings"` // visible meetings in this group
	Roles    []string `json:"roles"`
}

type MentionGroupsPage struct {
	Groups     []MentionGroup `json:"groups"`
	NextCursor string         `json:"nextCursor,omitempty"`
	Truncated  bool           `json:"truncated"`
}

// MeetingPeopleEdges lists the people edges of one meeting. Labels are keyed
// "{kind}:{id}" and only hold targets the viewer can still read.
type MeetingPeopleEdges struct {
	Edges  []PeopleLink      `json:"edges"`
	Labels map[string]string `json:"labels"`
}

// PeopleEdgeInput binds a meeting to a person with a role (spec E4).
type PeopleEdgeInput struct {
	MeetingID string `json:"meetingId"`
	ToKind    string `json:"toKind"`
	ToID      string `json:"toId"`
	Role      string `json:"role"`
	SourceRef string `json:"sourceRef,omitempty"`
}

// ResolveMentionInput names the member or contact a mention group refers to.
type ResolveMentionInput struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type ResolveMentionResult struct {
	Written   int  `json:"written"`
	Remaining bool `json:"remaining"`
}
