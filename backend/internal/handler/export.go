package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

// ExportHandler handles export-related requests
type ExportHandler struct {
	meetingService *service.MeetingService
	notionService  *service.NotionService
	repo           *repository.DynamoDBRepository
}

// NewExportHandler creates a new export handler
func NewExportHandler(
	meetingService *service.MeetingService,
	notionService *service.NotionService,
	repo *repository.DynamoDBRepository,
) *ExportHandler {
	return &ExportHandler{
		meetingService: meetingService,
		notionService:  notionService,
		repo:           repo,
	}
}

// ExportMeeting handles POST /api/meetings/{meetingId}/export
func (h *ExportHandler) ExportMeeting(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	meetingID := chi.URLParam(r, "meetingId")

	if meetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "meetingId is required")
		return
	}

	var req model.ExportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	// Get meeting details
	meetingDetail, err := h.meetingService.GetMeetingDetail(ctx, userID, meetingID)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
			return
		}
		if errors.Is(err, service.ErrForbidden) {
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	switch req.Format {
	case "pdf":
		// PDF export - placeholder, return content as text for now
		content := h.generatePDFContent(meetingDetail)
		filename := fmt.Sprintf("%s.txt", sanitizeFilename(meetingDetail.Title))
		writeJSON(w, http.StatusOK, model.ExportResponse{
			Format:   "pdf",
			Filename: &filename,
			Content:  &content,
		})

	case "notion":
		// Export to Notion
		integration, err := h.repo.GetIntegration(ctx, userID, "notion")
		if err != nil {
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
			return
		}
		if integration == nil {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Notion integration not configured")
			return
		}

		content := h.generateMarkdownContent(meetingDetail)
		_, pageURL, err := h.notionService.CreatePage(ctx, integration.APIKey, meetingDetail.Title, content)
		if err != nil {
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Failed to create Notion page: "+err.Error())
			return
		}

		writeJSON(w, http.StatusOK, model.ExportResponse{
			Format: "notion",
			URL:    &pageURL,
		})

	case "obsidian":
		// Generate Obsidian-compatible markdown
		content, filename := h.generateObsidianContent(ctx, userID, meetingDetail)
		writeJSON(w, http.StatusOK, model.ExportResponse{
			Format:   "obsidian",
			Filename: &filename,
			Content:  &content,
		})

	default:
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid format. Supported: pdf, notion, obsidian")
	}
}

// ExportObsidian handles GET /api/meetings/{meetingId}/export/obsidian
func (h *ExportHandler) ExportObsidian(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	meetingID := chi.URLParam(r, "meetingId")

	if meetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "meetingId is required")
		return
	}

	// Get meeting details
	meetingDetail, err := h.meetingService.GetMeetingDetail(ctx, userID, meetingID)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
			return
		}
		if errors.Is(err, service.ErrForbidden) {
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	content, filename := h.generateObsidianContent(ctx, userID, meetingDetail)

	writeJSON(w, http.StatusOK, map[string]string{
		"filename": filename,
		"content":  content,
	})
}

// generatePDFContent generates content for PDF export
func (h *ExportHandler) generatePDFContent(meeting *model.MeetingDetailResponse) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Title: %s\n", meeting.Title))
	sb.WriteString(fmt.Sprintf("Date: %s\n", meeting.Date))
	sb.WriteString(fmt.Sprintf("Status: %s\n\n", meeting.Status))

	if len(meeting.Participants) > 0 {
		sb.WriteString(fmt.Sprintf("Participants: %s\n\n", strings.Join(meeting.Participants, ", ")))
	}

	if meeting.Content != "" {
		sb.WriteString("--- Summary ---\n\n")
		sb.WriteString(meeting.Content)
		sb.WriteString("\n\n")
	}

	// Include selected transcript
	transcript := meeting.TranscriptA
	if meeting.SelectedTranscript != nil && *meeting.SelectedTranscript == "B" && meeting.TranscriptB != "" {
		transcript = meeting.TranscriptB
	}
	if transcript != "" {
		sb.WriteString("--- Transcription ---\n\n")
		sb.WriteString(transcript)
		sb.WriteString("\n")
	}

	return sb.String()
}

// generateMarkdownContent generates markdown content for Notion
func (h *ExportHandler) generateMarkdownContent(meeting *model.MeetingDetailResponse) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# %s\n\n", meeting.Title))
	sb.WriteString(fmt.Sprintf("**Date:** %s\n", meeting.Date))
	sb.WriteString(fmt.Sprintf("**Status:** %s\n\n", meeting.Status))

	if len(meeting.Participants) > 0 {
		sb.WriteString(fmt.Sprintf("**Participants:** %s\n\n", strings.Join(meeting.Participants, ", ")))
	}

	if meeting.Content != "" {
		sb.WriteString("## Summary\n\n")
		sb.WriteString(meeting.Content)
		sb.WriteString("\n\n")
	}

	// Include selected transcript
	transcript := meeting.TranscriptA
	if meeting.SelectedTranscript != nil && *meeting.SelectedTranscript == "B" && meeting.TranscriptB != "" {
		transcript = meeting.TranscriptB
	}
	if transcript != "" {
		sb.WriteString("## Transcription\n\n")
		sb.WriteString(transcript)
		sb.WriteString("\n")
	}

	return sb.String()
}

