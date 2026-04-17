package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// Model IDs for different use cases (cost optimization)
var (
	// ClaudeOpusModelID is for complex tasks (Q&A with tools, image analysis)
	ClaudeOpusModelID = getEnvOrDefault("BEDROCK_MODEL_ID", "global.anthropic.claude-opus-4-6-v1")
	// ClaudeSonnetModelID is for summarization (final meeting summary)
	ClaudeSonnetModelID = getEnvOrDefault("BEDROCK_SUMMARIZE_MODEL_ID", "global.anthropic.claude-sonnet-4-6")
	// ClaudeHaikuModelID is for live summary (fast, low-cost incremental updates)
	ClaudeHaikuModelID = "global.anthropic.claude-haiku-4-5-20251001-v1:0"
)

// stripCodeFences removes markdown code fences (```json ... ```) that LLMs sometimes wrap around JSON output.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		lines := strings.Split(s, "\n")
		if len(lines) >= 3 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
			s = strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
		}
	}
	return s
}

func getEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// BedrockService handles AI operations using Amazon Bedrock
type BedrockService struct {
	bedrockClient *bedrockruntime.Client
	s3Client      *s3.Client
	repo          *repository.DynamoDBRepository
}

// NewBedrockService creates a new Bedrock service
func NewBedrockService(
	bedrockClient *bedrockruntime.Client,
	s3Client *s3.Client,
	repo *repository.DynamoDBRepository,
) *BedrockService {
	return &BedrockService{
		bedrockClient: bedrockClient,
		s3Client:      s3Client,
		repo:          repo,
	}
}

// ClaudeRequest represents a request to Claude via Bedrock
type ClaudeRequest struct {
	AnthropicVersion string          `json:"anthropic_version"`
	MaxTokens        int             `json:"max_tokens"`
	Messages         []ClaudeMessage `json:"messages"`
	System           string          `json:"system,omitempty"`
}

// ClaudeMessage represents a message in a Claude conversation
type ClaudeMessage struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// ContentBlock represents a content block in a Claude message
type ContentBlock struct {
	Type   string       `json:"type"`
	Text   string       `json:"text,omitempty"`
	Source *ImageSource `json:"source,omitempty"`
}

// ImageSource represents an image source for Claude Vision
type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// ClaudeResponse represents a response from Claude via Bedrock
type ClaudeResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// speakerSegment represents a speaker-labeled transcript segment for summary generation
type speakerSegment struct {
	Speaker   string  `json:"speaker"`
	Text      string  `json:"text"`
	StartTime float64 `json:"startTime"`
	EndTime   float64 `json:"endTime"`
}

