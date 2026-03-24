package model

import "time"

// Meeting represents a meeting record in DynamoDB
// PK: USER#{userId}, SK: MEETING#{meetingId}
type Meeting struct {
	PK                 string    `dynamodbav:"PK"`
	SK                 string    `dynamodbav:"SK"`
	MeetingID          string    `dynamodbav:"meetingId"`
	UserID             string    `dynamodbav:"userId"`
	Title              string    `dynamodbav:"title"`
	Date               time.Time `dynamodbav:"date"`
	Content            string    `dynamodbav:"content,omitempty"`            // Markdown meeting notes
	TranscriptA        string    `dynamodbav:"transcriptA,omitempty"`        // AWS Transcribe result
	TranscriptB        string    `dynamodbav:"transcriptB,omitempty"`        // Nova Sonic result
	SelectedTranscript string    `dynamodbav:"selectedTranscript,omitempty"` // "A" or "B"
	AudioKey           string    `dynamodbav:"audioKey,omitempty"`           // S3 key for audio file
	SttProvider        string    `dynamodbav:"sttProvider,omitempty"`        // "transcribe" or "nova-sonic"
	TranscriptSegments string    `dynamodbav:"transcriptSegments,omitempty"` // JSON string of speaker-labeled segments
	Participants       []string  `dynamodbav:"participants,omitempty"`
	Tags               []string  `dynamodbav:"tags,omitempty"`
	Status             string    `dynamodbav:"status"` // recording, transcribing, summarizing, done, error
	CreatedAt          time.Time `dynamodbav:"createdAt"`
	UpdatedAt          time.Time `dynamodbav:"updatedAt"`
	GSI1PK             string    `dynamodbav:"GSI1PK,omitempty"` // USER#{userId} for date sorting
	GSI1SK             string    `dynamodbav:"GSI1SK,omitempty"` // timestamp for sorting
	EntityType         string    `dynamodbav:"entityType"`       // "MEETING"
}

// Attachment represents a file attachment for a meeting
// PK: MEETING#{meetingId}, SK: ATTACH#{attachmentId}
type Attachment struct {
	PK               string    `dynamodbav:"PK"`
	SK               string    `dynamodbav:"SK"`
	AttachmentID     string    `dynamodbav:"attachmentId"`
	MeetingID        string    `dynamodbav:"meetingId"`
	UserID           string    `dynamodbav:"userId"`
	OriginalKey      string    `dynamodbav:"originalKey"`
	ProcessedKey     string    `dynamodbav:"processedKey,omitempty"`
	Type             string    `dynamodbav:"type"`                       // photo, screenshot, diagram, whiteboard
	Status           string    `dynamodbav:"status"`                     // uploaded, processing, done
	Description      string    `dynamodbav:"description,omitempty"`
	ProcessedContent string    `dynamodbav:"processedContent,omitempty"` // Mermaid/markdown result
	CreatedAt        time.Time `dynamodbav:"createdAt"`
	EntityType       string    `dynamodbav:"entityType"` // "ATTACHMENT"
}

// Share represents a shared meeting access record
// For recipient lookup: PK: USER#{sharedToUserId}, SK: SHARED#{meetingId}
// For meeting's share list: PK: MEETING#{meetingId}, SK: SHARE_TO#{userId}
type Share struct {
	PK         string    `dynamodbav:"PK"`
	SK         string    `dynamodbav:"SK"`
	MeetingID  string    `dynamodbav:"meetingId"`
	OwnerID    string    `dynamodbav:"ownerId"`
	OwnerEmail string    `dynamodbav:"ownerEmail,omitempty"`
	SharedToID string    `dynamodbav:"sharedToId"`
	Email      string    `dynamodbav:"email"`
	Permission string    `dynamodbav:"permission"` // "read" or "edit"
	CreatedAt  time.Time `dynamodbav:"createdAt"`
	EntityType string    `dynamodbav:"entityType"` // "SHARE"
}

// User represents a user record (for search functionality)
// PK: USER#{userId}, SK: PROFILE
type User struct {
	PK         string    `dynamodbav:"PK"`
	SK         string    `dynamodbav:"SK"`
	UserID     string    `dynamodbav:"userId"`
	Email      string    `dynamodbav:"email"`
	Name       string    `dynamodbav:"name,omitempty"`
	CreatedAt  time.Time `dynamodbav:"createdAt"`
	GSI2PK     string    `dynamodbav:"GSI2PK,omitempty"` // EMAIL#{email} for email search
	GSI2SK     string    `dynamodbav:"GSI2SK,omitempty"` // USER#{userId}
	EntityType string    `dynamodbav:"entityType"`       // "USER"
}

// MeetingStatus constants
const (
	StatusRecording    = "recording"
	StatusTranscribing = "transcribing"
	StatusSummarizing  = "summarizing"
	StatusDone         = "done"
	StatusError        = "error"
)

// Permission constants
const (
	PermissionRead = "read"
	PermissionEdit = "edit"
)

// Attachment type constants (per API spec: photo, screenshot, diagram, whiteboard)
const (
	AttachTypePhoto      = "photo"
	AttachTypeScreenshot = "screenshot"
	AttachTypeDiagram    = "diagram"
	AttachTypeWhiteboard = "whiteboard"
)

// Attachment status constants
const (
	AttachStatusUploaded   = "uploaded"
	AttachStatusProcessing = "processing"
	AttachStatusDone       = "done"
)

// Key prefixes for single table design
const (
	PrefixUser       = "USER#"
	PrefixMeeting    = "MEETING#"
	PrefixAttachment = "ATTACH#"
	PrefixShare      = "SHARED#"
	PrefixShareTo    = "SHARE_TO#"
	PrefixEmail      = "EMAIL#"
	PrefixProfile    = "PROFILE"
)
