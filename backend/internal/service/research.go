package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"time"

	"encoding/json"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcore"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// ResearchService handles research task business logic.
type ResearchService struct {
	repo             *repository.ResearchRepository
	s3Client         *s3.Client
	agentCoreClient  *bedrockagentcore.Client
	kbBucketName     string
	agentRuntimeId   string
	endpointName     string
}

// NewResearchService creates a new ResearchService.
func NewResearchService(repo *repository.ResearchRepository, s3Client *s3.Client, agentCoreClient *bedrockagentcore.Client, kbBucketName, agentRuntimeId, endpointName string) *ResearchService {
	return &ResearchService{
		repo:            repo,
		s3Client:        s3Client,
		agentCoreClient: agentCoreClient,
		kbBucketName:    kbBucketName,
		agentRuntimeId:  agentRuntimeId,
		endpointName:    endpointName,
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

	// Invoke AgentCore Runtime asynchronously
	if s.agentCoreClient != nil && s.agentRuntimeId != "" {
		go s.invokeAgentCore(research)
	} else {
		log.Printf("AgentCore not configured (runtimeId=%q), skipping invocation for %s", s.agentRuntimeId, id)
	}

	return research, nil
}

// invokeAgentCore calls AgentCore Runtime with the research topic. Runs in a goroutine.
func (s *ResearchService) invokeAgentCore(research *model.Research) {
	ctx := context.Background()

	payload := map[string]string{
		"topic":      research.Topic,
		"mode":       research.Mode,
		"researchId": research.ResearchID,
	}
	payloadBytes, _ := json.Marshal(payload)

	agentRuntimeArn := s.agentRuntimeId
	qualifier := s.endpointName
	if qualifier == "" {
		qualifier = "ttobakResearchEndpoint"
	}

	log.Printf("Invoking AgentCore Runtime %s (endpoint=%s) for research %s", agentRuntimeArn, qualifier, research.ResearchID)

	output, err := s.agentCoreClient.InvokeAgentRuntime(ctx, &bedrockagentcore.InvokeAgentRuntimeInput{
		AgentRuntimeArn:  aws.String(agentRuntimeArn),
		Qualifier:        aws.String(qualifier),
		Payload:          payloadBytes,
		RuntimeSessionId: aws.String(research.ResearchID),
		ContentType:      aws.String("application/json"),
		Accept:           aws.String("application/json"),
	})
	if err != nil {
		log.Printf("Failed to invoke AgentCore for research %s: %v", research.ResearchID, err)
		s.repo.UpdateResearchFields(ctx, research.ResearchID, map[string]interface{}{
			"status":       "error",
			"errorMessage": fmt.Sprintf("AgentCore invocation failed: %v", err),
		})
		return
	}

	// Read response — agent runs inside AgentCore Runtime microVM
	if output.Response != nil {
		defer output.Response.Close()
		respBody, err := io.ReadAll(output.Response)
		if err != nil {
			log.Printf("AgentCore response read error for research %s: %v", research.ResearchID, err)
		} else {
			log.Printf("AgentCore response for research %s: %s", research.ResearchID, string(respBody)[:200])
		}
	}

	log.Printf("AgentCore completed for research %s", research.ResearchID)
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
