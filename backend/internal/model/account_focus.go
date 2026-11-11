package model

// AccountFocusGraph is one page of the person- or project-centered graph:
// the focus, its meetings (newest first) and the technologies those meetings
// cover. Every edge comes from a stored row: people links, project meeting
// refs and the meeting's stored tags (always origin "auto").
type AccountFocusGraph struct {
	Focus      FocusCard      `json:"focus"`
	Meetings   []FocusMeeting `json:"meetings"` // this page, at most 12
	Techs      []FocusTech    `json:"techs"`    // over all read focus meetings
	Other      []FocusTag     `json:"other"`    // tags outside the catalog
	NextCursor string         `json:"nextCursor,omitempty"`
	// Truncated reports that a row or meeting read bound was hit.
	Truncated bool `json:"truncated"`
}

// FocusCard describes the focus. Kind is contact, member or project; Detail
// is title/department, member role or project stage.
type FocusCard struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Label    string `json:"label"`
	Detail   string `json:"detail,omitempty"`
	Meetings int    `json:"meetings"`
}

// FocusMeeting is one meeting of the focus. ID is the link form
// {ownerUserId}:{meetingId}; Roles are the focus person's roles (empty for a
// project focus); Techs are catalog IDs.
type FocusMeeting struct {
	ID     string        `json:"id"`
	Title  string        `json:"title"`
	Date   string        `json:"date"` // RFC 3339
	Roles  []string      `json:"roles"`
	Techs  []string      `json:"techs"`
	People []FocusPerson `json:"people"`
}

// FocusPerson is a readable member or contact of a meeting with its roles.
type FocusPerson struct {
	Kind   string   `json:"kind"` // member|contact
	ID     string   `json:"id"`
	Label  string   `json:"label"`
	Detail string   `json:"detail,omitempty"`
	Roles  []string `json:"roles"`
}

// FocusTech is a catalog technology with the number of focus meetings that
// cover it. Group selects its color.
type FocusTech struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Group    string `json:"group"`
	Meetings int    `json:"meetings"`
}

// FocusTag is an uncatalogued tag with its focus meeting count.
type FocusTag struct {
	Tag      string `json:"tag"`
	Meetings int    `json:"meetings"`
}
