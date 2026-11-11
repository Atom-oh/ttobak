package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// multiAccountNewsRepo serves several accounts; membership is per account.
type multiAccountNewsRepo struct {
	*accountNewsTestRepo
	accounts    map[string]*model.Account
	members     map[string]bool
	refsBy      map[string][]model.AccountNewsSourceRef
	memberships []model.AccountMember
}

func (r *multiAccountNewsRepo) GetAccount(_ context.Context, id string) (*model.Account, error) {
	return r.accounts[id], nil
}
func (r *multiAccountNewsRepo) GetMember(_ context.Context, id, _ string) (*model.AccountMember, error) {
	if r.members[id] {
		return &model.AccountMember{}, nil
	}
	return nil, nil
}
func (r *multiAccountNewsRepo) ListAccountNewsSources(_ context.Context, id string) ([]model.AccountNewsSourceRef, error) {
	return r.refsBy[id], nil
}
func (r *multiAccountNewsRepo) ListAccountsForUser(context.Context, string) ([]model.AccountMember, error) {
	return r.memberships, nil
}

// newsSourceTestCrawler adds subscriptions and undecodable rows.
type newsSourceTestCrawler struct {
	*accountNewsTestCrawler
	subs    []model.CrawlerSubscription
	invalid map[string]bool
}

func (c *newsSourceTestCrawler) GetSource(ctx context.Context, id string) (*model.CrawlerSource, error) {
	if c.invalid[id] {
		return nil, fmt.Errorf("%w: legacy scalar", repository.ErrInvalidSource)
	}
	return c.accountNewsTestCrawler.GetSource(ctx, id)
}
func (c *newsSourceTestCrawler) ListUserSubscriptions(context.Context, string) ([]model.CrawlerSubscription, error) {
	return c.subs, nil
}

const (
	newsAcctA = "acc-a"
	newsAcctB = "acc-b"
	newsAcctC = "acc-c"
)

func newNewsSourcesTest() (*AccountNewsService, *multiAccountNewsRepo, *newsSourceTestCrawler) {
	repo := &multiAccountNewsRepo{
		accountNewsTestRepo: &accountNewsTestRepo{},
		accounts: map[string]*model.Account{
			newsAcctA: {AccountID: newsAcctA, Name: "Beta Corp"},
			newsAcctB: {AccountID: newsAcctB, Name: "Alpha Inc"},
			newsAcctC: {AccountID: newsAcctC, Name: "Gamma"},
		},
		members: map[string]bool{newsAcctA: true, newsAcctB: true},
		refsBy:  map[string][]model.AccountNewsSourceRef{},
		// acc-c is a stale index row: the member row is gone.
		memberships: []model.AccountMember{{AccountID: newsAcctA}, {PK: "ACCOUNT#" + newsAcctB}, {AccountID: newsAcctC}, {AccountID: newsAcctA}},
	}
	crawler := &newsSourceTestCrawler{
		accountNewsTestCrawler: &accountNewsTestCrawler{sources: map[string]*model.CrawlerSource{}},
		invalid:                map[string]bool{},
	}
	svc := NewAccountNewsService(repo, crawler, &InsightsService{})
	svc.SetNewsSourceDirectory(crawler, repo)
	return svc, repo, crawler
}

func addAccountNewsSource(repo *multiAccountNewsRepo, crawler *newsSourceTestCrawler, accountID, sourceID, name string) {
	repo.refsBy[accountID] = append(repo.refsBy[accountID], model.AccountNewsSourceRef{AccountID: accountID, SourceID: sourceID})
	crawler.sources[sourceID] = &model.CrawlerSource{SourceID: sourceID, SourceName: name, AccountID: accountID, Status: "idle", NewsQueries: []string{name}}
}

