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
	ActionItems        string            `dynamodbav:"actionItems,omitempty"`        // JSON string of extracted action items
	Notes              string            `dynamodbav:"notes,omitempty"`              // User-written meeting notes (post-recording)
	SpeakerMap         map[string]string `dynamodbav:"speakerMap,omitempty"`         // spk_0 -> "김팀장" mapping
	Participants       []string          `dynamodbav:"participants,omitempty"`
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
	FileName         string    `dynamodbav:"fileName,omitempty"`
	FileSize         int64     `dynamodbav:"fileSize,omitempty"`
	MimeType         string    `dynamodbav:"mimeType,omitempty"`
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

// CrawlerSource represents a crawler source configuration
// PK: USER#{userId}, SK: CRAWLER#{sourceId}
type CrawlerSource struct {
	SourceID      string   `dynamodbav:"sourceId" json:"sourceId"`
	SourceName    string   `dynamodbav:"sourceName" json:"sourceName"`
	Subscribers   []string `dynamodbav:"subscribers" json:"subscribers"`
	AWSServices   []string `dynamodbav:"awsServices" json:"awsServices"`
	NewsQueries   []string `dynamodbav:"newsQueries" json:"newsQueries"`
	NewsSources   []string `dynamodbav:"newsSources" json:"newsSources"`
	CustomUrls    []string `dynamodbav:"customUrls" json:"customUrls"`
	Schedule      string   `dynamodbav:"schedule" json:"schedule"`
	LastCrawledAt string   `dynamodbav:"lastCrawledAt" json:"lastCrawledAt"`
	Status        string   `dynamodbav:"status" json:"status"`
	DocumentCount int      `dynamodbav:"documentCount" json:"documentCount"`
}

// CrawlerSubscription represents a user's subscription to a crawler source
// PK: USER#{userId}, SK: CRAWL_SUB#{sourceId}
type CrawlerSubscription struct {
	SourceID    string   `dynamodbav:"sourceId" json:"sourceId"`
	AWSServices []string `dynamodbav:"awsServices" json:"awsServices"`
	NewsSources []string `dynamodbav:"newsSources" json:"newsSources"`
	CustomUrls  []string `dynamodbav:"customUrls" json:"customUrls"`
	AddedAt     string   `dynamodbav:"addedAt" json:"addedAt"`
}

// CrawledDocument represents a document fetched by the crawler
// PK: CRAWLER#{sourceId}, SK: DOC#{docHash}
type CrawledDocument struct {
	DocHash     string   `dynamodbav:"docHash,omitempty" json:"docHash"`
	SourceID    string   `dynamodbav:"-" json:"sourceId,omitempty"`
	Type        string   `dynamodbav:"type" json:"type"`
	Title       string   `dynamodbav:"title" json:"title"`
	URL         string   `dynamodbav:"url" json:"url"`
	Source      string   `dynamodbav:"source,omitempty" json:"source"`
	Summary     string   `dynamodbav:"summary,omitempty" json:"summary"`
	AWSServices []string `dynamodbav:"awsServices,omitempty" json:"awsServices,omitempty"`
	Tags        []string `dynamodbav:"tags,omitempty" json:"tags,omitempty"`
	S3Key       string   `dynamodbav:"s3Key,omitempty" json:"s3Key"`
	CrawledAt   int64    `dynamodbav:"crawledAt" json:"crawledAt"`
	InKB        bool     `dynamodbav:"inKB,omitempty" json:"inKB"`
	PubDate     string   `dynamodbav:"pubDate,omitempty" json:"pubDate,omitempty"`
}

// CrawlHistory represents a crawl execution history entry
// PK: CRAWLER#{sourceId}, SK: HISTORY#{timestamp}
type CrawlHistory struct {
	Timestamp   string   `dynamodbav:"timestamp" json:"timestamp"`
	DocsAdded   int      `dynamodbav:"docsAdded" json:"docsAdded"`
	DocsUpdated int      `dynamodbav:"docsUpdated" json:"docsUpdated"`
	Errors      []string `dynamodbav:"errors" json:"errors"`
	Duration    int      `dynamodbav:"duration" json:"duration"`
}

// Research represents a deep research task
// PK: USER#{userId}, SK: RESEARCH#{researchId}
type Research struct {
	ResearchID   string `dynamodbav:"researchId" json:"researchId"`
	UserID       string `dynamodbav:"userId" json:"userId"`
	Topic        string `dynamodbav:"topic" json:"topic"`
	Mode         string `dynamodbav:"mode" json:"mode"`
	Status       string `dynamodbav:"status" json:"status"`
	CreatedAt    string `dynamodbav:"createdAt" json:"createdAt"`
	CompletedAt  string `dynamodbav:"completedAt,omitempty" json:"completedAt,omitempty"`
	S3Key        string `dynamodbav:"s3Key,omitempty" json:"s3Key,omitempty"`
	SourceCount  int    `dynamodbav:"sourceCount,omitempty" json:"sourceCount,omitempty"`
	WordCount    int    `dynamodbav:"wordCount,omitempty" json:"wordCount,omitempty"`
	Summary      string `dynamodbav:"summary,omitempty" json:"summary,omitempty"`
	ErrorMessage string `dynamodbav:"errorMessage,omitempty" json:"errorMessage,omitempty"`
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

// Attachment type constants (per API spec: photo, screenshot, diagram, whiteboard, document, video, audio_file)
const (
	AttachTypePhoto      = "photo"
	AttachTypeScreenshot = "screenshot"
	AttachTypeDiagram    = "diagram"
	AttachTypeWhiteboard = "whiteboard"
	AttachTypeDocument   = "document"
	AttachTypeVideo      = "video"
	AttachTypeAudioFile  = "audio_file"
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
	PrefixCrawler    = "CRAWLER#"
	PrefixCrawlSub   = "CRAWL_SUB#"
	PrefixDoc        = "DOC#"
	PrefixHistory    = "HISTORY#"
	PrefixConfig     = "CONFIG"
	PrefixResearch   = "RESEARCH#"
)

// Config SK constants
const (
	ConfigSKAllowedDomains = "ALLOWED_DOMAINS"
)

// AllowedDomainsConfig represents the allowed email domains configuration
// PK: CONFIG, SK: ALLOWED_DOMAINS
type AllowedDomainsConfig struct {
	PK         string    `dynamodbav:"PK"`
	SK         string    `dynamodbav:"SK"`
	Domains    []string  `dynamodbav:"domains"`
	UpdatedAt  time.Time `dynamodbav:"updatedAt"`
	UpdatedBy  string    `dynamodbav:"updatedBy"`
	EntityType string    `dynamodbav:"entityType"`
}
