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
	Content            string    `dynamodbav:"content,omitempty"`                      // Markdown meeting notes
	TranscriptA        string    `dynamodbav:"transcriptA,omitempty"`                  // AWS Transcribe result
	TranscriptB        string    `dynamodbav:"transcriptB,omitempty"`                  // Nova Sonic result
	SelectedTranscript string    `dynamodbav:"selectedTranscript,omitempty"`           // "A" or "B"
	AudioKey           string    `dynamodbav:"audioKey,omitempty"`                     // S3 key for audio file (legacy single-file)
	AudioKeys          []string  `dynamodbav:"audioKeys,omitempty"`                    // Ordered S3 keys for multi-file uploads
	AudioPartCount     int       `dynamodbav:"audioPartCount,omitempty"`               // Total parts expected
	AudioPartsReady    int       `dynamodbav:"audioPartsReady,omitempty"`              // Parts with completed transcription
	AudioPartsReadySet []int     `dynamodbav:"audioPartsReadySet,omitempty,numberset"` // Set of completed part indices (source-of-truth for idempotent counting)
	// AllPartsEmittedAt marks the moment we first emitted the
	// `AllPartsTranscribed` EventBridge event for this meeting. Used as a
	// once-only lock so EventBridge at-least-once re-deliveries of part
	// transcripts don't republish duplicate events. Populated via
	// conditional update `SET allPartsEmittedAt = :now WHERE attribute_not_exists(...)`.
	AllPartsEmittedAt  string            `dynamodbav:"allPartsEmittedAt,omitempty"`
	SttProvider        string            `dynamodbav:"sttProvider,omitempty"`        // "transcribe" or "nova-sonic"
	TranscriptSegments string            `dynamodbav:"transcriptSegments,omitempty"` // JSON string of speaker-labeled segments
	ActionItems        string            `dynamodbav:"actionItems,omitempty"`        // JSON string of extracted action items
	Notes              string            `dynamodbav:"notes,omitempty"`              // User-written meeting notes (post-recording)
	LiveSummary        string            `dynamodbav:"liveSummary,omitempty"`        // Real-time summary built during recording (markdown incl. mermaid)
	SpeakerMap         map[string]string `dynamodbav:"speakerMap,omitempty"`         // spk_0 -> "김팀장" mapping
	// DiarizationSpeakerHint overrides len(Participants) as pyannote's
	// max_speakers bound when re-diarizing a meeting on demand (see
	// RediarizeMeeting) — set once a user-supplied headcount is known to be
	// more accurate than the registered Participants list.
	DiarizationSpeakerHint int      `dynamodbav:"diarizationSpeakerHint,omitempty"`
	Participants           []string `dynamodbav:"participants,omitempty"`
	Tags                   []string `dynamodbav:"tags,omitempty"`
	Sentiment              string   `dynamodbav:"sentiment,omitempty"`        // "positive", "neutral", "negative" — extracted by summarize Lambda
	Duration               int      `dynamodbav:"duration,omitempty"`         // Total audio length in seconds, written by the summarize Lambda
	Insights               string   `dynamodbav:"insights,omitempty"`         // JSON []MeetingInsight
	LinkedMeetingIDs       []string `dynamodbav:"linkedMeetingIds,omitempty"` // Chronologically ordered predecessor IDs
	AccountID              string   `dynamodbav:"accountId,omitempty"`        // linked Account
	// ProjectIDs links this meeting to zero or more projects (many-to-many).
	// Stored as a DynamoDB String Set (stringset tag), not a List, so link
	// mutations can use atomic ADD/DELETE without a read-modify-write race.
	ProjectIDs      []string  `dynamodbav:"projectIds,omitempty,stringset" json:"projectIds,omitempty"`
	SharedToAccount bool      `dynamodbav:"sharedToAccount,omitempty"` // published to account team
	NotionPageID    string    `dynamodbav:"notionPageId,omitempty"`    // Notion page created by a prior export; re-export updates it in place instead of creating a duplicate
	Status          string    `dynamodbav:"status"`                    // recording, transcribing, summarizing, done, error
	CreatedAt       time.Time `dynamodbav:"createdAt"`
	UpdatedAt       time.Time `dynamodbav:"updatedAt"`
	GSI1PK          string    `dynamodbav:"GSI1PK,omitempty"` // USER#{userId} for date sorting
	GSI1SK          string    `dynamodbav:"GSI1SK,omitempty"` // timestamp for sorting
	EntityType      string    `dynamodbav:"entityType"`       // "MEETING"
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
	Type             string    `dynamodbav:"type"`   // photo, screenshot, diagram, whiteboard
	Status           string    `dynamodbav:"status"` // uploaded, processing, done
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
	PK         string `dynamodbav:"PK"`
	SK         string `dynamodbav:"SK"`
	MeetingID  string `dynamodbav:"meetingId"`
	OwnerID    string `dynamodbav:"ownerId"`
	OwnerEmail string `dynamodbav:"ownerEmail,omitempty"`
	SharedToID string `dynamodbav:"sharedToId"`
	Email      string `dynamodbav:"email"`
	Permission string `dynamodbav:"permission"` // "read" or "edit"
	// Origin distinguishes a share created by ShareMeetingToAccount ("account")
	// from one created by the owner directly via ShareMeetingByEmail (""/direct).
	// RemoveMember's cleanup must only ever delete "account"-origin shares -- a
	// direct share is a separate grant the owner made explicitly and removing a
	// team member must never silently revoke it.
	Origin string `dynamodbav:"origin,omitempty"`
	// AccountID is set only on Origin=="account" shares (by CreateShareIfMember,
	// and retroactively by the backfill CLI) to the account that granted them.
	// DeleteShareIfAccountOrigin ties its delete condition to this field so a
	// meeting re-shared to a DIFFERENT account between RemoveMember's cleanup
	// read and its delete can never have that new account's grant swept up by
	// the old account's removal -- the meeting.AccountID re-check that guards
	// the read is a separate, non-atomic call and can't by itself prevent that
	// race; only a condition on the row being deleted can.
	AccountID  string    `dynamodbav:"accountId,omitempty"`
	CreatedAt  time.Time `dynamodbav:"createdAt"`
	EntityType string    `dynamodbav:"entityType"` // "SHARE"
}

