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

// ErrProjectHasLinks is returned by DeleteProject when the project still has
// linked accounts, meetings, research, or direct members. DeleteProject's
// repository call only removes the CONFIG + owner-index items -- it does
// NOT cascade-delete MEMBER# rows, the account/meeting/research reverse
// refs, or the projectId entries in linked meetings'/research's ProjectIDs
// sets, and no unlink API can reach a deleted project's own relations
// afterward (requireProjectAccess would return ErrNotFound first). Rejecting
// deletion while any relation exists avoids orphaning that data outright,
// rather than accepting silent, permanently-unreachable garbage.
var ErrProjectHasLinks = errors.New("project still has linked accounts, meetings, research, or members")

// projectRepo is the narrow persistence seam used by ProjectService.
type projectRepo interface {
	CreateProject(context.Context, *model.Project) error
	GetProject(context.Context, string) (*model.Project, error)
	GetProjectMember(context.Context, string, string) (*model.ProjectMember, error)
	PutProjectMember(context.Context, *model.ProjectMember) error
	DeleteProjectMember(context.Context, string, string) error
	ListProjectMembers(context.Context, string) ([]model.ProjectMember, error)
	ListProjectsForUser(context.Context, string) ([]model.Project, error)
	UpdateProjectFields(context.Context, string, map[string]interface{}) error
	DeleteProject(context.Context, string, string) error
	ProjectAccountLinkTransactional(context.Context, string, string, *model.ProjectRef) error
	ProjectAccountUnlinkTransactional(context.Context, string, string) error
	MeetingProjectLinkTransactional(context.Context, string, string, string, *model.ProjectMeetingRef) error
	MeetingProjectUnlinkTransactional(context.Context, string, string, string, string) error
	ResearchProjectLinkTransactional(context.Context, string, string, *model.ProjectResearchRef) error
	ResearchProjectUnlinkTransactional(context.Context, string, string) error
	PutProjectRef(context.Context, *model.ProjectRef) error
	ListProjectRefsForAccount(context.Context, string) ([]model.ProjectRef, error)
	PutProjectMeetingRef(context.Context, *model.ProjectMeetingRef) error
	ListProjectMeetingRefsForProject(context.Context, string) ([]model.ProjectMeetingRef, error)
	PutProjectResearchRef(context.Context, *model.ProjectResearchRef) error
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

func (s *ProjectService) CreateProject(ctx context.Context, ownerUserID, ownerEmail string, req *model.CreateProjectRequest) (*model.ProjectResponse, error) {
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
	// Wrap in the same camelCase ProjectResponse contract GetProject/UpdateProject
	// return -- the raw *model.Project only carries dynamodbav tags, so
	// serializing it directly would leak PK/SK/EntityType and break the
	// client's response shape immediately after creation.
	return projectResponse(project, nil), nil
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
	if len(project.AccountIDs) > 0 {
		return ErrProjectHasLinks
	}
	members, err := s.repo.ListProjectMembers(ctx, projectID)
	if err != nil {
		return err
	}
	if len(members) > 0 {
		return ErrProjectHasLinks
	}
	// Canonical-reverified counts, not raw ref counts: a MEETINGREF#/
	// RESEARCHREF# item can outlive the meeting/research it points at (e.g.
	// the meeting is deleted through the ordinary, unrelated meeting-delete
	// path, which knows nothing about project refs) and sit there forever
	// as an orphan -- a raw len(refs) > 0 check would then block deletion
	// of a project with no *actually* linked meetings/research left,
	// permanently. ListProjectMeetings/ListProjectResearch already do this
	// fail-closed re-verification (and MeetingID/ResearchID dedup); reuse
	// them here instead of re-deriving the same logic.
	meetings, err := s.ListProjectMeetings(ctx, userID, projectID)
	if err != nil {
		return err
	}
	if len(meetings) > 0 {
		return ErrProjectHasLinks
	}
	// NOT ListProjectResearch: that function filters out trashed research
	// (correct for a user-facing list -- nobody wants trashed items showing
	// up), but that same filter breaks the deletion guard's invariant. A
	// research trashed WHILE still linked would fail this check as if it
	// weren't linked, letting DeleteProject through; the trashed research's
	// canonical ProjectIDs still contains this project's id, and restoring
	// it (TrashResearch is reversible) resurrects an orphan link this
	// project's ID can never be reached to unlink again (GetProject 404s).
	// hasLinkedResearch below is the same canonical-reverification logic
	// minus the TrashedAt filter, precisely because this check cares about
	// "is the link still real," not "should this show up in a list."
	hasResearch, err := s.hasLinkedResearch(ctx, projectID)
	if err != nil {
		return err
	}
	if hasResearch {
		return ErrProjectHasLinks
	}
	if err := s.repo.DeleteProject(ctx, projectID, project.OwnerUserID); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			// The condition on the CONFIG delete (no accountIds) failed --
			// a LinkAccount committed between this function's guard reads
			// above and this delete. Same conclusion the guard would have
			// reached had it re-read a moment later: still linked.
			return ErrProjectHasLinks
		}
		return err
	}
	return nil
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

