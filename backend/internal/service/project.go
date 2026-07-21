package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// projectRepo is the narrow persistence seam used by ProjectService.
type projectRepo interface {
	CreateProject(context.Context, *model.Project) error
	GetProject(context.Context, string) (*model.Project, error)
	GetProjectMember(context.Context, string, string) (*model.ProjectMember, error)
	PutProjectMember(context.Context, *model.ProjectMember) error
	ListProjectMembers(context.Context, string) ([]model.ProjectMember, error)
	ListProjectsForUser(context.Context, string) ([]model.Project, error)
	UpdateProjectFields(context.Context, string, map[string]interface{}) error
	DeleteProject(context.Context, string, string) error
	AddProjectAccountLink(context.Context, string, string) error
	RemoveProjectAccountLink(context.Context, string, string) error
	AddMeetingProjectLink(context.Context, string, string, string) error
	RemoveMeetingProjectLink(context.Context, string, string, string) error
	AddResearchProjectLink(context.Context, string, string) error
	RemoveResearchProjectLink(context.Context, string, string) error
	PutProjectRef(context.Context, *model.ProjectRef) error
	DeleteProjectRef(context.Context, string, string) error
	ListProjectRefsForAccount(context.Context, string) ([]model.ProjectRef, error)
	PutProjectMeetingRef(context.Context, *model.ProjectMeetingRef) error
	DeleteProjectMeetingRef(context.Context, string, string) error
	ListProjectMeetingRefsForProject(context.Context, string) ([]model.ProjectMeetingRef, error)
	PutProjectResearchRef(context.Context, *model.ProjectResearchRef) error
	DeleteProjectResearchRef(context.Context, string, string) error
	ListProjectResearchRefsForProject(context.Context, string) ([]model.ProjectResearchRef, error)
	GetMember(context.Context, string, string) (*model.AccountMember, error)
	GetAccount(context.Context, string) (*model.Account, error)
	GetUserByEmail(context.Context, string) (*model.User, error)
	GetMeeting(context.Context, string, string) (*model.Meeting, error)
	GetResearchByID(context.Context, string) (*model.Research, error)
	BatchGetMeetings(context.Context, []repository.MeetingKey) ([]*model.Meeting, error)
	BatchGetResearchByIDs(context.Context, []string) ([]model.Research, error)
}

// ProjectRepo is exported for handler-package tests.
type ProjectRepo = projectRepo

type ProjectService struct {
	repo projectRepo
}

func NewProjectService(repo *repository.DynamoDBRepository) *ProjectService {
	return &ProjectService{repo: repo}
}

func newProjectServiceWithRepo(repo projectRepo) *ProjectService {
	return &ProjectService{repo: repo}
}

func NewProjectServiceForTest(repo ProjectRepo) *ProjectService {
	return &ProjectService{repo: repo}
}

func (s *ProjectService) CreateProject(ctx context.Context, ownerUserID, ownerEmail string, req *model.CreateProjectRequest) (*model.Project, error) {
	if req == nil || strings.TrimSpace(req.Name) == "" {
		return nil, ErrInvalidInput
	}
	now := time.Now().UTC()
	projectID := uuid.NewString()
	project := &model.Project{
		PK: model.PrefixProject + projectID, SK: model.SKProjectConfig,
		ProjectID: projectID, Name: strings.TrimSpace(req.Name),
		Description: req.Description, SfdcOpptyID: req.SfdcOpptyID,
		SfdcURL: req.SfdcURL, Stage: req.Stage, OwnerUserID: ownerUserID,
		CreatedAt: now, UpdatedAt: now, EntityType: model.EntityTypeProject,
	}
	if err := s.repo.CreateProject(ctx, project); err != nil {
		return nil, err
	}
	return project, nil
}

func (s *ProjectService) requireProjectAccess(ctx context.Context, userID, projectID string) (*model.Project, error) {
	project, err := s.repo.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, ErrNotFound
	}
	if project.OwnerUserID == userID {
		return project, nil
	}
	member, err := s.repo.GetProjectMember(ctx, projectID, userID)
	if err != nil {
		return nil, err
	}
	if member != nil {
		return project, nil
	}
	for _, accountID := range project.AccountIDs {
		member, err := s.repo.GetMember(ctx, accountID, userID)
		if err != nil {
			log.Printf("warn: failed to check account membership %s for project access: %v", accountID, err)
			continue
		}
		if member != nil {
			return project, nil
		}
	}
	return nil, ErrForbidden
}

