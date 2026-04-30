package model

import (
	"encoding/json"
	"time"
)

// CreateMeetingRequest represents the request body for creating a meeting
type CreateMeetingRequest struct {
	Title        string   `json:"title"`
	Date         string   `json:"date"`                   // ISO 8601 format
	Participants []string `json:"participants,omitempty"`
	SttProvider  string   `json:"sttProvider,omitempty"`  // "transcribe" or "nova-sonic"
}

// UpdateMeetingRequest represents the request body for updating a meeting
type UpdateMeetingRequest struct {
	Title              string   `json:"title,omitempty"`
	Content            string   `json:"content,omitempty"`
	Notes              string   `json:"notes,omitempty"`
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
	FileName  string `json:"fileName"`
	FileType  string `json:"fileType"`            // audio/webm, audio/mp4, audio/x-m4a, image/jpeg, image/png
	Category  string `json:"category"`            // "audio" or "image"
	MeetingID string `json:"meetingId,omitempty"` // required for image uploads
}

// PresignedURLResponse represents the response for presigned URL generation
type PresignedURLResponse struct {
	UploadURL string `json:"uploadUrl"`
	Key       string `json:"key"`
	ExpiresIn int    `json:"expiresIn"` // seconds
}

// UploadCompleteRequest represents the request body for upload completion notification
type UploadCompleteRequest struct {
	MeetingID string `json:"meetingId"`
	Key       string `json:"key"`
	Category  string `json:"category"`           // "audio", "image", or "file"
	FileName  string `json:"fileName,omitempty"`
	FileSize  int64  `json:"fileSize,omitempty"`
	MimeType  string `json:"mimeType,omitempty"`
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
	IsShared     bool     `json:"isShared"`
	SharedBy     *string  `json:"sharedBy,omitempty"`  // owner email if shared
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
	TranscriptA        string               `json:"transcriptA,omitempty"`
	TranscriptB        string               `json:"transcriptB,omitempty"`
	SelectedTranscript *string              `json:"selectedTranscript,omitempty"` // "A" | "B" | null
	AudioKey           string               `json:"audioKey,omitempty"`
	Transcription      json.RawMessage      `json:"transcription,omitempty"`
	Tags               []string             `json:"tags,omitempty"`
	ActionItems        json.RawMessage      `json:"actionItems,omitempty"`
	SpeakerMap         map[string]string    `json:"speakerMap,omitempty"`
	SttProvider        string               `json:"sttProvider,omitempty"`
	Attachments        []AttachmentResponse `json:"attachments,omitempty"`
	Shares             []ShareResponse      `json:"shares,omitempty"` // Only visible to owner
	CreatedAt          string               `json:"createdAt"`
	UpdatedAt          string               `json:"updatedAt"`
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
	UserID     string `json:"userId"`
	Email      string `json:"email"`
	Permission string `json:"permission"`
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
	APIKey string `json:"apiKey"`
}

// IntegrationStatusResponse represents the status of a single integration
type IntegrationStatusResponse struct {
	Configured bool   `json:"configured"`
	MaskedKey  string `json:"maskedKey,omitempty"`
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
	Status    string `json:"status"`
	JobID     string `json:"jobId,omitempty"`
	Message   string `json:"message,omitempty"`
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
