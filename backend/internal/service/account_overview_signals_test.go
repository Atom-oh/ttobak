package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

var signalsNow = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

type overviewTestRepo struct {
	member     bool
	memberGets int
	refs       []model.ProjectRef
	projects   map[string]*model.Project
	meetingRef map[string][]model.ProjectMeetingRef
	meetings   map[string]*model.Meeting
	events     map[string]*model.AccountEvent
	links      *overviewTestLinks
	// Flow fixtures.
	stageHist   map[string][]model.ProjectStageChange
	eventList   []model.AccountEvent // newest first
	eventMore   bool
	acctRefs    []model.MeetingRef // newest first
	acctRefMore bool
	// Graph fixtures.
	members     []model.AccountMember
	contacts    map[string]*model.AccountContact
	contactErrs map[string]error
	eventErrs   map[string]error
	eventGets   int
	meetingGets int
	contactGets int
	// People fixtures.
	people        []model.PeopleLink
	accountNames  map[string]string
	noMemberIn    map[string]bool
	users         map[string]*model.User
	peopleQueries int
}

func newOverviewTestRepo() *overviewTestRepo {
	return &overviewTestRepo{member: true, projects: map[string]*model.Project{}, meetingRef: map[string][]model.ProjectMeetingRef{},
		meetings: map[string]*model.Meeting{}, events: map[string]*model.AccountEvent{}, stageHist: map[string][]model.ProjectStageChange{}}
}

func (r *overviewTestRepo) GetAccount(_ context.Context, id string) (*model.Account, error) {
	return &model.Account{AccountID: id, Name: r.accountNames[id]}, nil
}
func (r *overviewTestRepo) GetMember(_ context.Context, account, _ string) (*model.AccountMember, error) {
	r.memberGets++
	if r.member && !r.noMemberIn[account] {
		return &model.AccountMember{}, nil
	}
	return nil, nil
}
func (r *overviewTestRepo) ListProjectRefsForAccount(context.Context, string) ([]model.ProjectRef, error) {
	return r.refs, nil
}
func (r *overviewTestRepo) GetProject(_ context.Context, id string) (*model.Project, error) {
	return r.projects[id], nil
}
func (r *overviewTestRepo) ListProjectMeetingRefsForProject(_ context.Context, id string) ([]model.ProjectMeetingRef, error) {
	return r.meetingRef[id], nil
}
func (r *overviewTestRepo) GetMeetingActionState(_ context.Context, owner, id string) (*model.Meeting, error) {
	r.meetingGets++
	return r.meetings[owner+":"+id], nil
}
func (r *overviewTestRepo) GetAccountEvent(_ context.Context, _ string, id string) (*model.AccountEvent, string, error) {
	r.eventGets++
	if err := r.eventErrs[id]; err != nil {
		return nil, "", err
	}
	return r.events[id], "", nil
}

func (r *overviewTestRepo) addProject(p *model.Project) {
	r.projects[p.ProjectID] = p
	r.refs = append(r.refs, model.ProjectRef{AccountID: linkAcct, ProjectID: p.ProjectID})
}

type overviewTestNews struct {
	sources []model.CrawlerSource
	docs    []model.CrawledDocument
}

func (n *overviewTestNews) accountSources(context.Context, string) ([]model.CrawlerSource, error) {
	return n.sources, nil
}
func (n *overviewTestNews) CurrentNews(context.Context, string, time.Time, time.Time) ([]model.CrawledDocument, error) {
	return n.docs, nil
}

// overviewTestLinks stores forward links; prefix reads emulate both the
// LINK# and LINKTO# key spaces.
type overviewTestLinks struct {
	links    []model.AccountLink
	bound    int // 0 = maxRows
	prefixes []string
}

func (r *overviewTestRepo) ListAccountLinksByPrefix(_ context.Context, _ string, prefix string, maxRows int) ([]model.AccountLink, bool, error) {
	r.links.prefixes = append(r.links.prefixes, prefix)
	if r.links.bound > 0 {
		maxRows = r.links.bound
	}
	out := []model.AccountLink{}
	for _, l := range r.links.links {
		if strings.HasPrefix(model.AccountLinkSortKey(l.Input()), prefix) || strings.HasPrefix(model.AccountLinkToSortKey(l.Input()), prefix) {
			if len(out) == maxRows {
				return out, true, nil
			}
			out = append(out, l)
		}
	}
	return out, false, nil
}

