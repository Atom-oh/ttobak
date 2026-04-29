package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"errors"
	"io"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type ResearchService struct {
	repo            *repository.ResearchRepository
	mainRepo        *repository.DynamoDBRepository
	s3Client        *s3.Client
	sfnClient       *sfn.Client
	kbBucketName    string
	stateMachineArn string
}

func NewResearchService(repo *repository.ResearchRepository, mainRepo *repository.DynamoDBRepository, s3Client *s3.Client, sfnClient *sfn.Client, kbBucketName, stateMachineArn string) *ResearchService {
	return &ResearchService{
		repo:            repo,
		mainRepo:        mainRepo,
		s3Client:        s3Client,
		sfnClient:       sfnClient,
		kbBucketName:    kbBucketName,
		stateMachineArn: stateMachineArn,
	}
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *ResearchService) CreateResearch(ctx context.Context, userId string, req *model.CreateResearchRequest) (*model.Research, error) {
	switch req.Mode {
	case "quick", "standard", "deep":
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
		Status:     "planning",
		CreatedAt:  now,
		S3Key:      fmt.Sprintf("shared/research/%s.md", id),
	}

	if err := s.repo.CreateResearch(ctx, research); err != nil {
		return nil, fmt.Errorf("failed to create research: %w", err)
	}

	if s.sfnClient != nil && s.stateMachineArn != "" {
		input := map[string]string{
			"researchId":  id,
			"userId":      userId,
			"topic":       req.Topic,
			"mode":        "plan",
			"qualityMode": req.Mode,
			"s3Key":       research.S3Key,
		}
		inputBytes, _ := json.Marshal(input)

		execName := s.sfnExecutionName(id, "plan")
		_, err := s.sfnClient.StartExecution(ctx, &sfn.StartExecutionInput{
			StateMachineArn: aws.String(s.stateMachineArn),
			Name:            aws.String(execName),
			Input:           aws.String(string(inputBytes)),
		})
		if err != nil {
			log.Printf("Failed to start research SFN for %s: %v", id, err)
			s.repo.UpdateResearchFields(ctx, id, map[string]interface{}{
				"status":       "error",
				"errorMessage": fmt.Sprintf("Failed to start research pipeline: %v", err),
			})
			research.Status = "error"
			research.ErrorMessage = fmt.Sprintf("Failed to start research pipeline: %v", err)
		} else {
			log.Printf("Started research SFN for %s", id)
		}
	} else {
		log.Printf("Research SFN not configured (arn=%q), marking %s as error", s.stateMachineArn, id)
		s.repo.UpdateResearchFields(ctx, id, map[string]interface{}{
			"status":       "error",
			"errorMessage": "Research pipeline not configured",
		})
		research.Status = "error"
		research.ErrorMessage = "Research pipeline not configured"
	}

	return research, nil
}

func (s *ResearchService) ListResearch(ctx context.Context, userId string, includeTrashed bool) (*model.ResearchListResponse, error) {
	items, err := s.repo.ListUserResearch(ctx, userId)
	if err != nil {
		return nil, fmt.Errorf("failed to list research: %w", err)
	}
	if !includeTrashed {
		filtered := make([]model.Research, 0, len(items))
		for _, r := range items {
			if r.TrashedAt == "" {
				filtered = append(filtered, r)
			}
		}
		items = filtered
	}

	// Include research shared with this user
	if s.mainRepo != nil {
		shares, err := s.mainRepo.ListSharesForUser(ctx, userId)
		if err != nil {
			log.Printf("warn: failed to list shared research for %s: %v", userId, err)
		} else {
			for _, share := range shares {
				if share.EntityType != "RESEARCH_SHARE" {
					continue
				}
				research, err := s.repo.GetResearch(ctx, share.MeetingID)
				if err != nil || research == nil {
					continue
				}
				if !includeTrashed && research.TrashedAt != "" {
					continue
				}
				research.IsShared = true
				research.SharedBy = share.OwnerEmail
				items = append(items, *research)
			}
		}
	}

	return &model.ResearchListResponse{Research: items}, nil
}

