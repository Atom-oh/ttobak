package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type mockProjectRepo struct {
	projects             map[string]*model.Project
	projectMembers       map[string]*model.ProjectMember
	accountMembers       map[string]*model.AccountMember
	accounts             map[string]*model.Account
	users                map[string]*model.User
	meetings             map[string]*model.Meeting
	research             map[string]*model.Research
	projectRefs          map[string][]model.ProjectRef
	meetingRefs          map[string][]model.ProjectMeetingRef
	researchRefs         map[string][]model.ProjectResearchRef
	deleteAfterGet       string
	failPutRefFor        string
	failDeleteProjectRef bool
}

func newMockProjectRepo() *mockProjectRepo {
	return &mockProjectRepo{
		projects: make(map[string]*model.Project), projectMembers: make(map[string]*model.ProjectMember),
		accountMembers: make(map[string]*model.AccountMember), accounts: make(map[string]*model.Account),
		users: make(map[string]*model.User), meetings: make(map[string]*model.Meeting),
		research: make(map[string]*model.Research), projectRefs: make(map[string][]model.ProjectRef),
		meetingRefs: make(map[string][]model.ProjectMeetingRef), researchRefs: make(map[string][]model.ProjectResearchRef),
	}
}

func projectMemberKey(projectID, userID string) string        { return projectID + "|" + userID }
func projectAccountMemberKey(accountID, userID string) string { return accountID + "|" + userID }
func projectMeetingKey(ownerID, meetingID string) string      { return ownerID + "|" + meetingID }

func cloneProject(p *model.Project) *model.Project {
	if p == nil {
		return nil
	}
	cp := *p
	cp.AccountIDs = append([]string(nil), p.AccountIDs...)
	return &cp
}
func cloneProjectMeeting(m *model.Meeting) *model.Meeting {
	if m == nil {
		return nil
	}
	cp := *m
	cp.ProjectIDs = append([]string(nil), m.ProjectIDs...)
	return &cp
}
func cloneProjectResearch(r *model.Research) *model.Research {
	if r == nil {
		return nil
	}
	cp := *r
	cp.ProjectIDs = append([]string(nil), r.ProjectIDs...)
	return &cp
}