// SummarizeTranscript generates meeting notes (content) from the transcript using Claude
func (s *BedrockService) SummarizeTranscript(ctx context.Context, meetingID string) (string, error) {
	meeting, err := s.repo.GetMeetingByID(ctx, meetingID)
	if err != nil {
		return "", fmt.Errorf("failed to get meeting: %w", err)
	}
	if meeting == nil {
		return "", fmt.Errorf("meeting not found: %s", meetingID)
	}

	// Use the selected transcript, or default to A, or B if A not available
	transcript := meeting.TranscriptA
	if meeting.SelectedTranscript == "B" && meeting.TranscriptB != "" {
		transcript = meeting.TranscriptB
	} else if transcript == "" && meeting.TranscriptB != "" {
		transcript = meeting.TranscriptB
	}
	if transcript == "" {
		return "", fmt.Errorf("no transcript available for meeting: %s", meetingID)
	}

	systemPrompt := `You are an expert meeting assistant. Create comprehensive, well-structured meeting notes in Markdown.

Your output MUST follow this exact structure:

# 회의록

## 참석자
- 화자별 식별 및 주요 역할 추정

## 개요
- 회의 핵심 요약 (3-5문장)

## 화자별 주요 발언
### [Speaker Label]
- 주요 발언 요약 (2-3개)

## 주요 논의 사항
- 논의된 핵심 토픽 (상세하게)

## 결정 사항
- 합의된 결정들

## 액션 아이템
- [ ] 담당자(Speaker Label): 할 일 내용

Format in Korean unless the transcript is entirely in English.
Use bullet points and checkboxes. Include timestamps where available.`

	// Build speaker-labeled prompt if segments exist
	userPrompt := fmt.Sprintf("다음 회의 녹취록을 바탕으로 회의록을 작성해주세요:\n\n%s", transcript)

	if meeting.TranscriptSegments != "" {
		var segments []speakerSegment
		if err := json.Unmarshal([]byte(meeting.TranscriptSegments), &segments); err == nil && len(segments) > 0 {
			var sb strings.Builder
			sb.WriteString("다음은 화자별로 분리된 회의 녹취록입니다:\n\n")
			for _, seg := range segments {
				sb.WriteString(fmt.Sprintf("[%s %.0f초~%.0f초] %s\n", seg.Speaker, seg.StartTime, seg.EndTime, seg.Text))
			}
			userPrompt = sb.String() + "\n\n위 녹취록을 바탕으로 회의록을 작성해주세요."
		}
	}

	request := ClaudeRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        4096,
		System:           systemPrompt,
		Messages: []ClaudeMessage{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "text", Text: userPrompt},
				},
			},
		},
	}

	// Use Sonnet for summarization (cost optimization)
	content, err := s.invokeClaudeModelWithID(ctx, request, ClaudeSonnetModelID)
	if err != nil {
		return "", fmt.Errorf("failed to generate content: %w", err)
	}

	// Atomic partial update — only set content and status to avoid clobbering
	// concurrent writes to other fields (e.g., audioKey from CompleteUpload)
	if err := s.repo.UpdateMeetingFields(ctx, meeting.UserID, meetingID, map[string]interface{}{
		"content": content,
		"status":  model.StatusDone,
	}); err != nil {
		return "", fmt.Errorf("failed to update meeting: %w", err)
	}

	return content, nil
}

// AnalyzeImage analyzes an image using Claude Vision
func (s *BedrockService) AnalyzeImage(ctx context.Context, bucket, key string) (string, string, error) {
	// Download image from S3
	result, err := s.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to get image from S3: %w", err)
	}
	defer result.Body.Close()

	imageData, err := io.ReadAll(result.Body)
	if err != nil {
		return "", "", fmt.Errorf("failed to read image data: %w", err)
	}

	// Determine media type
	mediaType := s.getImageMediaType(key)
	if mediaType == "" {
		return "", "", fmt.Errorf("unsupported image format")
	}

	// Encode image to base64
	imageBase64 := base64.StdEncoding.EncodeToString(imageData)

	// First, classify the image
	classification, err := s.classifyImage(ctx, imageBase64, mediaType)
	if err != nil {
		return "", "", fmt.Errorf("failed to classify image: %w", err)
	}

	// Then, analyze based on classification
	analysis, err := s.analyzeByClassification(ctx, imageBase64, mediaType, classification)
	if err != nil {
		return "", "", fmt.Errorf("failed to analyze image: %w", err)
	}

	return classification, analysis, nil
}

// classifyImage determines the type of image
func (s *BedrockService) classifyImage(ctx context.Context, imageBase64, mediaType string) (string, error) {
	systemPrompt := `You are an image classifier. Classify the image into exactly one of these categories:
- diagram: System architecture diagrams, flowcharts, UML diagrams, technical diagrams
- whiteboard: Handwritten notes, whiteboard photos, sketches, mind maps
- screenshot: Screenshots of applications, web pages, code
- photo: General photos, people, objects

Respond with ONLY the category name, nothing else.`

	request := ClaudeRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        50,
		System:           systemPrompt,
		Messages: []ClaudeMessage{
			{
				Role: "user",
				Content: []ContentBlock{
					{
						Type: "image",
						Source: &ImageSource{
							Type:      "base64",
							MediaType: mediaType,
							Data:      imageBase64,
						},
					},
					{Type: "text", Text: "Classify this image."},
				},
			},
		},
	}

	response, err := s.invokeClaudeModel(ctx, request)
	if err != nil {
		return "", err
	}

	// Normalize the response
	classification := strings.ToLower(strings.TrimSpace(response))
	validCategories := []string{model.AttachTypeDiagram, model.AttachTypeWhiteboard, model.AttachTypeScreenshot, model.AttachTypePhoto}
	for _, cat := range validCategories {
		if strings.Contains(classification, cat) {
			return cat, nil
		}
	}

	return model.AttachTypePhoto, nil
}