func TestListAllNewsSources_ContentsAndOrder(t *testing.T) {
	svc, repo, crawler := newNewsSourcesTest()
	addAccountNewsSource(repo, crawler, newsAcctA, "acct_acc-a_00000001", "Zeta")
	addAccountNewsSource(repo, crawler, newsAcctA, "acct_acc-a_00000002", "Eta")
	addAccountNewsSource(repo, crawler, newsAcctB, "acct_acc-b_00000001", "Kappa")
	addAccountNewsSource(repo, crawler, newsAcctC, "acct_acc-c_00000001", "Hidden")
	// A ref whose CONFIG names another account, and a legacy undecodable row.
	repo.refsBy[newsAcctA] = append(repo.refsBy[newsAcctA], model.AccountNewsSourceRef{SourceID: "acct_acc-b_00000001"}, model.AccountNewsSourceRef{SourceID: "acct_acc-a_00000003"})
	crawler.invalid["acct_acc-a_00000003"] = true

	crawler.sources["aws-blog"] = &model.CrawlerSource{SourceID: "aws-blog", SourceName: "Beta", Status: "active", Subscribers: []string{"user-1"}, UserTiedAt: "t"}
	crawler.sources["alpha-news"] = &model.CrawlerSource{SourceName: "Alpha", Status: "idle"}
	crawler.sources["operator"] = &model.CrawlerSource{SourceID: "operator", SourceName: "Op", GlobalNewsSource: true}
	crawler.sources["owned"] = &model.CrawlerSource{SourceID: "owned", SourceName: "Owned", AccountID: newsAcctB}
	crawler.invalid["legacy"] = true
	for _, id := range []string{"aws-blog", "alpha-news", "acct_acc-a_00000001", "acct_acc-c_00000001", "operator", "owned", "legacy", "gone"} {
		crawler.subs = append(crawler.subs, model.CrawlerSubscription{SourceID: id})
	}

	resp, err := svc.ListAll(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ id, kind, account, name string }
	got := []row{}
	for _, item := range resp.Sources {
		got = append(got, row{item.SourceID, item.Kind, item.AccountName, item.SourceName})
		if !item.CanEdit || item.Aliases == nil || item.CustomUrls == nil || item.NewsSources == nil || item.NewsQueries == nil {
			t.Fatalf("item shape: %+v", item)
		}
		if (item.Kind == "common") != (item.AccountID == "") {
			t.Fatalf("account fields: %+v", item)
		}
	}
	want := []row{
		{"acct_acc-b_00000001", "account", "Alpha Inc", "Kappa"},
		{"acct_acc-a_00000002", "account", "Beta Corp", "Eta"},
		{"acct_acc-a_00000001", "account", "Beta Corp", "Zeta"},
		{"alpha-news", "common", "", "Alpha"},
		{"aws-blog", "common", "", "Beta"},
	}
	if !reflect.DeepEqual(got, want) || resp.Truncated {
		t.Fatalf("list:\n got %+v\nwant %+v (truncated %v)", got, want, resp.Truncated)
	}
}

func TestListAllNewsSources_NonMemberAndBounds(t *testing.T) {
	svc, repo, crawler := newNewsSourcesTest()
	addAccountNewsSource(repo, crawler, newsAcctA, "acct_acc-a_00000001", "Zeta")
	repo.members = map[string]bool{}
	resp, err := svc.ListAll(context.Background(), "user-1")
	if err != nil || len(resp.Sources) != 0 || resp.Sources == nil {
		t.Fatalf("non-member: %+v %v", resp, err)
	}

	repo.memberships = nil
	for i := 0; i < maxNewsSourceAccounts+1; i++ {
		repo.memberships = append(repo.memberships, model.AccountMember{AccountID: fmt.Sprintf("acc-%03d", i)})
	}
	if resp, err = svc.ListAll(context.Background(), "user-1"); err != nil || !resp.Truncated {
		t.Fatalf("account cap: %+v %v", resp, err)
	}

	long := strings.Repeat("가", 300)
	repo.memberships = nil
	crawler.sources["long"] = &model.CrawlerSource{SourceID: "long", SourceName: long}
	crawler.subs = []model.CrawlerSubscription{{SourceID: "long"}}
	if resp, err = svc.ListAll(context.Background(), "user-1"); err != nil || len(resp.Sources) != 1 ||
		len([]rune(resp.Sources[0].SourceName)) != maxOriginNameRunes || resp.Truncated {
		t.Fatalf("bounded name: %+v %v", resp, err)
	}

	unwired := NewAccountNewsService(repo, crawler, &InsightsService{})
	if _, err := unwired.ListAll(context.Background(), "user-1"); err == nil {
		t.Fatal("an unwired directory must fail closed")
	}
}

func newsSourcePtr[T any](v T) *T { return &v }

