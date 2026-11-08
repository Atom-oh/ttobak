package model

// AccountEvent is a scheduled customer engagement in an account. Title,
// location, attendees and notes are sealed at rest (ADR-063); the start time
// stays plaintext in the sort key so events can be listed in time order.
type AccountEvent struct {
	EventID   string          `json:"eventId"`
	Revision  string          `json:"revision"`
	Title     string          `json:"title"`
	StartAt   string          `json:"startAt"`         // RFC 3339, normalized to UTC
	EndAt     string          `json:"endAt,omitempty"` // RFC 3339, normalized to UTC
	AllDay    bool            `json:"allDay"`
	Location  string          `json:"location"`
	Attendees []EventAttendee `json:"attendees"`
	Notes     string          `json:"notes"`
	CreatedBy string          `json:"createdBy"`
	CreatedAt string          `json:"createdAt"`
	UpdatedAt string          `json:"updatedAt"`
}

// EventAttendee optionally references a contact in the same account; Name is
// always kept so an event still reads correctly after the contact changes.
type EventAttendee struct {
	ContactID string `json:"contactId,omitempty"`
	Name      string `json:"name"`
	Email     string `json:"email,omitempty"`
}

type AccountEventsPage struct {
	Events     []AccountEvent `json:"events"`
	NextCursor string         `json:"nextCursor,omitempty"`
}