func projectResponse(project *model.Project, members []model.ProjectMember) *model.ProjectResponse {
	memberDTOs := make([]model.ProjectMemberDTO, 0, len(members))
	for _, member := range members {
		memberDTOs = append(memberDTOs, model.ProjectMemberDTO{UserID: member.UserID, Email: member.Email})
	}
	accountIDs := append([]string{}, project.AccountIDs...)
	return &model.ProjectResponse{
		ProjectID: project.ProjectID, Name: project.Name, Description: project.Description,
		SfdcOpptyID: project.SfdcOpptyID, SfdcURL: project.SfdcURL, Stage: project.Stage,
		OwnerUserID: project.OwnerUserID, AccountIDs: accountIDs, Members: memberDTOs,
		CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt,
	}
}

func (s *ProjectService) GetProject(ctx context.Context, userID, projectID string) (*model.ProjectResponse, error) {
	project, err := s.requireProjectAccess(ctx, userID, projectID)
	if err != nil {
		return nil, err
	}
	members, err := s.repo.ListProjectMembers(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return projectResponse(project, members), nil
}

func (s *ProjectService) ListMyProjects(ctx context.Context, userID string) ([]model.ProjectSummary, error) {
	projects, err := s.repo.ListProjectsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]model.ProjectSummary, 0, len(projects))
	for _, project := range projects {
		out = append(out, model.ProjectSummary{ProjectID: project.ProjectID, Name: project.Name, Stage: project.Stage, SfdcOpptyID: project.SfdcOpptyID})
	}
	return out, nil
}

func (s *ProjectService) UpdateProject(ctx context.Context, userID, projectID string, req *model.UpdateProjectRequest) (*model.ProjectResponse, error) {
	if req == nil || strings.TrimSpace(req.Name) == "" {
		return nil, ErrInvalidInput
	}
	project, err := s.repo.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, ErrNotFound
	}
	if project.OwnerUserID != userID {
		return nil, ErrForbidden
	}
	now := time.Now().UTC()
	fields := map[string]interface{}{
		"name": strings.TrimSpace(req.Name), "description": req.Description,
		"sfdcOpptyId": req.SfdcOpptyID, "sfdcUrl": req.SfdcURL,
		"stage": req.Stage, "updatedAt": now,
	}
	if err := s.repo.UpdateProjectFields(ctx, projectID, fields); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	project.Name = strings.TrimSpace(req.Name)
	project.Description = req.Description
	project.SfdcOpptyID = req.SfdcOpptyID
	project.SfdcURL = req.SfdcURL
	project.Stage = req.Stage
	project.UpdatedAt = now
	members, err := s.repo.ListProjectMembers(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return projectResponse(project, members), nil
}

func (s *ProjectService) DeleteProject(ctx context.Context, userID, projectID string) error {
	project, err := s.repo.GetProject(ctx, projectID)
	if err != nil {
		return err
	}
	if project == nil {
		return ErrNotFound
	}
	if project.OwnerUserID != userID {
		return ErrForbidden
	}
	return s.repo.DeleteProject(ctx, projectID, project.OwnerUserID)
}

func (s *ProjectService) AddMember(ctx context.Context, requesterUserID, projectID string, req *model.AddProjectMemberRequest) (*model.ProjectMemberDTO, error) {
	project, err := s.repo.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, ErrNotFound
	}
	if project.OwnerUserID != requesterUserID {
		return nil, ErrForbidden
	}
	if req == nil || strings.TrimSpace(req.Email) == "" {
		return nil, ErrInvalidInput
	}
	user, err := s.repo.GetUserByEmail(ctx, strings.TrimSpace(req.Email))
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrUserNotFound
	}
	existing, err := s.repo.GetProjectMember(ctx, projectID, user.UserID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, ErrMemberExists
	}
	member := &model.ProjectMember{
		PK: model.PrefixProject + projectID, SK: model.PrefixProjectMember + user.UserID,
		ProjectID: projectID, UserID: user.UserID, Email: user.Email,
		AddedAt: time.Now().UTC(), GSI1PK: model.PrefixUser + user.UserID,
		GSI1SK: model.PrefixProject + projectID, EntityType: model.EntityTypeProjectMember,
	}
	if err := s.repo.PutProjectMember(ctx, member); err != nil {
		return nil, err
	}
	return &model.ProjectMemberDTO{UserID: user.UserID, Email: user.Email}, nil
}

