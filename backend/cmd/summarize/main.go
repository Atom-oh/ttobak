package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagent"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

var (
	bedrockService  *service.BedrockService
	kbExportService *service.KBExportService
	repo            *repository.DynamoDBRepository
	s3Client        *s3.Client
)

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("failed to load AWS config: %v", err)
	}

	// Bedrock may require a specific region
	bedrockRegion := os.Getenv("BEDROCK_REGION")
	if bedrockRegion == "" {
		bedrockRegion = "us-west-2"
	}

	bedrockCfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion(bedrockRegion))
	if err != nil {
		log.Fatalf("failed to load Bedrock config: %v", err)
	}

	dynamoClient := dynamodb.NewFromConfig(cfg)
	s3Client = s3.NewFromConfig(cfg)
	bedrockClient := bedrockruntime.NewFromConfig(bedrockCfg)

	tableName := os.Getenv("TABLE_NAME")
	if tableName == "" {
		tableName = "ttobak-main"
	}
	bucketName := os.Getenv("BUCKET_NAME")
	if bucketName == "" {
		bucketName = "ttobak-assets"
	}

	repo = repository.NewDynamoDBRepositoryWithS3(dynamoClient, tableName, s3Client, bucketName)
	bedrockService = service.NewBedrockService(bedrockClient, s3Client, repo)

	// KB export service — gracefully skips if not configured
	kbBucketName := os.Getenv("KB_BUCKET_NAME")
	kbID := os.Getenv("KB_ID")
	dataSourceID := os.Getenv("DATA_SOURCE_ID")
	var bedrockAgentClient *bedrockagent.Client
	if kbBucketName != "" {
		bedrockAgentClient = bedrockagent.NewFromConfig(cfg)
	}
	kbExportService = service.NewKBExportService(s3Client, bedrockAgentClient, kbBucketName, kbID, dataSourceID)
}

// TranscribeResult represents the AWS Transcribe output JSON structure
type TranscribeResult struct {
	Results struct {
		Transcripts []struct {
			Transcript string `json:"transcript"`
		} `json:"transcripts"`
		SpeakerLabels *SpeakerLabels `json:"speaker_labels,omitempty"`
		Items         []TranscribeItem `json:"items,omitempty"`
	} `json:"results"`
	Status string `json:"status"`
}

// SpeakerLabels represents the speaker diarization results
type SpeakerLabels struct {
	Speakers int              `json:"speakers"`
	Segments []SpeakerSegment `json:"segments"`
}

// SpeakerSegment represents a contiguous speech segment by one speaker
type SpeakerSegment struct {
	StartTime    string        `json:"start_time"`
	EndTime      string        `json:"end_time"`
	SpeakerLabel string        `json:"speaker_label"`
	Items        []SpeakerItem `json:"items"`
}

// SpeakerItem represents a word within a speaker segment
type SpeakerItem struct {
	StartTime    string `json:"start_time"`
	EndTime      string `json:"end_time"`
	SpeakerLabel string `json:"speaker_label"`
}

// TranscribeItem represents a word/punctuation in the transcribe output
type TranscribeItem struct {
	StartTime    string `json:"start_time,omitempty"`
	EndTime      string `json:"end_time,omitempty"`
	Type         string `json:"type"` // "pronunciation" or "punctuation"
	Alternatives []struct {
		Confidence string `json:"confidence"`
		Content    string `json:"content"`
	} `json:"alternatives"`
}

// TranscriptSegmentOut represents a speaker-labeled transcript segment for the API response
type TranscriptSegmentOut struct {
	Speaker   string  `json:"speaker"`
	Text      string  `json:"text"`
	StartTime float64 `json:"startTime"`
	EndTime   float64 `json:"endTime"`
}