// analyzeByClassification analyzes the image based on its classification
func (s *BedrockService) analyzeByClassification(ctx context.Context, imageBase64, mediaType, classification string) (string, error) {
	var systemPrompt string

	switch classification {
	case model.AttachTypeDiagram:
		systemPrompt = `You are an expert at reading architecture diagrams.
Convert this architecture diagram into a Mermaid diagram format.
Use appropriate Mermaid diagram types (flowchart, sequence, class, etc.) based on the content.
Output ONLY the Mermaid code, wrapped in markdown code blocks with 'mermaid' language identifier.
If you cannot accurately represent it in Mermaid, describe the architecture in structured text instead.`

	case model.AttachTypeWhiteboard:
		systemPrompt = `You are an expert at reading handwritten content and whiteboard notes.
Extract and transcribe all text, diagrams, and ideas from this whiteboard/handwritten image.
Organize the content logically with:
- Main ideas/headings
- Supporting points
- Any diagrams or relationships (describe them)
- Action items if visible

Format the output in clean Markdown. Preserve the original language of the text.`

	case model.AttachTypeScreenshot:
		systemPrompt = `You are an expert at analyzing screenshots.
Extract all visible text and describe the UI elements, data, or code shown.
If it's code, format it in appropriate code blocks.
If it's a UI, describe the layout and key information.
Format the output in clean Markdown.`

	default: // photo
		systemPrompt = `Analyze this image and provide a detailed description of its contents.
If there is text, extract it.
If there are diagrams or charts, describe them.
Format the output in clean Markdown.`
	}

	request := ClaudeRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        4096,
		System:           systemPrompt,
		Messages: []ClaudeMessage{
			{
				Role: "user",
				Content: []ContentBlock{
					{
						Type: "image",
						Source: &ImageSource{
							Type:      "base64",
							MediaType: mediaType,
							Data:      imageBase64,
						},
					},
					{Type: "text", Text: "Analyze this image according to your instructions."},
				},
			},
		},
	}

	return s.invokeClaudeModel(ctx, request)
}

// ProcessImageAttachment processes an uploaded image for a meeting
func (s *BedrockService) ProcessImageAttachment(ctx context.Context, meetingID, attachmentID, bucket, key string) error {
	classification, analysis, err := s.AnalyzeImage(ctx, bucket, key)
	if err != nil {
		return fmt.Errorf("failed to analyze image: %w", err)
	}

	// Update attachment with analysis
	attachment, err := s.repo.GetAttachment(ctx, meetingID, attachmentID)
	if err != nil {
		return fmt.Errorf("failed to get attachment: %w", err)
	}
	if attachment == nil {
		return fmt.Errorf("attachment not found")
	}

	attachment.Type = classification
	attachment.ProcessedContent = analysis
	attachment.Status = model.AttachStatusDone
	return s.repo.UpdateAttachment(ctx, attachment)
}

// invokeClaudeModel sends a request to Claude via Bedrock and returns the response
func (s *BedrockService) invokeClaudeModel(ctx context.Context, request ClaudeRequest) (string, error) {
	return s.invokeClaudeModelWithID(ctx, request, ClaudeOpusModelID)
}

