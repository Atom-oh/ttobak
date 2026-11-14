package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

var ctxTestNow = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)

// ctxFake implements every CustomerUpdateContext dependency over in-memory
// maps keyed by account id.
type ctxFake struct {
	accounts    []model.AccountSummary
	listErr     error
	notMember   map[string]error
	disabled    map[string]bool
	projects    map[string][]model.ProjectSummary
	projectErr  map[string]error
	insights    map[string][]model.AccountInsightDTO
	meetings    map[string][]model.MeetingRefDTO
	details     map[string]*model.MeetingDetailResponse // meetingID -> detail; absent = forbidden
	news        map[string][]model.CrawledDocument
	newsErr     map[string]error
	memberNames map[string][]string
	services    map[string][]string
	servicesErr map[string]error

	meetingReads  int
	newsCalls     []string
	insightCalls  []string
	insightWindow [2]time.Time
	newsWindow    [2]time.Time
}

func newCtxFake() *ctxFake {
	return &ctxFake{
		notMember: map[string]error{}, disabled: map[string]bool{},
		projects: map[string][]model.ProjectSummary{}, projectErr: map[string]error{},
		insights: map[string][]model.AccountInsightDTO{}, meetings: map[string][]model.MeetingRefDTO{},
		details: map[string]*model.MeetingDetailResponse{}, news: map[string][]model.CrawledDocument{},
		newsErr: map[string]error{}, memberNames: map[string][]string{},
		services: map[string][]string{}, servicesErr: map[string]error{},
	}
}

func (f *ctxFake) ListAccounts(context.Context, string) ([]model.AccountSummary, error) {
	return f.accounts, f.listErr
}
func (f *ctxFake) RequireDirectMember(_ context.Context, _, accountID string) error {
	return f.notMember[accountID]
}
func (f *ctxFake) GetAccountOpt(_ context.Context, _, accountID string) (bool, error) {
	return !f.disabled[accountID], nil
}
func (f *ctxFake) ListAccountProjects(_ context.Context, _, accountID string) ([]model.ProjectSummary, error) {
	return f.projects[accountID], f.projectErr[accountID]
}
func (f *ctxFake) ListAccountInsights(_ context.Context, _, accountID string, from, to time.Time, _ []string) ([]model.AccountInsightDTO, error) {
	f.insightCalls = append(f.insightCalls, accountID)
	f.insightWindow = [2]time.Time{from, to}
	var out []model.AccountInsightDTO
	for _, in := range f.insights[accountID] {
		if !in.OccurredAt.Before(from) && !in.OccurredAt.After(to) {
			out = append(out, in)
		}
	}
	return out, nil
}
func (f *ctxFake) ListAccountMeetings(_ context.Context, _, accountID string) ([]model.MeetingRefDTO, error) {
	return f.meetings[accountID], nil
}
func (f *ctxFake) MeetingNotes(_ context.Context, _, meetingID string) (string, error) {
	f.meetingReads++
	d, ok := f.details[meetingID]
	if !ok {
		return "", ErrForbidden
	}
	return d.Notes, nil
}
func (f *ctxFake) CurrentNews(_ context.Context, accountID string, from, to time.Time) ([]model.CrawledDocument, error) {
	f.newsCalls = append(f.newsCalls, accountID)
	f.newsWindow = [2]time.Time{from, to}
	return f.news[accountID], f.newsErr[accountID]
}
func (f *ctxFake) AccountMemberNames(_ context.Context, _, accountID string) ([]string, error) {
	return f.memberNames[accountID], nil
}
func (f *ctxFake) ServicesInUse(_ context.Context, accountID string) ([]string, error) {
	return f.services[accountID], f.servicesErr[accountID]
}

func (f *ctxFake) deps() CustomerUpdateContextDeps {
	return CustomerUpdateContextDeps{
		Accounts: f, Members: f, Settings: f, Projects: f, Insights: f,
		Meetings: f, MeetingReader: f, News: f, MemberNames: f, Services: f,
	}
}

