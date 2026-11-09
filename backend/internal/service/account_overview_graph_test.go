package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
)

const graphDocHash = "0123456789abcdef0123456789abcdef"

func (r *overviewTestRepo) ListAccountLinks(_ context.Context, _ string, cursor string, limit int32) ([]model.AccountLink, *string, error) {
	start := 0
	if cursor != "" {
		start, _ = strconv.Atoi(cursor)
	}
	rows := r.links.links[min(start, len(r.links.links)):]
	if len(rows) > int(limit) {
		next := strconv.Itoa(start + int(limit))
		return rows[:limit], &next, nil
	}
	return rows, nil, nil
}

func (r *overviewTestRepo) ListAccountMembers(context.Context, string) ([]model.AccountMember, error) {
	return r.members, nil
}

func (r *overviewTestRepo) GetAccountContact(_ context.Context, _ string, id string) (*model.AccountContact, error) {
	return r.contacts[id], nil
}

type graphTestDocs struct {
	docs map[string]*model.CrawledDocument
}

func (d *graphTestDocs) GetDocument(_ context.Context, sourceID, docHash string) (*model.CrawledDocument, error) {
	return d.docs[sourceID+":"+docHash], nil
}

func newGraphTest() (*AccountOverviewService, *overviewTestRepo, *graphTestDocs, *overviewTestLinks) {
	svc, repo, _, links := newOverviewTest()
	docs := &graphTestDocs{docs: map[string]*model.CrawledDocument{}}
	svc.docs = docs
	repo.contacts = map[string]*model.AccountContact{}
	return svc, repo, docs, links
}

func graphIndex(g *model.AccountGraph) (map[string]model.GraphNode, map[string]bool) {
	nodes := map[string]model.GraphNode{}
	for _, n := range g.Nodes {
		nodes[n.ID] = n
	}
	edges := map[string]bool{}
	for _, e := range g.Edges {
		edges[e.From+">"+e.To+">"+e.Kind] = true
	}
	return nodes, edges
}

func checkGraphConsistent(t *testing.T, g *model.AccountGraph) {
	t.Helper()
	nodes, _ := graphIndex(g)
	if len(nodes) != len(g.Nodes) {
		t.Fatalf("duplicate nodes")
	}
	for _, e := range g.Edges {
		if _, ok := nodes[e.From]; !ok {
			t.Fatalf("edge from missing node %+v", e)
		}
		if _, ok := nodes[e.To]; !ok {
			t.Fatalf("edge to missing node %+v", e)
		}
	}
	if raw, _ := json.Marshal(g); len(raw) > MaxAccountGraphBytes {
		t.Fatalf("graph is %d bytes", len(raw))
	}
}

func TestGraph_RequiresDirectMember(t *testing.T) {
	svc, repo, _, _ := newGraphTest()
	repo.member = false
	if _, err := svc.Graph(context.Background(), linkUser, linkAcct); err != ErrForbidden {
		t.Fatalf("err=%v", err)
	}
}

func TestGraph_HidesEdgesToUnpublishedMeeting(t *testing.T) {
	svc, repo, docs, links := newGraphTest()
	newsID := linkSource + ":" + graphDocHash
	docs.docs[newsID] = &model.CrawledDocument{SourceID: linkSource, DocHash: graphDocHash, Type: "news", Title: "news title"}
	repo.meetings["o1:m1"] = flowMeeting("o1", "m1", signalsNow.Add(-day), false)
	repo.meetings["o1:m2"] = flowMeeting("o1", "m2", signalsNow.Add(-day), true)
	repo.acctRefs = []model.MeetingRef{acctMeetingRef("o1", "m1", signalsNow.Add(-day)), acctMeetingRef("o1", "m2", signalsNow.Add(-2*day))}
	links.links = []model.AccountLink{{FromKind: "news", FromID: newsID, ToKind: "meeting", ToID: "o1:m1", Origin: "auto", Evidence: "x"}}

	g, err := svc.Graph(context.Background(), linkUser, linkAcct)
	if err != nil {
		t.Fatal(err)
	}
	checkGraphConsistent(t, g)
	nodes, edges := graphIndex(g)
	if _, ok := nodes["meeting:o1:m1"]; ok {
		t.Fatalf("unpublished meeting produced a node: %+v", g.Nodes)
	}
	if _, ok := nodes["news:"+newsID]; ok {
		t.Fatalf("dropped link still produced its news node: %+v", g.Nodes)
	}
	for _, e := range g.Edges {
		if strings.Contains(e.From, "m1") || strings.Contains(e.To, "m1") {
			t.Fatalf("edge to unpublished meeting: %+v", e)
		}
	}
	if !edges["meeting:o1:m2>account:"+linkAcct+">meeting_account"] {
		t.Fatalf("published meeting missing: %+v", g.Edges)
	}
}