// invokeClaudeModelWithID sends a request to Claude via Bedrock using a specific model ID
func (s *BedrockService) invokeClaudeModelWithID(ctx context.Context, request ClaudeRequest, modelID string) (string, error) {
	requestBody, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	output, err := s.bedrockClient.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(modelID),
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
		Body:        requestBody,
	})
	if err != nil {
		return "", fmt.Errorf("failed to invoke model: %w", err)
	}

	var response ClaudeResponse
	if err := json.Unmarshal(output.Body, &response); err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if len(response.Content) == 0 {
		return "", fmt.Errorf("empty response from model")
	}

	// Concatenate all text content
	var result strings.Builder
	for _, block := range response.Content {
		if block.Type == "text" {
			result.WriteString(block.Text)
		}
	}

	return result.String(), nil
}

// ActionItem represents an extracted action item from a meeting transcript
type ActionItem struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Completed bool   `json:"completed"`
	Assignee  string `json:"assignee,omitempty"`
	DueDate   string `json:"dueDate,omitempty"`
	Priority  string `json:"priority,omitempty"`
}

// ExtractActionItems extracts action items from a meeting transcript using Claude Haiku
func (s *BedrockService) ExtractActionItems(ctx context.Context, meetingID string) (string, error) {
	meeting, err := s.repo.GetMeetingByID(ctx, meetingID)
	if err != nil {
		return "", fmt.Errorf("failed to get meeting: %w", err)
	}
	if meeting == nil {
		return "", fmt.Errorf("meeting not found: %s", meetingID)
	}

	// Use the selected transcript, or default to A, or B if A not available
	transcript := meeting.TranscriptA
	if meeting.SelectedTranscript == "B" && meeting.TranscriptB != "" {
		transcript = meeting.TranscriptB
	} else if transcript == "" && meeting.TranscriptB != "" {
		transcript = meeting.TranscriptB
	}
	if transcript == "" {
		return "[]", nil // No transcript, return empty array
	}

	systemPrompt := `회의 트랜스크립트에서 액션 아이템(해야 할 일)을 추출하세요.
각 액션 아이템에 대해 아래를 식별하세요:
- text: 할 일 설명 (한국어로 작성, 필수)
- assignee: 담당자 (화자 라벨 예: "spk_0", "spk_1")
- priority: high, medium, low (중요도/긴급도 기준)
- dueDate: 명시적으로 언급된 경우만 (ISO 형식 YYYY-MM-DD)

유효한 JSON 배열만 반환하세요. 액션 아이템이 없으면 []를 반환하세요.
예시:
[{"text":"보고서 작성 완료","assignee":"spk_0","priority":"high","completed":false}]`

	// Build prompt with speaker segments if available
	userPrompt := fmt.Sprintf("Extract action items from this meeting transcript:\n\n%s", transcript)

	if meeting.TranscriptSegments != "" {
		var segments []speakerSegment
		if err := json.Unmarshal([]byte(meeting.TranscriptSegments), &segments); err == nil && len(segments) > 0 {
			var sb strings.Builder
			sb.WriteString("Extract action items from this speaker-labeled meeting transcript:\n\n")
			for _, seg := range segments {
				sb.WriteString(fmt.Sprintf("[%s] %s\n", seg.Speaker, seg.Text))
			}
			userPrompt = sb.String()
		}
	}

	request := ClaudeRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        1024,
		System:           systemPrompt,
		Messages: []ClaudeMessage{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "text", Text: userPrompt},
				},
			},
		},
	}

	// Use Haiku for action item extraction (fast, cheap)
	response, err := s.invokeClaudeModelWithID(ctx, request, ClaudeHaikuModelID)
	if err != nil {
		return "", fmt.Errorf("failed to extract action items: %w", err)
	}

	// Validate JSON response (strip code fences LLMs sometimes add)
	response = stripCodeFences(response)
	var items []ActionItem
	if err := json.Unmarshal([]byte(response), &items); err != nil {
		// If parsing fails, return empty array
		return "[]", nil
	}

	// Assign stable IDs to each item
	for i := range items {
		items[i].ID = fmt.Sprintf("ai_%d", i+1)
	}

	// Re-serialize to ensure consistent format
	result, err := json.Marshal(items)
	if err != nil {
		return "[]", nil
	}

	return string(result), nil
}