// addAccount registers a direct-member account with one open opportunity.
func (f *ctxFake) addAccount(id, name string) {
	f.accounts = append(f.accounts, model.AccountSummary{AccountID: id, Name: name, Role: "member"})
	f.projects[id] = []model.ProjectSummary{{ProjectID: "p-" + id, Name: "Opp " + id, Stage: "Qualified", SfdcOpptyID: "006" + id}}
}

func newTestUpdateContext(t *testing.T, f *ctxFake) *CustomerUpdateContext {
	t.Helper()
	return NewCustomerUpdateContext(f.deps(), func() time.Time { return ctxTestNow })
}

func packIDs(packs []AccountPack) []string {
	ids := make([]string, 0, len(packs))
	for _, p := range packs {
		ids = append(ids, p.AccountID)
	}
	return ids
}

func TestUpdateContextScope(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *ctxFake)
		want  bool
	}{
		{name: "direct member with open opportunity", setup: func(*ctxFake) {}, want: true},
		{name: "non-member", setup: func(f *ctxFake) { f.notMember["a1"] = ErrForbidden }},
		{name: "ancestor reader only", setup: func(f *ctxFake) {
			f.accounts[0].Role = ""
			f.accounts[0].AccessPermission = "read"
			f.accounts[0].InheritedFromAccountID = "parent"
			f.notMember["a1"] = ErrForbidden
		}},
		{name: "missing account", setup: func(f *ctxFake) { f.notMember["a1"] = ErrNotFound }},
		{name: "setting disabled", setup: func(f *ctxFake) { f.disabled["a1"] = true }},
		{name: "no sfdc id", setup: func(f *ctxFake) { f.projects["a1"][0].SfdcOpptyID = "" }},
		{name: "launched", setup: func(f *ctxFake) { f.projects["a1"][0].Stage = "Launched" }},
		{name: "closed lost", setup: func(f *ctxFake) { f.projects["a1"][0].Stage = "Closed Lost" }},
		{name: "unset stage is open", setup: func(f *ctxFake) { f.projects["a1"][0].Stage = "" }, want: true},
		{name: "no projects", setup: func(f *ctxFake) { f.projects["a1"] = nil }},
		{name: "one open among closed", setup: func(f *ctxFake) {
			f.projects["a1"] = []model.ProjectSummary{
				{ProjectID: "p1", Stage: "Launched", SfdcOpptyID: "006a"},
				{ProjectID: "p2", Stage: "Qualified"},
				{ProjectID: "p3", Stage: "Technical Validation", SfdcOpptyID: "006c"},
			}
		}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newCtxFake()
			f.addAccount("a1", "한빛리테일")
			tc.setup(f)
			c := newTestUpdateContext(t, f)
			got, err := c.InScope(context.Background(), "user-1", "a1")
			if err != nil {
				t.Fatalf("InScope: %v", err)
			}
			if got != tc.want {
				t.Fatalf("InScope = %v, want %v", got, tc.want)
			}
			packs, err := c.Build(context.Background(), "user-1", nil)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if (len(packs) == 1) != tc.want {
				t.Fatalf("Build packs = %v, want in scope %v", packIDs(packs), tc.want)
			}
		})
	}
}

func TestUpdateContextInScopeErrors(t *testing.T) {
	f := newCtxFake()
	f.addAccount("a1", "A")
	f.projectErr["a1"] = errors.New("dynamo down")
	c := newTestUpdateContext(t, f)
	if _, err := c.InScope(context.Background(), "user-1", "a1"); err == nil {
		t.Fatal("a project read failure must be returned, not treated as out of scope")
	}
	if ok, err := c.InScope(context.Background(), "user-1", "../a1"); ok || err != nil {
		t.Fatalf("invalid account id: %v, %v; want false, nil", ok, err)
	}
}

func TestUpdateContextDraftsRemaining(t *testing.T) {
	f := newCtxFake()
	f.addAccount("a1", "A1")
	f.addAccount("a2", "A2")
	f.addAccount("a3", "A3")
	packs, err := newTestUpdateContext(t, f).Build(context.Background(), "user-1", map[string]int{"a1": 1, "a2": 3, "a3": 4})
	if err != nil {
		t.Fatal(err)
	}
	if got := packIDs(packs); !reflect.DeepEqual(got, []string{"a1"}) {
		t.Fatalf("packs = %v, want [a1]", got)
	}
	if packs[0].DraftsRemaining != 2 {
		t.Fatalf("draftsRemaining = %d, want 2", packs[0].DraftsRemaining)
	}
}

