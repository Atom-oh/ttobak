package service

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// KnowledgeService handles RAG/Q&A operations
type KnowledgeService struct {
	bedrockRuntimeClient *bedrockagentruntime.Client
	repo                 *repository.DynamoDBRepository
	kbID                 string // Bedrock Knowledge Base ID
	modelARN             string // Model ARN for generation
}

// NewKnowledgeService creates a new knowledge service
func NewKnowledgeService(
	bedrockRuntimeClient *bedrockagentruntime.Client,
	repo *repository.DynamoDBRepository,
	kbID string,
	modelARN string,
) *KnowledgeService {
	return &KnowledgeService{
		bedrockRuntimeClient: bedrockRuntimeClient,
		repo:                 repo,
		kbID:                 kbID,
		modelARN:             modelARN,
	}
}

// Ask queries the knowledge base with a question about a meeting
func (s *KnowledgeService) Ask(ctx context.Context, userID, meetingID, question string) (*model.AskQuestionResponse, error) {
	// Get the meeting to provide context
	meeting, err := s.repo.GetMeeting(ctx, userID, meetingID)
	if err != nil {
		return nil, err
	}
	if meeting == nil {
		// Check shared access
		share, err := s.repo.GetShare(ctx, userID, meetingID)
		if err != nil {
			return nil, err
		}
		if share == nil {
			return nil, fmt.Errorf("meeting not found")
		}
		meeting, err = s.repo.GetMeetingByID(ctx, meetingID)
		if err != nil {
			return nil, err
		}
		if meeting == nil {
			return nil, fmt.Errorf("meeting not found")
		}
	}

	// Build context from meeting
	meetingContext := fmt.Sprintf("Meeting: %s\nDate: %s\n", meeting.Title, meeting.Date.Format("2006-01-02"))
	if len(meeting.Participants) > 0 {
		meetingContext += fmt.Sprintf("Participants: %v\n", meeting.Participants)
	}
	if meeting.Content != "" {
		meetingContext += fmt.Sprintf("\nMeeting Summary:\n%s\n", meeting.Content)
	}

	// Use selected transcript or both
	transcript := meeting.TranscriptA
	if meeting.SelectedTranscript == "B" && meeting.TranscriptB != "" {
		transcript = meeting.TranscriptB
	} else if meeting.TranscriptB != "" && meeting.TranscriptA == "" {
		transcript = meeting.TranscriptB
	}
	if transcript != "" {
		meetingContext += fmt.Sprintf("\nTranscript:\n%s\n", transcript)
	}

	return s.askWithContext(ctx, question, meetingContext)
}

// AskLive queries the knowledge base with client-provided context (for live recording)
func (s *KnowledgeService) AskLive(ctx context.Context, question, contextText string) (*model.AskQuestionResponse, error) {
	return s.askWithContext(ctx, question, contextText)
}

// askWithContext is the shared implementation for KB Q&A with arbitrary context
func (s *KnowledgeService) askWithContext(ctx context.Context, question, contextText string) (*model.AskQuestionResponse, error) {
	// Build the full prompt
	fullQuestion := fmt.Sprintf("Based on the following meeting context, answer the question.\n\n%s\n\nQuestion: %s", contextText, question)

	// If KB is not configured, return a simple response
	if s.bedrockRuntimeClient == nil || s.kbID == "" {
		return &model.AskQuestionResponse{
			Answer:  "Knowledge Base is not configured. Please configure it to enable Q&A functionality.",
			Sources: []string{},
		}, nil
	}

	// Call Bedrock RetrieveAndGenerate
	result, err := s.bedrockRuntimeClient.RetrieveAndGenerate(ctx, &bedrockagentruntime.RetrieveAndGenerateInput{
		Input: &types.RetrieveAndGenerateInput{
			Text: aws.String(fullQuestion),
		},
		RetrieveAndGenerateConfiguration: &types.RetrieveAndGenerateConfiguration{
			Type: types.RetrieveAndGenerateTypeKnowledgeBase,
			KnowledgeBaseConfiguration: &types.KnowledgeBaseRetrieveAndGenerateConfiguration{
				KnowledgeBaseId: aws.String(s.kbID),
				ModelArn:        aws.String(s.modelARN),
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve and generate: %w", err)
	}

	// Extract answer and sources
	answer := ""
	if result.Output != nil && result.Output.Text != nil {
		answer = *result.Output.Text
	}

	var sources []string
	if result.Citations != nil {
		for _, citation := range result.Citations {
			if citation.RetrievedReferences != nil {
				for _, ref := range citation.RetrievedReferences {
					if ref.Location != nil && ref.Location.S3Location != nil && ref.Location.S3Location.Uri != nil {
						sources = append(sources, *ref.Location.S3Location.Uri)
					}
				}
			}
		}
	}

	return &model.AskQuestionResponse{
		Answer:  answer,
		Sources: sources,
	}, nil
}