type overviewTestInsights struct{ page *model.AccountInsightsPage }

func (i *overviewTestInsights) ListAccountInsightsPage(context.Context, string, string, time.Time, time.Time, []string) (*model.AccountInsightsPage, error) {
	return i.page, nil
}

func newOverviewTest() (*AccountOverviewService, *overviewTestRepo, *overviewTestNews, *overviewTestLinks) {
	repo := newOverviewTestRepo()
	news := &overviewTestNews{sources: []model.CrawlerSource{{SourceID: linkSource, AccountID: linkAcct}}}
	links := &overviewTestLinks{}
	repo.links = links
	svc := &AccountOverviewService{repo: repo, news: news, insights: &overviewTestInsights{page: &model.AccountInsightsPage{}}}
	return svc, repo, news, links
}

func ago(d time.Duration) *time.Time {
	t := signalsNow.Add(-d)
	return &t
}

const day = 24 * time.Hour

func stallCodes(r []model.StallReason) map[string]bool {
	out := map[string]bool{}
	for _, x := range r {
		out[x.Code] = true
	}
	return out
}

func TestStallReasons(t *testing.T) {
	for _, tc := range []struct {
		name     string
		project  model.Project
		ages     []time.Duration
		upcoming bool
		want     []string
	}{
		{"20 days not stale", model.Project{Stage: "Qualified", StageChangedAt: ago(20 * day)}, nil, true, nil},
		{"21 days stale", model.Project{Stage: "Qualified", StageChangedAt: ago(21 * day)}, nil, true, []string{model.StallStageStale}},
		{"nil stage time not stale", model.Project{Stage: "Qualified"}, nil, true, nil},
		{"action 13 days", model.Project{Stage: "Prospect"}, []time.Duration{13 * day}, true, nil},
		{"action 14 days", model.Project{Stage: "Prospect"}, []time.Duration{13 * day, 14 * day}, true, []string{model.StallOpenAction}},
		{"no upcoming", model.Project{Stage: "Prospect"}, nil, false, []string{model.StallNoUpcoming}},
		{"terminal launched never", model.Project{Stage: "launched", StageChangedAt: ago(90 * day)}, []time.Duration{90 * day}, false, nil},
		{"terminal closed lost never", model.Project{Stage: "Closed Lost", StageChangedAt: ago(90 * day)}, []time.Duration{90 * day}, false, nil},
		{"all three", model.Project{Stage: "Committed", StageChangedAt: ago(30 * day)}, []time.Duration{30 * day}, false,
			[]string{model.StallStageStale, model.StallOpenAction, model.StallNoUpcoming}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := StallReasons(tc.project, tc.ages, tc.upcoming, signalsNow)
			if got == nil || len(got) != len(tc.want) {
				t.Fatalf("got %+v want %v", got, tc.want)
			}
			codes := stallCodes(got)
			for _, c := range tc.want {
				if !codes[c] {
					t.Fatalf("missing %s in %+v", c, got)
				}
			}
		})
	}
}

func TestSignals_NoSourceState(t *testing.T) {
	svc, _, news, _ := newOverviewTest()
	news.sources = nil
	news.docs = []model.CrawledDocument{{SourceID: linkSource, DocHash: linkHash, Title: "ignored"}}
	sig, err := svc.Signals(context.Background(), linkUser, linkAcct, signalsNow)
	if err != nil {
		t.Fatal(err)
	}
	if sig.NewsState != model.NewsStateNoSource || len(sig.News) != 0 || sig.News == nil {
		t.Fatalf("got %+v", sig)
	}

	news.sources = []model.CrawlerSource{{SourceID: linkSource}}
	news.docs = []model.CrawledDocument{{SourceID: linkSource, DocHash: linkHash, QualityStatus: "DELETED"}}
	sig, err = svc.Signals(context.Background(), linkUser, linkAcct, signalsNow)
	if err != nil || sig.NewsState != model.NewsStateNoRecentNews || len(sig.News) != 0 {
		t.Fatalf("got %+v err=%v", sig, err)
	}
}

