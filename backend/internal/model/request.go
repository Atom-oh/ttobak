package model

import (
	"encoding/json"
	"time"
)

// CreateMeetingRequest represents the request body for creating a meeting
type CreateMeetingRequest struct {
	Title            string   `json:"title"`
	Date             string   `json:"date"` // ISO 8601 format
	Participants     []string `json:"participants,omitempty"`
	SttProvider      string   `json:"sttProvider,omitempty"` // "transcribe" or "nova-sonic"
	LinkedMeetingIDs []string `json:"linkedMeetingIds,omitempty"`
}

// MaxLiveSummaryRunes caps the client-settable liveSummary field, both at
// write time (service.UpdateMeeting) and when folded into the summarize
// prompt (cmd/summarize) -- one shared limit so the two can't drift.
const MaxLiveSummaryRunes = 32000

// UpdateMeetingRequest represents the request body for updating a meeting
type UpdateMeetingRequest struct {
	Title   string `json:"title,omitempty"`
	Content string `json:"content,omitempty"`
	// Notes is a pointer so "key omitted" (nil, preserve existing notes) is
	// distinguishable from "explicitly set to empty string" (non-nil
	// pointer to "", clears notes) -- a plain string can't represent
	// "clear the notes" since Go's zero value for string is already "".
	Notes *string `json:"notes,omitempty"`
	// LiveSummary is a pointer for the same omit-vs-explicit-empty semantics
	// as Notes: nil preserves the stored value, non-nil "" clears it.
	// Capped at MaxLiveSummaryRunes on write (service.UpdateMeeting) and
	// again when folded into the summarize prompt (cmd/summarize).
	//
	// Known residual risk (narrow window, not closed by this field): it's
	// written via a partial UpdateItem (UpdateMeetingFields), but other
	// mutations (UpdateSpeakers, SelectTranscript, account linking) still go
	// through UpdateMeeting's whole-item PutItem. A concurrent PutItem
	// carrying a read-time snapshot from before this field's write would
	// silently revert it -- the exact class of pre-existing, cross-cutting
	// issue ADR-025's Consequences section already tracks for AccountID/
	// SharedToAccount (fixed there only for ProjectIDs, via a
	// ConditionExpression + retry). Real-world exposure here is small: the
	// writers above run at points in a meeting's lifecycle that rarely
	// overlap with active live-summary writes (during/just after
	// recording).
	LiveSummary        *string  `json:"liveSummary,omitempty"`
	TranscriptA        string   `json:"transcriptA,omitempty"`
	SelectedTranscript string   `json:"selectedTranscript,omitempty"` // "A" or "B"
	Participants       []string `json:"participants,omitempty"`
	Status             string   `json:"status,omitempty"`
}

// SelectTranscriptRequest represents the request body for selecting a transcript
type SelectTranscriptRequest struct {
	Selected string `json:"selected"` // "A" or "B"
}

// UpdateSpeakersRequest represents the request body for mapping speaker labels to names
type UpdateSpeakersRequest struct {
	SpeakerMap map[string]string `json:"speakerMap"` // e.g. {"spk_0": "김팀장", "spk_1": "이매니저"}
}

// ShareMeetingRequest represents the request body for sharing a meeting
type ShareMeetingRequest struct {
	Email      string `json:"email"`
	Permission string `json:"permission"` // "read" or "edit"
}

// PresignedURLRequest represents the request body for generating a presigned URL
type PresignedURLRequest struct {
	FileName   string `json:"fileName"`
	FileType   string `json:"fileType"`            // audio/webm, audio/mp4, audio/x-m4a, image/jpeg, image/png
	Category   string `json:"category"`            // "audio" or "image"
	MeetingID  string `json:"meetingId,omitempty"` // required for image uploads
	PartIndex  int    `json:"partIndex,omitempty"` // 0-based index for multi-file audio
	TotalParts int    `json:"totalParts,omitempty"`
}

// PresignedURLResponse represents the response for presigned URL generation
type PresignedURLResponse struct {
	UploadURL string `json:"uploadUrl"`
	Key       string `json:"key"`
	ExpiresIn int    `json:"expiresIn"` // seconds
}