func (s *ProjectService) putProjectRef(ctx context.Context, project *model.Project, accountID string) error {
	return s.repo.PutProjectRef(ctx, &model.ProjectRef{
		PK: model.PrefixAccount + accountID, SK: model.PrefixProjectRef + project.ProjectID,
		AccountID: accountID, ProjectID: project.ProjectID, OwnerUserID: project.OwnerUserID,
		Name: project.Name, CreatedAt: time.Now().UTC(), EntityType: model.EntityTypeProjectRef,
	})
}

func (s *ProjectService) LinkAccount(ctx context.Context, userID, projectID, accountID string) ([]string, error) {
	project, err := s.repo.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, ErrNotFound
	}
	if project.OwnerUserID != userID {
		return nil, ErrForbidden
	}
	member, err := s.repo.GetMember(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	if member == nil {
		return nil, ErrForbidden
	}
	if contains(project.AccountIDs, accountID) {
		// The canonical link may have succeeded while a prior reverse-index
		// write failed. Re-attempt the idempotent Put so retries self-heal.
		if err := s.putProjectRef(ctx, project, accountID); err != nil {
			return nil, fmt.Errorf("account link succeeded but the account's index write failed -- retry to heal: %w", err)
		}
		return append([]string{}, project.AccountIDs...), nil
	}
	if err := s.repo.AddProjectAccountLink(ctx, projectID, accountID); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	// Surface the reverse-index failure: the canonical ADD is committed and
	// a caller retry will take the already-linked self-healing path above.
	if err := s.putProjectRef(ctx, project, accountID); err != nil {
		return nil, fmt.Errorf("account link succeeded but the account's index write failed -- retry to heal: %w", err)
	}
	return append(append([]string{}, project.AccountIDs...), accountID), nil
}

func (s *ProjectService) UnlinkAccount(ctx context.Context, userID, projectID, accountID string) ([]string, error) {
	project, err := s.repo.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, ErrNotFound
	}
	if project.OwnerUserID != userID {
		return nil, ErrForbidden
	}
	member, err := s.repo.GetMember(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	if member == nil {
		return nil, ErrForbidden
	}
	if err := s.repo.RemoveProjectAccountLink(ctx, projectID, accountID); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := s.repo.DeleteProjectRef(ctx, accountID, projectID); err != nil {
		log.Printf("warn: failed to delete project ref %s/%s: %v", accountID, projectID, err)
	}
	remaining := make([]string, 0, len(project.AccountIDs))
	for _, id := range project.AccountIDs {
		if id != accountID {
			remaining = append(remaining, id)
		}
	}
	return remaining, nil
}

func projectMeetingRefSK(meeting *model.Meeting) string {
	return model.PrefixProjectMeetingRef + meeting.Date.UTC().Format(time.RFC3339) + "#" + meeting.MeetingID
}

func (s *ProjectService) putProjectMeetingRef(ctx context.Context, projectID, ownerUserID string, meeting *model.Meeting) error {
	return s.repo.PutProjectMeetingRef(ctx, &model.ProjectMeetingRef{
		PK: model.PrefixProject + projectID, SK: projectMeetingRefSK(meeting),
		ProjectID: projectID, MeetingID: meeting.MeetingID, OwnerUserID: ownerUserID,
		Title: meeting.Title, Date: meeting.Date, EntityType: model.EntityTypeProjectMeetingRef,
	})
}

func (s *ProjectService) LinkMeeting(ctx context.Context, userID, projectID, meetingID string) error {
	meeting, err := s.repo.GetMeeting(ctx, userID, meetingID)
	if err != nil {
		return err
	}
	if meeting == nil {
		return ErrNotFound
	}
	if _, err := s.requireProjectAccess(ctx, userID, projectID); err != nil {
		return err
	}
	if contains(meeting.ProjectIDs, projectID) {
		return s.putProjectMeetingRef(ctx, projectID, userID, meeting)
	}
	if err := s.repo.AddMeetingProjectLink(ctx, userID, meetingID, projectID); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return ErrNotFound
		}
		return err
	}
	// Surface ref failures so a retry can heal through the idempotent path.
	return s.putProjectMeetingRef(ctx, projectID, userID, meeting)
}