func TestSignals_CandidateOnlyWhenUnlinked(t *testing.T) {
	svc, repo, news, links := newOverviewTest()
	repo.addProject(&model.Project{ProjectID: "p1", Name: "P1", AccountIDs: []string{linkAcct}})
	news.docs = []model.CrawledDocument{
		{SourceID: linkSource, DocHash: "aaaa1111", Title: "linked", OpportunityCount: 2, QualityScore: 0.9, CrawledAt: 10},
		{SourceID: linkSource, DocHash: "bbbb2222", Title: "unlinked", OpportunityCount: 1, QualityScore: 0.8, CrawledAt: 10},
		{SourceID: linkSource, DocHash: "cccc3333", Title: "low score", OpportunityCount: 1, QualityScore: 0.5, CrawledAt: 10},
	}
	links.links = []model.AccountLink{{FromKind: "news", FromID: linkSource + ":aaaa1111", ToKind: "project", ToID: "p1"}}
	sig, err := svc.Signals(context.Background(), linkUser, linkAcct, signalsNow)
	if err != nil {
		t.Fatal(err)
	}
	if sig.NewsState != model.NewsStateOK || len(sig.News) != 3 {
		t.Fatalf("got %+v", sig)
	}
	byTitle := map[string]model.SignalNews{}
	for _, n := range sig.News {
		byTitle[n.Title] = n
	}
	if n := byTitle["linked"]; n.Candidate || len(n.LinkedProjectIDs) != 1 || n.LinkedProjectIDs[0] != "p1" || n.ID != linkSource+":aaaa1111" {
		t.Fatalf("linked: %+v", n)
	}
	if n := byTitle["unlinked"]; !n.Candidate || len(n.LinkedProjectIDs) != 0 || n.LinkedProjectIDs == nil {
		t.Fatalf("unlinked: %+v", n)
	}
	if n := byTitle["low score"]; n.Candidate {
		t.Fatalf("low score: %+v", n)
	}
}

func TestSignals_OrderByQualityThenRecency(t *testing.T) {
	svc, _, news, _ := newOverviewTest()
	for _, d := range []struct {
		hash  string
		score float64
		at    int64
	}{{"h1", 0.5, 300}, {"h2", 0.9, 100}, {"h3", 0.9, 200}, {"h4", 0.1, 999}} {
		news.docs = append(news.docs, model.CrawledDocument{SourceID: linkSource, DocHash: d.hash, Title: d.hash, QualityScore: d.score, CrawledAt: d.at})
	}
	for i := 0; i < 12; i++ {
		news.docs = append(news.docs, model.CrawledDocument{SourceID: linkSource, DocHash: "z" + string(rune('a'+i)), QualityScore: 0.01})
	}
	sig, err := svc.Signals(context.Background(), linkUser, linkAcct, signalsNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig.News) != maxSignalsNews {
		t.Fatalf("len=%d", len(sig.News))
	}
	want := []string{"h3", "h2", "h1", "h4"}
	for i, w := range want {
		if sig.News[i].Title != w {
			t.Fatalf("order[%d]=%s want %s (%+v)", i, sig.News[i].Title, w, sig.News[:4])
		}
	}
}

func TestOverview_RemovedMemberForbidden(t *testing.T) {
	svc, repo, _, _ := newOverviewTest()
	if _, err := svc.Signals(context.Background(), linkUser, linkAcct, signalsNow); err != nil {
		t.Fatal(err)
	}
	repo.member = false
	if _, err := svc.Signals(context.Background(), linkUser, linkAcct, signalsNow); !errors.Is(err, ErrForbidden) {
		t.Fatalf("err=%v", err)
	}
	if repo.memberGets != 2 {
		t.Fatalf("membership must be re-read per call, got %d reads", repo.memberGets)
	}
}

func actionMeeting(owner, id string, date time.Time, items string, published bool, projects ...string) *model.Meeting {
	m := publishedMeeting(owner, id)
	m.SharedToAccount = published
	m.Date = date
	m.ActionItems = items
	m.ProjectIDs = projects
	return m
}