func (s *ResearchService) GetResearchDetail(ctx context.Context, researchId, userId string) (*model.ResearchResponse, error) {
	research, err := s.repo.GetResearch(ctx, researchId)
	if err != nil {
		return nil, fmt.Errorf("failed to get research: %w", err)
	}
	if research == nil {
		return nil, ErrNotFound
	}

	isOwner := research.UserID == userId
	if !isOwner {
		share, err := s.repo.GetResearchShare(ctx, userId, researchId)
		if err != nil {
			return nil, fmt.Errorf("failed to check share: %w", err)
		}
		if share == nil {
			return nil, ErrForbidden
		}
	}

	if research.TrashedAt != "" && !isOwner {
		return nil, ErrNotFound
	}

	resp := &model.ResearchResponse{Research: *research}

	if research.Status == "done" && research.S3Key != "" {
		content, err := s.readS3Content(ctx, research.S3Key)
		if err != nil {
			fmt.Printf("warn: failed to read research content from S3: %v\n", err)
		} else {
			resp.Content = content
		}
	}

	if isOwner {
		shares, _ := s.repo.ListSharesForResearch(ctx, researchId)
		if len(shares) > 0 {
			shareResponses := make([]model.ShareResponse, len(shares))
			for i, sh := range shares {
				shareResponses[i] = model.ShareResponse{
					UserID:     sh.SharedToID,
					Email:      sh.Email,
					Permission: sh.Permission,
				}
			}
			resp.Shares = shareResponses
		}
	}

	return resp, nil
}

func (s *ResearchService) TrashResearch(ctx context.Context, researchId, userId string) error {
	research, err := s.repo.GetResearch(ctx, researchId)
	if err != nil {
		return fmt.Errorf("failed to get research: %w", err)
	}
	if research == nil {
		return ErrNotFound
	}
	if research.UserID != userId {
		return ErrForbidden
	}

	return s.repo.UpdateResearchFields(ctx, researchId, map[string]interface{}{
		"trashedAt": time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *ResearchService) RestoreResearch(ctx context.Context, researchId, userId string) error {
	research, err := s.repo.GetResearch(ctx, researchId)
	if err != nil {
		return fmt.Errorf("failed to get research: %w", err)
	}
	if research == nil {
		return ErrNotFound
	}
	if research.UserID != userId {
		return ErrForbidden
	}
	if research.TrashedAt == "" {
		return nil
	}

	return s.repo.RemoveResearchField(ctx, researchId, "trashedAt")
}

// sfnExecutionName generates a unique SFN execution name: research-{id prefix}-{mode}-{random}
func (s *ResearchService) sfnExecutionName(researchId, mode string) string {
	prefix := researchId
	if len(prefix) > 16 {
		prefix = prefix[:16]
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		suffix = []byte(fmt.Sprintf("%04d", time.Now().UnixNano()%10000))
	}
	return fmt.Sprintf("research-%s-%s-%s", prefix, mode, hex.EncodeToString(suffix))
}

// startSFN is a helper to start a Step Functions execution with the given input map.
func (s *ResearchService) startSFN(ctx context.Context, researchId, mode string, extra map[string]string) error {
	if s.sfnClient == nil || s.stateMachineArn == "" {
		return fmt.Errorf("research SFN not configured")
	}

	input := map[string]string{
		"researchId": researchId,
		"mode":       mode,
	}
	for k, v := range extra {
		input[k] = v
	}
	inputBytes, _ := json.Marshal(input)

	execName := s.sfnExecutionName(researchId, mode)
	_, err := s.sfnClient.StartExecution(ctx, &sfn.StartExecutionInput{
		StateMachineArn: aws.String(s.stateMachineArn),
		Name:            aws.String(execName),
		Input:           aws.String(string(inputBytes)),
	})
	if err != nil {
		return fmt.Errorf("failed to start SFN (%s): %w", mode, err)
	}
	log.Printf("Started research SFN for %s mode=%s", researchId, mode)
	return nil
}

// TriggerAgentRespond triggers the Agent to respond to a user's chat message.
func (s *ResearchService) TriggerAgentRespond(ctx context.Context, researchId, userId string) error {
	research, err := s.repo.GetResearch(ctx, researchId)
	if err != nil {
		return fmt.Errorf("failed to get research: %w", err)
	}
	if research == nil {
		return ErrNotFound
	}
	if research.UserID != userId {
		return ErrForbidden
	}
	return s.startSFN(ctx, researchId, "respond", map[string]string{
		"topic": research.Topic,
	})
}

// ApproveResearch changes status to "approved" and triggers execution.
func (s *ResearchService) ApproveResearch(ctx context.Context, researchId, userId string) error {
	research, err := s.repo.GetResearch(ctx, researchId)
	if err != nil {
		return fmt.Errorf("failed to get research: %w", err)
	}
	if research == nil {
		return ErrNotFound
	}
	if research.UserID != userId {
		return ErrForbidden
	}
	if research.Status != "planning" {
		return fmt.Errorf("research status is %s, expected planning: %w", research.Status, ErrStatusMismatch)
	}

	if err := s.repo.UpdateResearchFieldsConditional(ctx, researchId, map[string]interface{}{
		"status": "running",
	}, "planning"); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return ErrStatusMismatch
		}
		return fmt.Errorf("failed to update status to running: %w", err)
	}

	extra := map[string]string{
		"userId":      research.UserID,
		"topic":       research.Topic,
		"s3Key":       research.S3Key,
		"qualityMode": research.Mode,
	}
	return s.startSFN(ctx, researchId, "execute", extra)
}

