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
	"github.com/ttobak/backend/internal/service"
)

// mockHandlerProjectRepo embeds the full project repository seam and provides
// in-memory implementations for the operations exercised by these handlers.
type mockHandlerProjectRepo struct {
	service.ProjectRepo
	projects       map[string]*model.Project
	projectMembers map[string]*model.ProjectMember
	accountMembers map[string]*model.AccountMember
	projectRefs    map[string][]model.ProjectRef
}

func newMockHandlerProjectRepo() *mockHandlerProjectRepo {
	return &mockHandlerProjectRepo{
		projects:       make(map[string]*model.Project),
		projectMembers: make(map[string]*model.ProjectMember),
		accountMembers: make(map[string]*model.AccountMember),
		projectRefs:    make(map[string][]model.ProjectRef),
	}
}

func handlerProjectMemberKey(projectID, userID string) string {
	return projectID + "|" + userID
}

func handlerProjectAccountMemberKey(accountID, userID string) string {
	return accountID + "|" + userID
}

func cloneHandlerProject(project *model.Project) *model.Project {
	if project == nil {
		return nil
	}
	copy := *project
	copy.AccountIDs = append([]string(nil), project.AccountIDs...)
	return &copy
}

func (m *mockHandlerProjectRepo) GetProject(_ context.Context, projectID string) (*model.Project, error) {
	return cloneHandlerProject(m.projects[projectID]), nil
}

func (m *mockHandlerProjectRepo) GetProjectMember(_ context.Context, projectID, userID string) (*model.ProjectMember, error) {
	member := m.projectMembers[handlerProjectMemberKey(projectID, userID)]
	if member == nil {
		return nil, nil
	}
	copy := *member
	return &copy, nil
}

func (m *mockHandlerProjectRepo) ListProjectMembers(_ context.Context, projectID string) ([]model.ProjectMember, error) {
	members := []model.ProjectMember{}
	for _, member := range m.projectMembers {
		if member.ProjectID == projectID {
			members = append(members, *member)
		}
	}
	return members, nil
}

func (m *mockHandlerProjectRepo) GetMember(_ context.Context, accountID, userID string) (*model.AccountMember, error) {
	member := m.accountMembers[handlerProjectAccountMemberKey(accountID, userID)]
	if member == nil {
		return nil, nil
	}
	copy := *member
	return &copy, nil
}

func (m *mockHandlerProjectRepo) AddProjectAccountLink(_ context.Context, projectID, accountID string) error {
	project := m.projects[projectID]
	if project == nil {
		return fmt.Errorf("%w: project missing", repository.ErrConditionFailed)
	}
	for _, existing := range project.AccountIDs {
		if existing == accountID {
			return nil
		}
	}
	project.AccountIDs = append(project.AccountIDs, accountID)
	return nil
}

func (m *mockHandlerProjectRepo) PutProjectRef(_ context.Context, ref *model.ProjectRef) error {
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

func newStubProjectHandler() (*ProjectHandler, *mockHandlerProjectRepo) {
	repo := newMockHandlerProjectRepo()
	return NewProjectHandler(service.NewProjectServiceForTest(repo)), repo
}

func serveProjectRequest(h *ProjectHandler, method, path string, body []byte, userID string) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	router.Get("/api/projects/{projectId}", h.GetProject)
	router.Post("/api/projects/{projectId}/accounts", h.LinkAccount)

	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request = withUserEmailCtx(request, userID, userID+"@example.com")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestProjectHandlerGetProject_ForbiddenAndNotFound(t *testing.T) {
	h, repo := newStubProjectHandler()
	repo.projects["p1"] = &model.Project{ProjectID: "p1", Name: "Migration", OwnerUserID: "owner"}

	tests := []struct {
		name       string
		projectID  string
		wantStatus int
	}{
		{name: "stranger is forbidden", projectID: "p1", wantStatus: http.StatusForbidden},
		{name: "missing project is not found", projectID: "missing", wantStatus: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := serveProjectRequest(h, http.MethodGet, "/api/projects/"+test.projectID, nil, "stranger")
			if response.Code != test.wantStatus {
				t.Fatalf("expected %d, got %d (%s)", test.wantStatus, response.Code, response.Body.String())
			}
		})
	}
}

func TestProjectHandlerLinkAccount(t *testing.T) {
	tests := []struct {
		name       string
		projectID  string
		userID     string
		body       string
		seed       func(*mockHandlerProjectRepo)
		wantStatus int
	}{
		{
			name: "missing accountId body", projectID: "p1", userID: "owner", body: `{}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "non-owner", projectID: "p1", userID: "member", body: `{"accountId":"a1"}`,
			seed: func(repo *mockHandlerProjectRepo) {
				repo.projects["p1"] = &model.Project{ProjectID: "p1", Name: "Migration", OwnerUserID: "owner"}
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "missing project", projectID: "missing", userID: "owner", body: `{"accountId":"a1"}`,
			wantStatus: http.StatusNotFound,
		},
		{
			name: "happy path", projectID: "p1", userID: "owner", body: `{"accountId":"a1"}`,
			seed: func(repo *mockHandlerProjectRepo) {
				repo.projects["p1"] = &model.Project{ProjectID: "p1", Name: "Migration", OwnerUserID: "owner"}
				repo.accountMembers[handlerProjectAccountMemberKey("a1", "owner")] = &model.AccountMember{AccountID: "a1", UserID: "owner"}
			},
			wantStatus: http.StatusOK,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, repo := newStubProjectHandler()
			if test.seed != nil {
				test.seed(repo)
			}
			response := serveProjectRequest(h, http.MethodPost, "/api/projects/"+test.projectID+"/accounts", []byte(test.body), test.userID)
			if response.Code != test.wantStatus {
				t.Fatalf("expected %d, got %d (%s)", test.wantStatus, response.Code, response.Body.String())
			}
			if test.wantStatus == http.StatusOK {
				var result struct {
					AccountIDs []string `json:"accountIds"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if len(result.AccountIDs) != 1 || result.AccountIDs[0] != "a1" {
					t.Fatalf("unexpected response: %+v", result)
				}
			}
		})
	}
}