// RemoveMember revokes a direct membership (owner only). Without this, a
// member added via AddMember could never be removed short of deleting the
// whole project -- a real access-revocation gap, since members can read
// every linked meeting's insights and linked research for as long as their
// membership row exists.
func (s *ProjectService) RemoveMember(ctx context.Context, requesterUserID, projectID, targetUserID string) error {
	project, err := s.repo.GetProject(ctx, projectID)
	if err != nil {
		return err
	}
	if project == nil {
		return ErrNotFound
	}
	if project.OwnerUserID != requesterUserID {
		return ErrForbidden
	}
	return s.repo.DeleteProjectMember(ctx, projectID, targetUserID)
}

func (s *ProjectService) putProjectRef(ctx context.Context, project *model.Project, accountID string) error {
	return s.repo.PutProjectRef(ctx, &model.ProjectRef{
		PK: model.PrefixAccount + accountID, SK: model.PrefixProjectRef + project.ProjectID,
		AccountID: accountID, ProjectID: project.ProjectID, OwnerUserID: project.OwnerUserID,
		Name: project.Name, CreatedAt: time.Now().UTC(), EntityType: model.EntityTypeProjectRef,
	})
}

// LinkAccount links a project (owner only) to an account the caller is a
// member of. Idempotent: linking an already-linked account is a no-op.
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
		// Already linked in the canonical record, but re-attempt the ref
		// write anyway: a *previous* LinkAccount call could have committed
		// the transactional link while this project was on an older,
		// non-transactional code path -- this branch heals any pre-existing
		// gap without requiring a data migration.
		if err := s.putProjectRef(ctx, project, accountID); err != nil {
			return nil, fmt.Errorf("account link succeeded but the account's index write failed -- retry to heal: %w", err)
		}
		return append([]string{}, project.AccountIDs...), nil
	}
	// Atomic ADD on accountIds + PROJECTREF# Put, in ONE TransactWriteItems
	// call -- two separate requests here could interleave with a concurrent
	// Link/Unlink of the SAME (project, account) pair and land with the
	// canonical set linked but the ref deleted, permanently hiding a
	// genuinely-linked project from ListAccountProjects (mirrors
	// ResearchService.LinkAccount / AGENTS.md's documented convention).
	ref := &model.ProjectRef{
		PK: model.PrefixAccount + accountID, SK: model.PrefixProjectRef + projectID,
		AccountID: accountID, ProjectID: projectID, OwnerUserID: project.OwnerUserID,
		Name: project.Name, CreatedAt: time.Now().UTC(), EntityType: model.EntityTypeProjectRef,
	}
	if err := s.repo.ProjectAccountLinkTransactional(ctx, projectID, accountID, ref); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to link account: %w", err)
	}
	return append(append([]string{}, project.AccountIDs...), accountID), nil
}

// UnlinkAccount removes a project↔account link (project owner only). Unlike
// LinkAccount, this does NOT require the caller to currently be a member of
// accountID: requiring it created a revocation deadlock -- if the owner is
// later removed from that account, GetMember would return nil and nobody
// could ever unlink it again, while every remaining member of that account
// keeps indefinite access to the project's meetings/research/insights via
// requireProjectAccess's account-inheritance path. Unlinking an existing
// link should only need proof of project ownership, the same authority that
// created the link in the first place.
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
	// Atomic DELETE on accountIds + ref delete, in ONE TransactWriteItems
	// call -- see LinkAccount's comment for why the two writes must land
	// together, not as separate requests.
	if err := s.repo.ProjectAccountUnlinkTransactional(ctx, projectID, accountID); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return nil, ErrNotFound
		}
		return nil, err
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