func (s *ProjectService) UnlinkMeeting(ctx context.Context, userID, projectID, meetingID string) error {
	meeting, err := s.repo.GetMeeting(ctx, userID, meetingID)
	if err != nil {
		return err
	}
	if meeting == nil {
		return ErrNotFound
	}
	if _, err := s.requireProjectAccess(ctx, userID, projectID); err != nil {
		return err
	}
	if err := s.repo.RemoveMeetingProjectLink(ctx, userID, meetingID, projectID); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return ErrNotFound
		}
		return err
	}
	if err := s.repo.DeleteProjectMeetingRef(ctx, projectID, projectMeetingRefSK(meeting)); err != nil {
		log.Printf("warn: failed to delete project meeting ref %s/%s: %v", projectID, meetingID, err)
	}
	return nil
}

func (s *ProjectService) putProjectResearchRef(ctx context.Context, projectID string, research *model.Research) error {
	return s.repo.PutProjectResearchRef(ctx, &model.ProjectResearchRef{
		PK: model.PrefixProject + projectID, SK: model.PrefixProjectResearchRef + research.ResearchID,
		ProjectID: projectID, ResearchID: research.ResearchID, OwnerUserID: research.UserID,
		Topic: research.Topic, CreatedAt: time.Now().UTC(), EntityType: model.EntityTypeProjectResearchRef,
	})
}

func (s *ProjectService) LinkResearch(ctx context.Context, userID, projectID, researchID string) error {
	research, err := s.repo.GetResearchByID(ctx, researchID)
	if err != nil {
		return err
	}
	if research == nil {
		return ErrNotFound
	}
	if research.UserID != userID {
		return ErrForbidden
	}
	if _, err := s.requireProjectAccess(ctx, userID, projectID); err != nil {
		return err
	}
	if contains(research.ProjectIDs, projectID) {
		return s.putProjectResearchRef(ctx, projectID, research)
	}
	if err := s.repo.AddResearchProjectLink(ctx, researchID, projectID); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return ErrNotFound
		}
		return err
	}
	return s.putProjectResearchRef(ctx, projectID, research)
}

func (s *ProjectService) UnlinkResearch(ctx context.Context, userID, projectID, researchID string) error {
	research, err := s.repo.GetResearchByID(ctx, researchID)
	if err != nil {
		return err
	}
	if research == nil {
		return ErrNotFound
	}
	if research.UserID != userID {
		return ErrForbidden
	}
	if _, err := s.requireProjectAccess(ctx, userID, projectID); err != nil {
		return err
	}
	if err := s.repo.RemoveResearchProjectLink(ctx, researchID, projectID); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return ErrNotFound
		}
		return err
	}
	if err := s.repo.DeleteProjectResearchRef(ctx, projectID, researchID); err != nil {
		log.Printf("warn: failed to delete project research ref %s/%s: %v", projectID, researchID, err)
	}
	return nil
}

func (s *ProjectService) projectMeetings(ctx context.Context, projectID string) ([]model.ProjectMeetingRef, map[string]*model.Meeting, error) {
	refs, err := s.repo.ListProjectMeetingRefsForProject(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	if len(refs) == 0 {
		return refs, map[string]*model.Meeting{}, nil
	}
	keys := make([]repository.MeetingKey, len(refs))
	for i, ref := range refs {
		keys[i] = repository.MeetingKey{OwnerID: ref.OwnerUserID, MeetingID: ref.MeetingID}
	}
	meetings, err := s.repo.BatchGetMeetings(ctx, keys)
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[string]*model.Meeting, len(meetings))
	for _, meeting := range meetings {
		if meeting != nil {
			byID[meeting.MeetingID] = meeting
		}
	}
	return refs, byID, nil
}

func (s *ProjectService) ListProjectMeetings(ctx context.Context, userID, projectID string) ([]model.ProjectMeetingRefDTO, error) {
	if _, err := s.requireProjectAccess(ctx, userID, projectID); err != nil {
		return nil, err
	}
	refs, byID, err := s.projectMeetings(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]model.ProjectMeetingRefDTO, 0, len(refs))
	for _, ref := range refs {
		meeting, ok := byID[ref.MeetingID]
		// Refs are candidates only. Re-check the canonical string set and
		// fail closed if an unlink left a stale reverse-index item behind.
		if !ok || !contains(meeting.ProjectIDs, projectID) {
			continue
		}
		out = append(out, model.ProjectMeetingRefDTO{
			MeetingID: meeting.MeetingID, OwnerUserID: ref.OwnerUserID,
			Title: meeting.Title, Date: meeting.Date,
		})
	}
	return out, nil
}

