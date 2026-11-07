package model

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
