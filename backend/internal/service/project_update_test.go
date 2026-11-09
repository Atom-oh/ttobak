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

func (m *mockProjectRepo) UpdateProjectWithStage(ctx context.Context, id string, fields map[string]interface{}, expectedStage string, change *model.ProjectStageChange) error {
	p := m.projects[id]
	if p == nil || m.stageConflict || p.Stage != expectedStage {
		return fmt.Errorf("%w: stage condition failed", repository.ErrConditionFailed)
	}
	if err := m.UpdateProjectFields(ctx, id, fields); err != nil {
		return err
	}
	m.stageChanges = append(m.stageChanges, *change)
	at := change.ChangedAt
	p.StageChangedAt = &at
	return nil
}

func newUpdateFixture(stage string) (*ProjectService, *mockProjectRepo) {
	repo := newMockProjectRepo()
	repo.projects["p1"] = &model.Project{ProjectID: "p1", Name: "Migration", OwnerUserID: "owner", Stage: stage}
	return NewProjectServiceForTest(repo), repo
}

func TestUpdateProject_RejectsUnknownStage(t *testing.T) {
	svc, repo := newUpdateFixture("Qualified")
	_, err := svc.UpdateProject(context.Background(), "owner", "p1", &model.UpdateProjectRequest{Name: "Migration", Stage: "POC"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if repo.projects["p1"].Stage != "Qualified" || len(repo.stageChanges) != 0 {
		t.Fatal("rejected update must not write")
	}
	if _, err := svc.CreateProject(context.Background(), "owner", &model.CreateProjectRequest{Name: "X", Stage: "POC"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("create: expected ErrInvalidInput, got %v", err)
	}
}

func TestUpdateProject_StageChangeWritesHistory(t *testing.T) {
	svc, repo := newUpdateFixture("Qualified")
	resp, err := svc.UpdateProject(context.Background(), "owner", "p1", &model.UpdateProjectRequest{Name: "Migration", Stage: "technical validation"})
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.stageChanges) != 1 {
		t.Fatalf("expected 1 history row, got %d", len(repo.stageChanges))
	}
	c := repo.stageChanges[0]
	if c.From != "Qualified" || c.To != "Technical Validation" || c.ChangedBy != "owner" || c.ProjectID != "p1" {
		t.Fatalf("unexpected change %+v", c)
	}
	if resp.Stage != "Technical Validation" || !resp.StageMapped || resp.StageChangedAt == nil {
		t.Fatalf("unexpected response %+v", resp)
	}
}

func TestUpdateProject_SameStageNoHistory(t *testing.T) {
	svc, repo := newUpdateFixture("Qualified")
	if _, err := svc.UpdateProject(context.Background(), "owner", "p1", &model.UpdateProjectRequest{Name: "Renamed", Stage: "Qualified"}); err != nil {
		t.Fatal(err)
	}
	if len(repo.stageChanges) != 0 || repo.projects["p1"].Name != "Renamed" {
		t.Fatalf("same stage must update fields without history: %+v", repo.stageChanges)
	}
}

func TestUpdateProject_LegacyStageEchoedBack(t *testing.T) {
	svc, repo := newUpdateFixture("POC")
	resp, err := svc.UpdateProject(context.Background(), "owner", "p1", &model.UpdateProjectRequest{Name: "Renamed", Stage: "POC"})
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.stageChanges) != 0 || resp.Stage != "POC" || resp.StageMapped {
		t.Fatalf("legacy stage must be preserved unmapped: %+v", resp)
	}
}

func TestUpdateProject_ConcurrentStageChangeConflict(t *testing.T) {
	svc, repo := newUpdateFixture("Qualified")
	repo.stageConflict = true
	_, err := svc.UpdateProject(context.Background(), "owner", "p1", &model.UpdateProjectRequest{Name: "Migration", Stage: "Committed"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestUpdateProject_NonOwnerForbidden(t *testing.T) {
	svc, _ := newUpdateFixture("Qualified")
	_, err := svc.UpdateProject(context.Background(), "stranger", "p1", &model.UpdateProjectRequest{Name: "Migration", Stage: "Committed"})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestUpdateProject_UnchangedStageNotWritten(t *testing.T) {
	svc, repo := newUpdateFixture("Qualified")
	if _, err := svc.UpdateProject(context.Background(), "owner", "p1", &model.UpdateProjectRequest{Name: "Renamed", Stage: "Qualified"}); err != nil {
		t.Fatal(err)
	}
	if repo.lastFields == nil {
		t.Fatal("expected a field update")
	}
	for _, key := range []string{"stage", "stageChangedAt"} {
		if _, ok := repo.lastFields[key]; ok {
			t.Fatalf("unchanged stage must not write %q: %v", key, repo.lastFields)
		}
	}
}

// listRowRepo serves the batch list-row projection like the DynamoDB repository.
type listRowRepo struct{ *mockProjectRepo }

func (r listRowRepo) BatchGetProjectListRows(_ context.Context, ids []string) (map[string]*model.Project, error) {
	out := map[string]*model.Project{}
	for _, id := range ids {
		if p := r.projects[id]; p != nil {
			out[id] = &model.Project{PK: p.PK, SK: p.SK, EntityType: p.EntityType, ProjectID: p.ProjectID, Name: p.Name,
				Stage: p.Stage, StageChangedAt: p.StageChangedAt, SfdcOpptyID: p.SfdcOpptyID, OwnerUserID: p.OwnerUserID,
				AccountIDs: append([]string{}, p.AccountIDs...)}
		}
	}
	return out, nil
}

func TestListMyProjects_InheritedSummaryCarriesStageChangedAt(t *testing.T) {
	repo := newMockProjectRepo()
	project := seedProject(repo, "p1", "owner")
	project.AccountIDs = []string{"acc1"}
	project.Stage = "Committed"
	changed := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	project.StageChangedAt = &changed
	repo.accountMembers[projectAccountMemberKey("acc1", "carol")] = &model.AccountMember{AccountID: "acc1", UserID: "carol"}
	repo.accounts["acc1"] = &model.Account{AccountID: "acc1"}
	repo.projectRefs["acc1"] = []model.ProjectRef{{ProjectID: "p1", AccountID: "acc1"}}

	projects, err := newProjectServiceWithRepo(listRowRepo{repo}).ListMyProjects(context.Background(), "carol")
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].StageChangedAt == nil || !projects[0].StageChangedAt.Equal(changed) || !projects[0].StageMapped {
		t.Fatalf("inherited summary lost stageChangedAt: %+v", projects)
	}
}

func TestProjectListRowFieldsIncludeStageChangedAt(t *testing.T) {
	for _, name := range repository.ProjectListRowFields {
		if name == "stageChangedAt" {
			return
		}
	}
	t.Fatal("ProjectListRowFields must project stageChangedAt")
}