const (
	ShareOriginAccount = "account"
)

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

// UserLogin represents the last-login timestamp for the admin user-management
// panel, written by the Cognito PostAuthentication trigger
// (infra/lambda/post-authentication). Deliberately a separate item from
// User/PROFILE above: writing lastLoginAt onto PROFILE would risk creating a
// stub profile item (missing email/GSI2PK/GSI2SK) if the trigger's write
// raced GetOrCreateUser on a user's very first login, silently breaking
// email-based search/sharing for that user. This item carries no GSI keys.
// PK: USER#{userId}, SK: LOGIN
type UserLogin struct {
	PK          string    `dynamodbav:"PK"`
	SK          string    `dynamodbav:"SK"`
	LastLoginAt time.Time `dynamodbav:"lastLoginAt"`
	EntityType  string    `dynamodbav:"entityType"` // "USER_LOGIN"
}

// CrawlerSource represents a crawler source configuration
// PK: USER#{userId}, SK: CRAWLER#{sourceId}
type CrawlerSource struct {
	SourceID      string   `dynamodbav:"sourceId" json:"sourceId"`
	SourceName    string   `dynamodbav:"sourceName" json:"sourceName"`
	OwnerID       string   `dynamodbav:"ownerId,omitempty" json:"ownerId,omitempty"` // creator; subscribing later never changes this
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
	NewsQueries []string `dynamodbav:"newsQueries" json:"newsQueries"`
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
	ResearchID string `dynamodbav:"researchId" json:"researchId"`
	UserID     string `dynamodbav:"userId" json:"userId"`
	// Title is a user-editable display label, defaulting to Topic at
	// creation. Kept distinct from Topic (the original research prompt,
	// permanently immutable) so a rename never mutates the prompt that
	// the agent pipeline (research-worker, research-agent) actually acts
	// on -- only the UI's display string changes.
	Title        string `dynamodbav:"title,omitempty" json:"title,omitempty"`
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
	ParentID     string `dynamodbav:"parentId,omitempty" json:"parentId,omitempty"`
	TrashedAt    string `dynamodbav:"trashedAt,omitempty" json:"trashedAt,omitempty"`
	IsShared     bool   `dynamodbav:"-" json:"isShared,omitempty"`
	SharedBy     string `dynamodbav:"-" json:"sharedBy,omitempty"`
	// AccountIDs links this research to zero or more accounts (many-to-many),
	// mirroring Meeting.AccountID but pluralized since one research report can
	// be relevant to several customer accounts. See account.go's ResearchRef
	// for the reverse-lookup item stored in each account's partition.
	//
	// Stored as a DynamoDB String Set (stringset tag), not a List -- this
	// lets LinkAccount/UnlinkAccount use atomic ADD/DELETE update
	// expressions instead of a read-modify-write on the whole list, which
	// would otherwise lose one side's change when two accounts are
	// (un)linked concurrently (ADD/DELETE on a set has no such race, and is
	// naturally idempotent for a value already present/absent).
	AccountIDs []string `dynamodbav:"accountIds,omitempty,stringset" json:"accountIds,omitempty"`
	// ProjectIDs links this research to zero or more projects (many-to-many).
	// Stored as a DynamoDB String Set (stringset tag), not a List, so link
	// mutations can use atomic ADD/DELETE without a read-modify-write race.
	ProjectIDs []string `dynamodbav:"projectIds,omitempty,stringset" json:"projectIds,omitempty"`
}

