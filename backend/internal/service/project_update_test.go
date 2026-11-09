package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

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
