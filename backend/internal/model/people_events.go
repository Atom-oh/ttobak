package model

// People resolve reasons.
const (
	PeopleResolveReasonSpeakers = "speakers"
	PeopleResolveReasonContacts = "contacts"
	// PeopleResolveReasonManual follows a member's manual binding, so the
	// resolver drops the auto edges it now overrides.
	PeopleResolveReasonManual = "manual"
)

// PeopleResolveRequested asks the summarize worker to rerun the people
// resolver: for one meeting (OwnerID and MeetingID set) or for the account's
// recent meetings (both empty). It carries IDs and a reason only.
type PeopleResolveRequested struct {
	AccountID string `json:"accountId"`
	OwnerID   string `json:"ownerId,omitempty"`
	MeetingID string `json:"meetingId,omitempty"`
	Reason    string `json:"reason"`
}
