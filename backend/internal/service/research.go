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

// researchMainRepo is the slice of the main-table repository ResearchService
// needs (user lookup for sharing, account membership + refs for account
// linking). Kept as an interface, mirroring AccountService's accountRepo, so
// tests can supply an in-memory fake instead of a real DynamoDB table.
type researchMainRepo interface {
	ListSharesForUser(ctx context.Context, userID string) ([]model.Share, error)
	GetUserByEmail(ctx context.Context, email string) (*model.User, error)
	GetMember(ctx context.Context, accountID, userID string) (*model.AccountMember, error)
	PutResearchRef(ctx context.Context, ref *model.ResearchRef) error
	DeleteResearchRef(ctx context.Context, accountID, researchID string) error
	ListResearchRefsForAccount(ctx context.Context, accountID string) ([]model.ResearchRef, error)
}

// researchRepo is the slice of ResearchRepository ResearchService needs.
// Kept as an interface (mirrors researchMainRepo above) so tests can supply
// an in-memory fake instead of a real DynamoDB table.
type researchRepo interface {
	CreateResearch(ctx context.Context, research *model.Research) error
	GetResearch(ctx context.Context, researchId string) (*model.Research, error)
	UpdateResearchFieldsConditional(ctx context.Context, researchId string, fields map[string]interface{}, expectedStatus string) error
	UpdateResearchFields(ctx context.Context, researchId string, fields map[string]interface{}) error
	ListUserResearch(ctx context.Context, userId string) ([]model.Research, error)
	BatchGetResearch(ctx context.Context, researchIds []string) ([]model.Research, error)
	ListSubPages(ctx context.Context, userId, parentId string) ([]model.Research, error)
	RemoveResearchField(ctx context.Context, researchId, fieldName string) error
	DeleteResearch(ctx context.Context, researchId, userId string) error
	CreateResearchShare(ctx context.Context, researchID, ownerID, ownerEmail, sharedToID, email, permission string) (*model.Share, error)
	GetResearchShare(ctx context.Context, sharedToID, researchID string) (*model.Share, error)
	DeleteResearchShare(ctx context.Context, sharedToID, researchID string) error
	ListSharesForResearch(ctx context.Context, researchID string) ([]model.Share, error)
}

