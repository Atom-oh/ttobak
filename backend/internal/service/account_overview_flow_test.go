package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func (r *overviewTestRepo) ListProjectStageHistory(_ context.Context, projectID string, limit int) ([]model.ProjectStageChange, error) {
	rows := r.stageHist[projectID]
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (r *overviewTestRepo) ListAccountEvents(_ context.Context, _ string, _, _ string, descending bool, limit int, _ string) ([]model.AccountEvent, string, error) {
	if !descending {
		return nil, "", fmt.Errorf("flow must read newest events first")
	}
	rows := r.eventList
	next := ""
	if len(rows) > limit {
		rows, next = rows[:limit], "EVENT#more"
	} else if r.eventMore {
		next = "EVENT#more"
	}
	return rows, next, nil
}

func (r *overviewTestRepo) ListMeetingRefsForAccountPage(_ context.Context, _ string, _ string, limit int32) ([]model.MeetingRef, *string, error) {
	rows := r.acctRefs
	var next *string
	if len(rows) > int(limit) || r.acctRefMore {
		if len(rows) > int(limit) {
			rows = rows[:limit]
		}
		more := "cursor"
		next = &more
	}
	return rows, next, nil
}

func flowEventID(i int) string { return fmt.Sprintf("0f8c1d2e-3a4b-4c5d-8e6f-%012x", i+1) }

func flowEvent(i int, start time.Time) model.AccountEvent {
	return model.AccountEvent{EventID: flowEventID(i), Title: fmt.Sprintf("event %d", i), StartAt: start.UTC().Format(time.RFC3339)}
}

func (r *overviewTestRepo) addEvent(e model.AccountEvent) {
	r.eventList = append(r.eventList, e)
	copy := e
	r.events[e.EventID] = &copy
}

func acctMeetingRef(owner, id string, date time.Time) model.MeetingRef {
	return model.MeetingRef{PK: model.PrefixAccount + linkAcct, SK: model.PrefixMeetingRef + date.UTC().Format(time.RFC3339) + "#" + id,
		AccountID: linkAcct, MeetingID: id, OwnerUserID: owner, Title: "ref " + id, Date: date, EntityType: model.EntityTypeMeetingRef}
}

func flowMeeting(owner, id string, date time.Time, published bool, projects ...string) *model.Meeting {
	m := actionMeeting(owner, id, date, "", published, projects...)
	m.Title = "meeting " + id
	return m
}

func TestFlow_WorkshopToStage(t *testing.T) {
	svc, repo, _, links := newOverviewTest()
	repo.addProject(&model.Project{ProjectID: "p1", Name: "P1", Stage: "Qualified", StageChangedAt: ago(21 * day), AccountIDs: []string{linkAcct}})
	workshop := flowEvent(0, signalsNow.Add(-40*day))
	repo.addEvent(workshop)
	links.links = []model.AccountLink{{FromKind: "event", FromID: workshop.EventID, ToKind: "project", ToID: "p1", Origin: "auto", Evidence: "workshop on p1"}}
	repo.meetingRef["p1"] = []model.ProjectMeetingRef{{MeetingID: "m2", OwnerUserID: "o1"}, {MeetingID: "m1", OwnerUserID: "o1"}, {MeetingID: "m3", OwnerUserID: "o1"}}
	repo.meetings["o1:m1"] = flowMeeting("o1", "m1", signalsNow.Add(-35*day), true, "p1")
	repo.meetings["o1:m2"] = flowMeeting("o1", "m2", signalsNow.Add(-30*day), true, "p1")
	repo.meetings["o1:m3"] = flowMeeting("o1", "m3", signalsNow.Add(-25*day), false, "p1") // unpublished
	repo.stageHist["p1"] = []model.ProjectStageChange{{PK: "PROJECT#p1", SK: "STAGEHIST#x", ProjectID: "p1", From: "Prospect", To: "Qualified", ChangedAt: *ago(21 * day), EntityType: model.EntityTypeProjectStageChange}}
	svc.insights = &overviewTestInsights{page: &model.AccountInsightsPage{Insights: []model.AccountInsightDTO{
		{Type: "need", Text: strings.Repeat("가", 400), SourceType: "meeting", SourceID: "m1", OccurredAt: signalsNow.Add(-35 * day)},
	}}}

	flow, err := svc.Flow(context.Background(), linkUser, linkAcct, signalsNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(flow.Rows) != 1 || flow.Truncated {
		t.Fatalf("got %+v", flow)
	}
	row := flow.Rows[0]
	if row.ProjectID != "p1" || len(row.Activity) != 1 || row.Activity[0].ID != workshop.EventID || row.Activity[0].Kind != "event" ||
		row.Activity[0].Origin != "auto" || row.Activity[0].Evidence != "workshop on p1" || row.Activity[0].Title != "event 0" {
		t.Fatalf("activity %+v", row.Activity)
	}
	if len(row.Response) != 2 || row.Response[0].ID != "o1:m1" || row.Response[1].ID != "o1:m2" || row.Response[0].Kind != "meeting" {
		t.Fatalf("response %+v", row.Response)
	}
	if n := len([]rune(row.Response[0].Need)); n != maxSignalsTextRunes || row.Response[1].Need != "" || row.Response[0].Title != "meeting m1" {
		t.Fatalf("need runes=%d response %+v", n, row.Response[1])
	}
	if row.Oppty == nil || row.Oppty.ProjectID != "p1" || len(row.History) != 1 || row.History[0].To != "Qualified" {
		t.Fatalf("oppty/history %+v %+v", row.Oppty, row.History)
	}
	if row.Result.State != model.FlowStateStalled || !stallCodes(row.Result.Stall)[model.StallStageStale] {
		t.Fatalf("result %+v", row.Result)
	}
	if len(flow.Unassigned.Activity) != 0 || flow.Unassigned.ProjectID != "" {
		t.Fatalf("linked workshop must not be unassigned: %+v", flow.Unassigned)
	}
	raw, _ := json.Marshal(flow)
	if strings.Contains(string(raw), `"PK"`) || strings.Contains(string(raw), "STAGEHIST#") {
		t.Fatalf("history leaks storage keys: %s", raw)
	}
}

func TestFlow_SkipsProjectNoLongerInAccount(t *testing.T) {
	svc, repo, _, _ := newOverviewTest()
	repo.addProject(&model.Project{ProjectID: "p1", Name: "P1", Stage: "Qualified", AccountIDs: []string{linkAcct}})
	repo.addProject(&model.Project{ProjectID: "p2", Name: "left", Stage: "Qualified", AccountIDs: []string{"other"}})
	repo.refs = append(repo.refs, model.ProjectRef{AccountID: linkAcct, ProjectID: "missing"})
	flow, err := svc.Flow(context.Background(), linkUser, linkAcct, signalsNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(flow.Rows) != 1 || flow.Rows[0].ProjectID != "p1" {
		t.Fatalf("rows %+v", flow.Rows)
	}

	// A meeting still listing the departed project is not shown in any row.
	repo.meetingRef["p2"] = []model.ProjectMeetingRef{{MeetingID: "m1", OwnerUserID: "o1"}}
	repo.meetings["o1:m1"] = flowMeeting("o1", "m1", signalsNow.Add(-day), true, "p2")
	repo.acctRefs = []model.MeetingRef{acctMeetingRef("o1", "m1", signalsNow.Add(-day))}
	flow, err = svc.Flow(context.Background(), linkUser, linkAcct, signalsNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(flow.Rows) != 1 || len(flow.Rows[0].Response) != 0 || len(flow.Unassigned.Response) != 1 {
		t.Fatalf("got %+v", flow)
	}
	// Membership is re-read per call.
	repo.member = false
	if _, err := svc.Flow(context.Background(), linkUser, linkAcct, signalsNow); !errors.Is(err, ErrForbidden) {
		t.Fatalf("removed member err=%v", err)
	}
}

func TestFlow_UnassignedHoldsUnlinkedEvents(t *testing.T) {
	svc, repo, _, links := newOverviewTest()
	repo.addProject(&model.Project{ProjectID: "p1", Name: "P1", Stage: "Qualified", AccountIDs: []string{linkAcct}})
	repo.addProject(&model.Project{ProjectID: "pX", Name: "left", AccountIDs: []string{"other"}})
	linked, unlinked, leftLinked := flowEvent(0, signalsNow.Add(-day)), flowEvent(1, signalsNow.Add(-2*day)), flowEvent(2, signalsNow.Add(-3*day))
	for _, e := range []model.AccountEvent{linked, unlinked, leftLinked} {
		repo.addEvent(e)
	}
	links.links = []model.AccountLink{
		{FromKind: "event", FromID: linked.EventID, ToKind: "project", ToID: "p1", Origin: "manual"},
		{FromKind: "event", FromID: leftLinked.EventID, ToKind: "project", ToID: "pX", Origin: "manual"},
	}
	repo.acctRefs = []model.MeetingRef{
		acctMeetingRef("o1", "mA", signalsNow.Add(-day)),
		acctMeetingRef("o1", "mB", signalsNow.Add(-2*day)),
		acctMeetingRef("o1", "mC", signalsNow.Add(-3*day)),
	}
	repo.meetings["o1:mA"] = flowMeeting("o1", "mA", signalsNow.Add(-day), true)
	repo.meetings["o1:mB"] = flowMeeting("o1", "mB", signalsNow.Add(-2*day), true, "p1")
	repo.meetings["o1:mC"] = flowMeeting("o1", "mC", signalsNow.Add(-3*day), false)
	svc.insights = &overviewTestInsights{page: &model.AccountInsightsPage{Insights: []model.AccountInsightDTO{
		{Type: "need", Text: "needs DR", SourceType: "meeting", SourceID: "mA", OccurredAt: signalsNow.Add(-day)},
	}}}

	flow, err := svc.Flow(context.Background(), linkUser, linkAcct, signalsNow)
	if err != nil {
		t.Fatal(err)
	}
	un := flow.Unassigned
	if un.ProjectID != "" || len(un.Activity) != 2 || un.Activity[0].ID != unlinked.EventID || un.Activity[1].ID != leftLinked.EventID {
		t.Fatalf("unassigned activity %+v", un.Activity)
	}
	if un.Activity[0].Origin != "" || un.Activity[0].Evidence != "" {
		t.Fatalf("unassigned has no link origin: %+v", un.Activity[0])
	}
	if len(un.Response) != 1 || un.Response[0].ID != "o1:mA" || un.Response[0].Need != "needs DR" {
		t.Fatalf("unassigned response %+v", un.Response)
	}
	if un.Activity == nil || un.History == nil || un.Result.Stall == nil || un.Oppty != nil {
		t.Fatalf("unassigned shape %+v", un)
	}
	if len(flow.Rows) != 1 || len(flow.Rows[0].Activity) != 1 || flow.Rows[0].Activity[0].ID != linked.EventID {
		t.Fatalf("rows %+v", flow.Rows)
	}
	for _, p := range links.prefixes {
		if !strings.HasPrefix(p, "LINK#event#") && !strings.HasPrefix(p, "LINKTO#project#") {
			t.Fatalf("unexpected link read %q", p)
		}
	}

	// Only the newest events are examined; a hit bound flags the response.
	links.bound = 1
	links.links = append(links.links, model.AccountLink{FromKind: "event", FromID: linked.EventID, ToKind: "project", ToID: "p9"})
	flow, err = svc.Flow(context.Background(), linkUser, linkAcct, signalsNow)
	if err != nil || !flow.Truncated {
		t.Fatalf("bound: %+v err=%v", flow, err)
	}
}

func TestFlow_UnknownWhenNoHistory(t *testing.T) {
	svc, repo, _, _ := newOverviewTest()
	repo.addProject(&model.Project{ProjectID: "p1", Name: "no time", Stage: "Qualified", AccountIDs: []string{linkAcct}})
	repo.addProject(&model.Project{ProjectID: "p2", Name: "unmapped", Stage: "Someday", StageChangedAt: ago(day), AccountIDs: []string{linkAcct}})
	repo.addProject(&model.Project{ProjectID: "p3", Name: "history", Stage: "Qualified", AccountIDs: []string{linkAcct}})
	repo.addProject(&model.Project{ProjectID: "p4", Name: "done", Stage: "Launched", AccountIDs: []string{linkAcct}})
	repo.stageHist["p3"] = []model.ProjectStageChange{{PK: "PROJECT#p3", SK: "STAGEHIST#x", ProjectID: "p3", To: "Qualified", ChangedAt: *ago(day), EntityType: model.EntityTypeProjectStageChange}}
	// A malformed history row (other project) is ignored.
	repo.stageHist["p1"] = []model.ProjectStageChange{{PK: "PROJECT#p9", SK: "STAGEHIST#x", ProjectID: "p9", To: "Qualified", EntityType: model.EntityTypeProjectStageChange}}
	flow, err := svc.Flow(context.Background(), linkUser, linkAcct, signalsNow)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, row := range flow.Rows {
		states[row.ProjectID] = row.Result.State
		if row.ProjectID == "p1" && len(row.History) != 0 {
			t.Fatalf("malformed history kept: %+v", row.History)
		}
	}
	want := map[string]string{"p1": model.FlowStateUnknown, "p2": model.FlowStateUnknown, "p3": model.FlowStateStalled, "p4": model.FlowStateTerminal}
	for id, s := range want {
		if states[id] != s {
			t.Fatalf("%s state %q want %q (%v)", id, states[id], s, states)
		}
	}
	if flow.Rows[len(flow.Rows)-1].ProjectID != "p4" {
		t.Fatalf("terminal must sort last: %+v", flow.Rows)
	}

	// A project with an upcoming linked event and a fresh stage is ACTIVE.
	repo.projects["p3"].StageChangedAt = ago(day)
	e := flowEvent(0, signalsNow.Add(day))
	repo.addEvent(e)
	svc.repo.(*overviewTestRepo).links.links = []model.AccountLink{{FromKind: "event", FromID: e.EventID, ToKind: "project", ToID: "p3", Origin: "manual"}}
	flow, err = svc.Flow(context.Background(), linkUser, linkAcct, signalsNow)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range flow.Rows {
		if row.ProjectID == "p3" && row.Result.State != model.FlowStateActive {
			t.Fatalf("p3 %+v", row.Result)
		}
	}
}

func TestFlow_CapsAndTruncatedFlag(t *testing.T) {
	svc, repo, _, _ := newOverviewTest()
	for i := 0; i < 25; i++ {
		stage := "Qualified"
		if i < 3 {
			stage = "Closed Lost"
		}
		repo.addProject(&model.Project{ProjectID: fmt.Sprintf("p%02d", i), Name: "P", Stage: stage, StageChangedAt: ago(time.Duration(i) * time.Hour), AccountIDs: []string{linkAcct}})
	}
	flow, err := svc.Flow(context.Background(), linkUser, linkAcct, signalsNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(flow.Rows) != maxFlowRows || !flow.Truncated {
		t.Fatalf("rows=%d truncated=%v", len(flow.Rows), flow.Truncated)
	}
	if flow.Rows[0].ProjectID != "p03" || flow.Rows[19].ProjectID != "p22" {
		t.Fatalf("order: first=%s last=%s", flow.Rows[0].ProjectID, flow.Rows[19].ProjectID)
	}
	for _, row := range flow.Rows {
		if row.Result.State == model.FlowStateTerminal {
			t.Fatalf("terminal row %s ahead of nonterminal ones", row.ProjectID)
		}
	}

	// Column caps and the response byte bound.
	svc, repo, _, links := newOverviewTest()
	big := strings.Repeat("가", 400)
	repo.addProject(&model.Project{ProjectID: "p1", Name: big, Stage: "Qualified", StageChangedAt: ago(day), AccountIDs: []string{linkAcct}})
	for i := 0; i < 8; i++ {
		e := flowEvent(i, signalsNow.Add(-time.Duration(i+1)*day))
		e.Title = big
		repo.addEvent(e)
		links.links = append(links.links, model.AccountLink{FromKind: "event", FromID: e.EventID, ToKind: "project", ToID: "p1", Origin: "auto", Evidence: big})
		id := fmt.Sprintf("m%d", i)
		repo.meetingRef["p1"] = append(repo.meetingRef["p1"], model.ProjectMeetingRef{MeetingID: id, OwnerUserID: "o1"})
		m := flowMeeting("o1", id, signalsNow.Add(-time.Duration(i+1)*day), true, "p1")
		m.Title = big
		repo.meetings["o1:"+id] = m
	}
	for i := 0; i < 15; i++ {
		repo.stageHist["p1"] = append(repo.stageHist["p1"], model.ProjectStageChange{PK: "PROJECT#p1", SK: "STAGEHIST#x", ProjectID: "p1", From: big, To: big, ChangedAt: *ago(day), EntityType: model.EntityTypeProjectStageChange})
	}
	flow, err = svc.Flow(context.Background(), linkUser, linkAcct, signalsNow)
	if err != nil {
		t.Fatal(err)
	}
	row := flow.Rows[0]
	if len(row.Activity) != maxFlowColumnItems || len(row.Response) != maxFlowColumnItems || len(row.History) != maxFlowHistory {
		t.Fatalf("caps: a=%d r=%d h=%d", len(row.Activity), len(row.Response), len(row.History))
	}
	if row.Activity[0].Date >= row.Activity[1].Date || len([]rune(row.Activity[0].Title)) != maxSignalsTextRunes || len([]rune(row.Activity[0].Evidence)) != maxSignalsTextRunes {
		t.Fatalf("activity order/caps %+v", row.Activity[:2])
	}

	// Many rows of capped text are trimmed to fit the response bound.
	for i := 2; i < 20; i++ {
		id := fmt.Sprintf("p%d", i)
		repo.addProject(&model.Project{ProjectID: id, Name: big, Stage: "Qualified", StageChangedAt: ago(day), AccountIDs: []string{linkAcct}})
		repo.meetingRef[id] = repo.meetingRef["p1"]
		repo.stageHist[id] = repo.stageHist["p1"]
		for k := 0; k < 8; k++ {
			repo.meetings[fmt.Sprintf("o1:m%d", k)].ProjectIDs = append(repo.meetings[fmt.Sprintf("o1:m%d", k)].ProjectIDs, id)
		}
	}
	flow, err = svc.Flow(context.Background(), linkUser, linkAcct, signalsNow)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(flow)
	if len(raw) > MaxAccountFlowBytes || !flow.Truncated || len(flow.Rows) == 0 {
		t.Fatalf("bytes=%d truncated=%v rows=%d", len(raw), flow.Truncated, len(flow.Rows))
	}
}