func TestUpdateAccountNewsSource(t *testing.T) {
	const id = "acct_acc-a_00000001"
	ctx := context.Background()
	t.Run("happy path replaces only present fields", func(t *testing.T) {
		svc, repo, crawler := newNewsSourcesTest()
		addAccountNewsSource(repo, crawler, newsAcctA, id, "Old")
		crawler.sources[id].Aliases = []string{"keep"}
		item, err := svc.Update(ctx, "user-1", newsAcctA, id, &model.UpdateAccountNewsSourceRequest{
			SourceName: newsSourcePtr(" New "), NewsQueries: &[]string{"q", "Q", "r"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if item.SourceID != id || item.SourceName != "New" || item.Kind != "account" || item.AccountName != "Beta Corp" ||
			!reflect.DeepEqual(item.NewsQueries, []string{"q", "r"}) || !reflect.DeepEqual(item.Aliases, []string{"keep"}) || !item.CanEdit {
			t.Fatalf("item: %+v", item)
		}
		if len(repo.updates) != 1 || repo.updates[0].Aliases != nil || *repo.updates[0].SourceName != "New" {
			t.Fatalf("write: %+v", repo.updates)
		}
	})
	for name, tc := range map[string]struct {
		setup func(*multiAccountNewsRepo, *newsSourceTestCrawler)
		req   model.UpdateAccountNewsSourceRequest
		want  error
	}{
		"non-member":        {func(r *multiAccountNewsRepo, _ *newsSourceTestCrawler) { r.members[newsAcctA] = false }, model.UpdateAccountNewsSourceRequest{SourceName: newsSourcePtr("n")}, ErrForbidden},
		"unknown source":    {func(r *multiAccountNewsRepo, _ *newsSourceTestCrawler) { r.refsBy[newsAcctA] = nil }, model.UpdateAccountNewsSourceRequest{SourceName: newsSourcePtr("n")}, ErrNotFound},
		"foreign config":    {func(_ *multiAccountNewsRepo, c *newsSourceTestCrawler) { c.sources[id].AccountID = newsAcctB }, model.UpdateAccountNewsSourceRequest{SourceName: newsSourcePtr("n")}, ErrNotFound},
		"other account ref": {func(r *multiAccountNewsRepo, _ *newsSourceTestCrawler) { r.members[newsAcctB] = true }, model.UpdateAccountNewsSourceRequest{SourceName: newsSourcePtr("n")}, ErrNotFound},
		"deleted":           {func(_ *multiAccountNewsRepo, c *newsSourceTestCrawler) { c.sources[id].Status = "disabled" }, model.UpdateAccountNewsSourceRequest{SourceName: newsSourcePtr("n")}, ErrNotFound},
		"deleted at commit": {func(r *multiAccountNewsRepo, _ *newsSourceTestCrawler) { r.updateErr = repository.ErrConditionFailed }, model.UpdateAccountNewsSourceRequest{SourceName: newsSourcePtr("n")}, ErrNotFound},
		"revoked at commit": {func(r *multiAccountNewsRepo, _ *newsSourceTestCrawler) { r.updateErr = repository.ErrAccountNewsAccess }, model.UpdateAccountNewsSourceRequest{SourceName: newsSourcePtr("n")}, ErrForbidden},
		"empty name":        {nil, model.UpdateAccountNewsSourceRequest{SourceName: newsSourcePtr(" ")}, ErrInvalidInput},
		"long name":         {nil, model.UpdateAccountNewsSourceRequest{SourceName: newsSourcePtr(strings.Repeat("a", 201))}, ErrInvalidInput},
		"empty queries":     {nil, model.UpdateAccountNewsSourceRequest{NewsQueries: &[]string{}}, ErrInvalidInput},
		"bad query":         {nil, model.UpdateAccountNewsSourceRequest{NewsQueries: &[]string{"bad\x00"}}, ErrInvalidInput},
		"too many aliases":  {nil, model.UpdateAccountNewsSourceRequest{Aliases: &[]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15", "16", "17", "18", "19", "20", "21"}}, ErrInvalidInput},
		"nothing to change": {nil, model.UpdateAccountNewsSourceRequest{}, ErrInvalidInput},
	} {
		t.Run(name, func(t *testing.T) {
			svc, repo, crawler := newNewsSourcesTest()
			addAccountNewsSource(repo, crawler, newsAcctA, id, "Old")
			target := newsAcctA
			if tc.setup != nil {
				tc.setup(repo, crawler)
			}
			if name == "other account ref" {
				target = newsAcctB // the ref lives under acc-a only
			}
			_, err := svc.Update(ctx, "user-1", target, id, &tc.req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			atCommit := name == "deleted at commit" || name == "revoked at commit"
			if !atCommit && len(repo.updates) != 0 {
				t.Fatalf("rejected edit reached the write: %+v", repo.updates)
			}
		})
	}
	svc, _, _ := newNewsSourcesTest()
	if _, err := svc.Update(ctx, "user-1", newsAcctA, "../x", &model.UpdateAccountNewsSourceRequest{SourceName: newsSourcePtr("n")}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid id: %v", err)
	}
}