// Handler processes EventBridge S3 events for completed transcriptions
func Handler(ctx context.Context, raw json.RawMessage) error {
	var event model.EventBridgeS3Event
	if err := json.Unmarshal(raw, &event); err != nil {
		return fmt.Errorf("failed to unmarshal EventBridge event: %w", err)
	}

	bucket := event.Detail.Bucket.Name
	key := event.Detail.Object.Key

	// URL decode the key (handles both + and %XX encoding)
	if decoded, err := url.QueryUnescape(key); err == nil {
		key = decoded
	}

	log.Printf("Processing transcript: bucket=%s, key=%s", bucket, key)

	// Only process transcript files
	if !strings.HasPrefix(key, "transcripts/") {
		log.Printf("Skipping non-transcript file: %s", key)
		return nil
	}

	// Extract meeting ID from key
	// Expected format: transcripts/{meetingID}.json or transcripts/{meetingID}-nova.json
	meetingID := extractMeetingIDFromTranscriptKey(key)
	if meetingID == "" {
		log.Printf("Could not extract meeting ID from key: %s", key)
		return nil
	}

	isNova := strings.Contains(key, "-nova.json")

	// Download and parse transcript
	transcript, segments, err := downloadAndParseTranscript(ctx, bucket, key)
	if err != nil {
		log.Printf("Failed to parse transcript: %v", err)
		setMeetingError(ctx, meetingID)
		return nil
	}

	// Update meeting with transcript and speaker segments (atomic partial update)
	err = updateMeetingTranscript(ctx, meetingID, transcript, segments, isNova)
	if err != nil {
		log.Printf("Failed to update meeting with transcript: %v", err)
		setMeetingError(ctx, meetingID)
		return nil
	}

	log.Printf("Updated meeting %s with transcript (nova=%v)", meetingID, isNova)

	// We just saved the transcript, so proceed directly to summary generation.
	// Avoid re-reading via GSI (eventual consistency can return stale data).
	meeting, err := repo.GetMeetingByID(ctx, meetingID)
	if err != nil || meeting == nil {
		log.Printf("Failed to get meeting via GSI, retrying after 1s: %v", err)
		time.Sleep(1 * time.Second)
		meeting, err = repo.GetMeetingByID(ctx, meetingID)
		if err != nil || meeting == nil {
			log.Printf("Still failed to get meeting: %v", err)
			return nil
		}
	}

	// Generate summary — transcript was just saved so it's guaranteed to exist
	if transcript != "" {
		if meeting.Status != model.StatusError {
			repo.UpdateMeetingFields(ctx, meeting.UserID, meetingID, map[string]interface{}{
				"status": model.StatusSummarizing,
			})

			content, err := bedrockService.SummarizeTranscript(ctx, meetingID)
			if err != nil {
				log.Printf("Failed to generate summary: %v", err)
				repo.UpdateMeetingFields(ctx, meeting.UserID, meetingID, map[string]interface{}{
					"status": model.StatusError,
				})
				return nil
			}

			log.Printf("Generated content for meeting %s: %d characters", meetingID, len(content))

			// Extract action items using Haiku (fast, cheap)
			actionItems, err := bedrockService.ExtractActionItems(ctx, meetingID)
			if err != nil {
				log.Printf("Failed to extract action items (non-fatal): %v", err)
			} else {
				if err := repo.UpdateMeetingFields(ctx, meeting.UserID, meetingID, map[string]interface{}{
					"actionItems": actionItems,
				}); err != nil {
					log.Printf("Failed to save action items: %v", err)
				} else {
					log.Printf("Extracted action items for meeting %s: %s", meetingID, actionItems)
				}
			}

			// Extract tags using Haiku (fast, cheap)
			tags, err := bedrockService.ExtractTags(ctx, meetingID)
			if err != nil {
				log.Printf("Failed to extract tags (non-fatal): %v", err)
			} else if len(tags) > 0 {
				if err := repo.UpdateMeetingFields(ctx, meeting.UserID, meetingID, map[string]interface{}{
					"tags": tags,
				}); err != nil {
					log.Printf("Failed to save tags: %v", err)
				} else {
					log.Printf("Extracted tags for meeting %s: %v", meetingID, tags)
				}
			}

			// KB Export: generate meeting context document and upload to KB bucket
			// Re-fetch meeting to get the latest state (summary, action items, tags now saved)
			updatedMeeting, err := repo.GetMeetingByID(ctx, meetingID)
			if err != nil {
				log.Printf("Failed to re-fetch meeting for KB export (non-fatal): %v", err)
			} else if updatedMeeting != nil {
				attachments, _ := repo.ListAttachments(ctx, meetingID)
				doc := service.GenerateMeetingDocument(updatedMeeting, attachments)
				if err := kbExportService.ExportToKB(ctx, updatedMeeting.UserID, meetingID, doc); err != nil {
					log.Printf("Failed to export to KB (non-fatal): %v", err)
				}
				// P2: Auto-trigger KB ingestion
				if err := kbExportService.TriggerIngestion(ctx); err != nil {
					log.Printf("Failed to trigger KB ingestion (non-fatal): %v", err)
				}
			}
		}
	}

	return nil
}

