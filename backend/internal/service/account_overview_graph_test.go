package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
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
	r.contactGets++
	if err := r.contactErrs[id]; err != nil {
		return nil, err
	}
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

func graphContactFixture(repo *overviewTestRepo) (good, bad string, event model.AccountEvent) {
	good, bad = flowEventID(80), flowEventID(81)
	repo.contacts[good] = &model.AccountContact{ContactID: good, Name: "good"}
	event = flowEvent(0, signalsNow.Add(-day))
	event.Attendees = []model.EventAttendee{{ContactID: bad, Name: "bad"}, {ContactID: good, Name: "good"}}
	repo.addEvent(event)
	return good, bad, event
}

func TestGraph_SkipsUnopenableContact(t *testing.T) {
	svc, repo, _, _ := newGraphTest()
	good, bad, event := graphContactFixture(repo)
	repo.contactErrs = map[string]error{bad: fmt.Errorf("open contact: %w", fmt.Errorf("%w: tampered", repository.ErrContactUnreadable))}
	g, err := svc.Graph(context.Background(), linkUser, linkAcct)
	if err != nil {
		t.Fatalf("one unopenable contact failed the graph: %v", err)
	}
	checkGraphConsistent(t, g)
	nodes, edges := graphIndex(g)
	if _, ok := nodes["contact:"+bad]; ok {
		t.Fatal("unopenable contact produced a node")
	}
	if !edges["contact:"+good+">event:"+event.EventID+">attends"] || !g.Truncated {
		t.Fatalf("good contact missing or not truncated: %+v truncated=%v", g.Edges, g.Truncated)
	}
}

func TestGraph_FailsWhenSealingUnavailable(t *testing.T) {
	for name, err := range map[string]error{
		"key":       fmt.Errorf("open contact: %w", repository.ErrContactEncryptionUnavailable),
		"transport": errors.New("dynamodb: request timeout"),
	} {
		svc, repo, _, _ := newGraphTest()
		_, bad, _ := graphContactFixture(repo)
		repo.contactErrs = map[string]error{bad: err}
		if _, got := svc.Graph(context.Background(), linkUser, linkAcct); !errors.Is(got, err) {
			t.Fatalf("%s: got %v", name, got)
		}
	}
}

func TestGraph_SkipsUnopenableLinkedEvent(t *testing.T) {
	svc, repo, _, links := newGraphTest()
	repo.addProject(&model.Project{ProjectID: "p1", Name: "P1", AccountIDs: []string{linkAcct}})
	bad := flowEventID(7)
	repo.eventErrs = map[string]error{bad: fmt.Errorf("%w: tampered", repository.ErrEventUnreadable)}
	links.links = []model.AccountLink{{FromKind: "event", FromID: bad, ToKind: "project", ToID: "p1", Origin: "manual"}}
	g, err := svc.Graph(context.Background(), linkUser, linkAcct)
	if err != nil {
		t.Fatalf("one unopenable event failed the graph: %v", err)
	}
	nodes, _ := graphIndex(g)
	if _, ok := nodes["event:"+bad]; ok || !g.Truncated {
		t.Fatalf("event node=%v truncated=%v", ok, g.Truncated)
	}
	repo.eventErrs[bad] = fmt.Errorf("open event: %w", repository.ErrEventEncryptionUnavailable)
	if _, err := svc.Graph(context.Background(), linkUser, linkAcct); !errors.Is(err, repository.ErrEventEncryptionUnavailable) {
		t.Fatalf("missing key must fail: %v", err)
	}
}