type ResearchService struct {
	repo            researchRepo
	mainRepo        researchMainRepo
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

// newResearchServiceWithRepo is for same-package (service) tests: it accepts
// the researchMainRepo interface directly so a test can supply an in-memory
// fake instead of a real DynamoDB table (mirrors newAccountServiceWithRepo).
func newResearchServiceWithRepo(repo researchRepo, mainRepo researchMainRepo) *ResearchService {
	return &ResearchService{repo: repo, mainRepo: mainRepo}
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

	// Include research shared with this user (BatchGetItem to avoid N+1)
	if s.mainRepo != nil {
		shares, err := s.mainRepo.ListSharesForUser(ctx, userId)
		if err != nil {
			log.Printf("warn: failed to list shared research for %s: %v", userId, err)
		} else {
			var researchShares []model.Share
			for _, share := range shares {
				if share.EntityType == "RESEARCH_SHARE" {
					researchShares = append(researchShares, share)
				}
			}
			if len(researchShares) > 0 {
				ids := make([]string, len(researchShares))
				for i, share := range researchShares {
					ids[i] = share.MeetingID
				}
				sharedResearch, err := s.repo.BatchGetResearch(ctx, ids)
				if err != nil {
					log.Printf("warn: failed to batch get shared research: %v", err)
				} else {
					shareByID := make(map[string]model.Share, len(researchShares))
					for _, share := range researchShares {
						shareByID[share.MeetingID] = share
					}
					for _, r := range sharedResearch {
						if !includeTrashed && r.TrashedAt != "" {
							continue
						}
						r.IsShared = true
						if sh, ok := shareByID[r.ResearchID]; ok {
							r.SharedBy = sh.OwnerEmail
						}
						items = append(items, r)
					}
				}
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
		if share != nil {
			research.IsShared = true
		} else if s.mainRepo != nil && s.hasAccountAccess(ctx, research.AccountIDs, userId) {
			// Not shared directly, but the caller is a member of an account
			// this research is linked to -- grant read access. IsShared=true
			// keeps the owner-only UI (account chips, share button) hidden
			// for this viewer, same as a direct share would.
			research.IsShared = true
		} else {
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

// hasAccountAccess reports whether userId is a member of any account in accountIDs.
func (s *ResearchService) hasAccountAccess(ctx context.Context, accountIDs []string, userId string) bool {
	for _, accID := range accountIDs {
		member, err := s.mainRepo.GetMember(ctx, accID, userId)
		if err != nil {
			log.Printf("warn: failed to check account membership %s for research access: %v", accID, err)
			continue
		}
		if member != nil {
			return true
		}
	}
	return false
}

// LinkAccount links a research (owner only) to an account the caller is a
// member of. Idempotent: linking an already-linked account is a no-op.
func (s *ResearchService) LinkAccount(ctx context.Context, userID, researchID, accountID string) ([]string, error) {
	if s.mainRepo == nil {
		return nil, fmt.Errorf("account linking not configured")
	}
	research, err := s.repo.GetResearch(ctx, researchID)
	if err != nil {
		return nil, fmt.Errorf("failed to get research: %w", err)
	}
	if research == nil {
		return nil, ErrNotFound
	}
	if research.UserID != userID {
		return nil, ErrForbidden
	}

	member, err := s.mainRepo.GetMember(ctx, accountID, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to check account membership: %w", err)
	}
	if member == nil {
		return nil, ErrForbidden
	}

	for _, id := range research.AccountIDs {
		if id == accountID {
			return research.AccountIDs, nil // already linked
		}
	}
	accountIDs := append(append([]string{}, research.AccountIDs...), accountID)

	if err := s.repo.UpdateResearchFields(ctx, researchID, map[string]interface{}{
		"accountIds": accountIDs,
	}); err != nil {
		return nil, fmt.Errorf("failed to link account: %w", err)
	}

	if err := s.mainRepo.PutResearchRef(ctx, &model.ResearchRef{
		PK:          model.PrefixAccount + accountID,
		SK:          model.PrefixResearchRef + researchID,
		AccountID:   accountID,
		ResearchID:  researchID,
		OwnerUserID: userID,
		Topic:       research.Topic,
		CreatedAt:   time.Now().UTC(),
		EntityType:  model.EntityTypeResearchRef,
	}); err != nil {
		log.Printf("warn: failed to write research ref for account %s: %v", accountID, err)
	}

	return accountIDs, nil
}

// UnlinkAccount removes a research↔account link (owner only).
func (s *ResearchService) UnlinkAccount(ctx context.Context, userID, researchID, accountID string) ([]string, error) {
	if s.mainRepo == nil {
		return nil, fmt.Errorf("account linking not configured")
	}
	research, err := s.repo.GetResearch(ctx, researchID)
	if err != nil {
		return nil, fmt.Errorf("failed to get research: %w", err)
	}
	if research == nil {
		return nil, ErrNotFound
	}
	if research.UserID != userID {
		return nil, ErrForbidden
	}

	remaining := make([]string, 0, len(research.AccountIDs))
	for _, id := range research.AccountIDs {
		if id != accountID {
			remaining = append(remaining, id)
		}
	}

	if err := s.repo.UpdateResearchFields(ctx, researchID, map[string]interface{}{
		"accountIds": remaining,
	}); err != nil {
		return nil, fmt.Errorf("failed to unlink account: %w", err)
	}

	if err := s.mainRepo.DeleteResearchRef(ctx, accountID, researchID); err != nil {
		log.Printf("warn: failed to delete research ref for account %s: %v", accountID, err)
	}

	return remaining, nil
}

// summaryPreviewMaxLen caps AccountResearchDTO.Summary so the account
// reference panel's list view never carries a full research summary.
const summaryPreviewMaxLen = 300

// truncateRunes cuts s to at most n runes, rune-boundary safe (Korean/multi-byte
// text would otherwise risk a byte-index cut landing mid-character).
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ListAccountResearch returns research linked to accountID, for members only.
func (s *ResearchService) ListAccountResearch(ctx context.Context, userID, accountID string) ([]model.AccountResearchDTO, error) {
	if s.mainRepo == nil {
		return nil, fmt.Errorf("account linking not configured")
	}
	member, err := s.mainRepo.GetMember(ctx, accountID, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to check account membership: %w", err)
	}
	if member == nil {
		return nil, ErrForbidden
	}

	refs, err := s.mainRepo.ListResearchRefsForAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to list research refs: %w", err)
	}
	if len(refs) == 0 {
		return []model.AccountResearchDTO{}, nil
	}

	ids := make([]string, len(refs))
	for i, ref := range refs {
		ids[i] = ref.ResearchID
	}
	items, err := s.repo.BatchGetResearch(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to batch get research: %w", err)
	}

	dtos := make([]model.AccountResearchDTO, 0, len(items))
	for _, r := range items {
		if r.TrashedAt != "" {
			continue
		}
		summary := truncateRunes(r.Summary, summaryPreviewMaxLen)
		dtos = append(dtos, model.AccountResearchDTO{
			ResearchID:  r.ResearchID,
			Topic:       r.Topic,
			Summary:     summary,
			Status:      r.Status,
			OwnerUserID: r.UserID,
			CreatedAt:   r.CreatedAt,
		})
	}
	return dtos, nil
}