func (m *mockProjectRepo) CreateProject(_ context.Context, p *model.Project) error {
	m.projects[p.ProjectID] = cloneProject(p)
	return nil
}
func (m *mockProjectRepo) GetProject(_ context.Context, id string) (*model.Project, error) {
	p := cloneProject(m.projects[id])
	if p != nil && m.deleteAfterGet == id {
		delete(m.projects, id)
		m.deleteAfterGet = ""
	}
	return p, nil
}
func (m *mockProjectRepo) GetProjectMember(_ context.Context, projectID, userID string) (*model.ProjectMember, error) {
	member := m.projectMembers[projectMemberKey(projectID, userID)]
	if member == nil {
		return nil, nil
	}
	cp := *member
	return &cp, nil
}
func (m *mockProjectRepo) PutProjectMember(_ context.Context, member *model.ProjectMember) error {
	cp := *member
	m.projectMembers[projectMemberKey(member.ProjectID, member.UserID)] = &cp
	return nil
}
func (m *mockProjectRepo) ListProjectMembers(_ context.Context, projectID string) ([]model.ProjectMember, error) {
	out := []model.ProjectMember{}
	for _, member := range m.projectMembers {
		if member.ProjectID == projectID {
			out = append(out, *member)
		}
	}
	return out, nil
}
func (m *mockProjectRepo) ListProjectsForUser(_ context.Context, userID string) ([]model.Project, error) {
	out := []model.Project{}
	for _, p := range m.projects {
		if p.OwnerUserID == userID || m.projectMembers[projectMemberKey(p.ProjectID, userID)] != nil {
			out = append(out, *cloneProject(p))
		}
	}
	return out, nil
}
func (m *mockProjectRepo) UpdateProjectFields(_ context.Context, id string, fields map[string]interface{}) error {
	p := m.projects[id]
	if p == nil {
		return fmt.Errorf("%w: project missing", repository.ErrConditionFailed)
	}
	if v, ok := fields["name"]; ok {
		p.Name = v.(string)
	}
	if v, ok := fields["description"]; ok {
		p.Description = v.(string)
	}
	if v, ok := fields["sfdcOpptyId"]; ok {
		p.SfdcOpptyID = v.(string)
	}
	if v, ok := fields["sfdcUrl"]; ok {
		p.SfdcURL = v.(string)
	}
	if v, ok := fields["stage"]; ok {
		p.Stage = v.(string)
	}
	if v, ok := fields["updatedAt"]; ok {
		p.UpdatedAt = v.(time.Time)
	}
	return nil
}
func (m *mockProjectRepo) DeleteProject(_ context.Context, id, _ string) error {
	delete(m.projects, id)
	return nil
}
func (m *mockProjectRepo) AddProjectAccountLink(_ context.Context, projectID, accountID string) error {
	p := m.projects[projectID]
	if p == nil {
		return fmt.Errorf("%w: project missing", repository.ErrConditionFailed)
	}
	if !contains(p.AccountIDs, accountID) {
		p.AccountIDs = append(p.AccountIDs, accountID)
	}
	return nil
}
func (m *mockProjectRepo) RemoveProjectAccountLink(_ context.Context, projectID, accountID string) error {
	p := m.projects[projectID]
	if p == nil {
		return fmt.Errorf("%w: project missing", repository.ErrConditionFailed)
	}
	out := make([]string, 0, len(p.AccountIDs))
	for _, id := range p.AccountIDs {
		if id != accountID {
			out = append(out, id)
		}
	}
	p.AccountIDs = out
	return nil
}
func (m *mockProjectRepo) AddMeetingProjectLink(_ context.Context, ownerID, meetingID, projectID string) error {
	meeting := m.meetings[projectMeetingKey(ownerID, meetingID)]
	if meeting == nil {
		return fmt.Errorf("%w: meeting missing", repository.ErrConditionFailed)
	}
	if !contains(meeting.ProjectIDs, projectID) {
		meeting.ProjectIDs = append(meeting.ProjectIDs, projectID)
	}
	return nil
}
func (m *mockProjectRepo) RemoveMeetingProjectLink(_ context.Context, ownerID, meetingID, projectID string) error {
	meeting := m.meetings[projectMeetingKey(ownerID, meetingID)]
	if meeting == nil {
		return fmt.Errorf("%w: meeting missing", repository.ErrConditionFailed)
	}
	out := []string{}
	for _, id := range meeting.ProjectIDs {
		if id != projectID {
			out = append(out, id)
		}
	}
	meeting.ProjectIDs = out
	return nil
}
func (m *mockProjectRepo) AddResearchProjectLink(_ context.Context, researchID, projectID string) error {
	r := m.research[researchID]
	if r == nil {
		return fmt.Errorf("%w: research missing", repository.ErrConditionFailed)
	}
	if !contains(r.ProjectIDs, projectID) {
		r.ProjectIDs = append(r.ProjectIDs, projectID)
	}
	return nil
}
func (m *mockProjectRepo) RemoveResearchProjectLink(_ context.Context, researchID, projectID string) error {
	r := m.research[researchID]
	if r == nil {
		return fmt.Errorf("%w: research missing", repository.ErrConditionFailed)
	}
	out := []string{}
	for _, id := range r.ProjectIDs {
		if id != projectID {
			out = append(out, id)
		}
	}
	r.ProjectIDs = out
	return nil
}
func (m *mockProjectRepo) PutProjectRef(_ context.Context, ref *model.ProjectRef) error {
	if m.failPutRefFor == ref.AccountID {
		return errors.New("simulated project ref failure")
	}
	refs := m.projectRefs[ref.AccountID]
	for i := range refs {
		if refs[i].ProjectID == ref.ProjectID {
			refs[i] = *ref
			m.projectRefs[ref.AccountID] = refs
			return nil
		}
	}
	m.projectRefs[ref.AccountID] = append(refs, *ref)
	return nil
}
func (m *mockProjectRepo) DeleteProjectRef(_ context.Context, accountID, projectID string) error {
	if m.failDeleteProjectRef {
		return errors.New("simulated best-effort delete failure")
	}
	refs := m.projectRefs[accountID]
	out := refs[:0]
	for _, ref := range refs {
		if ref.ProjectID != projectID {
			out = append(out, ref)
		}
	}
	m.projectRefs[accountID] = out
	return nil
}
func (m *mockProjectRepo) ListProjectRefsForAccount(_ context.Context, accountID string) ([]model.ProjectRef, error) {
	return append([]model.ProjectRef(nil), m.projectRefs[accountID]...), nil
}
func (m *mockProjectRepo) PutProjectMeetingRef(_ context.Context, ref *model.ProjectMeetingRef) error {
	refs := m.meetingRefs[ref.ProjectID]
	for i := range refs {
		if refs[i].MeetingID == ref.MeetingID {
			refs[i] = *ref
			m.meetingRefs[ref.ProjectID] = refs
			return nil
		}
	}
	m.meetingRefs[ref.ProjectID] = append(refs, *ref)
	return nil
}
func (m *mockProjectRepo) DeleteProjectMeetingRef(_ context.Context, projectID, sk string) error {
	refs := m.meetingRefs[projectID]
	out := refs[:0]
	for _, ref := range refs {
		if ref.SK != sk {
			out = append(out, ref)
		}
	}
	m.meetingRefs[projectID] = out
	return nil
}
func (m *mockProjectRepo) ListProjectMeetingRefsForProject(_ context.Context, projectID string) ([]model.ProjectMeetingRef, error) {
	return append([]model.ProjectMeetingRef(nil), m.meetingRefs[projectID]...), nil
}
func (m *mockProjectRepo) PutProjectResearchRef(_ context.Context, ref *model.ProjectResearchRef) error {
	refs := m.researchRefs[ref.ProjectID]
	for i := range refs {
		if refs[i].ResearchID == ref.ResearchID {
			refs[i] = *ref
			m.researchRefs[ref.ProjectID] = refs
			return nil
		}
	}
	m.researchRefs[ref.ProjectID] = append(refs, *ref)
	return nil
}
func (m *mockProjectRepo) DeleteProjectResearchRef(_ context.Context, projectID, researchID string) error {
	refs := m.researchRefs[projectID]
	out := refs[:0]
	for _, ref := range refs {
		if ref.ResearchID != researchID {
			out = append(out, ref)
		}
	}
	m.researchRefs[projectID] = out
	return nil
}
func (m *mockProjectRepo) ListProjectResearchRefsForProject(_ context.Context, projectID string) ([]model.ProjectResearchRef, error) {
	return append([]model.ProjectResearchRef(nil), m.researchRefs[projectID]...), nil
}
func (m *mockProjectRepo) GetMember(_ context.Context, accountID, userID string) (*model.AccountMember, error) {
	member := m.accountMembers[projectAccountMemberKey(accountID, userID)]
	if member == nil {
		return nil, nil
	}
	cp := *member
	return &cp, nil
}
func (m *mockProjectRepo) GetAccount(_ context.Context, accountID string) (*model.Account, error) {
	account := m.accounts[accountID]
	if account == nil {
		return nil, nil
	}
	cp := *account
	return &cp, nil
}
func (m *mockProjectRepo) GetUserByEmail(_ context.Context, email string) (*model.User, error) {
	user := m.users[email]
	if user == nil {
		return nil, nil
	}
	cp := *user
	return &cp, nil
}
func (m *mockProjectRepo) GetMeeting(_ context.Context, ownerID, meetingID string) (*model.Meeting, error) {
	return cloneProjectMeeting(m.meetings[projectMeetingKey(ownerID, meetingID)]), nil
}
func (m *mockProjectRepo) GetResearchByID(_ context.Context, researchID string) (*model.Research, error) {
	return cloneProjectResearch(m.research[researchID]), nil
}
func (m *mockProjectRepo) BatchGetMeetings(_ context.Context, keys []repository.MeetingKey) ([]*model.Meeting, error) {
	out := []*model.Meeting{}
	for i := len(keys) - 1; i >= 0; i-- {
		if meeting := cloneProjectMeeting(m.meetings[projectMeetingKey(keys[i].OwnerID, keys[i].MeetingID)]); meeting != nil {
			out = append(out, meeting)
		}
	}
	return out, nil
}
func (m *mockProjectRepo) BatchGetResearchByIDs(_ context.Context, ids []string) ([]model.Research, error) {
	out := []model.Research{}
	for i := len(ids) - 1; i >= 0; i-- {
		if r := cloneProjectResearch(m.research[ids[i]]); r != nil {
			out = append(out, *r)
		}
	}
	return out, nil
}