func TestGraph_LinksAndContacts(t *testing.T) {
	svc, repo, docs, links := newGraphTest()
	repo.addProject(&model.Project{ProjectID: "p1", Name: "P1", AccountIDs: []string{linkAcct}})
	repo.projects["gone"] = &model.Project{ProjectID: "gone", Name: "G", AccountIDs: []string{"other"}}
	repo.refs = append(repo.refs, model.ProjectRef{AccountID: linkAcct, ProjectID: "gone"})
	newsID := linkSource + ":" + graphDocHash
	docs.docs[newsID] = &model.CrawledDocument{SourceID: linkSource, DocHash: graphDocHash, Type: "news", Title: "news title"}
	deletedID := linkSource + ":" + strings.Repeat("e", 32)
	docs.docs[deletedID] = &model.CrawledDocument{Type: "news", Title: "deleted", QualityStatus: "DELETED"}
	foreignID := "acct_other_0000abcd:" + graphDocHash
	docs.docs[foreignID] = &model.CrawledDocument{Type: "news", Title: "foreign"}
	techID := techLinkSourceID + ":" + graphDocHash
	docs.docs[techID] = &model.CrawledDocument{Type: "tech", Title: "tech title"}
	contactID := flowEventID(90)
	repo.contacts[contactID] = &model.AccountContact{ContactID: contactID, Name: "김철수"}
	event := flowEvent(0, signalsNow.Add(-day))
	event.Attendees = []model.EventAttendee{{ContactID: contactID, Name: "stale"}, {ContactID: flowEventID(91), Name: "removed"}, {Name: "no contact"}}
	repo.addEvent(event)
	repo.meetings["o1:m1"] = flowMeeting("o1", "m1", signalsNow.Add(-day), true, "p1", "gone")
	repo.acctRefs = []model.MeetingRef{acctMeetingRef("o1", "m1", signalsNow.Add(-day))}
	repo.members = []model.AccountMember{{PK: model.PrefixAccount + linkAcct, SK: model.PrefixMember + linkUser, AccountID: linkAcct, UserID: linkUser, Email: "a@example.com", Role: "owner"}}
	links.links = []model.AccountLink{
		{FromKind: "news", FromID: newsID, ToKind: "project", ToID: "p1", Origin: "manual"},
		{FromKind: "news", FromID: deletedID, ToKind: "project", ToID: "p1", Origin: "manual"},
		{FromKind: "news", FromID: foreignID, ToKind: "project", ToID: "p1", Origin: "manual"},
		{FromKind: "tech", FromID: techID, ToKind: "project", ToID: "gone", Origin: "manual"},
		{FromKind: "event", FromID: event.EventID, ToKind: "project", ToID: "p1", Origin: "auto"},
		{FromKind: "news", FromID: newsID, ToKind: "meeting", ToID: "o1:m1", Origin: "auto"},
	}

	g, err := svc.Graph(context.Background(), linkUser, linkAcct)
	if err != nil {
		t.Fatal(err)
	}
	checkGraphConsistent(t, g)
	nodes, edges := graphIndex(g)
	acct := "account:" + linkAcct
	for _, want := range []string{
		"project:p1>" + acct + ">project_account",
		"meeting:o1:m1>" + acct + ">meeting_account",
		"meeting:o1:m1>project:p1>meeting_project",
		"news:" + newsID + ">project:p1>link",
		"news:" + newsID + ">meeting:o1:m1>link",
		"event:" + event.EventID + ">project:p1>link",
		"contact:" + contactID + ">event:" + event.EventID + ">attends",
		"member:" + linkUser + ">" + acct + ">member_of",
	} {
		if !edges[want] {
			t.Fatalf("missing edge %s in %+v", want, g.Edges)
		}
	}
	for _, absent := range []string{"project:gone", "news:" + deletedID, "news:" + foreignID, "tech:" + techID, "contact:" + flowEventID(91)} {
		if _, ok := nodes[absent]; ok {
			t.Fatalf("unexpected node %s", absent)
		}
	}
	if nodes["contact:"+contactID].Label != "김철수" || nodes["news:"+newsID].Label != "news title" || !strings.Contains(nodes["member:"+linkUser].Label, "a@example.com") {
		t.Fatalf("labels: %+v", g.Nodes)
	}
	if g.Truncated {
		t.Fatalf("unexpected truncation")
	}
}

