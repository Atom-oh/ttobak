package handler

import (
	"context"
	"encoding/json"
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
		if err.Error() == "not found" {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
			return
		}
		if err.Error() == "forbidden" {
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
		if err.Error() == "not found" {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
			return
		}
		if err.Error() == "forbidden" {
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

// generateObsidianContent generates Obsidian-compatible markdown with YAML frontmatter
func (h *ExportHandler) generateObsidianContent(ctx context.Context, userID string, meeting *model.MeetingDetailResponse) (string, string) {
	var sb strings.Builder

	// Parse date for frontmatter
	dateStr := meeting.Date
	parsedDate, err := time.Parse(time.RFC3339, meeting.Date)
	if err == nil {
		dateStr = parsedDate.Format("2006-01-02")
	}

	// YAML frontmatter
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("title: \"%s\"\n", escapeYAMLString(meeting.Title)))
	sb.WriteString(fmt.Sprintf("date: %s\n", dateStr))

	if len(meeting.Participants) > 0 {
		sb.WriteString("participants:\n")
		for _, p := range meeting.Participants {
			sb.WriteString(fmt.Sprintf("  - %s\n", p))
		}
	}

	sb.WriteString(fmt.Sprintf("status: %s\n", meeting.Status))
	sb.WriteString("---\n\n")

	// Title
	sb.WriteString(fmt.Sprintf("# %s\n\n", meeting.Title))

	// Summary
	if meeting.Content != "" {
		sb.WriteString("## Summary\n\n")
		sb.WriteString(meeting.Content)
		sb.WriteString("\n\n")
	}

	// Action Items (extracted from content if present)
	actionItems := extractActionItems(meeting.Content)
	if len(actionItems) > 0 {
		sb.WriteString("## Action Items\n\n")
		for _, item := range actionItems {
			sb.WriteString(fmt.Sprintf("- [ ] %s\n", item))
		}
		sb.WriteString("\n")
	}

	// Transcription
	transcript := meeting.TranscriptA
	if meeting.SelectedTranscript != nil && *meeting.SelectedTranscript == "B" && meeting.TranscriptB != "" {
		transcript = meeting.TranscriptB
	}
	if transcript != "" {
		sb.WriteString("## Transcription\n\n")
		sb.WriteString(transcript)
		sb.WriteString("\n\n")
	}

	// Related Meetings (find by participants or date proximity)
	relatedMeetings := h.findRelatedMeetings(ctx, userID, meeting)
	if len(relatedMeetings) > 0 {
		sb.WriteString("## Related Meetings\n\n")
		for _, related := range relatedMeetings {
			sb.WriteString(fmt.Sprintf("- [[%s]]\n", related))
		}
		sb.WriteString("\n")
	}

	// Generate filename
	filename := fmt.Sprintf("%s - %s.md", dateStr, sanitizeFilename(meeting.Title))

	return sb.String(), filename
}

// findRelatedMeetings finds related meetings by participants
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
				related = append(related, m.Title)
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
	// Escape double quotes
	return strings.ReplaceAll(s, "\"", "\\\"")
}