func seedProject(repo *mockProjectRepo, id, owner string) *model.Project {
	p := &model.Project{ProjectID: id, Name: "Project " + id, OwnerUserID: owner, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	repo.projects[id] = p
	return p
}
func addProjectAccountMember(repo *mockProjectRepo, accountID, userID string) {
	repo.accountMembers[projectAccountMemberKey(accountID, userID)] = &model.AccountMember{AccountID: accountID, UserID: userID}
}

func TestCreateProject_SetsOwner(t *testing.T) {
	repo := newMockProjectRepo()
	project, err := newProjectServiceWithRepo(repo).CreateProject(context.Background(), "owner-1", "owner@example.com", &model.CreateProjectRequest{Name: "  Migration  "})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if project.ProjectID == "" || project.OwnerUserID != "owner-1" || project.Name != "Migration" {
		t.Fatalf("unexpected project: %+v", project)
	}
	if repo.projects[project.ProjectID] == nil || project.EntityType != model.EntityTypeProject {
		t.Fatalf("project not persisted correctly: %+v", project)
	}
}

func TestRequireProjectAccess(t *testing.T) {
	repo := newMockProjectRepo()
	project := seedProject(repo, "p1", "owner")
	repo.projectMembers[projectMemberKey("p1", "direct")] = &model.ProjectMember{ProjectID: "p1", UserID: "direct"}
	project.AccountIDs = []string{"a1"}
	addProjectAccountMember(repo, "a1", "inherited")
	svc := newProjectServiceWithRepo(repo)
	for _, userID := range []string{"owner", "direct", "inherited"} {
		if _, err := svc.requireProjectAccess(context.Background(), userID, "p1"); err != nil {
			t.Errorf("expected %s access, got %v", userID, err)
		}
	}
	if _, err := svc.requireProjectAccess(context.Background(), "stranger", "p1"); !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
	if _, err := svc.requireProjectAccess(context.Background(), "owner", "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestProjectLinkAccount_ReLinkHealsMissingRef(t *testing.T) {
	repo := newMockProjectRepo()
	seedProject(repo, "p1", "owner")
	addProjectAccountMember(repo, "a1", "owner")
	svc := newProjectServiceWithRepo(repo)
	if _, err := svc.LinkAccount(context.Background(), "owner", "p1", "a1"); err != nil {
		t.Fatal(err)
	}
	repo.projectRefs["a1"] = nil
	if _, err := svc.LinkAccount(context.Background(), "owner", "p1", "a1"); err != nil {
		t.Fatalf("re-link failed: %v", err)
	}
	if len(repo.projectRefs["a1"]) != 1 {
		t.Fatalf("expected healed ref, got %v", repo.projectRefs["a1"])
	}
}

func TestLinkAccount_RefFailureSurfacedAndRetryHeals(t *testing.T) {
	repo := newMockProjectRepo()
	seedProject(repo, "p1", "owner")
	addProjectAccountMember(repo, "a1", "owner")
	repo.failPutRefFor = "a1"
	svc := newProjectServiceWithRepo(repo)
	if _, err := svc.LinkAccount(context.Background(), "owner", "p1", "a1"); err == nil {
		t.Fatal("expected ref failure")
	}
	if !contains(repo.projects["p1"].AccountIDs, "a1") {
		t.Fatal("canonical ADD should remain committed")
	}
	repo.failPutRefFor = ""
	if _, err := svc.LinkAccount(context.Background(), "owner", "p1", "a1"); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if len(repo.projectRefs["a1"]) != 1 {
		t.Fatal("retry did not heal ref")
	}
}

func TestLinkAccount_ConcurrentDeleteNoZombie(t *testing.T) {
	repo := newMockProjectRepo()
	seedProject(repo, "p1", "owner")
	addProjectAccountMember(repo, "a1", "owner")
	repo.deleteAfterGet = "p1"
	_, err := newProjectServiceWithRepo(repo).LinkAccount(context.Background(), "owner", "p1", "a1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if repo.projects["p1"] != nil {
		t.Fatal("concurrent delete was resurrected")
	}
}

func TestUnlinkAccount_CanonicalRemovalToleratesRefDeleteFailure(t *testing.T) {
	repo := newMockProjectRepo()
	p := seedProject(repo, "p1", "owner")
	p.AccountIDs = []string{"a1"}
	addProjectAccountMember(repo, "a1", "owner")
	repo.projectRefs["a1"] = []model.ProjectRef{{ProjectID: "p1", AccountID: "a1"}}
	repo.failDeleteProjectRef = true
	ids, err := newProjectServiceWithRepo(repo).UnlinkAccount(context.Background(), "owner", "p1", "a1")
	if err != nil {
		t.Fatalf("best-effort ref delete should not fail unlink: %v", err)
	}
	if len(ids) != 0 || contains(repo.projects["p1"].AccountIDs, "a1") {
		t.Fatalf("canonical link remains: %v", repo.projects["p1"].AccountIDs)
	}
}

func TestProjectLists_ExcludeStaleCanonicalLinks(t *testing.T) {
	repo := newMockProjectRepo()
	seedProject(repo, "p1", "owner")
	repo.meetings[projectMeetingKey("owner", "m1")] = &model.Meeting{MeetingID: "m1", UserID: "owner"}
	repo.meetingRefs["p1"] = []model.ProjectMeetingRef{{ProjectID: "p1", MeetingID: "m1", OwnerUserID: "owner"}}
	repo.research["r1"] = &model.Research{ResearchID: "r1", UserID: "owner"}
	repo.researchRefs["p1"] = []model.ProjectResearchRef{{ProjectID: "p1", ResearchID: "r1"}}
	svc := newProjectServiceWithRepo(repo)
	meetings, err := svc.ListProjectMeetings(context.Background(), "owner", "p1")
	if err != nil || len(meetings) != 0 {
		t.Fatalf("expected stale meeting excluded, got %v err=%v", meetings, err)
	}
	research, err := svc.ListProjectResearch(context.Background(), "owner", "p1")
	if err != nil || len(research) != 0 {
		t.Fatalf("expected stale research excluded, got %v err=%v", research, err)
	}
}

func TestListProjectMeetings_PreservesRefOrder(t *testing.T) {
	repo := newMockProjectRepo()
	seedProject(repo, "p1", "owner")
	for _, id := range []string{"m1", "m2", "m3"} {
		repo.meetings[projectMeetingKey("owner", id)] = &model.Meeting{MeetingID: id, UserID: "owner", ProjectIDs: []string{"p1"}}
	}
	repo.meetingRefs["p1"] = []model.ProjectMeetingRef{{MeetingID: "m1", OwnerUserID: "owner"}, {MeetingID: "m2", OwnerUserID: "owner"}, {MeetingID: "m3", OwnerUserID: "owner"}}
	items, err := newProjectServiceWithRepo(repo).ListProjectMeetings(context.Background(), "owner", "p1")
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"m1", "m2", "m3"} {
		if items[i].MeetingID != want {
			t.Fatalf("order mismatch: %+v", items)
		}
	}
}

func TestGetProjectInsights_AggregatesAndFilters(t *testing.T) {
	repo := newMockProjectRepo()
	seedProject(repo, "p1", "owner")
	april := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	may := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	repo.meetings[projectMeetingKey("owner", "m1")] = &model.Meeting{MeetingID: "m1", UserID: "owner", Date: april, ProjectIDs: []string{"p1"}, Insights: `[{"type":"risk","text":"April risk"}]`}
	repo.meetings[projectMeetingKey("owner", "m2")] = &model.Meeting{MeetingID: "m2", UserID: "owner", Date: may, ProjectIDs: []string{"p1"}, Insights: `[{"type":"risk","text":"May risk"},{"type":"tech","text":"EKS"}]`}
	repo.meetingRefs["p1"] = []model.ProjectMeetingRef{{MeetingID: "m1", OwnerUserID: "owner"}, {MeetingID: "m2", OwnerUserID: "owner"}}
	svc := newProjectServiceWithRepo(repo)
	all, err := svc.GetProjectInsights(context.Background(), "owner", "p1", time.Time{}, time.Time{}, nil)
	if err != nil || len(all) != 3 {
		t.Fatalf("expected 3 aggregated insights, got %v err=%v", all, err)
	}
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 31, 23, 59, 59, 0, time.UTC)
	filtered, err := svc.GetProjectInsights(context.Background(), "owner", "p1", from, to, []string{model.InsightRisk})
	if err != nil || len(filtered) != 1 || filtered[0].Text != "May risk" {
		t.Fatalf("unexpected filtered insights: %v err=%v", filtered, err)
	}
}

func TestGetProjectInsights_NoLinksReturnsEmpty(t *testing.T) {
	repo := newMockProjectRepo()
	seedProject(repo, "p1", "owner")
	items, err := newProjectServiceWithRepo(repo).GetProjectInsights(context.Background(), "owner", "p1", time.Time{}, time.Time{}, nil)
	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("expected non-nil empty result, got %#v err=%v", items, err)
	}
}

func TestLinkMeeting_NonOwnerNotFound(t *testing.T) {
	repo := newMockProjectRepo()
	seedProject(repo, "p1", "owner")
	repo.meetings[projectMeetingKey("owner", "m1")] = &model.Meeting{MeetingID: "m1", UserID: "owner"}
	err := newProjectServiceWithRepo(repo).LinkMeeting(context.Background(), "stranger", "p1", "m1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