func downloadAndParseTranscript(ctx context.Context, bucket, key string) (string, []TranscriptSegmentOut, error) {
	result, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return "", nil, fmt.Errorf("failed to download transcript: %w", err)
	}
	defer result.Body.Close()

	data, err := io.ReadAll(result.Body)
	if err != nil {
		return "", nil, fmt.Errorf("failed to read transcript: %w", err)
	}

	var transcribeResult TranscribeResult
	if err := json.Unmarshal(data, &transcribeResult); err != nil {
		return "", nil, fmt.Errorf("failed to parse transcript JSON: %w", err)
	}

	if len(transcribeResult.Results.Transcripts) == 0 {
		return "", nil, fmt.Errorf("no transcript found in result")
	}

	transcript := transcribeResult.Results.Transcripts[0].Transcript
	segments := extractSpeakerSegments(&transcribeResult)

	return transcript, segments, nil
}

// extractSpeakerSegments builds speaker-labeled segments from Transcribe output
func extractSpeakerSegments(result *TranscribeResult) []TranscriptSegmentOut {
	if result.Results.SpeakerLabels == nil || len(result.Results.SpeakerLabels.Segments) == 0 {
		return nil
	}

	// Build a map from item start_time -> content for pronunciation items
	itemContent := make(map[string]string)
	for _, item := range result.Results.Items {
		if len(item.Alternatives) > 0 {
			if item.Type == "pronunciation" && item.StartTime != "" {
				itemContent[item.StartTime] = item.Alternatives[0].Content
			}
		}
	}

	// Walk speaker segments and accumulate text
	var segments []TranscriptSegmentOut
	for _, seg := range result.Results.SpeakerLabels.Segments {
		var words []string
		for _, item := range seg.Items {
			if content, ok := itemContent[item.StartTime]; ok {
				words = append(words, content)
			}
		}
		if len(words) == 0 {
			continue
		}

		startTime, _ := strconv.ParseFloat(seg.StartTime, 64)
		endTime, _ := strconv.ParseFloat(seg.EndTime, 64)

		segments = append(segments, TranscriptSegmentOut{
			Speaker:   seg.SpeakerLabel,
			Text:      strings.Join(words, " "),
			StartTime: startTime,
			EndTime:   endTime,
		})
	}

	return segments
}

// setMeetingError sets a meeting's status to error via atomic update.
// Logs and swallows errors since this is a best-effort error path.
func setMeetingError(ctx context.Context, meetingID string) {
	meeting, err := repo.GetMeetingByID(ctx, meetingID)
	if err != nil || meeting == nil {
		return
	}
	repo.UpdateMeetingFields(ctx, meeting.UserID, meetingID, map[string]interface{}{
		"status": model.StatusError,
	})
}

func extractMeetingIDFromTranscriptKey(key string) string {
	// Expected format: transcripts/{meetingID}.json or transcripts/{meetingID}-nova.json
	key = strings.TrimPrefix(key, "transcripts/")
	key = strings.TrimSuffix(key, ".json")
	key = strings.TrimSuffix(key, "-nova")
	return key
}

func updateMeetingTranscript(ctx context.Context, meetingID, transcript string, segments []TranscriptSegmentOut, isNova bool) error {
	// Get meeting to obtain userID for the primary key
	meeting, err := repo.GetMeetingByID(ctx, meetingID)
	if err != nil {
		return err
	}
	if meeting == nil {
		return fmt.Errorf("meeting not found")
	}

	// Build partial update — only touch transcript fields
	fields := map[string]interface{}{}
	if isNova {
		fields["transcriptB"] = transcript
	} else {
		fields["transcriptA"] = transcript
	}

	// Save speaker segments as JSON string
	if len(segments) > 0 {
		segJSON, err := json.Marshal(segments)
		if err == nil {
			fields["transcriptSegments"] = string(segJSON)
			log.Printf("Saved %d speaker segments for meeting %s", len(segments), meetingID)
		}
	}

	return repo.UpdateMeetingFields(ctx, meeting.UserID, meetingID, fields)
}

func main() {
	lambda.Start(Handler)
}