// CreateSubPage creates a child research and triggers execution.
func (s *ResearchService) CreateSubPage(ctx context.Context, userId, parentId, topic string) (*model.Research, error) {
	// Verify parent research exists, is owned by user, and is done
	parent, err := s.repo.GetResearch(ctx, parentId)
	if err != nil {
		return nil, fmt.Errorf("failed to get parent research: %w", err)
	}
	if parent == nil {
		return nil, ErrNotFound
	}
	if parent.UserID != userId {
		return nil, ErrForbidden
	}
	if parent.Status != "done" {
		return nil, fmt.Errorf("parent research status is %s, expected done", parent.Status)
	}

	id := generateID()
	now := time.Now().UTC().Format(time.RFC3339)

	research := &model.Research{
		ResearchID: id,
		UserID:     userId,
		Topic:      topic,
		Mode:       "deep",
		Status:     "running",
		CreatedAt:  now,
		S3Key:      fmt.Sprintf("shared/research/%s.md", id),
		ParentID:   parentId,
	}

	if err := s.repo.CreateResearch(ctx, research); err != nil {
		return nil, fmt.Errorf("failed to create sub-page: %w", err)
	}

	err = s.startSFN(ctx, id, "subpage", map[string]string{
		"userId":      userId,
		"topic":       topic,
		"s3Key":       research.S3Key,
		"parentId":    parentId,
		"qualityMode": "deep",
	})
	if err != nil {
		log.Printf("Failed to start sub-page SFN for %s: %v", id, err)
		s.repo.UpdateResearchFields(ctx, id, map[string]interface{}{
			"status":       "error",
			"errorMessage": fmt.Sprintf("Failed to start sub-page pipeline: %v", err),
		})
		research.Status = "error"
		research.ErrorMessage = fmt.Sprintf("Failed to start sub-page pipeline: %v", err)
	}

	return research, nil
}

// ListSubPages returns child research items for a given parent.
func (s *ResearchService) ListSubPages(ctx context.Context, userId, parentId string) ([]model.Research, error) {
	return s.repo.ListSubPages(ctx, userId, parentId)
}

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

// ShareResearchByEmail shares a research with a user identified by email
func (s *ResearchService) ShareResearchByEmail(ctx context.Context, ownerID, ownerEmail, researchId, targetEmail, permission string) (*model.Share, error) {
	research, err := s.repo.GetResearch(ctx, researchId)
	if err != nil {
		return nil, err
	}
	if research == nil {
		return nil, ErrNotFound
	}
	if research.UserID != ownerID {
		return nil, ErrForbidden
	}

	targetUser, err := s.mainRepo.GetUserByEmail(ctx, targetEmail)
	if err != nil {
		return nil, err
	}
	if targetUser == nil {
		return nil, ErrUserNotFound
	}

	if ownerID == targetUser.UserID {
		return nil, ErrSelfShare
	}

	return s.repo.CreateResearchShare(ctx, researchId, ownerID, ownerEmail, targetUser.UserID, targetEmail, permission)
}

// RevokeResearchShare revokes a research share (owner only)
func (s *ResearchService) RevokeResearchShare(ctx context.Context, ownerID, researchId, sharedToID string) error {
	research, err := s.repo.GetResearch(ctx, researchId)
	if err != nil {
		return err
	}
	if research == nil {
		return ErrNotFound
	}
	if research.UserID != ownerID {
		return ErrForbidden
	}

	return s.repo.DeleteResearchShare(ctx, sharedToID, researchId)
}