// obsidianActionItem mirrors the ActionItem struct from bedrock.go for JSON parsing
type obsidianActionItem struct {
	Text     string `json:"text"`
	Assignee string `json:"assignee,omitempty"`
	DueDate  string `json:"dueDate,omitempty"`
	Priority string `json:"priority,omitempty"`
	Done     bool   `json:"done"`
}

// obsidianSegment mirrors TranscriptSegmentOut for JSON parsing
type obsidianSegment struct {
	Speaker   string  `json:"speaker"`
	Text      string  `json:"text"`
	StartTime float64 `json:"startTime"`
	EndTime   float64 `json:"endTime"`
}

// formatTimestamp formats seconds into HH:MM:SS
func formatTimestamp(seconds float64) string {
	total := int(seconds)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

// generateObsidianContent generates Obsidian-compatible markdown with YAML frontmatter
func (h *ExportHandler) generateObsidianContent(ctx context.Context, userID string, meeting *model.MeetingDetailResponse) (string, string) {
	var sb strings.Builder

	// Parse date for frontmatter
	dateStr := meeting.Date
	parsedDate, err := time.Parse(time.RFC3339, meeting.Date)
	if err == nil {
		dateStr = parsedDate.Format("2006-01-02")
	}

	// Generate filename first (used for aliases)
	filename := fmt.Sprintf("%s - %s.md", dateStr, sanitizeFilename(meeting.Title))

	// ── YAML Frontmatter ──
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("title: \"%s\"\n", escapeYAMLString(meeting.Title)))
	sb.WriteString(fmt.Sprintf("date: %s\n", dateStr))
	sb.WriteString("type: meeting\n")
	sb.WriteString(fmt.Sprintf("status: %s\n", meeting.Status))

	if meeting.SttProvider != "" {
		sb.WriteString(fmt.Sprintf("stt-provider: %s\n", meeting.SttProvider))
	}

	if len(meeting.Participants) > 0 {
		sb.WriteString("participants:\n")
		for _, p := range meeting.Participants {
			sb.WriteString(fmt.Sprintf("  - \"%s\"\n", escapeYAMLString(p)))
		}
	}

	if len(meeting.Tags) > 0 {
		sb.WriteString("tags:\n")
		for _, t := range meeting.Tags {
			sb.WriteString(fmt.Sprintf("  - %s\n", t))
		}
	} else {
		sb.WriteString("tags:\n  - meeting\n")
	}

	// Aliases for natural linking
	if parsedDate, err := time.Parse(time.RFC3339, meeting.Date); err == nil {
		alias := parsedDate.Format("1월 2일") + " 미팅"
		sb.WriteString("aliases:\n")
		sb.WriteString(fmt.Sprintf("  - \"%s\"\n", alias))
	}

	sb.WriteString("---\n\n")

	// ── Title ──
	sb.WriteString(fmt.Sprintf("# %s\n\n", meeting.Title))

	// ── Summary ──
	if meeting.Content != "" {
		sb.WriteString("## 요약\n\n")
		sb.WriteString(meeting.Content)
		sb.WriteString("\n\n")
	}

	// ── Action Items (structured JSON if available, fallback to text extraction) ──
	var structuredItems []obsidianActionItem
	hasStructured := false
	if len(meeting.ActionItems) > 0 {
		if err := json.Unmarshal(meeting.ActionItems, &structuredItems); err == nil && len(structuredItems) > 0 {
			hasStructured = true
		}
	}

	if hasStructured {
		sb.WriteString("## 액션 아이템\n\n")
		for _, item := range structuredItems {
			checkbox := "- [ ] "
			text := item.Text
			if item.Done {
				checkbox = "- [x] "
				text = "~~" + text + "~~"
			}
			sb.WriteString(checkbox + text)
			if item.Assignee != "" {
				sb.WriteString(" @" + item.Assignee)
			}
			if item.Priority != "" {
				sb.WriteString(" #" + item.Priority)
			}
			if item.DueDate != "" {
				sb.WriteString(" 📅 " + item.DueDate)
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	} else {
		// Fallback: extract from content text
		actionItems := extractActionItems(meeting.Content)
		if len(actionItems) > 0 {
			sb.WriteString("## 액션 아이템\n\n")
			for _, item := range actionItems {
				sb.WriteString(fmt.Sprintf("- [ ] %s\n", item))
			}
			sb.WriteString("\n")
		}
	}

	// ── Speaker-labeled Transcription (structured segments if available) ──
	var segments []obsidianSegment
	hasSegments := false
	if meeting.Transcription != nil && len(meeting.Transcription) > 2 {
		if err := json.Unmarshal(meeting.Transcription, &segments); err == nil && len(segments) > 0 {
			hasSegments = true
		}
	}

	if hasSegments {
		sb.WriteString("## 전사록\n\n")
		for _, seg := range segments {
			timeRange := fmt.Sprintf("%s ~ %s", formatTimestamp(seg.StartTime), formatTimestamp(seg.EndTime))
			sb.WriteString(fmt.Sprintf("> **%s** (%s)\n", seg.Speaker, timeRange))
			sb.WriteString(fmt.Sprintf("> %s\n\n", seg.Text))
		}
	} else {
		// Fallback: plain transcript
		transcript := meeting.TranscriptA
		if meeting.SelectedTranscript != nil && *meeting.SelectedTranscript == "B" && meeting.TranscriptB != "" {
			transcript = meeting.TranscriptB
		}
		if transcript != "" {
			sb.WriteString("## 전사록\n\n")
			sb.WriteString(transcript)
			sb.WriteString("\n\n")
		}
	}

	// ── Attachments with ProcessedContent ──
	if len(meeting.Attachments) > 0 {
		hasContent := false
		for _, att := range meeting.Attachments {
			if att.ProcessedContent != "" || att.Description != "" {
				hasContent = true
				break
			}
		}
		if hasContent {
			sb.WriteString("## 첨부 자료\n\n")
			for _, att := range meeting.Attachments {
				if att.Description != "" {
					sb.WriteString(fmt.Sprintf("### %s\n\n", att.Description))
				} else if att.Type != "" {
					sb.WriteString(fmt.Sprintf("### %s\n\n", att.Type))
				}
				if att.ProcessedContent != "" {
					sb.WriteString(att.ProcessedContent)
					sb.WriteString("\n\n")
				}
			}
		}
	}

	// ── Related Meetings (wiki-links matching filename format) ──
	relatedMeetings := h.findRelatedMeetings(ctx, userID, meeting)
	if len(relatedMeetings) > 0 {
		sb.WriteString("## 관련 회의\n\n")
		for _, related := range relatedMeetings {
			sb.WriteString(fmt.Sprintf("- [[%s]]\n", related))
		}
		sb.WriteString("\n")
	}

	return sb.String(), filename
}

// findRelatedMeetings finds related meetings by participants, returning Obsidian-compatible filenames
func (h *ExportHandler) findRelatedMeetings(ctx context.Context, userID string, meeting *model.MeetingDetailResponse) []string {
	var related []string

	// Get user's meetings
	result, err := h.repo.ListMeetings(ctx, repository.ListMeetingsParams{
		UserID: userID,
		Tab:    "all",
		Limit:  50,
	})
	if err != nil {
		return related
	}

	// Find meetings with overlapping participants
	participantSet := make(map[string]bool)
	for _, p := range meeting.Participants {
		participantSet[p] = true
	}

	for _, m := range result.Meetings {
		if m.MeetingID == meeting.MeetingID {
			continue
		}

		// Check for overlapping participants
		for _, p := range m.Participants {
			if participantSet[p] {
				// Format as filename to match Obsidian wiki-link resolution
				dateStr := m.Date.Format("2006-01-02")
				related = append(related, fmt.Sprintf("%s - %s", dateStr, sanitizeFilename(m.Title)))
				break
			}
		}

		// Limit to 5 related meetings
		if len(related) >= 5 {
			break
		}
	}

	return related
}

// extractActionItems extracts action items from meeting content
func extractActionItems(content string) []string {
	var items []string
	lines := strings.Split(content, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Look for lines that start with action-like prefixes
		if strings.HasPrefix(line, "- [ ]") || strings.HasPrefix(line, "* [ ]") {
			item := strings.TrimPrefix(line, "- [ ]")
			item = strings.TrimPrefix(item, "* [ ]")
			item = strings.TrimSpace(item)
			if item != "" {
				items = append(items, item)
			}
		} else if strings.HasPrefix(strings.ToLower(line), "action:") ||
			strings.HasPrefix(strings.ToLower(line), "todo:") ||
			strings.HasPrefix(strings.ToLower(line), "task:") {
			item := line[strings.Index(line, ":")+1:]
			item = strings.TrimSpace(item)
			if item != "" {
				items = append(items, item)
			}
		}
	}

	return items
}

// sanitizeFilename removes or replaces invalid characters from filenames
func sanitizeFilename(name string) string {
	// Replace invalid characters
	invalid := []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|"}
	result := name
	for _, char := range invalid {
		result = strings.ReplaceAll(result, char, "_")
	}
	// Trim spaces
	result = strings.TrimSpace(result)
	// Limit length
	if len(result) > 100 {
		result = result[:100]
	}
	return result
}

// escapeYAMLString escapes special characters in YAML strings
func escapeYAMLString(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return s
}