func TestSignals_SkipsUnpublishedMeetingActions(t *testing.T) {
	svc, repo, _, links := newOverviewTest()
	repo.addProject(&model.Project{ProjectID: "p1", Name: "P1", Stage: "Qualified", AccountIDs: []string{linkAcct}})
	repo.meetingRef["p1"] = []model.ProjectMeetingRef{{MeetingID: "m1", OwnerUserID: "o1"}, {MeetingID: "m2", OwnerUserID: "o1"}, {MeetingID: "m3", OwnerUserID: "o1"}}
	old := signalsNow.Add(-30 * day)
	open := `[{"id":"a1","text":"t","completed":false}]`
	// m1 is not published, m2 does not list p1, m3's item is completed.
	repo.meetings["o1:m1"] = actionMeeting("o1", "m1", old, open, false, "p1")
	repo.meetings["o1:m2"] = actionMeeting("o1", "m2", old, open, true, "other")
	repo.meetings["o1:m3"] = actionMeeting("o1", "m3", old, `[{"id":"a2","completed":true}]`, true, "p1")
	repo.events[linkEventID] = &model.AccountEvent{EventID: linkEventID, StartAt: signalsNow.Add(day).Format(time.RFC3339)}
	links.links = []model.AccountLink{{FromKind: "event", FromID: linkEventID, ToKind: "project", ToID: "p1"}}

	sig, err := svc.Signals(context.Background(), linkUser, linkAcct, signalsNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig.Opportunities) != 1 || len(sig.Opportunities[0].Stall) != 0 {
		t.Fatalf("got %+v", sig.Opportunities)
	}

	// Publishing m1 makes its open item count; a past event no longer counts.
	repo.meetings["o1:m1"].SharedToAccount = true
	repo.events[linkEventID].StartAt = signalsNow.Add(-day).Format(time.RFC3339)
	sig, err = svc.Signals(context.Background(), linkUser, linkAcct, signalsNow)
	if err != nil {
		t.Fatal(err)
	}
	codes := stallCodes(sig.Opportunities[0].Stall)
	if !codes[model.StallOpenAction] || !codes[model.StallNoUpcoming] || len(codes) != 2 {
		t.Fatalf("got %+v", sig.Opportunities[0].Stall)
	}
}

func TestSignals_OpportunityFields(t *testing.T) {
	svc, repo, _, _ := newOverviewTest()
	repo.addProject(&model.Project{ProjectID: "p1", Name: "P1", Stage: "technical validation", StageChangedAt: ago(5 * day), AccountIDs: []string{linkAcct}})
	repo.addProject(&model.Project{ProjectID: "p2", Name: "P2", Stage: "Launched", AccountIDs: []string{linkAcct}})
	repo.addProject(&model.Project{ProjectID: "p3", Name: "P3", Stage: "Someday", AccountIDs: []string{linkAcct}})
	repo.addProject(&model.Project{ProjectID: "p4", Name: "gone", AccountIDs: []string{"other"}})
	sig, err := svc.Signals(context.Background(), linkUser, linkAcct, signalsNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig.Opportunities) != 3 {
		t.Fatalf("got %+v", sig.Opportunities)
	}
	p1, p2, p3 := sig.Opportunities[0], sig.Opportunities[1], sig.Opportunities[2]
	if p1.Stage != "Technical Validation" || !p1.StageMapped || p1.DaysInStage == nil || *p1.DaysInStage != 5 || p1.Terminal {
		t.Fatalf("p1 %+v", p1)
	}
	if !p2.Terminal || len(p2.Stall) != 0 || p2.Stall == nil || p2.DaysInStage != nil {
		t.Fatalf("p2 %+v", p2)
	}
	if p3.StageMapped || p3.Stage != "Someday" || p3.Terminal || !stallCodes(p3.Stall)[model.StallNoUpcoming] {
		t.Fatalf("p3 %+v", p3)
	}
}

func TestSignals_InsightsNewestFirstAndBounded(t *testing.T) {
	svc, _, _, _ := newOverviewTest()
	long := make([]rune, 400)
	for i := range long {
		long[i] = '가'
	}
	page := &model.AccountInsightsPage{RestrictedCount: 2}
	for i := 0; i < 15; i++ {
		page.Insights = append(page.Insights, model.AccountInsightDTO{Type: "need", Text: string(long), OccurredAt: signalsNow.Add(time.Duration(i) * time.Hour)})
	}
	svc.insights = &overviewTestInsights{page: page}
	sig, err := svc.Signals(context.Background(), linkUser, linkAcct, signalsNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig.Insights) != maxSignalsInsights || sig.RestrictedCount != 2 || !sig.Insights[0].OccurredAt.After(sig.Insights[1].OccurredAt) {
		t.Fatalf("got %d %+v", len(sig.Insights), sig.Insights[:2])
	}
	if n := len([]rune(sig.Insights[0].Text)); n != maxSignalsTextRunes {
		t.Fatalf("text runes=%d", n)
	}
}

