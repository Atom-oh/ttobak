package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// ResearchService handles research task business logic.
type ResearchService struct {
	repo          *repository.ResearchRepository
	s3Client      *s3.Client
	agentClient   *bedrockagentruntime.Client
	kbBucketName  string
	agentId       string
	agentAliasId  string
}

// NewResearchService creates a new ResearchService.
func NewResearchService(repo *repository.ResearchRepository, s3Client *s3.Client, agentClient *bedrockagentruntime.Client, kbBucketName, agentId, agentAliasId string) *ResearchService {
	return &ResearchService{
		repo:         repo,
		s3Client:     s3Client,
		agentClient:  agentClient,
		kbBucketName: kbBucketName,
		agentId:      agentId,
		agentAliasId: agentAliasId,
	}
}

// generateID returns a random 32-character hex string.
func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// CreateResearch validates the request, persists a new research record, and returns it.
// Agent invocation will be added later when the CDK-managed Bedrock agent is created.
func (s *ResearchService) CreateResearch(ctx context.Context, userId string, req *model.CreateResearchRequest) (*model.Research, error) {
	// Validate mode
	switch req.Mode {
	case "quick", "standard", "deep":
		// valid
	default:
		return nil, fmt.Errorf("invalid mode %q: must be quick, standard, or deep", req.Mode)
	}

	id := generateID()
	now := time.Now().UTC().Format(time.RFC3339)

	research := &model.Research{
		ResearchID: id,
		UserID:     userId,
		Topic:      req.Topic,
		Mode:       req.Mode,
		Status:     "running",
		CreatedAt:  now,
		S3Key:      fmt.Sprintf("shared/research/%s.md", id),
	}

	if err := s.repo.CreateResearch(ctx, research); err != nil {
		return nil, fmt.Errorf("failed to create research: %w", err)
	}

	// Invoke Bedrock Agent asynchronously
	if s.agentClient != nil && s.agentId != "" && s.agentAliasId != "" {
		go s.invokeAgent(research)
	} else {
		log.Printf("Research agent not configured (agentId=%q), skipping invocation for %s", s.agentId, id)
	}

	return research, nil
}

// invokeAgent calls the Bedrock Agent with the research topic. Runs in a goroutine.
func (s *ResearchService) invokeAgent(research *model.Research) {
	ctx := context.Background()
	prompt := fmt.Sprintf("Research topic: %s\nMode: %s\nResearch ID: %s\n\nPlease conduct the research following the pipeline for %s mode and call save_report when complete.",
		research.Topic, research.Mode, research.ResearchID, research.Mode)

	log.Printf("Invoking Bedrock Agent %s for research %s", s.agentId, research.ResearchID)

	output, err := s.agentClient.InvokeAgent(ctx, &bedrockagentruntime.InvokeAgentInput{
		AgentId:      aws.String(s.agentId),
		AgentAliasId: aws.String(s.agentAliasId),
		SessionId:    aws.String(research.ResearchID),
		InputText:    aws.String(prompt),
	})
	if err != nil {
		log.Printf("Failed to invoke agent for research %s: %v", research.ResearchID, err)
		s.repo.UpdateResearchFields(ctx, research.ResearchID, map[string]interface{}{
			"status":       "error",
			"errorMessage": fmt.Sprintf("Agent invocation failed: %v", err),
		})
		return
	}

	// Consume the streaming response (agent runs asynchronously via tools)
	stream := output.GetStream()
	evReader := stream.Reader
	defer stream.Close()

	for event := range evReader.Events() {
		// Just consume events — the agent calls save_report tool which updates DynamoDB
		_ = event
	}
	if err := evReader.Err(); err != nil {
		log.Printf("Agent stream error for research %s: %v", research.ResearchID, err)
		s.repo.UpdateResearchFields(ctx, research.ResearchID, map[string]interface{}{
			"status":       "error",
			"errorMessage": fmt.Sprintf("Agent execution failed: %v", err),
		})
	} else {
		log.Printf("Agent completed for research %s", research.ResearchID)
	}
}

// ListResearch returns all research tasks belonging to a user.
func (s *ResearchService) ListResearch(ctx context.Context, userId string) (*model.ResearchListResponse, error) {
	items, err := s.repo.ListUserResearch(ctx, userId)
	if err != nil {
		return nil, fmt.Errorf("failed to list research: %w", err)
	}

	return &model.ResearchListResponse{Research: items}, nil
}

// GetResearchDetail retrieves a single research record. If the research is done
// and has an S3 key, the generated content is fetched from S3 and included.
func (s *ResearchService) GetResearchDetail(ctx context.Context, researchId string) (*model.ResearchResponse, error) {
	research, err := s.repo.GetResearch(ctx, researchId)
	if err != nil {
		return nil, fmt.Errorf("failed to get research: %w", err)
	}
	if research == nil {
		return nil, ErrNotFound
	}

	resp := &model.ResearchResponse{
		Research: *research,
	}

	// Fetch content from S3 when the research is complete
	if research.Status == "done" && research.S3Key != "" {
		content, err := s.readS3Content(ctx, research.S3Key)
		if err != nil {
			// Log but don't fail — metadata is still useful
			fmt.Printf("warn: failed to read research content from S3: %v\n", err)
		} else {
			resp.Content = content
		}
	}

	return resp, nil
}

// DeleteResearch removes a research record and its S3 artifact.
func (s *ResearchService) DeleteResearch(ctx context.Context, researchId, userId string) error {
	research, err := s.repo.GetResearch(ctx, researchId)
	if err != nil {
		return fmt.Errorf("failed to get research: %w", err)
	}
	if research == nil {
		return ErrNotFound
	}

	// Delete S3 object if it exists
	if research.S3Key != "" {
		_, err := s.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: &s.kbBucketName,
			Key:    &research.S3Key,
		})
		if err != nil {
			// Log but continue — DynamoDB record should still be cleaned up
			fmt.Printf("warn: failed to delete research S3 object %s: %v\n", research.S3Key, err)
		}
	}

	if err := s.repo.DeleteResearch(ctx, researchId, userId); err != nil {
		return fmt.Errorf("failed to delete research: %w", err)
	}

	return nil
}

// readS3Content fetches the full body of an S3 object as a string.
func (s *ResearchService) readS3Content(ctx context.Context, key string) (string, error) {
	out, err := s.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.kbBucketName,
		Key:    &key,
	})
	if err != nil {
		return "", fmt.Errorf("s3 GetObject: %w", err)
	}
	defer out.Body.Close()

	data, err := io.ReadAll(out.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	return string(data), nil
}
