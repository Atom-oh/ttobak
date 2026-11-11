package model

// ContactSourceSfdc marks a contact created or linked by the SFDC sync.
const ContactSourceSfdc = "sfdc"

// CanonicalSfdcID returns the 18-character form of a Salesforce record ID.
// It accepts exactly 15 or 18 ASCII alphanumeric characters. A 15-character
// ID gains the standard case-checksum suffix: for each 5-character chunk, bit
// i is set when character i is an uppercase A-Z, and the 5-bit value maps
// 0-25 to 'A'-'Z' and 26-31 to '0'-'5'. An 18-character ID is returned
// unchanged only if its suffix matches its first 15 characters exactly.
// Every SFDC ID that enters a key, a sealed field or a comparison uses this
// form, so the two spellings of one record are one key.
func CanonicalSfdcID(id string) (string, bool) {
	if len(id) != 15 && len(id) != 18 {
		return "", false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
			return "", false
		}
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ012345"
	suffix := make([]byte, 3)
	for chunk := 0; chunk < 3; chunk++ {
		bits := 0
		for i := 0; i < 5; i++ {
			if c := id[chunk*5+i]; c >= 'A' && c <= 'Z' {
				bits |= 1 << i
			}
		}
		suffix[chunk] = alphabet[bits]
	}
	if len(id) == 18 && id[15:] != string(suffix) {
		return "", false
	}
	return id[:15] + string(suffix), true
}

// ValidSfdcID reports whether CanonicalSfdcID accepts id.
func ValidSfdcID(id string) bool {
	_, ok := CanonicalSfdcID(id)
	return ok
}

// AccountContact is stored in the account partition. Fields are returned even
// when empty so clients can safely round-trip all Remember export columns.
type AccountContact struct {
	PK               string `dynamodbav:"PK" json:"-"`
	SK               string `dynamodbav:"SK" json:"-"`
	ContactID        string `dynamodbav:"contactId" json:"contactId"`
	Revision         string `dynamodbav:"revision" json:"revision"`
	Name             string `dynamodbav:"name" json:"name"`
	Company          string `dynamodbav:"company" json:"company"`
	Title            string `dynamodbav:"title" json:"title"`
	Department       string `dynamodbav:"department" json:"department"`
	Email            string `dynamodbav:"email" json:"email"`
	Address          string `dynamodbav:"address" json:"address"`
	WorkPhone        string `dynamodbav:"workPhone" json:"workPhone"`
	Fax              string `dynamodbav:"fax" json:"fax"`
	Phone            string `dynamodbav:"phone" json:"phone"`
	CardRegisteredAt string `dynamodbav:"cardRegisteredAt" json:"cardRegisteredAt"`
	CardBook         string `dynamodbav:"cardBook" json:"cardBook"`
	Group            string `dynamodbav:"group" json:"group"`
	Notes            string `dynamodbav:"notes" json:"notes"`
	// SFDC link fields are sealed with the other fields; only the plain
	// SFDCCONTACT#/SFDCSUPPRESS# rows hold the IDs outside the envelope.
	Source        string `dynamodbav:"source,omitempty" json:"source,omitempty"`
	SfdcContactID string `dynamodbav:"sfdcContactId,omitempty" json:"sfdcContactId,omitempty"`
	SfdcAccountID string `dynamodbav:"sfdcAccountId,omitempty" json:"sfdcAccountId,omitempty"`
	SfdcLeftAt    string `dynamodbav:"sfdcLeftAt,omitempty" json:"sfdcLeftAt,omitempty"`
}

type AccountContactsPage struct {
	Contacts   []AccountContact `json:"contacts"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

type SpeakerTeamMember struct {
	UserID string `json:"userId"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

type SpeakerTeam struct {
	Members    []SpeakerTeamMember `json:"members"`
	NextCursor string              `json:"nextCursor,omitempty"`
}