// UploadCompleteRequest represents the request body for upload completion notification
type UploadCompleteRequest struct {
	MeetingID  string `json:"meetingId"`
	Key        string `json:"key"`
	Category   string `json:"category"` // "audio", "image", or "file"
	FileName   string `json:"fileName,omitempty"`
	FileSize   int64  `json:"fileSize,omitempty"`
	MimeType   string `json:"mimeType,omitempty"`
	PartIndex  int    `json:"partIndex,omitempty"`  // 0-based index for multi-file audio
	TotalParts int    `json:"totalParts,omitempty"` // total number of audio parts
}

// UploadCompleteResponse represents the response for upload completion
type UploadCompleteResponse struct {
	Status string `json:"status"` // "processing"
}

// MeetingListResponse represents the response for listing meetings
type MeetingListResponse struct {
	Meetings   []MeetingListItem `json:"meetings"`
	NextCursor *string           `json:"nextCursor"` // base64-encoded LastEvaluatedKey or null
}

// MeetingListItem represents a meeting in list view
type MeetingListItem struct {
	MeetingID    string   `json:"meetingId"`
	Title        string   `json:"title"`
	Date         string   `json:"date"`
	Status       string   `json:"status"`
	Summary      string   `json:"summary,omitempty"` // First 200 chars
	Participants []string `json:"participants,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	Sentiment    string   `json:"sentiment,omitempty"`
	Duration     int      `json:"duration,omitempty"`
	IsShared     bool     `json:"isShared"`
	SharedBy     *string  `json:"sharedBy,omitempty"`   // owner email if shared
	Permission   *string  `json:"permission,omitempty"` // "read" | "edit" if shared
	CreatedAt    string   `json:"createdAt"`
	UpdatedAt    string   `json:"updatedAt"`
}

// MeetingDetailResponse represents a meeting in detail view
type MeetingDetailResponse struct {
	MeetingID          string               `json:"meetingId"`
	UserID             string               `json:"userId"`
	Title              string               `json:"title"`
	Date               string               `json:"date"`
	Status             string               `json:"status"`
	Participants       []string             `json:"participants,omitempty"`
	Content            string               `json:"content,omitempty"`
	Notes              string               `json:"notes,omitempty"`
	LiveSummary        string               `json:"liveSummary,omitempty"`
	TranscriptA        string               `json:"transcriptA,omitempty"`
	TranscriptB        string               `json:"transcriptB,omitempty"`
	SelectedTranscript *string              `json:"selectedTranscript,omitempty"` // "A" | "B" | null
	AudioKey           string               `json:"audioKey,omitempty"`
	AudioKeys          []string             `json:"audioKeys,omitempty"`
	AudioPartCount     int                  `json:"audioPartCount,omitempty"`
	AudioPartsReady    int                  `json:"audioPartsReady,omitempty"`
	Transcription      json.RawMessage      `json:"transcription,omitempty"`
	Tags               []string             `json:"tags,omitempty"`
	ActionItems        json.RawMessage      `json:"actionItems,omitempty"`
	SpeakerMap         map[string]string    `json:"speakerMap,omitempty"`
	SttProvider        string               `json:"sttProvider,omitempty"`
	LinkedMeetingIDs   []string             `json:"linkedMeetingIds,omitempty"`
	NotionPageID       string               `json:"notionPageId,omitempty"`
	Permission         string               `json:"permission"` // "owner", "read", or "edit"
	Attachments        []AttachmentResponse `json:"attachments,omitempty"`
	Shares             []ShareResponse      `json:"shares,omitempty"` // Only visible to owner
	SimRun             *SimRunResponse      `json:"simRun,omitempty"` // ADR-031 cost/sizing simulator, singleton per meeting
	CreatedAt          string               `json:"createdAt"`
	UpdatedAt          string               `json:"updatedAt"`
}

// SimChartResponse is one generated chart with its presigned CloudFront URL,
// attached by the handler the same way AttachmentResponse.URL is (see
// MeetingHandler.GetMeeting's presign loop).
type SimChartResponse struct {
	Key string `json:"key"`
	URL string `json:"url,omitempty"`
}

// SimRunResponse is the API shape of model.SimRun (ADR-031). Requirements/
// Options are re-parsed from their JSON-string storage form into typed
// slices for the frontend confirm form.
type SimRunResponse struct {
	SimRunID     string          `json:"simRunId"`
	Status       string          `json:"status"`
	Requirements []SimRequirement `json:"requirements,omitempty"`
	Options      []SimOption      `json:"options,omitempty"`
	Charts       []SimChartResponse `json:"charts,omitempty"`
	ReportMarkdown string        `json:"reportMarkdown,omitempty"`
	CodeKey        string        `json:"codeKey,omitempty"`
	PriceSnapshotAt string       `json:"priceSnapshotAt,omitempty"`
	ErrorMessage    string       `json:"errorMessage,omitempty"`
	CreatedAt       string       `json:"createdAt"`
	UpdatedAt       string       `json:"updatedAt"`
}

// CreateSimulationRequest is the body of POST /api/meetings/{id}/sim -- the
// user-confirmed/corrected form. Every field is re-validated server-side
// (see service.validateSimRequirements); nothing here is trusted as-is.
type CreateSimulationRequest struct {
	Requirements []SimRequirement `json:"requirements"`
	Options      []SimOption      `json:"options"`
}

// AttachmentResponse represents an attachment in API responses
type AttachmentResponse struct {
	AttachmentID     string `json:"attachmentId"`
	OriginalKey      string `json:"originalKey"`
	ProcessedKey     string `json:"processedKey,omitempty"`
	URL              string `json:"url,omitempty"`
	Type             string `json:"type"` // photo, screenshot, diagram, whiteboard, document, video, audio_file
	Status           string `json:"status"`
	Description      string `json:"description,omitempty"`
	ProcessedContent string `json:"processedContent,omitempty"`
	FileName         string `json:"fileName,omitempty"`
	FileSize         int64  `json:"fileSize,omitempty"`
	MimeType         string `json:"mimeType,omitempty"`
}

// UserSearchResponse represents a user in search results
type UserSearchResponse struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
	Name   string `json:"name,omitempty"`
}

// UserSearchListResponse represents the response for user search
type UserSearchListResponse struct {
	Users []UserSearchResponse `json:"users"`
}

// ShareResponse represents a share record in API responses
type ShareResponse struct {
	UserID     string     `json:"userId"`
	Email      string     `json:"email"`
	Permission string     `json:"permission"`
	SharedAt   *time.Time `json:"sharedAt,omitempty"`
}

// SharedWithResponse represents the response for sharing a meeting
type SharedWithResponse struct {
	SharedWith ShareResponse `json:"sharedWith"`
}

// APIError represents the error object in API responses
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ErrorResponse represents an error response matching API spec
type ErrorResponse struct {
	Error APIError `json:"error"`
}

// HealthResponse represents the health check response
type HealthResponse struct {
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
}

// MeetingUpdateResponse represents the response for updating a meeting
type MeetingUpdateResponse struct {
	MeetingID string `json:"meetingId"`
	UpdatedAt string `json:"updatedAt"`
}

// LinkMeetingsRequest represents the request body for linking follow-up meetings
type LinkMeetingsRequest struct {
	LinkedMeetingIDs []string `json:"linkedMeetingIds"`
}

// RediarizeMeetingRequest represents the request body for re-running speaker
// diarization with an updated speaker-count hint (see UploadService.RediarizeMeeting).
type RediarizeMeetingRequest struct {
	SpeakerCount int `json:"speakerCount"`
}

// AudioURLResponse represents the response for audio URL(s)
type AudioURLResponse struct {
	AudioUrl  string   `json:"audioUrl,omitempty"`
	AudioUrls []string `json:"audioUrls,omitempty"`
}

// InviteUserRequest represents the request body for inviting a new user (admin-only)
type InviteUserRequest struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
	Admin bool   `json:"admin,omitempty"`
}

// InviteUserResponse represents the response after successfully inviting a user
type InviteUserResponse struct {
	Email         string `json:"email"`
	Invited       bool   `json:"invited"`
	AddedToAdmins bool   `json:"addedToAdmins"`
}

// AdminUserSummary represents one row of the admin user-management panel
// (GET /api/settings/users). UserID is the Cognito Username, which for this
// pool is always the sub (see service.UserAdminService doc notes).
type AdminUserSummary struct {
	UserID      string     `json:"userId"`
	Email       string     `json:"email"`
	Name        string     `json:"name,omitempty"`
	Status      string     `json:"status"` // Cognito UserStatus (e.g. CONFIRMED, FORCE_CHANGE_PASSWORD)
	Enabled     bool       `json:"enabled"`
	IsAdmin     bool       `json:"isAdmin"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastLoginAt *time.Time `json:"lastLoginAt"` // nil = no login recorded yet (not necessarily dormant -- see Dormant)
	Dormant     bool       `json:"dormant"`     // true only when LastLoginAt is set and older than the dormancy threshold
}