func TestUpdateContextCountsCappedInScopeAccounts(t *testing.T) {
	f := newCtxFake()
	f.addAccount("a1", "A1")
	f.addAccount("a2", "A2")
	f.addAccount("a3", "A3")
	f.projects["a3"] = nil // capped but not in scope: not counted
	packs, stats, err := newTestUpdateContext(t, f).BuildWithStats(context.Background(), "user-1", map[string]int{"a1": 3, "a2": 1, "a3": 3})
	if err != nil {
		t.Fatal(err)
	}
	if got := packIDs(packs); !reflect.DeepEqual(got, []string{"a2"}) || stats.Capped != 1 || !reflect.DeepEqual(stats.CappedAccounts, []string{"a1"}) {
		t.Fatalf("packs = %v capped = %d", got, stats.Capped)
	}
}

func TestUpdateContextCapsAccountsAndOrders(t *testing.T) {
	f := newCtxFake()
	for i := 41; i >= 1; i-- {
		f.addAccount(fmt.Sprintf("acct-%02d", i), fmt.Sprintf("Account %02d", i))
	}
	packs, err := newTestUpdateContext(t, f).Build(context.Background(), "user-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 40 {
		t.Fatalf("packs = %d, want 40", len(packs))
	}
	ids := packIDs(packs)
	if !sort.StringsAreSorted(ids) || ids[0] != "acct-01" || ids[39] != "acct-40" {
		t.Fatalf("packs not the first 40 in id order: %v", ids)
	}
}

func TestUpdateContextOneFailingAccountIsSkipped(t *testing.T) {
	f := newCtxFake()
	f.addAccount("a1", "A1")
	f.addAccount("a2", "A2")
	f.newsErr["a1"] = errors.New("news down")
	c := newTestUpdateContext(t, f)
	packs, stats, err := c.BuildWithStats(context.Background(), "user-1", nil)
	if err != nil {
		t.Fatalf("one failing account failed Build: %v", err)
	}
	if got := packIDs(packs); !reflect.DeepEqual(got, []string{"a2"}) {
		t.Fatalf("packs = %v, want [a2]", got)
	}
	if stats.Failed != 1 {
		t.Fatalf("stats.Failed = %d, want 1", stats.Failed)
	}

	f.newsErr["a1"] = context.DeadlineExceeded
	if _, err := c.Build(context.Background(), "user-1", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline err = %v, want returned", err)
	}
	f.newsErr["a1"] = nil
	f.listErr = errors.New("list failed")
	if _, err := c.Build(context.Background(), "user-1", nil); err == nil {
		t.Fatal("account list failure must fail Build")
	}
}

func TestUpdateContextBillingFailureIsNoServices(t *testing.T) {
	f := newCtxFake()
	f.addAccount("a1", "A1")
	f.addAccount("a2", "A2")
	f.services["a2"] = []string{"Amazon Bedrock"}
	f.servicesErr["a1"] = errors.New("s3 throttled")
	packs, stats, err := newTestUpdateContext(t, f).BuildWithStats(context.Background(), "user-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 2 || len(packs[0].Services) != 0 || !reflect.DeepEqual(packs[1].Services, []string{"Amazon Bedrock"}) {
		t.Fatalf("packs = %+v", packs)
	}
	if stats.ServicesErrors != 1 {
		t.Fatalf("ServicesErrors = %d, want 1", stats.ServicesErrors)
	}
}