func (s *ProjectService) ListProjectResearch(ctx context.Context, userID, projectID string) ([]model.ProjectResearchDTO, error) {
	if _, err := s.requireProjectAccess(ctx, userID, projectID); err != nil {
		return nil, err
	}
	refs, err := s.repo.ListProjectResearchRefsForProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return []model.ProjectResearchDTO{}, nil
	}
	ids := make([]string, len(refs))
	for i, ref := range refs {
		ids[i] = ref.ResearchID
	}
	items, err := s.repo.BatchGetResearchByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]model.Research, len(items))
	for _, research := range items {
		byID[research.ResearchID] = research
	}
	out := make([]model.ProjectResearchDTO, 0, len(items))
	for _, id := range ids {
		research, ok := byID[id]
		// Refs are candidates only; canonical ProjectIDs is authoritative.
		if !ok || research.TrashedAt != "" || !contains(research.ProjectIDs, projectID) {
			continue
		}
		out = append(out, model.ProjectResearchDTO{
			ResearchID: research.ResearchID, Topic: research.Topic,
			Summary: truncateRunes(research.Summary, summaryPreviewMaxLen), Status: research.Status,
			OwnerUserID: research.UserID, CreatedAt: research.CreatedAt,
		})
	}
	return out, nil
}

func (s *ProjectService) GetProjectInsights(ctx context.Context, userID, projectID string, from, to time.Time, types []string) ([]model.ProjectInsightDTO, error) {
	if _, err := s.requireProjectAccess(ctx, userID, projectID); err != nil {
		return nil, err
	}
	refs, byID, err := s.projectMeetings(ctx, projectID)
	if err != nil {
		return nil, err
	}
	typeSet := make(map[string]bool, len(types))
	for _, insightType := range types {
		typeSet[insightType] = true
	}
	out := make([]model.ProjectInsightDTO, 0)
	for _, ref := range refs {
		meeting, ok := byID[ref.MeetingID]
		if !ok || !contains(meeting.ProjectIDs, projectID) {
			continue
		}
		if !from.IsZero() && meeting.Date.Before(from) {
			continue
		}
		if !to.IsZero() && meeting.Date.After(to) {
			continue
		}
		if strings.TrimSpace(meeting.Insights) == "" {
			continue
		}
		var parsed []model.MeetingInsight
		if err := json.Unmarshal([]byte(meeting.Insights), &parsed); err != nil {
			log.Printf("warn: failed to parse insights for project meeting %s: %v", meeting.MeetingID, err)
			continue
		}
		for _, insight := range parsed {
			if len(typeSet) > 0 && !typeSet[insight.Type] {
				continue
			}
			out = append(out, model.ProjectInsightDTO{
				Type: insight.Type, Text: insight.Text, SourceID: meeting.MeetingID,
				OccurredAt: meeting.Date, TsMarker: insight.TsMarker, Entities: insight.Entities,
			})
		}
	}
	return out, nil
}

func (s *ProjectService) GetProjectBrief(ctx context.Context, userID, projectID string, from, to time.Time, types []string) (*model.ProjectBrief, error) {
	project, err := s.GetProject(ctx, userID, projectID)
	if err != nil {
		return nil, err
	}
	meetings, err := s.ListProjectMeetings(ctx, userID, projectID)
	if err != nil {
		return nil, err
	}
	research, err := s.ListProjectResearch(ctx, userID, projectID)
	if err != nil {
		return nil, err
	}
	insights, err := s.GetProjectInsights(ctx, userID, projectID, from, to, types)
	if err != nil {
		return nil, err
	}
	byType := make(map[string][]model.ProjectInsightDTO)
	for _, insight := range insights {
		byType[insight.Type] = append(byType[insight.Type], insight)
	}
	return &model.ProjectBrief{Project: project, Meetings: meetings, Research: research, InsightsByType: byType}, nil
}

func (s *ProjectService) ListAccountProjects(ctx context.Context, userID, accountID string) ([]model.ProjectSummary, error) {
	member, err := s.repo.GetMember(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	if member == nil {
		return nil, ErrForbidden
	}
	refs, err := s.repo.ListProjectRefsForAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]model.ProjectSummary, 0, len(refs))
	for _, ref := range refs {
		project, err := s.repo.GetProject(ctx, ref.ProjectID)
		if err != nil {
			return nil, err
		}
		// Never trust a reverse-index ref without checking the canonical set.
		if project == nil || !contains(project.AccountIDs, accountID) {
			continue
		}
		out = append(out, model.ProjectSummary{ProjectID: project.ProjectID, Name: project.Name, Stage: project.Stage, SfdcOpptyID: project.SfdcOpptyID})
	}
	return out, nil
}
