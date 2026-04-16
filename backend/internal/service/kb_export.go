package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagent"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
)

// KBExportService handles exporting meeting context documents to the KB bucket.
type KBExportService struct {
	s3Client           *s3.Client
	bedrockAgentClient *bedrockagent.Client
	kbBucketName       string
	kbID               string
	dataSourceID       string
}

// NewKBExportService creates a new KB export service.
// Pass empty kbBucketName to disable KB export entirely.
func NewKBExportService(
	s3Client *s3.Client,
	bedrockAgentClient *bedrockagent.Client,
	kbBucketName string,
	kbID string,
	dataSourceID string,
) *KBExportService {
	return &KBExportService{
		s3Client:           s3Client,
		bedrockAgentClient: bedrockAgentClient,
		kbBucketName:       kbBucketName,
		kbID:               kbID,
		dataSourceID:       dataSourceID,
	}
}

// TranscriptSegment mirrors the summarize Lambda's segment output format.
type TranscriptSegment struct {
	Speaker   string  `json:"speaker"`
	Text      string  `json:"text"`
	StartTime float64 `json:"startTime"`
	EndTime   float64 `json:"endTime"`
}

// GenerateMeetingDocument creates a structured markdown document from meeting data.
func GenerateMeetingDocument(meeting *model.Meeting, attachments []model.Attachment) string {
	var b strings.Builder

	// Title and metadata
	b.WriteString(fmt.Sprintf("# %s\n\n", meeting.Title))
	b.WriteString(fmt.Sprintf("- **Date**: %s\n", meeting.Date.Format("2006-01-02 15:04")))
	b.WriteString(fmt.Sprintf("- **Meeting ID**: %s\n", meeting.MeetingID))
	if len(meeting.Participants) > 0 {
		b.WriteString(fmt.Sprintf("- **Participants**: %s\n", strings.Join(meeting.Participants, ", ")))
	}
	if meeting.SttProvider != "" {
		b.WriteString(fmt.Sprintf("- **STT Provider**: %s\n", meeting.SttProvider))
	}
	b.WriteString("\n")

	// User notes
	if meeting.Notes != "" {
		b.WriteString("## Meeting Notes\n\n")
		b.WriteString(meeting.Notes)
		b.WriteString("\n\n")
	}

	// AI Summary
	if meeting.Content != "" {
		b.WriteString("## Summary\n\n")
		b.WriteString(meeting.Content)
		b.WriteString("\n\n")
	}

	// Action items
	if meeting.ActionItems != "" {
		var items []struct {
			Text      string `json:"text"`
			Completed bool   `json:"completed"`
			Assignee  string `json:"assignee,omitempty"`
			DueDate   string `json:"dueDate,omitempty"`
		}
		if err := json.Unmarshal([]byte(meeting.ActionItems), &items); err == nil && len(items) > 0 {
			b.WriteString("## Action Items\n\n")
			for _, item := range items {
				check := "[ ]"
				if item.Completed {
					check = "[x]"
				}
				line := fmt.Sprintf("- %s %s", check, item.Text)
				if item.Assignee != "" {
					line += fmt.Sprintf(" (@%s)", item.Assignee)
				}
				if item.DueDate != "" {
					line += fmt.Sprintf(" — due %s", item.DueDate)
				}
				b.WriteString(line + "\n")
			}
			b.WriteString("\n")
		}
	}

	// Transcript (speaker-labeled segments preferred, fallback to raw text)
	if meeting.TranscriptSegments != "" {
		var segments []TranscriptSegment
		if err := json.Unmarshal([]byte(meeting.TranscriptSegments), &segments); err == nil && len(segments) > 0 {
			b.WriteString("## Transcript\n\n")
			for _, seg := range segments {
				minutes := int(seg.StartTime) / 60
				seconds := int(seg.StartTime) % 60
				b.WriteString(fmt.Sprintf("**%s** [%02d:%02d]: %s\n\n", seg.Speaker, minutes, seconds, seg.Text))
			}
		}
	} else if meeting.TranscriptA != "" {
		b.WriteString("## Transcript\n\n")
		b.WriteString(meeting.TranscriptA)
		b.WriteString("\n\n")
	}

	// Attachments metadata
	if len(attachments) > 0 {
		b.WriteString("## Attachments\n\n")
		for _, att := range attachments {
			name := att.FileName
			if name == "" {
				name = att.AttachmentID
			}
			b.WriteString(fmt.Sprintf("- **%s** (type: %s, status: %s)\n", name, att.Type, att.Status))
			if att.Description != "" {
				b.WriteString(fmt.Sprintf("  - Description: %s\n", att.Description))
			}
		}
		b.WriteString("\n")
	}

	return b.String()
}

// ExportToKB writes a meeting context document to the KB S3 bucket.
// Returns nil if KB is not configured (kbBucketName is empty).
func (s *KBExportService) ExportToKB(ctx context.Context, userID, meetingID, document string) error {
	if s.kbBucketName == "" {
		log.Printf("KB export skipped: KB_BUCKET_NAME not configured")
		return nil
	}

	key := fmt.Sprintf("meetings/%s/%s.md", userID, meetingID)

	_, err := s.s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.kbBucketName),
		Key:         aws.String(key),
		Body:        bytes.NewReader([]byte(document)),
		ContentType: aws.String("text/markdown"),
	})
	if err != nil {
		return fmt.Errorf("failed to export meeting document to KB: %w", err)
	}

	log.Printf("Exported meeting %s context document to KB: s3://%s/%s (%d bytes)", meetingID, s.kbBucketName, key, len(document))
	return nil
}

// TriggerIngestion starts a Bedrock KB ingestion job.
// Gracefully skips if KB is not configured.
func (s *KBExportService) TriggerIngestion(ctx context.Context) error {
	if s.bedrockAgentClient == nil || s.kbID == "" || s.kbID == "PENDING" || s.dataSourceID == "" || s.dataSourceID == "PENDING" {
		log.Printf("KB ingestion skipped: KB not configured (kbID=%s, dataSourceID=%s)", s.kbID, s.dataSourceID)
		return nil
	}

	result, err := s.bedrockAgentClient.StartIngestionJob(ctx, &bedrockagent.StartIngestionJobInput{
		KnowledgeBaseId: aws.String(s.kbID),
		DataSourceId:    aws.String(s.dataSourceID),
		Description:     aws.String(fmt.Sprintf("Auto-ingestion after meeting summary at %s", time.Now().Format(time.RFC3339))),
	})
	if err != nil {
		return fmt.Errorf("failed to start KB ingestion job: %w", err)
	}

	jobID := ""
	if result.IngestionJob != nil && result.IngestionJob.IngestionJobId != nil {
		jobID = *result.IngestionJob.IngestionJobId
	}
	log.Printf("KB ingestion job started: %s", jobID)
	return nil
}