// AdminUserListResponse represents the response for the admin user list
type AdminUserListResponse struct {
	Users []AdminUserSummary `json:"users"`
	// LastLoginUnavailable is true when the DynamoDB join for last-login
	// timestamps failed; Users is still returned with LastLoginAt/Dormant
	// left at their zero values rather than failing the whole request.
	LastLoginUnavailable bool `json:"lastLoginUnavailable,omitempty"`
	// Truncated is true if the Cognito user pool has more users than the
	// endpoint's internal pagination safety cap covers.
	Truncated bool `json:"truncated,omitempty"`
}

// AdminUserActionResponse represents the response for a single admin action
// (delete, enable, disable) on a user.
type AdminUserActionResponse struct {
	UserID string `json:"userId"`
	// Warning surfaces a non-fatal problem with the action (e.g. session
	// sign-out failed, or the admins group is now empty) without failing the
	// request -- the primary action already succeeded.
	Warning string `json:"warning,omitempty"`
}

// NewErrorResponse creates a new error response
func NewErrorResponse(code, message string) ErrorResponse {
	return ErrorResponse{
		Error: APIError{
			Code:    code,
			Message: message,
		},
	}
}

// Error codes
const (
	ErrCodeBadRequest    = "BAD_REQUEST"
	ErrCodeUnauthorized  = "UNAUTHORIZED"
	ErrCodeForbidden     = "FORBIDDEN"
	ErrCodeNotFound      = "NOT_FOUND"
	ErrCodeInternalError = "INTERNAL_ERROR"
)