const MaxAudioParts = 10

func (m *Meeting) GetEffectiveAudioKeys() []string {
	if len(m.AudioKeys) > 0 {
		return m.AudioKeys
	}
	if m.AudioKey != "" {
		return []string{m.AudioKey}
	}
	return nil
}

func (m *Meeting) IsMultiPart() bool {
	return m.AudioPartCount > 1
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

	// PrefixPendingShare / PrefixPendingAccount / PrefixPendingMeeting key the
	// PendingShare item below: PK: PENDINGSHARE#{email}, SK:
	// PENDING_ACCOUNT#{accountId} or PENDING_MEETING#{meetingId}.
	PrefixPendingShare   = "PENDINGSHARE#"
	PrefixPendingAccount = "PENDING_ACCOUNT#"
	PrefixPendingMeeting = "PENDING_MEETING#"
)

// PendingShare records an Account- or Meeting-share grant made to an email
// address that has been invited (a Cognito account exists) but has never
// completed a first login -- so no PROFILE row exists yet and
// GetUserByEmail can't resolve it to a userID. AddMember/ShareMeetingByEmail
// write one of these instead of failing outright when the target is in
// exactly this state; DynamoDBRepository.MaterializePendingShares turns it
// into a real AccountMember or Share row the moment GetOrCreateUser creates
// that email's PROFILE row (its first authenticated request after
// accepting the invite), so the grant becomes visible immediately on
// sign-in with no separate "pending invites" step for the invitee.
// PK: PENDINGSHARE#{email}, SK: PENDING_ACCOUNT#{accountId} | PENDING_MEETING#{meetingId}
type PendingShare struct {
	PK              string    `dynamodbav:"PK"`
	SK              string    `dynamodbav:"SK"`
	Email           string    `dynamodbav:"email"`
	Kind            string    `dynamodbav:"kind"` // "account" | "meeting"
	AccountID       string    `dynamodbav:"accountId,omitempty"`
	MeetingID       string    `dynamodbav:"meetingId,omitempty"`
	Role            string    `dynamodbav:"role,omitempty"`       // set when Kind=="account"
	Permission      string    `dynamodbav:"permission,omitempty"` // set when Kind=="meeting"
	InvitedByUserID string    `dynamodbav:"invitedByUserId"`
	InvitedByEmail  string    `dynamodbav:"invitedByEmail,omitempty"`
	CreatedAt       time.Time `dynamodbav:"createdAt"`
	// TTL is a DynamoDB TTL epoch-seconds timestamp (infra/lib/storage-
	// stack.ts's `timeToLiveAttribute: 'ttl'`) -- a mis-typed or stale
	// invite's queued grant is not revocable by its inviter (no list/cancel
	// API, see docs/superpowers/specs/2026-08-04-pending-email-invites-
	// design.md's explicit YAGNI), so this bounds how long it stays
	// claimable instead of granting access at an arbitrary future point.
	TTL        int64  `dynamodbav:"ttl"`
	EntityType string `dynamodbav:"entityType"` // "PENDING_SHARE"
}

// PendingShareTTL is how long a queued grant stays claimable before DynamoDB
// TTL expires it -- long enough that a real invitee who's slow to log in
// isn't punished, short enough that a mis-typed email doesn't sit as a
// silent standing grant forever.
const PendingShareTTL = 30 * 24 * time.Hour

const (
	PendingShareKindAccount = "account"
	PendingShareKindMeeting = "meeting"

	EntityTypePendingShare = "PENDING_SHARE"
)

// SKUserLogin is the sort key for the UserLogin item (see UserLogin doc comment).
const SKUserLogin = "LOGIN"

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
