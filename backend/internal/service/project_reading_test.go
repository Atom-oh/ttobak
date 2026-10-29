package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type projectReadingRepo struct {
	*mockProjectRepo
	queries, memberReads, meetingReads, researchReads int
	afterQuery                                        func()
}

func TestProjectReadingDescendantGrantInvalidatesAfterReachableReparent(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	s, r := projectReadingFixture()
	r.projects["p"].OwnerUserID = "owner"
	r.projects["p"].AccountIDs = []string{"child"}
	access := newDescendantAccessRepo()
	r.accounts = access.accounts
	r.accountMembers = map[string]*model.AccountMember{"parent|reader": access.members["parent/reader"]}
	q := url.Values{"section": {"metadata"}, "pageSize": {"10"}}
	page := readProjectPage(t, s, q)
	q.Set("cursor", page["page"].(map[string]interface{})["nextCursor"].(string))
	r.accounts["child"].ParentAccountID = "root"
	r.accountMembers["root|reader"] = &model.AccountMember{PK: "ACCOUNT#root", SK: "MEMBER#reader", EntityType: model.EntityTypeAccountMember, AccountID: "root", UserID: "reader"}
	if body, err := s.Read(context.Background(), "reader", "p", q); body != nil || !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatalf("reparented current grant continued old read: %s/%v", body, err)
	}
}
func (r *projectReadingRepo) ListProjectMembers(context.Context, string) ([]model.ProjectMember, error) {
	return nil, errors.New("unbounded members")
}
func (r *projectReadingRepo) ListProjectMeetingRefsForProject(context.Context, string) ([]model.ProjectMeetingRef, error) {
	return nil, errors.New("unbounded meetings")
}
func (r *projectReadingRepo) ListProjectResearchRefsForProject(context.Context, string) ([]model.ProjectResearchRef, error) {
	return nil, errors.New("unbounded research")
}
func (r *projectReadingRepo) BatchGetMeetings(context.Context, []repository.MeetingKey) ([]*model.Meeting, error) {
	return nil, errors.New("eventual batch meetings")
}
func (r *projectReadingRepo) BatchGetResearchByIDs(context.Context, []string) ([]model.Research, error) {
	return nil, errors.New("eventual batch research")
}
func (r *projectReadingRepo) GetMember(ctx context.Context, a, u string) (*model.AccountMember, error) {
	r.memberReads++
	return r.mockProjectRepo.GetMember(ctx, a, u)
}
func (r *projectReadingRepo) GetMeeting(ctx context.Context, u, id string) (*model.Meeting, error) {
	r.meetingReads++
	return r.mockProjectRepo.GetMeeting(ctx, u, id)
}
func (r *projectReadingRepo) GetProjectReadingResearch(ctx context.Context, id string) (*model.Research, error) {
	r.researchReads++
	return r.mockProjectRepo.GetResearchByID(ctx, id)
}
func (r *projectReadingRepo) QueryProjectReading(_ context.Context, id, phase, after string) (*repository.ProjectReadingPage, error) {
	r.queries++
	rows := map[string]interface{}{}
	switch phase {
	case "members":
		for _, m := range r.projectMembers {
			if m.ProjectID == id {
				rows[m.SK] = *m
			}
		}
	case "meetings", "insights":
		for _, m := range r.meetingRefs[id] {
			rows[m.SK] = m
		}
	case "research":
		for _, m := range r.researchRefs[id] {
			rows[m.SK] = m
		}
	default:
		return nil, ErrInvalidInput
	}
	keys := []string{}
	for k := range rows {
		if k > after {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	page := &repository.ProjectReadingPage{}
	for i, k := range keys {
		if i == 10 {
			page.NextKey = keys[i-1]
			break
		}
		switch row := rows[k].(type) {
		case model.ProjectMember:
			page.Members = append(page.Members, row)
		case model.ProjectMeetingRef:
			page.Meetings = append(page.Meetings, row)
		case model.ProjectResearchRef:
			page.Research = append(page.Research, row)
		}
	}
	if r.afterQuery != nil {
		cb := r.afterQuery
		r.afterQuery = nil
		cb()
	}
	return page, nil
}
func projectReadingFixture() (*ProjectReadingService, *projectReadingRepo) {
	r := &projectReadingRepo{mockProjectRepo: newMockProjectRepo()}
	r.projects["p"] = &model.Project{ProjectID: "p", OwnerUserID: "reader", Description: strings.Repeat("프로젝트😀\"\\\n", 6000), AccountIDs: []string{"b", "a"}}
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 15; i++ {
		id := fmt.Sprintf("%02d", i)
		r.projectMembers[projectMemberKey("p", id)] = &model.ProjectMember{ProjectID: "p", UserID: id, SK: "MEMBER#" + id}
		insights, _ := json.Marshal([]model.MeetingInsight{{Type: model.InsightNeed, Text: strings.Repeat("통찰😀\"\\", 900), Evidence: "PRIVATE_EVIDENCE"}})
		r.meetings[projectMeetingKey("author", id)] = &model.Meeting{UserID: "author", MeetingID: id, ProjectIDs: []string{"p"}, Title: "Meeting " + id, Insights: string(insights), Date: now}
		r.meetingRefs["p"] = append(r.meetingRefs["p"], model.ProjectMeetingRef{ProjectID: "p", SK: "MEETINGREF#" + id, MeetingID: id, OwnerUserID: "author"})
		r.research[id] = &model.Research{ResearchID: id, UserID: "author", Topic: "Research " + id, ProjectIDs: []string{"p"}}
		r.researchRefs["p"] = append(r.researchRefs["p"], model.ProjectResearchRef{ProjectID: "p", SK: "RESEARCHREF#" + id, ResearchID: id, OwnerUserID: "author"})
	}
	return NewProjectReadingService(r), r
}
func readProjectPage(t *testing.T, s *ProjectReadingService, q url.Values) map[string]interface{} {
	t.Helper()
	raw, err := s.Read(context.Background(), "reader", "p", q)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 10000 || strings.Contains(string(raw), "PRIVATE_EVIDENCE") {
		t.Fatal("size/Evidence bound")
	}
	envelope, _ := json.Marshal(map[string]interface{}{"content": []map[string]string{{"type": "text", "text": string(raw)}}, "structuredContent": json.RawMessage(raw)})
	if len(envelope) > 32000 {
		t.Fatal("duplicated MCP envelope exceeds bound")
	}
	var p map[string]interface{}
	if json.Unmarshal(raw, &p) != nil {
		t.Fatal("invalid JSON")
	}
	return p
}
func TestProjectReadingRestoresLargeBriefWithoutUnboundedHelpers(t *testing.T) {
	s, r := projectReadingFixture()
	q := url.Values{"section": {"brief"}, "pageSize": {"8000"}}
	groups := map[string][]interface{}{}
	var buffer strings.Builder
	identity := ""
	for n := 0; ; n++ {
		queries, meetings, research := r.queries, r.meetingReads, r.researchReads
		p := readProjectPage(t, s, q)
		if r.queries-queries > 2 || r.meetingReads-meetings > 20 || r.researchReads-research > 20 {
			t.Fatal("unbounded source reads")
		}
		fragment := p["fragment"].(map[string]interface{})
		phase := p["phase"].(string)
		if fragment["startOffset"] == float64(0) {
			if buffer.Len() != 0 {
				t.Fatal("lost fragment")
			}
			identity = phase + p["revision"].(string)
		} else if identity != phase+p["revision"].(string) {
			t.Fatal("mixed revisions")
		}
		buffer.WriteString(p["dataJson"].(string))
		if fragment["complete"] == true {
			var data interface{}
			if json.Unmarshal([]byte(buffer.String()), &data) != nil {
				t.Fatal("invalid restored JSON")
			}
			groups[phase] = append(groups[phase], data)
			buffer.Reset()
		}
		page := p["page"].(map[string]interface{})
		if page["complete"] == true {
			break
		}
		if n > 600 {
			t.Fatal("never completes")
		}
		q.Set("cursor", page["nextCursor"].(string))
	}
	metadata := groups["metadata"][0].(map[string]interface{})
	if metadata["description"] != r.projects["p"].Description || metadata["members"] != nil {
		t.Fatal("metadata truncated")
	}
	for _, phase := range []string{"members", "meetings", "research", "insights"} {
		n := 0
		for _, v := range groups[phase] {
			n += len(v.([]interface{}))
		}
		if n != 15 {
			t.Fatalf("lost %s: %d", phase, n)
		}
	}
}
func TestProjectReadingAuthorizationScansBoundedWindowsAndPinsSelectedGrant(t *testing.T) {
	s, r := projectReadingFixture()
	p := r.projects["p"]
	p.OwnerUserID = "owner"
	p.AccountIDs = nil
	for i := 0; i < 121; i++ {
		p.AccountIDs = append(p.AccountIDs, fmt.Sprintf("a-%03d", i))
	}
	r.accountMembers["a-120|reader"] = &model.AccountMember{AccountID: "a-120", UserID: "reader", Role: "viewer"}
	q := url.Values{"section": {"metadata"}, "pageSize": {"10"}}
	for i := 0; i < 2; i++ {
		before := r.memberReads
		page := readProjectPage(t, s, q)
		if page["authorizationPending"] != true || page["dataJson"] != nil || page["revision"] != nil || r.memberReads-before != 50 || r.queries != 0 {
			t.Fatal("authorization preflight leaked data or exceeded work")
		}
		q.Set("cursor", page["page"].(map[string]interface{})["nextCursor"].(string))
	}
	page := readProjectPage(t, s, q)
	if page["authorizationPending"] != nil {
		t.Fatal("valid later grant omitted")
	}
	q.Set("cursor", page["page"].(map[string]interface{})["nextCursor"].(string))
	r.projectMembers["p|reader"] = &model.ProjectMember{ProjectID: "p", UserID: "reader"}
	delete(r.accountMembers, "a-120|reader")
	if _, err := s.Read(context.Background(), "reader", "p", q); !errors.Is(err, ErrForbidden) {
		t.Fatal("grant path silently switched", err)
	}
}

func TestProjectReadingDirectLinkedAccountWinsOverEarlierInheritedGrant(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	for _, acrossPages := range []bool{false, true} {
		t.Run(fmt.Sprint(acrossPages), func(t *testing.T) {
			s, r := projectReadingFixture()
			r.projects["p"].OwnerUserID = "owner"
			r.projects["p"].AccountIDs = []string{"child"}
			if acrossPages {
				for i := 0; i < 55; i++ {
					r.projects["p"].AccountIDs = append(r.projects["p"].AccountIDs, fmt.Sprintf("unrelated-%02d", i))
				}
			}
			r.projects["p"].AccountIDs = append(r.projects["p"].AccountIDs, "direct")
			access := newDescendantAccessRepo()
			r.accounts = access.accounts
			r.accountMembers = map[string]*model.AccountMember{"parent|reader": access.members["parent/reader"], "direct|reader": {AccountID: "direct", UserID: "reader", Role: model.RoleAM}}
			q := url.Values{"section": {"meetings"}, "pageSize": {"8000"}}
			for n := 0; n < 4; n++ {
				page := readProjectPage(t, s, q)
				if page["authorizationPending"] == true {
					q.Set("cursor", page["page"].(map[string]interface{})["nextCursor"].(string))
					continue
				}
				var meetings []model.ProjectMeetingRefDTO
				if err := json.Unmarshal([]byte(page["dataJson"].(string)), &meetings); err != nil {
					t.Fatal(err)
				}
				if len(meetings) != 10 {
					t.Fatalf("stronger direct project grant lost its independently permitted source metadata: %+v", meetings)
				}
				return
			}
			t.Fatal("direct grant scan failed to complete")
		})
	}
}
func TestProjectReadingPinsAccountPrivacyVersion(t *testing.T) {
	for _, duringQuery := range []bool{false, true} {
		t.Run(fmt.Sprintf("during_query_%t", duringQuery), func(t *testing.T) {
			s, r := projectReadingFixture()
			r.projects["p"].OwnerUserID = "owner"
			member := &model.AccountMember{AccountID: "a", UserID: "reader", Role: "viewer", SummaryPrivacyVersion: 1}
			r.accountMembers["a|reader"] = member
			q := url.Values{"section": {"members"}, "pageSize": {"10"}}
			if duringQuery {
				r.afterQuery = func() { member.SummaryPrivacyVersion++ }
			} else {
				page := readProjectPage(t, s, q)
				q.Set("cursor", page["page"].(map[string]interface{})["nextCursor"].(string))
				member.SummaryPrivacyVersion++
			}
			body, err := s.Read(context.Background(), "reader", "p", q)
			if body != nil || !errors.Is(err, ErrStaleReadingCursor) {
				t.Fatalf("changed account privacy epoch returned data: %v", err)
			}
		})
	}
}

func TestProjectReadingRejectsChangedSelectionMetadataAccessAndSources(t *testing.T) {
	for _, change := range []string{"selection", "metadata", "member", "meeting", "research"} {
		t.Run(change, func(t *testing.T) {
			s, r := projectReadingFixture()
			r.projects["p"].OwnerUserID = "owner"
			r.projectMembers["p|reader"] = &model.ProjectMember{ProjectID: "p", UserID: "reader"}
			section := "metadata"
			if change == "meeting" {
				section = "insights"
			}
			if change == "research" {
				section = "research"
			}
			q := url.Values{"section": {section}, "pageSize": {"10"}}
			page := readProjectPage(t, s, q)
			q.Set("cursor", page["page"].(map[string]interface{})["nextCursor"].(string))
			switch change {
			case "selection":
				q.Set("section", "members")
			case "metadata":
				r.projects["p"].Description = "changed"
			case "member":
				delete(r.projectMembers, "p|reader")
			case "meeting":
				r.meetings["author|00"].ProjectIDs = nil
			case "research":
				r.research["00"].TrashedAt = "2026-10-01"
			}
			body, err := s.Read(context.Background(), "reader", "p", q)
			if body != nil || !(errors.Is(err, ErrStaleReadingCursor) || errors.Is(err, ErrForbidden)) {
				t.Fatal("changed read accepted", err)
			}
		})
	}
}
func TestProjectReadingCurrentPrimaryLinksAndOwnershipOverrideStaleRefs(t *testing.T) {
	s, r := projectReadingFixture()
	r.meetings["author|00"].ProjectIDs = nil
	r.research["00"].TrashedAt = "2026-10-01"
	r.research["01"].ProjectIDs = nil
	r.research["02"].UserID = "different-owner"
	for _, section := range []string{"meetings", "research"} {
		page := readProjectPage(t, s, url.Values{"section": {section}})
		data := page["dataJson"].(string)
		if strings.Contains(data, `"00"`) || (section == "research" && (strings.Contains(data, `"01"`) || strings.Contains(data, `"02"`))) {
			t.Fatal("stale reference or foreign owner granted data")
		}
	}
	r.afterQuery = func() { r.projects["p"].OwnerUserID = "new-owner" }
	if body, err := s.Read(context.Background(), "reader", "p", url.Values{"section": {"members"}}); err == nil || body != nil {
		t.Fatal("revocation during source query accepted")
	}
}
func TestProjectReadingEmptyFilteredGroupsStillContinueAndSetOrderIsStable(t *testing.T) {
	s, r := projectReadingFixture()
	q := url.Values{"section": {"insights"}, "types": {"risk"}}
	page := readProjectPage(t, s, q)
	if page["dataJson"] != "[]" || page["page"].(map[string]interface{})["complete"] != false {
		t.Fatal("filtered group lost continuation")
	}
	q.Set("cursor", page["page"].(map[string]interface{})["nextCursor"].(string))
	r.projects["p"].AccountIDs = []string{"a", "b"}
	page = readProjectPage(t, s, q)
	if page["page"].(map[string]interface{})["complete"] != true {
		t.Fatal("set reordering broke read")
	}
}