func TestGraph_DropsLinkToRemovedEvent(t *testing.T) {
	svc, repo, _, links := newGraphTest()
	repo.addProject(&model.Project{ProjectID: "p1", Name: "P1", AccountIDs: []string{linkAcct}})
	removed := flowEventID(5)
	links.links = []model.AccountLink{{FromKind: "event", FromID: removed, ToKind: "project", ToID: "p1", Origin: "manual"}}
	g, err := svc.Graph(context.Background(), linkUser, linkAcct)
	if err != nil {
		t.Fatal(err)
	}
	checkGraphConsistent(t, g)
	nodes, _ := graphIndex(g)
	if _, ok := nodes["event:"+removed]; ok {
		t.Fatalf("removed event produced a node")
	}
	for _, e := range g.Edges {
		if e.Kind == model.GraphEdgeLink {
			t.Fatalf("link edge kept: %+v", e)
		}
	}
	if _, ok := nodes["project:p1"]; !ok {
		t.Fatalf("project missing")
	}
}

func TestGraph_NodeCap(t *testing.T) {
	svc, repo, _, _ := newGraphTest()
	for i := 0; i < 90; i++ {
		repo.addProject(&model.Project{ProjectID: fmt.Sprintf("p%03d", i), Name: "P", AccountIDs: []string{linkAcct}})
	}
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("m%03d", i)
		repo.meetings["o1:"+id] = flowMeeting("o1", id, signalsNow.Add(-day), true, "p000", "p001", "p002", "p003")
		repo.acctRefs = append(repo.acctRefs, acctMeetingRef("o1", id, signalsNow.Add(-day)))
	}
	g, err := svc.Graph(context.Background(), linkUser, linkAcct)
	if err != nil {
		t.Fatal(err)
	}
	checkGraphConsistent(t, g)
	if len(g.Nodes) != maxGraphNodes || len(g.Edges) > maxGraphEdges || !g.Truncated {
		t.Fatalf("nodes=%d edges=%d truncated=%v", len(g.Nodes), len(g.Edges), g.Truncated)
	}
	if len(g.Edges) != maxGraphEdges {
		t.Fatalf("edge cap not reached: %d", len(g.Edges))
	}
}

func TestGraph_LinkReadBound(t *testing.T) {
	svc, repo, _, links := newGraphTest()
	repo.addProject(&model.Project{ProjectID: "p1", Name: "P1", AccountIDs: []string{linkAcct}})
	for i := 0; i < maxGraphLinkRows+50; i++ {
		links.links = append(links.links, model.AccountLink{FromKind: "event", FromID: flowEventID(i), ToKind: "project", ToID: "p1", Origin: "manual"})
	}
	g, err := svc.Graph(context.Background(), linkUser, linkAcct)
	if err != nil {
		t.Fatal(err)
	}
	if !g.Truncated {
		t.Fatalf("link read bound did not truncate")
	}
}

func TestGraph_LabelsTruncated(t *testing.T) {
	svc, repo, docs, links := newGraphTest()
	long := strings.Repeat("가", 100)
	repo.addProject(&model.Project{ProjectID: "p1", Name: long, AccountIDs: []string{linkAcct}})
	newsID := linkSource + ":" + graphDocHash
	docs.docs[newsID] = &model.CrawledDocument{Type: "news", Title: long}
	links.links = []model.AccountLink{{FromKind: "news", FromID: newsID, ToKind: "project", ToID: "p1", Origin: "manual"}}
	g, err := svc.Graph(context.Background(), linkUser, linkAcct)
	if err != nil {
		t.Fatal(err)
	}
	nodes, _ := graphIndex(g)
	for _, id := range []string{"project:p1", "news:" + newsID} {
		label := nodes[id].Label
		if utf8.RuneCountInString(label) != maxGraphLabelRunes || !strings.HasPrefix(long, label) {
			t.Fatalf("%s label %q", id, label)
		}
	}
}
