package model

// Present distinguishes an absent attribute from its explicit empty value.
// Values retain DynamoDB's original strings, including timestamp formatting.
type SummaryValue struct {
	Present bool        `json:"present"`
	Value   interface{} `json:"value,omitempty"`
}
type SummaryCheck struct {
	PK     string                  `json:"pk"`
	SK     string                  `json:"sk"`
	Exists bool                    `json:"exists"`
	Fields map[string]SummaryValue `json:"fields,omitempty"`
}
type SummarySnapshot struct {
	Meeting     *Meeting
	Attachments []Attachment
	TextStates  map[string]*AttachmentTextState
	Checks      []SummaryCheck // first check is the canonical meeting
	Stored      map[string]interface{}
	Objects     []SummaryObject
}
type SummaryObject struct {
	Bucket    string `json:"bucket"`
	Key       string `json:"key"`
	ETag      string `json:"etag"`
	VersionID string `json:"versionId,omitempty"`
}