// AskQuestionRequest represents the request body for asking a question about a meeting
type AskQuestionRequest struct {
	Question string `json:"question"`
}

// AskLiveRequest represents the request body for live Q&A (no meetingId required)
type AskLiveRequest struct {
	Question string `json:"question"`
	Context  string `json:"context"`
}

// AskQuestionResponse represents the response for asking a question
type AskQuestionResponse struct {
	Answer  string   `json:"answer"`
	Sources []string `json:"sources,omitempty"`
}

// ExportRequest represents the request body for exporting a meeting
type ExportRequest struct {
	Format string `json:"format"` // "pdf", "notion", "obsidian"
}

// ExportResponse represents the response for exporting a meeting
type ExportResponse struct {
	Format   string  `json:"format"`
	URL      *string `json:"url,omitempty"`      // For PDF download or Notion page URL
	Filename *string `json:"filename,omitempty"` // For file downloads
	Content  *string `json:"content,omitempty"`  // For Obsidian markdown
}

// IntegrationRequest represents the request body for saving an integration
type IntegrationRequest struct {
	APIKey     string `json:"apiKey"`
	ParentPage string `json:"parentPage"` // Notion page/database URL or ID to create exports under
}

// IntegrationStatusResponse represents the status of a single integration
type IntegrationStatusResponse struct {
	Configured   bool   `json:"configured"`
	MaskedKey    string `json:"maskedKey,omitempty"`
	ParentPageID string `json:"parentPageId,omitempty"` // empty means a legacy record needing re-connect
}

// IntegrationsResponse represents the response for listing integrations
type IntegrationsResponse struct {
	Notion *IntegrationStatusResponse `json:"notion,omitempty"`
}

// AllowedDomainsResponse represents the response for allowed domains
type AllowedDomainsResponse struct {
	Domains  []string `json:"domains"`
	Enforced bool     `json:"enforced"`
}

// UpdateAllowedDomainsRequest represents the request body for updating allowed domains
type UpdateAllowedDomainsRequest struct {
	Domains []string `json:"domains"`
}

// KBUploadRequest represents the request body for KB file upload
type KBUploadRequest struct {
	FileName string `json:"fileName"`
	FileType string `json:"fileType"`
}

// KBUploadResponse represents the response for KB upload presigned URL
type KBUploadResponse struct {
	UploadURL string `json:"uploadUrl"`
	Key       string `json:"key"`
	ExpiresIn int    `json:"expiresIn"`
}

// KBFileResponse represents a file in the knowledge base
type KBFileResponse struct {
	FileID       string `json:"fileId"`
	FileName     string `json:"fileName"`
	FileType     string `json:"fileType"`
	Size         int64  `json:"size"`
	LastModified string `json:"lastModified"`
}

// KBFilesResponse represents the response for listing KB files
type KBFilesResponse struct {
	Files []KBFileResponse `json:"files"`
}

