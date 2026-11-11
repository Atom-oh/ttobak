package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
func (c *newsSourceTestCrawler) ListUserSubscriptionsBounded(_ context.Context, _ string, limit int) ([]model.CrawlerSubscription, bool, error) {
	if len(c.subs) > limit {
		return c.subs[:limit], true, nil
	}
	return c.subs, false, nil
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

	crawler.sources["aws-blog"] = &model.CrawlerSource{SourceID: "aws-blog", SourceName: "Beta", Status: "active"}
	crawler.sources["alpha-news"] = &model.CrawlerSource{SourceName: "Alpha", Status: "idle"}
	crawler.sources["operator"] = &model.CrawlerSource{SourceID: "operator", SourceName: "Op", GlobalNewsSource: true}
	crawler.sources["owned"] = &model.CrawlerSource{SourceID: "owned", SourceName: "Owned", AccountID: newsAcctB}
	crawler.invalid["legacy"] = true
	for _, id := range []string{"aws-blog", "alpha-news", "acct_acc-a_00000001", "acct_acc-c_00000001", "operator", "owned", "legacy", "gone"} {
		crawler.subs = append(crawler.subs, model.CrawlerSubscription{SourceID: id})
	}

	resp, err := svc.ListAll(context.Background(), "user-1", "", 0)
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
	resp, err := svc.ListAll(context.Background(), "user-1", "", 0)
	if err != nil || len(resp.Sources) != 0 || resp.Sources == nil {
		t.Fatalf("non-member: %+v %v", resp, err)
	}

	repo.memberships = nil
	for i := 0; i < maxNewsSourceAccounts+1; i++ {
		repo.memberships = append(repo.memberships, model.AccountMember{AccountID: fmt.Sprintf("acc-%03d", i)})
	}
	if resp, err = svc.ListAll(context.Background(), "user-1", "", 0); err != nil || !resp.Truncated {
		t.Fatalf("account cap: %+v %v", resp, err)
	}

	unwired := NewAccountNewsService(repo, crawler, &InsightsService{})
	if _, err := unwired.ListAll(context.Background(), "user-1", "", 0); err == nil {
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
	rename := model.UpdateAccountNewsSourceRequest{SourceName: newsSourcePtr("n")}
	for name, tc := range map[string]struct {
		setup func(*multiAccountNewsRepo, *newsSourceTestCrawler)
		req   model.UpdateAccountNewsSourceRequest
		want  error
	}{
		"non-member":        {func(r *multiAccountNewsRepo, _ *newsSourceTestCrawler) { r.members[newsAcctA] = false }, rename, ErrForbidden},
		"unknown source":    {func(r *multiAccountNewsRepo, _ *newsSourceTestCrawler) { r.refsBy[newsAcctA] = nil }, rename, ErrNotFound},
		"foreign config":    {func(_ *multiAccountNewsRepo, c *newsSourceTestCrawler) { c.sources[id].AccountID = newsAcctB }, rename, ErrNotFound},
		"other account ref": {func(r *multiAccountNewsRepo, _ *newsSourceTestCrawler) { r.members[newsAcctB] = true }, rename, ErrNotFound},
		"deleted":           {func(_ *multiAccountNewsRepo, c *newsSourceTestCrawler) { c.sources[id].Status = "disabled" }, rename, ErrNotFound},
		"deleted at commit": {func(r *multiAccountNewsRepo, _ *newsSourceTestCrawler) { r.updateErr = repository.ErrConditionFailed }, rename, ErrNotFound},
		"revoked at commit": {func(r *multiAccountNewsRepo, _ *newsSourceTestCrawler) { r.updateErr = repository.ErrAccountNewsAccess }, rename, ErrForbidden},
		"empty name":        {nil, model.UpdateAccountNewsSourceRequest{SourceName: newsSourcePtr(" ")}, ErrInvalidInput},
		"long name":         {nil, model.UpdateAccountNewsSourceRequest{SourceName: newsSourcePtr(strings.Repeat("a", 201))}, ErrInvalidInput},
		"empty queries":     {nil, model.UpdateAccountNewsSourceRequest{NewsQueries: &[]string{}}, ErrInvalidInput},
		"bad query":         {nil, model.UpdateAccountNewsSourceRequest{NewsQueries: &[]string{"bad\x00"}}, ErrInvalidInput},
		"too many aliases":  {nil, model.UpdateAccountNewsSourceRequest{Aliases: newsSourcePtr(strings.Split("1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21", ","))}, ErrInvalidInput},
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

func TestListAllNewsSources_PagingWalksDirectory(t *testing.T) {
	svc, repo, crawler := newNewsSourcesTest()
	for i := 0; i < 7; i++ {
		// Duplicate names exercise the ID tiebreak across page boundaries.
		addAccountNewsSource(repo, crawler, newsAcctA, fmt.Sprintf("acct_acc-a_0000000%d", i), fmt.Sprintf("S%d", i%3))
	}
	addAccountNewsSource(repo, crawler, newsAcctB, "acct_acc-b_00000001", "Only")
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("common-%d", i)
		crawler.sources[id] = &model.CrawlerSource{SourceID: id, SourceName: fmt.Sprintf("C%d", i%2)}
		crawler.subs = append(crawler.subs, model.CrawlerSubscription{SourceID: id})
	}
	full, err := svc.ListAll(context.Background(), "user-1", "", maxNewsSourcePage)
	if err != nil || len(full.Sources) != 14 || full.NextCursor != "" {
		t.Fatalf("full: %d %q %v", len(full.Sources), full.NextCursor, err)
	}
	for _, limit := range []int{1, 3, 5, 13, 14} {
		walked, cursor := []model.NewsSourceItem{}, ""
		for pages := 0; ; pages++ {
			page, err := svc.ListAll(context.Background(), "user-1", cursor, limit)
			if err != nil || pages > 20 || len(page.Sources) > limit {
				t.Fatalf("limit %d page %d: %d %v", limit, pages, len(page.Sources), err)
			}
			walked = append(walked, page.Sources...)
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		if !reflect.DeepEqual(walked, full.Sources) {
			t.Fatalf("limit %d: walk differs from the full order", limit)
		}
	}
	page, _ := svc.ListAll(context.Background(), "user-1", "", 2)
	key := newsSourceKeyOf(page.Sources[1])
	forge := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	tag := newsSourceCursorUser("user-1")
	for name, cursor := range map[string]string{
		"not base64":      "!!",
		"not json":        base64.RawURLEncoding.EncodeToString([]byte("nope")),
		"other user":      encodeNewsSourceCursor("user-2", key),
		"unknown version": forge(map[string]any{"v": 2, "u": tag, "k": key}),
		"unknown field":   forge(map[string]any{"v": 1, "u": tag, "k": key, "x": 1}),
		"bad kind":        forge(map[string]any{"v": 1, "u": tag, "k": [5]string{"2", "", "", "n", "id"}}),
		"common account":  forge(map[string]any{"v": 1, "u": tag, "k": [5]string{"1", "A", "acc", "n", "id"}}),
		"oversized":       strings.Repeat("A", maxNewsSourceCursor+4),
	} {
		if _, err := svc.ListAll(context.Background(), "user-1", cursor, 2); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func maximalNewsSource(id, accountID string) *model.CrawlerSource {
	// Control characters and U+2028 make encoding/json write 6 bytes each.
	worst := func(n int) string { return strings.Repeat("\x01\u2028", n/2+1) }
	list := func(count, runes int) []string {
		out := []string{}
		for i := 0; i < count; i++ {
			out = append(out, worst(runes))
		}
		return out
	}
	return &model.CrawlerSource{
		SourceID: id, SourceName: worst(400), AccountID: accountID, Status: worst(100), LastCrawledAt: worst(100),
		DocumentCount: -1 << 62, NewsQueries: list(40, 400), Aliases: list(30, 300), NewsSources: list(30, 300),
		CustomUrls: list(30, 5000),
	}
}

func TestListAllNewsSources_ItemAndPageBounds(t *testing.T) {
	item := newsSourceItem(maximalNewsSource("acct_acc-a_00000001", newsAcctA), model.NewsSourceKindAccount, newsAcctA, strings.Repeat("\x01", 500))
	encoded, err := json.Marshal(item)
	if err != nil || !item.ListsTruncated || len(encoded) > maxNewsSourceItemBytes {
		t.Fatalf("maximal item: %d bytes, truncated %v, %v", len(encoded), item.ListsTruncated, err)
	}
	if len(item.NewsQueries) == 0 || len(item.CustomUrls) == 0 || len([]rune(item.SourceName)) != maxOriginNameRunes {
		t.Fatalf("bounded item kept nothing: %+v", item)
	}
	small := newsSourceItem(&model.CrawlerSource{SourceID: "s", NewsQueries: []string{"q"}}, model.NewsSourceKindCommon, "", "")
	if small.ListsTruncated {
		t.Fatal("an item within bounds must not be flagged")
	}

	svc, repo, crawler := newNewsSourcesTest()
	repo.memberships = nil
	for a := 0; a < 10; a++ {
		accountID := fmt.Sprintf("acc-%03d", a)
		repo.accounts[accountID] = &model.Account{AccountID: accountID, Name: strings.Repeat("\x01", 300)}
		repo.members[accountID] = true
		repo.memberships = append(repo.memberships, model.AccountMember{AccountID: accountID})
		for i := 0; i < maxAccountNewsSources; i++ {
			id := fmt.Sprintf("acct_%s_%08d", accountID, i)
			repo.refsBy[accountID] = append(repo.refsBy[accountID], model.AccountNewsSourceRef{SourceID: id})
			crawler.sources[id] = maximalNewsSource(id, accountID)
		}
	}
	page, err := svc.ListAll(context.Background(), "user-1", "", maxNewsSourcePage)
	if err != nil || len(page.Sources) != maxNewsSourcePage {
		t.Fatalf("worst page: %d %v", len(page.Sources), err)
	}
	page.NextCursor = strings.Repeat("A", maxNewsSourceCursor)
	page.Truncated = true
	if encoded, _ := json.Marshal(page); len(encoded) > maxNewsSourcePageBytes || maxNewsSourcePageBytes*2 > 6*1024*1024 {
		t.Fatalf("worst page = %d bytes, bound %d", len(encoded), maxNewsSourcePageBytes)
	}
}

func TestListAllNewsSources_SubscriptionCapSetsTruncated(t *testing.T) {
	for _, count := range []int{maxNewsSourceSubscriptions, maxNewsSourceSubscriptions + 1} {
		svc, repo, crawler := newNewsSourcesTest()
		repo.memberships = nil
		for i := 0; i < count; i++ {
			id := fmt.Sprintf("common-%04d", i)
			crawler.sources[id] = &model.CrawlerSource{SourceID: id, SourceName: id}
			crawler.subs = append(crawler.subs, model.CrawlerSubscription{SourceID: id})
		}
		total, cursor := 0, ""
		var last *model.NewsSourcesResponse
		for {
			page, err := svc.ListAll(context.Background(), "user-1", cursor, maxNewsSourcePage)
			if err != nil {
				t.Fatal(err)
			}
			total, last = total+len(page.Sources), page
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		if total != maxNewsSourceSubscriptions || last.Truncated != (count > maxNewsSourceSubscriptions) {
			t.Fatalf("%d subscriptions: listed %d, truncated %v", count, total, last.Truncated)
		}
	}
}