func TestUpdateContextPackContents(t *testing.T) {
	f := newCtxFake()
	f.addAccount("a1", "  한빛\n리테일  ")
	f.addAccount("a2", "블루웨이브")
	f.addAccount("a3", "X")                                                                  // too short for the block list
	f.addAccount("a4", "한빛")                                                                 // contained in this account's name
	f.accounts = append(f.accounts, model.AccountSummary{AccountID: "a5", Name: "bluewave"}) // case duplicate of a member name
	f.memberNames["a1"] = []string{"김철수", "Bluewave", "김철수", "Y", "  "}
	f.services["a1"] = []string{"Amazon Bedrock"}
	f.projects["a1"] = []model.ProjectSummary{
		{ProjectID: "p9", Name: "Later", Stage: "", SfdcOpptyID: "0069"},
		{ProjectID: "p1", Name: "상품\t추천", Stage: "Technical Validation", SfdcOpptyID: "0061"},
		{ProjectID: "p5", Name: "Closed", Stage: "Launched", SfdcOpptyID: "0065"},
		{ProjectID: "p3", Name: "No SFDC", Stage: "Qualified"},
	}
	day := 24 * time.Hour
	f.news["a1"] = []model.CrawledDocument{
		{Title: "새 소식\n\n줄바꿈", Summary: strings.Repeat("요", 250), CrawledAt: ctxTestNow.Add(-1 * day).Unix()},
		{Title: "old", Summary: "s", CrawledAt: ctxTestNow.Add(-40 * day).Unix()}, // outside the window even if returned
		{Title: "", Summary: "no title", CrawledAt: ctxTestNow.Add(-2 * day).Unix()},
	}
	for i := 0; i < 7; i++ {
		f.news["a1"] = append(f.news["a1"], model.CrawledDocument{Title: fmt.Sprintf("n%d", i), Summary: "s", CrawledAt: ctxTestNow.Add(-time.Duration(3+i) * day).Unix()})
	}
	f.insights["a1"] = []model.AccountInsightDTO{
		{Type: "need", Text: "need\nnewest", OccurredAt: ctxTestNow.Add(-1 * day)},
		{Type: "trend", Text: "excluded type", OccurredAt: ctxTestNow.Add(-1 * day)},
		{Type: "risk", Text: strings.Repeat("위", 250), OccurredAt: ctxTestNow.Add(-2 * day)},
		{Type: "tech", Text: "too old", OccurredAt: ctxTestNow.Add(-91 * day)},
		{Type: "opportunity", Text: "o1", OccurredAt: ctxTestNow.Add(-3 * day)},
		{Type: "tech", Text: "t1", OccurredAt: ctxTestNow.Add(-4 * day)},
		{Type: "need", Text: "n2", OccurredAt: ctxTestNow.Add(-5 * day)},
		{Type: "need", Text: "n3 dropped", OccurredAt: ctxTestNow.Add(-6 * day)},
	}
	f.meetings["a1"] = []model.MeetingRefDTO{
		{MeetingID: "m-old", Date: ctxTestNow.Add(-10 * day)},
		{MeetingID: "m-new", Date: ctxTestNow.Add(-1 * day)},
		{MeetingID: "m-hidden", Date: ctxTestNow.Add(-2 * day)},
		{MeetingID: "m-mid", Date: ctxTestNow.Add(-3 * day)},
	}
	f.details["m-old"] = &model.MeetingDetailResponse{Notes: "should not appear"}
	f.details["m-new"] = &model.MeetingDetailResponse{Notes: "첫\n\n노트 " + strings.Repeat("가", 700)}
	f.details["m-mid"] = &model.MeetingDetailResponse{Notes: "mid notes"}

	c := newTestUpdateContext(t, f)
	pack, err := c.buildPack(context.Background(), "user-1", f.accounts[0], f.accounts, 3, &BuildStats{})
	if err != nil {
		t.Fatal(err)
	}
	if pack.Name != "한빛 리테일" {
		t.Fatalf("name = %q", pack.Name)
	}
	wantOpps := []AccountPackOpportunity{
		{ProjectID: "p1", Name: "상품 추천", Stage: "Technical Validation"},
		{ProjectID: "p9", Name: "Later", Stage: ""},
	}
	if !reflect.DeepEqual(pack.Opportunities, wantOpps) {
		t.Fatalf("opportunities = %+v", pack.Opportunities)
	}
	if len(pack.News) != 5 || pack.News[0].Title != "새 소식 줄바꿈" || len([]rune(pack.News[0].Summary)) != 200 || pack.News[4].Title != "n3" {
		t.Fatalf("news = %+v", pack.News)
	}
	if !f.newsWindow[0].Equal(ctxTestNow.Add(-30*day)) || !f.newsWindow[1].Equal(ctxTestNow) {
		t.Fatalf("news window = %v", f.newsWindow)
	}
	gotTypes := []string{}
	for _, in := range pack.Insights {
		gotTypes = append(gotTypes, in.Type+":"+in.Text[:min(len(in.Text), 6)])
	}
	if len(pack.Insights) != 5 || pack.Insights[0].Text != "need newest" || len([]rune(pack.Insights[1].Text)) != 200 || pack.Insights[4].Text != "n2" {
		t.Fatalf("insights = %v", gotTypes)
	}
	// m-hidden is unreadable, so the two newest readable meetings are m-new and m-mid.
	if len(pack.Notes) != 2 || pack.Notes[1] != "mid notes" {
		t.Fatalf("notes = %d entries", len(pack.Notes))
	}
	if !strings.HasPrefix(pack.Notes[0], "첫 노트 ") || len([]rune(pack.Notes[0])) != 600 {
		t.Fatalf("notes[0] = %q", pack.Notes[0][:20])
	}
	if packSize(t, pack) > maxAccountPackBytes {
		t.Fatalf("pack size %d", packSize(t, pack))
	}
	// Member names first, then other account names; "bluewave" is a case
	// duplicate of the member name "Bluewave".
	wantBlock := []string{"김철수", "Bluewave", "블루웨이브"}
	if !reflect.DeepEqual(pack.BlockList, wantBlock) {
		t.Fatalf("blockList = %q, want %q", pack.BlockList, wantBlock)
	}
	if pack.DraftsRemaining != 3 {
		t.Fatalf("draftsRemaining = %d", pack.DraftsRemaining)
	}
}