func TestSignalURLAndFit(t *testing.T) {
	for raw, want := range map[string]string{"https://a.example/x": "https://a.example/x", "javascript:alert(1)": "", "/relative": ""} {
		if got := signalURL(raw); got != want {
			t.Fatalf("%q -> %q", raw, got)
		}
	}
	sig := &model.AccountSignals{}
	big := strings.Repeat("가", 280)
	for i := 0; i < 200; i++ {
		sig.News = append(sig.News, model.SignalNews{Title: big, AWSRelevance: big, LinkedProjectIDs: []string{}})
	}
	if err := fitSignals(sig); err != nil {
		t.Fatal(err)
	}
	if len(sig.News) == 0 || len(sig.News) == 200 {
		t.Fatalf("len=%d", len(sig.News))
	}
}

func TestSignals_LinkBoundDegradesNotFails(t *testing.T) {
	svc, repo, news, links := newOverviewTest()
	repo.addProject(&model.Project{ProjectID: "p1", Name: "P1", Stage: "Qualified", AccountIDs: []string{linkAcct}})
	repo.addProject(&model.Project{ProjectID: "p2", Name: "P2", Stage: "Qualified", AccountIDs: []string{linkAcct}})
	repo.addProject(&model.Project{ProjectID: "pX", Name: "left", AccountIDs: []string{"other"}})
	newsID := linkSource + ":aaaa1111"
	news.docs = []model.CrawledDocument{{SourceID: linkSource, DocHash: "aaaa1111", Title: "n", OpportunityCount: 1, QualityScore: 0.9}}
	repo.events[linkEventID] = &model.AccountEvent{EventID: linkEventID, StartAt: signalsNow.Add(day).Format(time.RFC3339)}
	links.links = []model.AccountLink{
		{FromKind: "news", FromID: newsID, ToKind: "project", ToID: "pX"}, // project left the account
		{FromKind: "news", FromID: newsID, ToKind: "project", ToID: "p1"},
		{FromKind: "news", FromID: newsID, ToKind: "project", ToID: "p2"},
		{FromKind: "event", FromID: linkEventID, ToKind: "project", ToID: "p1"},
		{FromKind: "news", FromID: linkSource + ":other", ToKind: "project", ToID: "p2"}, // other news
	}
	sig, err := svc.Signals(context.Background(), linkUser, linkAcct, signalsNow)
	if err != nil {
		t.Fatal(err)
	}
	if sig.LinksTruncated || len(sig.News) != 1 || len(sig.News[0].LinkedProjectIDs) != 2 || sig.News[0].Candidate {
		t.Fatalf("unbounded: %+v", sig)
	}
	if stallCodes(sig.Opportunities[0].Stall)[model.StallNoUpcoming] || !stallCodes(sig.Opportunities[1].Stall)[model.StallNoUpcoming] {
		t.Fatalf("events: %+v", sig.Opportunities)
	}
	for _, p := range links.prefixes {
		if p != "LINK#news#"+newsID+"#project#" && !strings.HasPrefix(p, "LINKTO#project#") {
			t.Fatalf("unexpected link read %q", p)
		}
	}

	// A hit bound keeps the rows read and flags the response instead of failing.
	links.bound = 2
	sig, err = svc.Signals(context.Background(), linkUser, linkAcct, signalsNow)
	if err != nil {
		t.Fatal(err)
	}
	if !sig.LinksTruncated || len(sig.News) != 1 || len(sig.News[0].LinkedProjectIDs) != 1 || sig.News[0].LinkedProjectIDs[0] != "p1" ||
		len(sig.Opportunities) != 2 || len(sig.Insights) != 0 || sig.Insights == nil {
		t.Fatalf("bounded: %+v", sig)
	}
}
