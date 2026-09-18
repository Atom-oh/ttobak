package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

// ProjectHandler handles project HTTP requests.
type ProjectHandler struct {
	projectService *service.ProjectService
}

// NewProjectHandler creates a ProjectHandler.
func NewProjectHandler(projectService *service.ProjectService) *ProjectHandler {
	return &ProjectHandler{projectService: projectService}
}

func projectIDFromRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	projectID := chi.URLParam(r, "projectId")
	if projectID == "" || strings.Contains(projectID, "..") || strings.Contains(projectID, "/") {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid projectId")
		return "", false
	}
	return projectID, true
}

func writeProjectError(w http.ResponseWriter, err error, forbiddenMessage string) {
	switch {
	case errors.Is(err, service.ErrClientUpgradeRequired):
		writeError(w, http.StatusConflict, "CLIENT_UPGRADE_REQUIRED", "화면을 새로고침한 뒤 가입 대기 초대를 다시 요청하세요")
	case errors.Is(err, service.ErrInvitationsPaused), errors.Is(err, repository.ErrProjectInvitationsPaused):
		writeError(w, http.StatusServiceUnavailable, "INVITATIONS_PAUSED", "프로젝트 초대가 일시 중지됐습니다. 잠시 후 다시 시도해주세요")
	case errors.Is(err, service.ErrInvitationRequired):
		writeError(w, http.StatusConflict, "INVITATION_REQUIRED", "먼저 관리자가 사용자 초대 메일을 보내야 합니다")
	case errors.Is(err, service.ErrUserDisabled):
		writeError(w, http.StatusConflict, "USER_DISABLED", "비활성 사용자입니다. 관리자에게 문의하세요")
	case errors.Is(err, service.ErrSelfShare):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "프로젝트 소유자는 이미 접근할 수 있습니다")
	case errors.Is(err, service.ErrPendingAlreadyClaimed), errors.Is(err, repository.ErrConditionFailed):
		writeError(w, http.StatusConflict, "CONFLICT", "멤버십이 변경됐습니다. 목록을 새로 확인하세요")
	case errors.Is(err, repository.ErrInvalidCursor):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid cursor")
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, forbiddenMessage)
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Project not found")
	case errors.Is(err, service.ErrUserNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "No user with that email")
	case errors.Is(err, service.ErrMemberExists):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "User is already a member")
	case errors.Is(err, service.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid input")
	case errors.Is(err, service.ErrProjectHasLinks):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Unlink all accounts, meetings, research, and members before deleting")
	default:
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
	}
}

func parseProjectInsightFilters(w http.ResponseWriter, r *http.Request) (time.Time, time.Time, []string, bool) {
	var from, to time.Time
	if value := r.URL.Query().Get("from"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid 'from' (RFC3339)")
			return time.Time{}, time.Time{}, nil, false
		}
		from = parsed
	}
	if value := r.URL.Query().Get("to"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid 'to' (RFC3339)")
			return time.Time{}, time.Time{}, nil, false
		}
		to = parsed
	}
	var types []string
	if value := r.URL.Query().Get("types"); value != "" {
		for _, insightType := range strings.Split(value, ",") {
			insightType = strings.TrimSpace(insightType)
			if insightType == "" {
				continue
			}
			if !model.IsValidInsightType(insightType) {
				writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid insight type: "+insightType)
				return time.Time{}, time.Time{}, nil, false
			}
			types = append(types, insightType)
		}
	}
	return from, to, types, true
}

func (h *ProjectHandler) CreateProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req model.CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "name is required")
		return
	}
	project, err := h.projectService.CreateProject(ctx, middleware.GetUserID(ctx), &req)
	if err != nil {
		writeProjectError(w, err, "Access denied")
		return
	}
	writeJSON(w, http.StatusCreated, project)
}

func (h *ProjectHandler) ListMyProjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var hints []string
	if raw := r.URL.Query().Get("joinedProjectIds"); raw != "" {
		if len(raw) > 4000 {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid project discovery hints")
			return
		}
		hints = strings.Split(raw, ",")
	}
	projects, err := h.projectService.ListMyProjects(ctx, middleware.GetUserID(ctx), hints...)
	if err != nil {
		writeProjectError(w, err, "Access denied")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"projects": projects})
}

func (h *ProjectHandler) GetProject(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromRequest(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	project, err := h.projectService.GetProject(ctx, middleware.GetUserID(ctx), projectID)
	if err != nil {
		writeProjectError(w, err, "Access denied")
		return
	}
	writeJSON(w, http.StatusOK, project)
}

func (h *ProjectHandler) UpdateProject(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromRequest(w, r)
	if !ok {
		return
	}
	var req model.UpdateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "name is required")
		return
	}
	ctx := r.Context()
	project, err := h.projectService.UpdateProject(ctx, middleware.GetUserID(ctx), projectID, &req)
	if err != nil {
		writeProjectError(w, err, "Access denied")
		return
	}
	writeJSON(w, http.StatusOK, project)
}

func (h *ProjectHandler) DeleteProject(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromRequest(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if err := h.projectService.DeleteProject(ctx, middleware.GetUserID(ctx), projectID); err != nil {
		writeProjectError(w, err, "Access denied")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ProjectHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromRequest(w, r)
	if !ok {
		return
	}
	var req model.AddProjectMemberRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&req); err != nil || strings.TrimSpace(req.Email) == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "email is required")
		return
	}
	ctx := r.Context()
	member, err := h.projectService.AddMember(ctx, middleware.GetUserID(ctx), projectID, &req)
	if err != nil {
		writeProjectError(w, err, "Only the owner can add members")
		return
	}
	writeJSON(w, http.StatusCreated, member)
}