// ExtractTags extracts topic tags from a meeting transcript using Claude Haiku
func (s *BedrockService) ExtractTags(ctx context.Context, meetingID string) ([]string, error) {
	meeting, err := s.repo.GetMeetingByID(ctx, meetingID)
	if err != nil {
		return nil, fmt.Errorf("failed to get meeting: %w", err)
	}
	if meeting == nil {
		return nil, fmt.Errorf("meeting not found: %s", meetingID)
	}

	// Use the selected transcript, or default to A, or B if A not available
	transcript := meeting.TranscriptA
	if meeting.SelectedTranscript == "B" && meeting.TranscriptB != "" {
		transcript = meeting.TranscriptB
	} else if transcript == "" && meeting.TranscriptB != "" {
		transcript = meeting.TranscriptB
	}
	if transcript == "" {
		return []string{}, nil
	}

	systemPrompt := `You are an expert at categorizing meeting topics. Analyze the meeting transcript and extract 1-5 short tags that describe the main topics discussed.

Rules:
- Tags must be lowercase, single words or short hyphenated terms
- Maximum 5 tags, minimum 1
- Focus on technical domains, projects, and team areas
- Examples: "ai", "database", "security", "frontend", "devops", "infrastructure", "연구개발망", "dmz", "agentcore", "backend", "design", "planning"
- Return ONLY a valid JSON array of strings. Example: ["ai","database","security"]
- If nothing specific can be determined, return ["general"]`

	// Build prompt with speaker segments if available
	userPrompt := fmt.Sprintf("Extract topic tags from this meeting transcript:\n\n%s", transcript)

	if meeting.TranscriptSegments != "" {
		var segments []speakerSegment
		if err := json.Unmarshal([]byte(meeting.TranscriptSegments), &segments); err == nil && len(segments) > 0 {
			var sb strings.Builder
			sb.WriteString("Extract topic tags from this speaker-labeled meeting transcript:\n\n")
			for _, seg := range segments {
				sb.WriteString(fmt.Sprintf("[%s] %s\n", seg.Speaker, seg.Text))
			}
			userPrompt = sb.String()
		}
	}

	request := ClaudeRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        256,
		System:           systemPrompt,
		Messages: []ClaudeMessage{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "text", Text: userPrompt},
				},
			},
		},
	}

	// Use Haiku for tag extraction (fast, cheap)
	response, err := s.invokeClaudeModelWithID(ctx, request, ClaudeHaikuModelID)
	if err != nil {
		return nil, fmt.Errorf("failed to extract tags: %w", err)
	}

	// Validate JSON response (strip code fences LLMs sometimes add)
	response = stripCodeFences(response)
	var tags []string
	if err := json.Unmarshal([]byte(response), &tags); err != nil {
		return []string{}, nil
	}

	// Validate and normalize: max 5 tags, each max 30 chars, lowercase
	var validated []string
	for _, tag := range tags {
		tag = strings.TrimSpace(strings.ToLower(tag))
		if tag != "" && len(tag) <= 30 {
			validated = append(validated, tag)
		}
		if len(validated) >= 5 {
			break
		}
	}

	if len(validated) == 0 {
		return []string{}, nil
	}

	return validated, nil
}

// getImageMediaType determines the media type from the file key
func (s *BedrockService) getImageMediaType(key string) string {
	lower := strings.ToLower(key)
	switch {
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(lower, ".png"):
		return "image/png"
	case strings.HasSuffix(lower, ".gif"):
		return "image/gif"
	case strings.HasSuffix(lower, ".webp"):
		return "image/webp"
	default:
		return ""
	}
}
