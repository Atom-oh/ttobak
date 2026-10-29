package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func descendantProjectFixture() *mockProjectRepo {
	r := newMockProjectRepo()
	access := newDescendantAccessRepo()
	r.accounts = access.accounts
	r.accountMembers[projectAccountMemberKey("parent", "reader")] = access.members["parent/reader"]
	r.projects["project"] = &model.Project{ProjectID: "project", OwnerUserID: "owner", AccountIDs: []string{"child"}}
	for _, id := range []string{"published", "private", "unsafe"} {
		m := *access.meeting
		m.MeetingID, m.Title, m.ProjectIDs = id, id+" title", []string{"project"}
		m.Insights = `[{"type":"risk","text":"` + id + ` insight"}]`
		if id == "private" {
			m.SharedToAccount = false
		}
		if id == "unsafe" {
			m.SummaryPrivacy = nil
		}
		r.meetings[projectMeetingKey("owner", id)] = &m
		r.meetingRefs["project"] = append(r.meetingRefs["project"], model.ProjectMeetingRef{ProjectID: "project", MeetingID: id, OwnerUserID: "owner"})
	}
	return r
}

type descendantProjectListRepo struct{ *mockProjectRepo }

func (r *descendantProjectListRepo) ListChildAccountIDsPage(_ context.Context, parent, _ string, _ int32) ([]string, *string, error) {
	ids := []string{}
	for id, a := range r.accounts {
		if a.ParentAccountID == parent {
			ids = append(ids, id)
		}
	}
	return ids, nil, nil
}

func TestDescendantProjectReadFiltersPrivateSourcesAndDeniesMutation(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	r := descendantProjectFixture()
	r.projectRefs["child"] = []model.ProjectRef{{ProjectID: "project", AccountID: "child"}}
	s := newProjectServiceWithRepo(&descendantProjectListRepo{r})
	ctx := context.Background()
	if projects, err := s.ListMyProjects(ctx, "reader"); err != nil || len(projects) != 1 || projects[0].ProjectID != "project" {
		t.Fatalf("descendant project discovery: %+v/%v", projects, err)
	}
	if result, err := s.GetProject(ctx, "reader", "project"); err != nil || result.AccessPermission != model.PermissionRead || result.InheritedFromAccountID != "parent" {
		t.Fatalf("parent member project read: %v", err)
	}
	meetings, err := s.ListProjectMeetings(ctx, "reader", "project")
	if err != nil || len(meetings) != 2 {
		t.Fatalf("published sources only: %+v/%v", meetings, err)
	}
	for _, m := range meetings {
		if m.MeetingID == "private" {
			t.Fatal("private source title exposed")
		}
	}
	insights, err := s.GetProjectInsights(ctx, "reader", "project", r.accounts["child"].CreatedAt, r.accounts["child"].CreatedAt, nil)
	if err != nil || len(insights) != 1 || insights[0].SourceID != "published" {
		t.Fatalf("safe independent source insights only: %+v/%v", insights, err)
	}
	if _, err := s.requireProjectAccess(ctx, "reader", "project"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ancestor owner gained existing mutation grant: %v", err)
	}
	delete(r.accountMembers, projectAccountMemberKey("parent", "reader"))
	if _, err := s.GetProject(ctx, "reader", "project"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked member project read: %v", err)
	}
}

type descendantResearchMainRepo struct {
	*mockResearchMainRepo
	*descendantAccessRepo
}

func (r *descendantResearchMainRepo) GetMember(ctx context.Context, account, user string) (*model.AccountMember, error) {
	return r.descendantAccessRepo.GetMember(ctx, account, user)
}

func TestDescendantResearchReadAndStorageFailure(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	main := &descendantResearchMainRepo{newMockResearchMainRepo(), newDescendantAccessRepo()}
	repo := newMockResearchRepo()
	repo.byID["research"] = &model.Research{ResearchID: "research", UserID: "owner", AccountIDs: []string{"child"}}
	main.refs["child"] = []model.ResearchRef{{ResearchID: "research"}}
	s := newResearchServiceWithRepo(repo, main)
	if result, err := s.GetResearchDetail(context.Background(), "research", "reader"); err != nil || !result.IsShared {
		t.Fatalf("inherited research detail: %+v/%v", result, err)
	}
	if result, err := s.ListAccountResearch(context.Background(), "reader", "child"); err != nil || len(result) != 1 {
		t.Fatalf("inherited research listing: %+v/%v", result, err)
	}
	main.err = errors.New("storage unavailable")
	if _, err := s.GetResearchDetail(context.Background(), "research", "reader"); !errors.Is(err, main.err) {
		t.Fatalf("storage error hidden as permission denial: %v", err)
	}
}

func TestMeetingReadingCursorBindsCurrentAccountGrant(t *testing.T) {
	m := &model.Meeting{MeetingID: "meeting", Notes: strings.Repeat("saved notes ", 100), AccountReadGrantRevision: "grant-before"}
	opts := ReadingOptions{Kind: "meeting", Section: "notes", PageSize: 20, MaxBytes: 14000}
	state := &model.ActionItemsAnalysis{Status: model.AnalysisUnknown}
	body, err := renderMeetingReading(m, model.PermissionRead, state, opts)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Page struct {
			NextCursor string `json:"nextCursor"`
		} `json:"page"`
	}
	if err := json.Unmarshal(body, &page); err != nil || page.Page.NextCursor == "" {
		t.Fatalf("missing continuation: %s/%v", body, err)
	}
	opts.Cursor = page.Page.NextCursor
	m.AccountReadGrantRevision = "grant-after-reparent"
	if _, err := renderMeetingReading(m, model.PermissionRead, state, opts); !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatalf("old grant continuation retained: %v", err)
	}
}