func TestUpdateContextNotesReadsReadableMeetingsOnly(t *testing.T) {
	f := newCtxFake()
	f.addAccount("a1", "A1")
	f.meetings["a1"] = []model.MeetingRefDTO{
		{MeetingID: "m1", Date: ctxTestNow.Add(-1 * time.Hour)},
		{MeetingID: "m2", Date: ctxTestNow.Add(-2 * time.Hour)},
		{MeetingID: "m3", Date: ctxTestNow.Add(-3 * time.Hour)},
		{MeetingID: "m4", Date: ctxTestNow.Add(-4 * time.Hour)},
	}
	f.details["m2"] = &model.MeetingDetailResponse{Notes: "two"}
	f.details["m3"] = &model.MeetingDetailResponse{Notes: "three"}
	f.details["m4"] = &model.MeetingDetailResponse{Notes: "four"}
	packs, err := newTestUpdateContext(t, f).Build(context.Background(), "user-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(packs[0].Notes, []string{"two", "three"}) {
		t.Fatalf("notes = %q; the unreadable m1 must contribute nothing", packs[0].Notes)
	}
}

func TestUpdateContextNotesSwitch(t *testing.T) {
	for _, tc := range []struct {
		value string
		set   bool
		on    bool
	}{
		{set: false, on: true},
		{value: "1", set: true, on: true},
		{value: "true", set: true, on: true},
		{value: "0", set: true},
		{value: "false", set: true},
		{value: "", set: true},
		{value: "yes", set: true},
	} {
		t.Run(fmt.Sprintf("%v=%q", tc.set, tc.value), func(t *testing.T) {
			if tc.set {
				t.Setenv(customerUpdateIncludeNotesEnv, tc.value)
			} else {
				t.Setenv(customerUpdateIncludeNotesEnv, "x")
				os.Unsetenv(customerUpdateIncludeNotesEnv)
			}
			f := newCtxFake()
			f.addAccount("a1", "A1")
			f.meetings["a1"] = []model.MeetingRefDTO{{MeetingID: "m1", Date: ctxTestNow}}
			f.details["m1"] = &model.MeetingDetailResponse{Notes: "note"}
			packs, err := newTestUpdateContext(t, f).Build(context.Background(), "user-1", nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.on != (len(packs[0].Notes) == 1) {
				t.Fatalf("notes = %q, want on=%v", packs[0].Notes, tc.on)
			}
			if !tc.on && f.meetingReads != 0 {
				t.Fatalf("notes off still read %d meetings", f.meetingReads)
			}
		})
	}
}

func packSize(t *testing.T, p AccountPack) int {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

func basePack() AccountPack {
	return AccountPack{
		AccountID: "a1", Name: "A1", Services: []string{}, BlockList: []string{},
		Opportunities: []AccountPackOpportunity{{ProjectID: "p1", Name: "Opp", Stage: "Qualified"}},
		News:          []AccountPackNews{}, Insights: []AccountPackInsight{}, Notes: []string{},
		DraftsRemaining: 3,
	}
}

func TestUpdateContextShrinkOrder(t *testing.T) {
	big := strings.Repeat("가", 600) // 1,800 bytes
	t.Run("notes first from tail", func(t *testing.T) {
		p := basePack()
		p.Notes = []string{"keep", big, big, big, big}
		p.Insights = []AccountPackInsight{{Type: "need", Text: "i"}}
		p.News = []AccountPackNews{{Title: "t", Summary: "s"}}
		got, ok := fitAccountPack(p)
		if !ok || !reflect.DeepEqual(got.Notes, []string{"keep", big, big, big}) || len(got.Insights) != 1 || len(got.News) != 1 {
			t.Fatalf("ok=%v notes=%d insights=%d news=%d", ok, len(got.Notes), len(got.Insights), len(got.News))
		}
		if packSize(t, got) > maxAccountPackBytes {
			t.Fatalf("size %d", packSize(t, got))
		}
	})
	t.Run("then insights from tail", func(t *testing.T) {
		p := basePack()
		mid := strings.Repeat("나", 200) // 600 bytes
		p.Notes = []string{big}
		p.News = []AccountPackNews{{Title: "t", Summary: "s"}}
		for i := 0; i < 5; i++ {
			p.Insights = append(p.Insights, AccountPackInsight{Type: "need", Text: fmt.Sprintf("%d%s", i, mid)})
		}
		p.Name = strings.Repeat("다", 120)
		for i := 0; i < 8; i++ {
			p.BlockList = append(p.BlockList, fmt.Sprintf("%d%s", i, strings.Repeat("라", 120)))
		}
		p.Notes = []string{big, big}
		got, ok := fitAccountPack(p)
		if !ok || len(got.Notes) != 0 || len(got.Insights) == 0 || len(got.Insights) == 5 || len(got.News) != 1 {
			t.Fatalf("ok=%v notes=%d insights=%d news=%d", ok, len(got.Notes), len(got.Insights), len(got.News))
		}
		if !strings.HasPrefix(got.Insights[0].Text, "0") {
			t.Fatal("insights must be dropped from the tail")
		}
	})
	t.Run("then news from tail", func(t *testing.T) {
		p := basePack()
		long := strings.Repeat("뉴", 200)
		for i := 0; i < 5; i++ {
			p.News = append(p.News, AccountPackNews{Title: fmt.Sprintf("%d%s", i, long), Summary: long})
		}
		p.Insights = []AccountPackInsight{{Type: "need", Text: long}}
		p.Notes = []string{"n"}
		got, ok := fitAccountPack(p)
		if !ok || len(got.Notes) != 0 || len(got.Insights) != 0 || len(got.News) != 4 || !strings.HasPrefix(got.News[0].Title, "0") {
			t.Fatalf("ok=%v notes=%d insights=%d news=%d", ok, len(got.Notes), len(got.Insights), len(got.News))
		}
	})
	t.Run("then block list then opportunities to one", func(t *testing.T) {
		p := basePack()
		name := strings.Repeat("오", 120) // 360 bytes
		p.Opportunities = nil
		for i := 0; i < 10; i++ {
			p.Opportunities = append(p.Opportunities, AccountPackOpportunity{ProjectID: fmt.Sprintf("p%d", i), Name: name, Stage: name})
		}
		for i := 0; i < 10; i++ {
			p.BlockList = append(p.BlockList, fmt.Sprintf("%d%s", i, name))
		}
		got, ok := fitAccountPack(p)
		if !ok || len(got.BlockList) != 0 || len(got.Opportunities) == 10 || len(got.Opportunities) == 0 || got.Opportunities[0].ProjectID != "p0" {
			t.Fatalf("ok=%v block=%d opps=%d", ok, len(got.BlockList), len(got.Opportunities))
		}
		if packSize(t, got) > maxAccountPackBytes {
			t.Fatalf("size %d", packSize(t, got))
		}
	})
	t.Run("unfittable is skipped", func(t *testing.T) {
		p := basePack()
		p.Services = []string{strings.Repeat("서", 2100)}
		if _, ok := fitAccountPack(p); ok {
			t.Fatal("an unfittable pack must be reported")
		}
	})
}

func TestUpdateContextEmptyListsMarshalAsArrays(t *testing.T) {
	f := newCtxFake()
	f.addAccount("a1", "A1")
	packs, err := newTestUpdateContext(t, f).Build(context.Background(), "user-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(packs[0])
	for _, key := range []string{"services", "news", "insights", "notes", "blockList"} {
		if !strings.Contains(string(b), `"`+key+`":[]`) {
			t.Fatalf("%s is not [] in %s", key, b)
		}
	}
	if strings.Contains(string(b), "null") {
		t.Fatalf("null in %s", b)
	}
}

func jsonShape(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			out[k] = jsonShape(val)
		}
		return out
	case []any:
		if len(x) == 0 {
			return []any{}
		}
		return []any{jsonShape(x[0])}
	case string:
		return "string"
	case float64:
		return "number"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func TestUpdateContextPackMatchesRunContract(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "contracts", "customer-updates", "run-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Example struct {
			Accounts []map[string]any `json:"accounts"`
		} `json:"example"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	pack := AccountPack{
		AccountID: "a1", Name: "n", Services: []string{"s"},
		Opportunities: []AccountPackOpportunity{{ProjectID: "p", Name: "n", Stage: "s"}},
		News:          []AccountPackNews{{Title: "t", Summary: "s"}},
		Insights:      []AccountPackInsight{{Type: "need", Text: "t"}},
		Notes:         []string{"n"}, BlockList: []string{"b"}, DraftsRemaining: 3,
	}
	b, _ := json.Marshal(pack)
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	var want map[string]any = contract.Example.Accounts[0]
	if !reflect.DeepEqual(jsonShape(got), jsonShape(want)) {
		t.Fatalf("pack shape\n%v\nwant contract shape\n%v", jsonShape(got), jsonShape(want))
	}
}

func TestUpdateContextEmptyNotesMeetingCountsAsReadable(t *testing.T) {
	f := newCtxFake()
	f.addAccount("a1", "A1")
	f.meetings["a1"] = []model.MeetingRefDTO{
		{MeetingID: "m1", Date: ctxTestNow.Add(-1 * time.Hour)},
		{MeetingID: "m2", Date: ctxTestNow.Add(-2 * time.Hour)},
		{MeetingID: "m3", Date: ctxTestNow.Add(-3 * time.Hour)},
	}
	f.details["m1"] = &model.MeetingDetailResponse{Notes: "  "}
	f.details["m2"] = &model.MeetingDetailResponse{Notes: "two"}
	f.details["m3"] = &model.MeetingDetailResponse{Notes: "three"}
	packs, err := newTestUpdateContext(t, f).Build(context.Background(), "user-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(packs[0].Notes, []string{"two"}) || f.meetingReads != 2 {
		t.Fatalf("notes = %q after %d reads; m1 is readable and counts", packs[0].Notes, f.meetingReads)
	}
}

func TestUpdateContextBlockListForFinalize(t *testing.T) {
	f := newCtxFake()
	f.addAccount("a1", "한빛리테일")
	for i := 0; i < 450; i++ {
		f.accounts = append(f.accounts, model.AccountSummary{AccountID: fmt.Sprintf("b%03d", i), Name: fmt.Sprintf("Other %03d", i)})
	}
	for i := 0; i < 100; i++ {
		f.memberNames["a1"] = append(f.memberNames["a1"], fmt.Sprintf("Member %03d", i))
	}
	f.memberNames["a1"] = append(f.memberNames["a1"], "한빛", "X")
	c := newTestUpdateContext(t, f)
	got, err := c.BlockList(context.Background(), "user-1", "a1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 500 || got[0] != "Member 000" || got[99] != "Member 099" || got[100] != "Other 000" || got[499] != "Other 399" {
		t.Fatalf("block list len %d, first %q, [100] %q, last %q", len(got), got[0], got[100], got[len(got)-1])
	}
	for _, name := range got {
		if name == "한빛" || name == "X" || name == "한빛리테일" {
			t.Fatalf("block list kept %q", name)
		}
	}
	// The pack keeps member names first under its 100-entry cap.
	packs, err := c.Build(context.Background(), "user-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(packs[0].BlockList) == 0 || packs[0].BlockList[0] != "Member 000" || len(packs[0].BlockList) > 100 {
		t.Fatalf("pack block list = %d entries", len(packs[0].BlockList))
	}

	f.notMember["a1"] = ErrForbidden
	if _, err := c.BlockList(context.Background(), "user-1", "a1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-member err = %v", err)
	}
	if _, err := c.BlockList(context.Background(), "user-1", "../a1"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid id err = %v", err)
	}
}

func TestUpdateContextOpenOpportunitiesForFinalize(t *testing.T) {
	f := newCtxFake()
	f.addAccount("a1", "A1")
	f.projects["a1"] = nil
	for i := 12; i >= 0; i-- {
		f.projects["a1"] = append(f.projects["a1"], model.ProjectSummary{ProjectID: fmt.Sprintf("p%02d", i), Name: fmt.Sprintf("Opp\n%d", i), Stage: "Qualified", SfdcOpptyID: "006"})
	}
	f.projects["a1"] = append(f.projects["a1"],
		model.ProjectSummary{ProjectID: "p50", Stage: "Launched", SfdcOpptyID: "006"},
		model.ProjectSummary{ProjectID: "p51", Stage: "Qualified"},
		model.ProjectSummary{ProjectID: "p52", Name: "Unset", SfdcOpptyID: "006"},
	)
	c := newTestUpdateContext(t, f)
	got, err := c.OpenOpportunities(context.Background(), "user-1", "a1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 14 || got[0] != (AccountPackOpportunity{ProjectID: "p00", Name: "Opp 0", Stage: "Qualified"}) ||
		got[13] != (AccountPackOpportunity{ProjectID: "p52", Name: "Unset", Stage: ""}) {
		t.Fatalf("open opportunities = %+v", got)
	}
	f.notMember["a1"] = ErrForbidden
	if _, err := c.OpenOpportunities(context.Background(), "user-1", "a1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-member err = %v", err)
	}
}

func TestMeetingNotesReader(t *testing.T) {
	calls := 0
	var meeting *model.Meeting
	var accessErr error
	r := &MeetingNotesReader{access: func(_ context.Context, _, _ string) (*model.Meeting, string, error) {
		calls++
		return meeting, "owner", accessErr
	}}
	ctx := context.Background()
	if _, err := r.MeetingNotes(ctx, "user-1", "m1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no access err = %v", err)
	}
	meeting = &model.Meeting{MeetingID: "m1", Notes: "saved"}
	if got, err := r.MeetingNotes(ctx, "user-1", "m1"); err != nil || got != "saved" {
		t.Fatalf("notes = %q, %v", got, err)
	}
	accessErr = errors.New("read failed")
	if _, err := r.MeetingNotes(ctx, "user-1", "m1"); !errors.Is(err, accessErr) {
		t.Fatalf("read failure err = %v", err)
	}
	before := calls
	for _, bad := range [][2]string{{"", "m1"}, {"user-1", "../m1"}, {"user-1", ""}} {
		if _, err := r.MeetingNotes(ctx, bad[0], bad[1]); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%q: err = %v", bad, err)
		}
	}
	if calls != before {
		t.Fatal("invalid ids reached the access check")
	}
}