// KBSyncResponse represents the response for KB sync
type KBSyncResponse struct {
	Status  string `json:"status"`
	JobID   string `json:"jobId,omitempty"`
	Message string `json:"message,omitempty"`
}

// AddCrawlerSourceRequest represents the request body for adding a crawler source
type AddCrawlerSourceRequest struct {
	SourceName  string   `json:"sourceName"`
	AWSServices []string `json:"awsServices"`
	NewsSources []string `json:"newsSources"`
	CustomUrls  []string `json:"customUrls,omitempty"`
	NewsQueries []string `json:"newsQueries,omitempty"`
}

// UpdateCrawlerSourceRequest represents the request body for updating a crawler source
type UpdateCrawlerSourceRequest struct {
	AWSServices []string `json:"awsServices"`
	NewsSources []string `json:"newsSources"`
	NewsQueries []string `json:"newsQueries,omitempty"`
	CustomUrls  []string `json:"customUrls,omitempty"`
}

// CrawlerSourceResponse represents a single crawler source with its subscription
type CrawlerSourceResponse struct {
	Source       CrawlerSource       `json:"source"`
	Subscription CrawlerSubscription `json:"subscription"`
}

// CrawlerSourcesResponse represents the response for listing crawler sources
type CrawlerSourcesResponse struct {
	Sources []CrawlerSourceResponse `json:"sources"`
}

// CrawlHistoryResponse represents the response for crawl history
type CrawlHistoryResponse struct {
	History []CrawlHistory `json:"history"`
}

// InsightsResponse represents the response for insights/documents listing
type InsightsResponse struct {
	Documents  []CrawledDocument `json:"documents"`
	TotalCount int               `json:"totalCount"`
	Page       int               `json:"page"`
	Limit      int               `json:"limit"`
}

// InsightDetailResponse represents the full content of a crawled document
type InsightDetailResponse struct {
	CrawledDocument
	Content string `json:"content"`
}

// CreateResearchRequest represents the request body for creating a research task
type CreateResearchRequest struct {
	Topic string `json:"topic"`
	Mode  string `json:"mode"`
}

// ResearchResponse represents a single research task in API responses
type ResearchResponse struct {
	Research
	Content string          `json:"content,omitempty"`
	Shares  []ShareResponse `json:"shares,omitempty"`
}

// ResearchListResponse represents the response for listing research tasks
type ResearchListResponse struct {
	Research []Research `json:"research"`
}

// ToMeetingListItem converts a Meeting to MeetingListItem
func ToMeetingListItem(m *Meeting, isShared bool, sharedBy *string, permission *string) MeetingListItem {
	summary := m.Content
	if len(summary) > 200 {
		summary = summary[:200] + "..."
	}

	return MeetingListItem{
		MeetingID:    m.MeetingID,
		Title:        m.Title,
		Date:         m.Date.Format(time.RFC3339),
		Status:       m.Status,
		Summary:      summary,
		Participants: m.Participants,
		Tags:         m.Tags,
		Sentiment:    m.Sentiment,
		Duration:     m.Duration,
		IsShared:     isShared,
		SharedBy:     sharedBy,
		Permission:   permission,
		CreatedAt:    m.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    m.UpdatedAt.Format(time.RFC3339),
	}
}

// ToMeetingDetailResponse converts a Meeting to MeetingDetailResponse
func ToMeetingDetailResponse(m *Meeting, attachments []AttachmentResponse, shares []ShareResponse) MeetingDetailResponse {
	var selectedTranscript *string
	if m.SelectedTranscript != "" {
		selectedTranscript = &m.SelectedTranscript
	}

	return MeetingDetailResponse{
		MeetingID:          m.MeetingID,
		UserID:             m.UserID,
		Title:              m.Title,
		Date:               m.Date.Format(time.RFC3339),
		Status:             m.Status,
		Participants:       m.Participants,
		Content:            m.Content,
		Notes:              m.Notes,
		LiveSummary:        m.LiveSummary,
		TranscriptA:        m.TranscriptA,
		TranscriptB:        m.TranscriptB,
		SelectedTranscript: selectedTranscript,
		AudioKey:           m.AudioKey,
		Attachments:        attachments,
		Shares:             shares,
		CreatedAt:          m.CreatedAt.Format(time.RFC3339),
		UpdatedAt:          m.UpdatedAt.Format(time.RFC3339),
	}
}
