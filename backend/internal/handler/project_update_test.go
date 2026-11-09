package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func (m *mockHandlerProjectRepo) UpdateProjectFields(_ context.Context, id string, fields map[string]interface{}) error {
	p := m.projects[id]
	if p == nil {
		return fmt.Errorf("%w: project missing", repository.ErrConditionFailed)
	}
	if v, ok := fields["name"]; ok {
		p.Name = v.(string)
	}
	return nil
}

func (m *mockHandlerProjectRepo) UpdateProjectWithStage(ctx context.Context, id string, fields map[string]interface{}, expectedStage string, _ *model.ProjectStageChange) error {
	p := m.projects[id]
	if p == nil || p.Stage != expectedStage || m.stageConflict {
		return fmt.Errorf("%w: stage condition failed", repository.ErrConditionFailed)
	}
	if v, ok := fields["stage"]; ok {
		p.Stage = v.(string)
	}
	return m.UpdateProjectFields(ctx, id, fields)
}

func serveProjectUpdate(h *ProjectHandler, body map[string]string, userID string) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	router.Put("/api/projects/{projectId}", h.UpdateProject)
	raw, _ := json.Marshal(body)
	request := httptest.NewRequest(http.MethodPut, "/api/projects/p1", bytes.NewReader(raw))
	request = withUserEmailCtx(request, userID, userID+"@example.com")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestProjectHandlerUpdate_NonOwnerForbidden(t *testing.T) {
	h, repo := newStubProjectHandler()
	repo.projects["p1"] = &model.Project{ProjectID: "p1", Name: "Migration", OwnerUserID: "owner", Stage: "Qualified"}
	response := serveProjectUpdate(h, map[string]string{"name": "Migration", "stage": "Committed"}, "stranger")
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", response.Code, response.Body.String())
	}
	if repo.projects["p1"].Stage != "Qualified" {
		t.Fatal("non-owner must not change the stage")
	}
}

func TestProjectHandlerUpdate_StageErrors(t *testing.T) {
	h, repo := newStubProjectHandler()
	repo.projects["p1"] = &model.Project{ProjectID: "p1", Name: "Migration", OwnerUserID: "owner", Stage: "Qualified"}

	if response := serveProjectUpdate(h, map[string]string{"name": "Migration", "stage": "POC"}, "owner"); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown stage: expected 400, got %d", response.Code)
	}

	repo.stageConflict = true
	response := serveProjectUpdate(h, map[string]string{"name": "Migration", "stage": "Committed"}, "owner")
	if response.Code != http.StatusConflict {
		t.Fatalf("concurrent change: expected 409, got %d (%s)", response.Code, response.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Error.Code != "CONFLICT" {
		t.Fatalf("expected CONFLICT code, got %s", response.Body.String())
	}

	repo.stageConflict = false
	if response := serveProjectUpdate(h, map[string]string{"name": "Migration", "stage": "Committed"}, "owner"); response.Code != http.StatusOK {
		t.Fatalf("valid change: expected 200, got %d (%s)", response.Code, response.Body.String())
	}
}