// existingProjectMeetingRef finds an already-written MEETINGREF# item for
// meetingID, if one exists, returning its SK and the meeting owner's userID
// recorded on the ref (needed by UnlinkMeeting to target the right Meeting
// item -- Meeting's PK includes its owner's userID, which the caller of
// Unlink is not necessarily). The SK embeds meeting.Date at link time;
// recomputing it from the meeting's CURRENT (mutable) Date instead of
// looking this up would target a different item than the one actually
// written whenever Date changed since the link, and re-Put a second ref
// rather than refreshing the original -- duplicating this meeting in every
// ref-driven read (ListProjectMeetings, GetProjectInsights, GetProjectBrief).
func (s *ProjectService) existingProjectMeetingRef(ctx context.Context, projectID, meetingID string) (sk, ownerUserID string, err error) {
	refs, err := s.repo.ListProjectMeetingRefsForProject(ctx, projectID)
	if err != nil {
		return "", "", err
	}
	for _, ref := range refs {
		if ref.MeetingID == meetingID {
			return ref.SK, ref.OwnerUserID, nil
		}
	}
	return "", "", nil
}

func (s *ProjectService) putProjectMeetingRef(ctx context.Context, projectID, ownerUserID string, meeting *model.Meeting) error {
	sk, _, err := s.existingProjectMeetingRef(ctx, projectID, meeting.MeetingID)
	if err != nil {
		return err
	}
	if sk == "" {
		sk = projectMeetingRefSK(meeting)
	}
	return s.repo.PutProjectMeetingRef(ctx, &model.ProjectMeetingRef{
		PK: model.PrefixProject + projectID, SK: sk,
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
	// Atomic ADD on meeting.projectIds + MEETINGREF# Put, in ONE
	// TransactWriteItems call -- same interleaving hazard as
	// LinkAccount/UnlinkAccount above.
	ref := &model.ProjectMeetingRef{
		PK: model.PrefixProject + projectID, SK: projectMeetingRefSK(meeting),
		ProjectID: projectID, MeetingID: meeting.MeetingID, OwnerUserID: userID,
		Title: meeting.Title, Date: meeting.Date, EntityType: model.EntityTypeProjectMeetingRef,
	}
	if err := s.repo.MeetingProjectLinkTransactional(ctx, userID, meetingID, projectID, ref); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func (s *ProjectService) UnlinkMeeting(ctx context.Context, userID, projectID, meetingID string) error {
	// Deliberately NOT requireProjectAccess here (see below): that call
	// would deny a former member who lost project access via RemoveMember
	// but still owns the meeting they linked, recreating the exact deadlock
	// this function exists to avoid -- just in the other direction (the
	// resource owner, instead of the project owner, unable to unlink).
	project, err := s.repo.GetProject(ctx, projectID)
	if err != nil {
		return err
	}
	if project == nil {
		return ErrNotFound
	}
	// Look up the ref's ACTUAL existing SK and owner (see
	// existingProjectMeetingRef's comment) rather than recomputing the SK
	// from the meeting's current Date.
	refSK, ownerUserID, err := s.existingProjectMeetingRef(ctx, projectID, meetingID)
	if err != nil {
		return err
	}
	if refSK == "" {
		// No ref exists (already unlinked, or the meeting itself was deleted
		// out-of-band -- meeting deletion doesn't know about project refs --
		// leaving nothing here to clean up). Idempotent no-op rather than an
		// error the caller can't do anything about.
		return nil
	}
	// Authorized if EITHER: the caller owns the linked meeting (regardless
	// of their current project access -- a resource owner can always
	// revoke their own contribution), OR the caller owns the project
	// (regardless of whether they ever owned this particular meeting --
	// see LinkMeeting's comment on the mirror-image deadlock this avoids).
	// Neither check goes through requireProjectAccess's broader
	// direct-member/account-inherited-member allowance: an ordinary
	// project member with no stake in this specific link cannot unlink it.
	if ownerUserID != userID && project.OwnerUserID != userID {
		return ErrForbidden
	}
	if err := s.repo.MeetingProjectUnlinkTransactional(ctx, ownerUserID, meetingID, projectID, refSK); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return ErrNotFound
		}
		return err
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
	// Atomic ADD on research.projectIds + RESEARCHREF# Put, in ONE
	// TransactWriteItems call -- same interleaving hazard as the account and
	// meeting variants above.
	ref := &model.ProjectResearchRef{
		PK: model.PrefixProject + projectID, SK: model.PrefixProjectResearchRef + research.ResearchID,
		ProjectID: projectID, ResearchID: research.ResearchID, OwnerUserID: research.UserID,
		Topic: research.Topic, CreatedAt: time.Now().UTC(), EntityType: model.EntityTypeProjectResearchRef,
	}
	if err := s.repo.ResearchProjectLinkTransactional(ctx, researchID, projectID, ref); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func (s *ProjectService) UnlinkResearch(ctx context.Context, userID, projectID, researchID string) error {
	research, err := s.repo.GetResearchByID(ctx, researchID)
	if err != nil {
		return err
	}
	if research == nil {
		return ErrNotFound
	}
	// Deliberately GetProject, not requireProjectAccess -- see UnlinkMeeting's
	// comment. Authorized if EITHER the caller owns the research (regardless
	// of current project access) OR the caller owns the project (regardless
	// of research ownership); an ordinary project member with no stake in
	// this specific link cannot unlink it.
	project, err := s.repo.GetProject(ctx, projectID)
	if err != nil {
		return err
	}
	if project == nil {
		return ErrNotFound
	}
	if research.UserID != userID && project.OwnerUserID != userID {
		return ErrForbidden
	}
	if err := s.repo.ResearchProjectUnlinkTransactional(ctx, researchID, projectID); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return ErrNotFound
		}
		return err
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
	seen := make(map[string]bool, len(refs))
	out := make([]model.ProjectMeetingRefDTO, 0, len(refs))
	for _, ref := range refs {
		meeting, ok := byID[ref.MeetingID]
		// Refs are candidates only. Re-check the canonical string set and
		// fail closed if an unlink left a stale reverse-index item behind.
		// Also dedup by MeetingID -- defense-in-depth against a duplicate ref
		// (e.g. two MEETINGREF# items for the same meeting at different SKs,
		// which existingProjectMeetingRefSK now prevents going forward, but
		// this guard is cheap and catches it regardless of cause.
		if !ok || !contains(meeting.ProjectIDs, projectID) || seen[meeting.MeetingID] {
			continue
		}
		seen[meeting.MeetingID] = true
		out = append(out, model.ProjectMeetingRefDTO{
			MeetingID: meeting.MeetingID, OwnerUserID: ref.OwnerUserID,
			Title: meeting.Title, Date: meeting.Date,
		})
	}
	return out, nil
}

// hasLinkedResearch reports whether any research is canonically still
// linked to projectID -- the same reverification ListProjectResearch does
// (refs are candidates only; canonical ProjectIDs is authoritative),
// deliberately WITHOUT filtering out trashed research. See DeleteProject's
// comment: a research trashed while linked is still linked (TrashResearch
// is reversible), so the deletion guard must not treat it as unlinked.
func (s *ProjectService) hasLinkedResearch(ctx context.Context, projectID string) (bool, error) {
	refs, err := s.repo.ListProjectResearchRefsForProject(ctx, projectID)
	if err != nil {
		return false, err
	}
	if len(refs) == 0 {
		return false, nil
	}
	ids := make([]string, len(refs))
	for i, ref := range refs {
		ids[i] = ref.ResearchID
	}
	items, err := s.repo.BatchGetResearchByIDs(ctx, ids)
	if err != nil {
		return false, err
	}
	for _, research := range items {
		if contains(research.ProjectIDs, projectID) {
			return true, nil
		}
	}
	return false, nil
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
	seen := make(map[string]bool, len(refs))
	out := make([]model.ProjectInsightDTO, 0)
	for _, ref := range refs {
		meeting, ok := byID[ref.MeetingID]
		// Dedup by MeetingID -- see ListProjectMeetings' comment. Without
		// this, a duplicate ref would double-count every insight from that
		// meeting, not just list the meeting twice.
		if !ok || !contains(meeting.ProjectIDs, projectID) || seen[meeting.MeetingID] {
			continue
		}
		seen[meeting.MeetingID] = true
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
