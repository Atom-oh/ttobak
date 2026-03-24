package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

var (
	bedrockService *service.BedrockService
	repo           *repository.DynamoDBRepository
	s3Client       *s3.Client
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

	// URL decode the key
	key = strings.ReplaceAll(key, "+", " ")

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
		return nil
	}

	// Update meeting with transcript and speaker segments
	err = updateMeetingTranscript(ctx, meetingID, transcript, segments, isNova)
	if err != nil {
		log.Printf("Failed to update meeting with transcript: %v", err)
		return nil
	}

	log.Printf("Updated meeting %s with transcript (nova=%v)", meetingID, isNova)

	// Check if we should generate summary
	meeting, err := repo.GetMeetingByID(ctx, meetingID)
	if err != nil {
		log.Printf("Failed to get meeting: %v", err)
		return nil
	}

	// Generate summary if at least one transcript is available
	if meeting != nil && (meeting.TranscriptA != "" || meeting.TranscriptB != "") {
		if meeting.Status != model.StatusError {
			meeting.Status = model.StatusSummarizing
			repo.UpdateMeeting(ctx, meeting)

			content, err := bedrockService.SummarizeTranscript(ctx, meetingID)
			if err != nil {
				log.Printf("Failed to generate summary: %v", err)
				meeting.Status = model.StatusError
				repo.UpdateMeeting(ctx, meeting)
				return nil
			}

			log.Printf("Generated content for meeting %s: %d characters", meetingID, len(content))

			// Extract action items using Haiku (fast, cheap)
			actionItems, err := bedrockService.ExtractActionItems(ctx, meetingID)
			if err != nil {
				log.Printf("Failed to extract action items (non-fatal): %v", err)
			} else {
				// Re-fetch meeting to get updated state after summary
				meeting, err = repo.GetMeetingByID(ctx, meetingID)
				if err == nil && meeting != nil {
					meeting.ActionItems = actionItems
					if err := repo.UpdateMeeting(ctx, meeting); err != nil {
						log.Printf("Failed to save action items: %v", err)
					} else {
						log.Printf("Extracted action items for meeting %s: %s", meetingID, actionItems)
					}
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

func extractMeetingIDFromTranscriptKey(key string) string {
	// Expected format: transcripts/{meetingID}.json or transcripts/{meetingID}-nova.json
	key = strings.TrimPrefix(key, "transcripts/")
	key = strings.TrimSuffix(key, ".json")
	key = strings.TrimSuffix(key, "-nova")
	return key
}

func updateMeetingTranscript(ctx context.Context, meetingID, transcript string, segments []TranscriptSegmentOut, isNova bool) error {
	meeting, err := repo.GetMeetingByID(ctx, meetingID)
	if err != nil {
		return err
	}
	if meeting == nil {
		return fmt.Errorf("meeting not found")
	}

	if isNova {
		meeting.TranscriptB = transcript
	} else {
		meeting.TranscriptA = transcript
	}

	// Save speaker segments as JSON string
	if len(segments) > 0 {
		segJSON, err := json.Marshal(segments)
		if err == nil {
			meeting.TranscriptSegments = string(segJSON)
			log.Printf("Saved %d speaker segments for meeting %s", len(segments), meetingID)
		}
	}

	return repo.UpdateMeeting(ctx, meeting)
}

func main() {
	lambda.Start(Handler)
}