// Link rows whose endpoints are not nodes are not hydrated once the node cap
// is full, and distinct endpoint lookups stay bounded either way.
func TestGraph_LinkHydrationBounded(t *testing.T) {
	t.Run("node cap full", func(t *testing.T) {
		svc, repo, _, links := newGraphTest()
		for i := 0; i < 90; i++ {
			repo.addProject(&model.Project{ProjectID: fmt.Sprintf("p%03d", i), Name: "P", AccountIDs: []string{linkAcct}})
		}
		for i := 0; i < 100; i++ {
			id := fmt.Sprintf("m%03d", i)
			repo.meetings["o1:"+id] = flowMeeting("o1", id, signalsNow.Add(-day), true)
			repo.acctRefs = append(repo.acctRefs, acctMeetingRef("o1", id, signalsNow.Add(-day)))
		}
		// Events beyond the newest 50 are reachable only through links.
		for i := 0; i < 600; i++ {
			e := flowEvent(i, signalsNow.Add(-day))
			repo.events[e.EventID] = &e
			links.links = append(links.links, model.AccountLink{FromKind: "event", FromID: e.EventID, ToKind: "project", ToID: "p000", Origin: "manual"})
		}
		repo.meetingGets = 0
		g, err := svc.Graph(context.Background(), linkUser, linkAcct)
		if err != nil {
			t.Fatal(err)
		}
		checkGraphConsistent(t, g)
		if len(g.Nodes) != maxGraphNodes || !g.Truncated {
			t.Fatalf("nodes=%d truncated=%v", len(g.Nodes), g.Truncated)
		}
		if repo.eventGets != 0 {
			t.Fatalf("events hydrated past the node cap: %d", repo.eventGets)
		}
	})
	t.Run("lookup budget", func(t *testing.T) {
		svc, repo, _, links := newGraphTest()
		repo.addProject(&model.Project{ProjectID: "p1", Name: "P1", AccountIDs: []string{linkAcct}})
		for i := 0; i < 600; i++ {
			// Removed events: each row costs a lookup and adds nothing.
			links.links = append(links.links, model.AccountLink{FromKind: "event", FromID: flowEventID(i), ToKind: "project", ToID: "p1", Origin: "manual"})
			links.links = append(links.links, model.AccountLink{FromKind: "news", FromID: linkSource + ":" + graphDocHash, ToKind: "meeting", ToID: fmt.Sprintf("o1:gone%03d", i), Origin: "manual"})
		}
		g, err := svc.Graph(context.Background(), linkUser, linkAcct)
		if err != nil {
			t.Fatal(err)
		}
		checkGraphConsistent(t, g)
		if !g.Truncated || repo.eventGets+repo.meetingGets > maxGraphLinkLookups {
			t.Fatalf("lookups unbounded: events=%d meetings=%d truncated=%v", repo.eventGets, repo.meetingGets, g.Truncated)
		}
	})
}

// Attendee contacts are read under a budget and never once the node cap is
// full, however many events and attendees the account has.
func TestGraph_ContactReadsBounded(t *testing.T) {
	contactID := func(i int) string { return fmt.Sprintf("1f8c1d2e-3a4b-4c5d-8e6f-%012x", i+1) }
	fixture := func(fillCap bool) (*AccountOverviewService, *overviewTestRepo) {
		svc, repo, _, links := newGraphTest()
		repo.addProject(&model.Project{ProjectID: "p000", Name: "P", AccountIDs: []string{linkAcct}})
		if fillCap {
			for i := 1; i < 90; i++ {
				repo.addProject(&model.Project{ProjectID: fmt.Sprintf("p%03d", i), Name: "P", AccountIDs: []string{linkAcct}})
			}
			for i := 0; i < 100; i++ {
				id := fmt.Sprintf("m%03d", i)
				repo.meetings["o1:"+id] = flowMeeting("o1", id, signalsNow.Add(-day), true)
				repo.acctRefs = append(repo.acctRefs, acctMeetingRef("o1", id, signalsNow.Add(-day)))
			}
		}
		for i := 0; i < 50; i++ {
			e := flowEvent(i, signalsNow.Add(-day))
			for j := 0; j < 50; j++ {
				id := contactID(i*50 + j)
				repo.contacts[id] = &model.AccountContact{ContactID: id, Name: "c"}
				e.Attendees = append(e.Attendees, model.EventAttendee{ContactID: id, Name: "c"})
			}
			repo.addEvent(e)
			links.links = append(links.links, model.AccountLink{FromKind: "event", FromID: e.EventID, ToKind: "project", ToID: "p000", Origin: "manual"})
		}
		return svc, repo
	}
	for _, fillCap := range []bool{false, true} {
		svc, repo := fixture(fillCap)
		g, err := svc.Graph(context.Background(), linkUser, linkAcct)
		if err != nil {
			t.Fatal(err)
		}
		checkGraphConsistent(t, g)
		if !g.Truncated || repo.contactGets > maxGraphContactReads || (fillCap && repo.contactGets != 0) {
			t.Fatalf("fillCap=%v: contact reads %d truncated=%v", fillCap, repo.contactGets, g.Truncated)
		}
	}
}
