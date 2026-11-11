package model

// SFDC contact sync batch (POST /api/accounts/{accountId}/contacts/sfdc-sync).
// The Mac intake sends only SFDC IDs, name and title; email and phone never
// reach the server (ADR-021, SFDC contacts).

// SFDC sync item actions.
const (
	SfdcSyncActionCreate = "create"
	SfdcSyncActionLink   = "link"
)

// SFDC sync per-item results.
const (
	SfdcSyncCreated       = "created"
	SfdcSyncFilled        = "filled"
	SfdcSyncUnchanged     = "unchanged"
	SfdcSyncAlreadyLinked = "alreadyLinked"
	SfdcSyncSuppressed    = "suppressed"
	SfdcSyncConflict      = "conflict"
	SfdcSyncInvalid       = "invalid"
)

// SfdcContactSyncRequest is one bounded batch for one TTOBAK Account.
// Present lists every SFDC contact ID read from SfdcAccountIDs; it is
// required when, and only allowed when, SnapshotComplete is true.
type SfdcContactSyncRequest struct {
	SnapshotComplete bool                  `json:"snapshotComplete"`
	SfdcAccountIDs   []string              `json:"sfdcAccountIds"`
	Present          []string              `json:"present,omitempty"`
	Items            []SfdcContactSyncItem `json:"items"`
}

// SfdcContactSyncItem creates a contact or links an existing one. A link
// carries the contact ID and the revision the client observed.
type SfdcContactSyncItem struct {
	SfdcContactID string `json:"sfdcContactId"`
	SfdcAccountID string `json:"sfdcAccountId"`
	Name          string `json:"name"`
	Title         string `json:"title"`
	Action        string `json:"action"`
	ContactID     string `json:"contactId,omitempty"`
	Revision      string `json:"revision,omitempty"`
}

// SfdcContactSyncResult reports one item, in request order.
type SfdcContactSyncResult struct {
	SfdcContactID string `json:"sfdcContactId"`
	Result        string `json:"result"`
	ContactID     string `json:"contactId,omitempty"`
}

// SfdcContactSyncResponse counts departure marks set and cleared.
type SfdcContactSyncResponse struct {
	Results  []SfdcContactSyncResult `json:"results"`
	Marked   int                     `json:"marked"`
	Unmarked int                     `json:"unmarked"`
}