func (h *ProjectHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromRequest(w, r)
	if !ok {
		return
	}
	targetUserID := chi.URLParam(r, "userId")
	if targetUserID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "userId is required")
		return
	}
	ctx := r.Context()
	if err := h.projectService.RemoveMember(ctx, middleware.GetUserID(ctx), projectID, targetUserID); err != nil {
		writeProjectError(w, err, "Only the owner can remove members")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ProjectHandler) LinkAccount(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromRequest(w, r)
	if !ok {
		return
	}
	var req model.LinkProjectAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.AccountID) == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "accountId is required")
		return
	}
	ctx := r.Context()
	accountIDs, err := h.projectService.LinkAccount(ctx, middleware.GetUserID(ctx), projectID, req.AccountID)
	if err != nil {
		writeProjectError(w, err, "Access denied")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"accountIds": accountIDs})
}

func (h *ProjectHandler) UnlinkAccount(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromRequest(w, r)
	if !ok {
		return
	}
	accountID := chi.URLParam(r, "accountId")
	if accountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "accountId is required")
		return
	}
	ctx := r.Context()
	if _, err := h.projectService.UnlinkAccount(ctx, middleware.GetUserID(ctx), projectID, accountID); err != nil {
		writeProjectError(w, err, "Access denied")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ProjectHandler) LinkMeeting(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromRequest(w, r)
	if !ok {
		return
	}
	var req model.LinkProjectMeetingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.MeetingID) == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "meetingId is required")
		return
	}
	ctx := r.Context()
	if err := h.projectService.LinkMeeting(ctx, middleware.GetUserID(ctx), projectID, req.MeetingID); err != nil {
		writeProjectError(w, err, "Access denied")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ProjectHandler) UnlinkMeeting(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromRequest(w, r)
	if !ok {
		return
	}
	meetingID := chi.URLParam(r, "meetingId")
	if meetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "meetingId is required")
		return
	}
	ctx := r.Context()
	if err := h.projectService.UnlinkMeeting(ctx, middleware.GetUserID(ctx), projectID, meetingID); err != nil {
		writeProjectError(w, err, "Access denied")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ProjectHandler) LinkResearch(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromRequest(w, r)
	if !ok {
		return
	}
	var req model.LinkProjectResearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.ResearchID) == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "researchId is required")
		return
	}
	ctx := r.Context()
	if err := h.projectService.LinkResearch(ctx, middleware.GetUserID(ctx), projectID, req.ResearchID); err != nil {
		writeProjectError(w, err, "Access denied")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ProjectHandler) UnlinkResearch(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromRequest(w, r)
	if !ok {
		return
	}
	researchID := chi.URLParam(r, "researchId")
	if researchID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "researchId is required")
		return
	}
	ctx := r.Context()
	if err := h.projectService.UnlinkResearch(ctx, middleware.GetUserID(ctx), projectID, researchID); err != nil {
		writeProjectError(w, err, "Access denied")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ProjectHandler) ListProjectMeetings(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromRequest(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	meetings, err := h.projectService.ListProjectMeetings(ctx, middleware.GetUserID(ctx), projectID)
	if err != nil {
		writeProjectError(w, err, "Access denied")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"meetings": meetings})
}

func (h *ProjectHandler) ListProjectResearch(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromRequest(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	research, err := h.projectService.ListProjectResearch(ctx, middleware.GetUserID(ctx), projectID)
	if err != nil {
		writeProjectError(w, err, "Access denied")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"research": research})
}

func (h *ProjectHandler) GetProjectInsights(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromRequest(w, r)
	if !ok {
		return
	}
	from, to, types, ok := parseProjectInsightFilters(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	insights, err := h.projectService.GetProjectInsights(ctx, middleware.GetUserID(ctx), projectID, from, to, types)
	if err != nil {
		writeProjectError(w, err, "Access denied")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"insights": insights})
}

func (h *ProjectHandler) GetProjectBrief(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromRequest(w, r)
	if !ok {
		return
	}
	from, to, types, ok := parseProjectInsightFilters(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	brief, err := h.projectService.GetProjectBrief(ctx, middleware.GetUserID(ctx), projectID, from, to, types)
	if err != nil {
		writeProjectError(w, err, "Access denied")
		return
	}
	writeJSON(w, http.StatusOK, brief)
}

func (h *ProjectHandler) ListAccountProjects(w http.ResponseWriter, r *http.Request) {
	accountID := chi.URLParam(r, "accountId")
	if accountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "accountId is required")
		return
	}
	ctx := r.Context()
	projects, err := h.projectService.ListAccountProjects(ctx, middleware.GetUserID(ctx), accountID)
	if err != nil {
		writeProjectError(w, err, "Access denied")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"projects": projects})
}

func (h *ProjectHandler) ListPendingMembers(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromRequest(w, r)
	if !ok {
		return
	}
	result, err := h.projectService.ListPendingMembers(r.Context(), middleware.GetUserID(r.Context()), projectID, r.URL.Query().Get("cursor"))
	if err != nil {
		writeProjectError(w, err, "Only the owner can list pending members")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (h *ProjectHandler) RevokePendingMember(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromRequest(w, r)
	if !ok {
		return
	}
	var req model.AddProjectMemberRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request")
		return
	}
	if err := h.projectService.RevokePendingMember(r.Context(), middleware.GetUserID(r.Context()), projectID, req.Email); err != nil {
		writeProjectError(w, err, "Only the owner can revoke pending members")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
