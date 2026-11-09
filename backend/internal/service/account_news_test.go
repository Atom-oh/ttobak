package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const newsTestAccount = "0f8c1d2e-3a4b-4c5d-8e6f-7a8b9c0d1e2f"

type accountNewsTestRepo struct {
	account      *model.Account
	member       bool
	refs         []model.AccountNewsSourceRef
	created      []*model.CrawlerSource
	deleted      []string
	deleteActors []string
	createErr    []error
	deleteErr    error
	// observed records the (observedCount, limit) pair of each create call.
	observed [][2]int
	// counterModel emulates the NEWSSRC_COUNT row: nil counter is absent.
	counterModel bool
	counter      *int
	rev          int // advanced by every counter mutation, like sourceRev
	reconciles   int
	reconcileErr error
	afterList    func()
	// One-shot hooks between reconcile's counter read, ref list and write.
	afterReconcileRead, afterReconcileList func()
}

func (r *accountNewsTestRepo) GetAccount(context.Context, string) (*model.Account, error) {
	return r.account, nil
}
func (r *accountNewsTestRepo) GetMember(context.Context, string, string) (*model.AccountMember, error) {
	if r.member {
		return &model.AccountMember{}, nil
	}
	return nil, nil
}
func (r *accountNewsTestRepo) CreateAccountNewsSource(_ context.Context, src *model.CrawlerSource, ref *model.AccountNewsSourceRef, observedCount, limit int) error {
	r.observed = append(r.observed, [2]int{observedCount, limit})
	if r.counterModel && len(r.createErr) == 0 {
		if r.counter == nil {
			seed := observedCount
			r.counter = &seed
		}
		if *r.counter >= limit {
			return repository.ErrAccountNewsSourceLimit
		}
		*r.counter++
		r.rev++
	}
	if len(r.createErr) > 0 {
		err := r.createErr[0]
		r.createErr = r.createErr[1:]
		if err != nil {
			return err
		}
	}
	r.created = append(r.created, src)
	r.refs = append(r.refs, *ref)
	return nil
}
func (r *accountNewsTestRepo) ListAccountNewsSources(context.Context, string) ([]model.AccountNewsSourceRef, error) {
	refs := append([]model.AccountNewsSourceRef{}, r.refs...)
	if hook := r.afterList; hook != nil {
		r.afterList = nil
		hook()
	}
	return refs, nil
}
func (r *accountNewsTestRepo) ReconcileAccountNewsSourceCount(context.Context, string) (int, error) {
	r.reconciles++
	if r.reconcileErr != nil {
		return 0, r.reconcileErr
	}
	// Mirrors the repository: read count and revision, list refs, then write
	// only if the revision is unchanged.
	for attempt := 0; attempt < 3; attempt++ {
		if r.counter == nil {
			return len(r.refs), nil
		}
		count, rev := *r.counter, r.rev
		runOnce(&r.afterReconcileRead)
		refs := len(r.refs)
		runOnce(&r.afterReconcileList)
		if refs == count {
			return refs, nil
		}
		if r.counter == nil || *r.counter != count || r.rev != rev {
			continue
		}
		*r.counter = refs
		r.rev++
		return refs, nil
	}
	return 0, errors.New("counter kept changing")
}
func (r *accountNewsTestRepo) DeleteAccountNewsSource(_ context.Context, accountID, userID, sourceID string) error {
	r.deleted = append(r.deleted, accountID+"/"+sourceID)
	r.deleteActors = append(r.deleteActors, userID)
	if r.counterModel && r.deleteErr == nil {
		for i, ref := range r.refs {
			if ref.SourceID == sourceID {
				r.refs = append(r.refs[:i:i], r.refs[i+1:]...)
				if r.counter != nil && *r.counter > 0 {
					*r.counter--
					r.rev++
				}
				break
			}
		}
	}
	return r.deleteErr
}

func runOnce(hook *func()) {
	if h := *hook; h != nil {
		*hook = nil
		h()
	}
}

type accountNewsTestCrawler struct {
	sources map[string]*model.CrawlerSource
	docs    map[string][]model.CrawledDocument
	limits  []int32
}

func (c *accountNewsTestCrawler) GetSource(_ context.Context, id string) (*model.CrawlerSource, error) {
	src := c.sources[id]
	if src == nil {
		return nil, nil
	}
	copied := *src
	return &copied, nil
}
func (c *accountNewsTestCrawler) ListDocuments(_ context.Context, id, docType string, limit int32, _ map[string]types.AttributeValue) ([]model.CrawledDocument, map[string]types.AttributeValue, int, error) {
	c.limits = append(c.limits, limit)
	if docType != "news" {
		return nil, nil, 0, errors.New("unexpected doc type")
	}
	return c.docs[id], nil, len(c.docs[id]), nil
}

func newAccountNewsTest() (*AccountNewsService, *accountNewsTestRepo, *accountNewsTestCrawler) {
	repo := &accountNewsTestRepo{
		account: &model.Account{AccountID: newsTestAccount, Name: "Hanbit Manufacturing", Aliases: []string{"Hanbit"}},
		member:  true,
	}
	crawler := &accountNewsTestCrawler{sources: map[string]*model.CrawlerSource{}, docs: map[string][]model.CrawledDocument{}}
	svc := NewAccountNewsService(repo, crawler, &InsightsService{})
	return svc, repo, crawler
}

func TestCreateAccountNewsSource_DefaultsFromAccount(t *testing.T) {
	svc, repo, _ := newAccountNewsTest()
	src, err := svc.Create(context.Background(), "user-1", newsTestAccount, &model.CreateAccountNewsSourceRequest{SourceName: " Hanbit news "})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(src.NewsQueries, []string{"Hanbit Manufacturing", "Hanbit"}) {
		t.Fatalf("queries: %v", src.NewsQueries)
	}
	if !reflect.DeepEqual(src.Aliases, []string{"Hanbit"}) || !reflect.DeepEqual(src.Subscribers, []string{"user-1"}) {
		t.Fatalf("aliases/subscribers: %v %v", src.Aliases, src.Subscribers)
	}
	if src.AccountID != newsTestAccount || src.OwnerID != "user-1" || src.Status != "idle" || src.SourceName != "Hanbit news" {
		t.Fatalf("source: %+v", src)
	}
	if !regexp.MustCompile(`^acct_[a-z0-9-]+_[0-9a-f]{8}$`).MatchString(src.SourceID) || !strings.HasPrefix(src.SourceID, "acct_"+newsTestAccount+"_") {
		t.Fatalf("source id: %s", src.SourceID)
	}
	if len(repo.created) != 1 || len(repo.refs) != 1 || repo.refs[0].SourceID != src.SourceID || repo.refs[0].CreatedBy != "user-1" || repo.refs[0].AccountID != newsTestAccount {
		t.Fatalf("writes: %+v %+v", repo.created, repo.refs)
	}
}

func TestCreateAccountNewsSource_ExplicitQueriesAndCollisionRetry(t *testing.T) {
	svc, repo, _ := newAccountNewsTest()
	repo.createErr = []error{repository.ErrConditionFailed, nil}
	src, err := svc.Create(context.Background(), "user-1", newsTestAccount, &model.CreateAccountNewsSourceRequest{SourceName: "n", NewsQueries: []string{"a", "A", "b"}})
	if err != nil || !reflect.DeepEqual(src.NewsQueries, []string{"a", "b"}) {
		t.Fatalf("create: %+v %v", src, err)
	}
	for _, tc := range []model.CreateAccountNewsSourceRequest{
		{SourceName: " "},
		{SourceName: "n", NewsQueries: []string{"bad\x00"}},
		{SourceName: "n", CopyFromSourceID: "../x"},
	} {
		if _, err := svc.Create(context.Background(), "user-1", newsTestAccount, &tc); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
	repo.createErr = []error{repository.ErrAccountNewsAccess}
	if _, err := svc.Create(context.Background(), "user-1", newsTestAccount, &model.CreateAccountNewsSourceRequest{SourceName: "n"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("tx access failure: %v", err)
	}
}

func TestCreateAccountNewsSource_CopyLeavesOriginalUntouched(t *testing.T) {
	svc, repo, crawler := newAccountNewsTest()
	original := &model.CrawlerSource{
		SourceID: "hanbit", SourceName: "Hanbit", OwnerID: "other", Subscribers: []string{"other", "user-1"},
		NewsQueries: []string{"Hanbit robotics"}, Aliases: []string{"한빛"}, CustomUrls: []string{"https://example.com/news"}, Status: "idle",
	}
	snapshot := *original
	crawler.sources["hanbit"] = original
	src, err := svc.Create(context.Background(), "user-1", newsTestAccount, &model.CreateAccountNewsSourceRequest{SourceName: "Hanbit", CopyFromSourceID: "hanbit"})
	if err != nil {
		t.Fatal(err)
	}
	if src.SourceID == "hanbit" || !reflect.DeepEqual(src.NewsQueries, []string{"Hanbit robotics"}) ||
		!reflect.DeepEqual(src.Aliases, []string{"한빛"}) || !reflect.DeepEqual(src.CustomUrls, []string{"https://example.com/news"}) ||
		!reflect.DeepEqual(src.Subscribers, []string{"user-1"}) || src.OwnerID != "user-1" {
		t.Fatalf("copy: %+v", src)
	}
	if len(repo.created) != 1 || repo.created[0].SourceID == "hanbit" {
		t.Fatalf("only the new source may be written: %+v", repo.created)
	}
	if !reflect.DeepEqual(*original, snapshot) {
		t.Fatalf("original mutated: %+v", original)
	}
	// Mutating the new source must not alias the original's slices.
	src.CustomUrls[0] = "changed"
	if original.CustomUrls[0] != "https://example.com/news" {
		t.Fatal("copied slice aliases the original")
	}
}

func TestCreateAccountNewsSource_RejectsGlobalSource(t *testing.T) {
	for name, src := range map[string]*model.CrawlerSource{
		"global":       {SourceID: "global", Subscribers: []string{"user-1"}, GlobalNewsSource: true},
		"unsubscribed": {SourceID: "unsubscribed", Subscribers: []string{"other"}},
		// A former member of another account still listed as creator.
		"account": {SourceID: "account", Subscribers: []string{"user-1"}, AccountID: "other-account"},
		"missing": nil,
	} {
		t.Run(name, func(t *testing.T) {
			svc, repo, crawler := newAccountNewsTest()
			if src != nil {
				crawler.sources[name] = src
			}
			_, err := svc.Create(context.Background(), "user-1", newsTestAccount, &model.CreateAccountNewsSourceRequest{SourceName: "n", CopyFromSourceID: name})
			if !errors.Is(err, ErrInvalidInput) || len(repo.created) != 0 {
				t.Fatalf("%v %d", err, len(repo.created))
			}
		})
	}
}

func TestCreateAccountNewsSource_CapsSourcesPerAccount(t *testing.T) {
	svc, repo, _ := newAccountNewsTest()
	for i := 0; i < maxAccountNewsSources; i++ {
		repo.refs = append(repo.refs, model.AccountNewsSourceRef{SourceID: "s"})
	}
	if _, err := svc.Create(context.Background(), "user-1", newsTestAccount, &model.CreateAccountNewsSourceRequest{SourceName: "n"}); !errors.Is(err, ErrInvalidInput) || len(repo.created) != 0 {
		t.Fatalf("cap: %v", err)
	}
}

// A counter-at-cap rejection reconciles the counter to the refs once and
// retries; if the reconciled count is at the cap it is the same 400 as the
// pre-check, and reconcile errors surface.
func TestCreateAccountNewsSource_TransactionalCapRejection(t *testing.T) {
	svc, repo, _ := newAccountNewsTest()
	repo.refs = []model.AccountNewsSourceRef{{SourceID: "a"}, {SourceID: "b"}}
	repo.createErr = []error{repository.ErrAccountNewsSourceLimit, repository.ErrAccountNewsSourceLimit}
	if _, err := svc.Create(context.Background(), "user-1", newsTestAccount, &model.CreateAccountNewsSourceRequest{SourceName: "n"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("limit: %v", err)
	}
	if len(repo.created) != 0 || repo.reconciles != 1 || !reflect.DeepEqual(repo.observed, [][2]int{{2, maxAccountNewsSources}, {2, maxAccountNewsSources}}) {
		t.Fatalf("limit must reconcile once and retry once: %+v %d %v", repo.created, repo.reconciles, repo.observed)
	}

	svc, repo, _ = newAccountNewsTest()
	repo.reconcileErr = errors.New("throttled")
	repo.createErr = []error{repository.ErrAccountNewsSourceLimit}
	if _, err := svc.Create(context.Background(), "user-1", newsTestAccount, &model.CreateAccountNewsSourceRequest{SourceName: "n"}); err == nil || errors.Is(err, ErrInvalidInput) || len(repo.created) != 0 {
		t.Fatalf("reconcile error: %v", err)
	}
}

// Regression: an account whose sources predate the counter seeds it while a
// pre-counter delete commits between the ref read and the create, leaving the
// counter one above the refs. The next create that the refs allow repairs the
// counter and succeeds, and the cap still holds at exactly ten.
func TestCreateAccountNewsSource_RepairsSeedDriftAndKeepsCap(t *testing.T) {
	svc, repo, _ := newAccountNewsTest()
	repo.counterModel = true
	for i := 0; i < maxAccountNewsSources-1; i++ {
		repo.refs = append(repo.refs, model.AccountNewsSourceRef{SourceID: "pre"})
	}
	// The delete commits after Create listed nine refs; the counter is absent,
	// so the delete only pins it and does not change it.
	repo.afterList = func() { repo.refs = repo.refs[1:] }
	if _, err := svc.Create(context.Background(), "user-1", newsTestAccount, &model.CreateAccountNewsSourceRequest{SourceName: "n"}); err != nil {
		t.Fatal(err)
	}
	if *repo.counter != maxAccountNewsSources || len(repo.refs) != maxAccountNewsSources-1 {
		t.Fatalf("drift setup: counter=%d refs=%d", *repo.counter, len(repo.refs))
	}
	// Nine refs leave room: the counter-at-cap rejection reconciles to nine and
	// the retry creates the tenth source.
	if _, err := svc.Create(context.Background(), "user-1", newsTestAccount, &model.CreateAccountNewsSourceRequest{SourceName: "n"}); err != nil {
		t.Fatalf("drifted counter blocked a create under the cap: %v", err)
	}
	if repo.reconciles != 1 || *repo.counter != maxAccountNewsSources || len(repo.refs) != maxAccountNewsSources {
		t.Fatalf("repair: reconciles=%d counter=%d refs=%d", repo.reconciles, *repo.counter, len(repo.refs))
	}
	// At ten refs the cap holds.
	if _, err := svc.Create(context.Background(), "user-1", newsTestAccount, &model.CreateAccountNewsSourceRequest{SourceName: "n"}); !errors.Is(err, ErrInvalidInput) || len(repo.refs) != maxAccountNewsSources {
		t.Fatalf("cap: %v refs=%d", err, len(repo.refs))
	}
	// A counter that is genuinely at the cap (a concurrent create won) is not
	// raised by reconcile: it stays the same 400.
	repo.refs = repo.refs[:maxAccountNewsSources-1]
	repo.afterList = func() { repo.refs = append(repo.refs, model.AccountNewsSourceRef{SourceID: "concurrent"}) }
	if _, err := svc.Create(context.Background(), "user-1", newsTestAccount, &model.CreateAccountNewsSourceRequest{SourceName: "n"}); !errors.Is(err, ErrInvalidInput) || len(repo.refs) != maxAccountNewsSources {
		t.Fatalf("concurrent winner: %v refs=%d", err, len(repo.refs))
	}
}

// Regression for the reconcile ABA race: while reconcile runs, a delete
// (10->9) and a create (9->10) commit, so the count round-trips. The revision
// still changed, so reconcile does not write the stale nine refs, and the
// create that triggered it is rejected instead of making an eleventh source.
func TestCreateAccountNewsSource_ReconcileDeleteThenCreateKeepsCap(t *testing.T) {
	svc, repo, _ := newAccountNewsTest()
	repo.counterModel = true
	for i := 0; i < maxAccountNewsSources-1; i++ {
		repo.refs = append(repo.refs, model.AccountNewsSourceRef{SourceID: fmt.Sprintf("acct_a_%08d", i)})
	}
	nine := maxAccountNewsSources - 1
	repo.counter = &nine
	other := func(id string) {
		src := &model.CrawlerSource{SourceID: id, AccountID: newsTestAccount}
		ref := &model.AccountNewsSourceRef{AccountID: newsTestAccount, SourceID: id, CreatedBy: "user-2"}
		if err := repo.CreateAccountNewsSource(context.Background(), src, ref, len(repo.refs), maxAccountNewsSources); err != nil {
			t.Fatalf("concurrent create: %v", err)
		}
	}
	// This request's pre-check sees nine refs; a concurrent create then takes
	// the tenth slot, so this create's counter condition fails.
	repo.afterList = func() { other("acct_a_concurrent1") }
	repo.afterReconcileRead = func() {
		if err := repo.DeleteAccountNewsSource(context.Background(), newsTestAccount, "user-2", "acct_a_00000000"); err != nil {
			t.Fatal(err)
		}
	}
	repo.afterReconcileList = func() { other("acct_a_concurrent2") }
	_, err := svc.Create(context.Background(), "user-1", newsTestAccount, &model.CreateAccountNewsSourceRequest{SourceName: "n"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected the cap rejection, got %v", err)
	}
	if len(repo.refs) != maxAccountNewsSources || *repo.counter != maxAccountNewsSources {
		t.Fatalf("cap exceeded or counter wrong: refs=%d counter=%d", len(repo.refs), *repo.counter)
	}
}

func TestAccountNews_NonMemberForbidden(t *testing.T) {
	for name, setup := range map[string]func(*accountNewsTestRepo){
		"non-member":      func(r *accountNewsTestRepo) { r.member = false },
		"missing account": func(r *accountNewsTestRepo) { r.account = nil },
	} {
		t.Run(name, func(t *testing.T) {
			svc, repo, crawler := newAccountNewsTest()
			setup(repo)
			crawler.sources["hanbit"] = &model.CrawlerSource{SourceID: "hanbit", Subscribers: []string{"user-1"}}
			ctx := context.Background()
			if _, err := svc.List(ctx, "user-1", newsTestAccount); !errors.Is(err, ErrForbidden) {
				t.Fatalf("list: %v", err)
			}
			if _, err := svc.Create(ctx, "user-1", newsTestAccount, &model.CreateAccountNewsSourceRequest{SourceName: "n", CopyFromSourceID: "hanbit"}); !errors.Is(err, ErrForbidden) {
				t.Fatalf("create: %v", err)
			}
			if err := svc.Delete(ctx, "user-1", newsTestAccount, "acct_x_00000000"); !errors.Is(err, ErrForbidden) {
				t.Fatalf("delete: %v", err)
			}
			if len(repo.created) != 0 || len(repo.deleted) != 0 {
				t.Fatalf("unauthorized writes: %v %v", repo.created, repo.deleted)
			}
		})
	}
}

func TestListAccountNewsSources_DropsMismatchedConfig(t *testing.T) {
	svc, repo, crawler := newAccountNewsTest()
	repo.refs = []model.AccountNewsSourceRef{{SourceID: "acct_a_00000001"}, {SourceID: "acct_a_00000002"}, {SourceID: "acct_a_00000003"}, {SourceID: "acct_a_00000004"}}
	crawler.sources["acct_a_00000001"] = &model.CrawlerSource{SourceID: "acct_a_00000001", AccountID: newsTestAccount, Status: "idle"}
	crawler.sources["acct_a_00000002"] = &model.CrawlerSource{SourceID: "acct_a_00000002", AccountID: "other-account", Status: "idle"}
	crawler.sources["acct_a_00000003"] = &model.CrawlerSource{SourceID: "acct_a_00000003", AccountID: newsTestAccount, Status: "disabled"}
	got, err := svc.List(context.Background(), "user-1", newsTestAccount)
	if err != nil || len(got) != 1 || got[0].SourceID != "acct_a_00000001" {
		t.Fatalf("list: %+v %v", got, err)
	}
}

func TestDeleteAccountNewsSource(t *testing.T) {
	svc, repo, _ := newAccountNewsTest()
	if err := svc.Delete(context.Background(), "user-1", newsTestAccount, "acct_a_00000001"); err != nil || len(repo.deleted) != 1 || repo.deleted[0] != newsTestAccount+"/acct_a_00000001" {
		t.Fatalf("delete: %v %v", err, repo.deleted)
	}
	repo.deleteErr = repository.ErrConditionFailed
	if err := svc.Delete(context.Background(), "user-1", newsTestAccount, "acct_a_00000001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if err := svc.Delete(context.Background(), "user-1", newsTestAccount, "../x"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid id: %v", err)
	}
	// Membership revoked between the service check and the commit.
	repo.deleteErr = repository.ErrAccountNewsAccess
	if err := svc.Delete(context.Background(), "user-1", newsTestAccount, "acct_a_00000001"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked member: %v", err)
	}
	for _, actor := range repo.deleteActors {
		if actor != "user-1" {
			t.Fatalf("delete did not pass the acting user: %v", repo.deleteActors)
		}
	}
}

func TestCurrentNews_FiltersWindowAndCurrentVersion(t *testing.T) {
	svc, repo, crawler := newAccountNewsTest()
	const sid = "acct_a_00000001"
	repo.refs = []model.AccountNewsSourceRef{{SourceID: sid}, {SourceID: "acct_a_00000002"}}
	crawler.sources[sid] = &model.CrawlerSource{SourceID: sid, AccountID: newsTestAccount, Status: "idle"}
	crawler.sources["acct_a_00000002"] = &model.CrawlerSource{SourceID: "acct_a_00000002", AccountID: "other", Status: "idle"}
	from, to := time.Unix(1000, 0), time.Unix(2000, 0)
	doc := func(hash string, at int64) model.CrawledDocument {
		return model.CrawledDocument{SourceID: sid, DocHash: hash, Type: "news", Title: hash, CrawledAt: at}
	}
	old, newer, before, after, gone, unpublished, moved :=
		doc("old", 1000), doc("newer", 1500), doc("before", 999), doc("after", 2000), doc("gone", 1200), doc("unpublished", 1300), doc("moved", 1400)
	crawler.docs[sid] = []model.CrawledDocument{old, newer, before, after, gone, unpublished, moved}
	crawler.docs["acct_a_00000002"] = []model.CrawledDocument{doc("foreign", 1500)}
	movedCurrent := moved
	movedCurrent.CrawledAt = 5000
	fixture := legacyNewsRows(t, sid, []model.CrawledDocument{old, newer, before, after, movedCurrent})
	// A versioned row that is not a valid current publication is excluded.
	fixture.rows[repository.NewsReadKey{SourceID: sid, SortKey: "DOC#unpublished"}] = newsAV(t, map[string]any{
		"PK": "CRAWLER#" + sid, "SK": "DOC#unpublished", "type": "news", "qualityStatus": "PUBLISHED", "schemaVersion": 1, "crawledAt": 1300,
	})
	svc.insights = &InsightsService{news: fixture}

	got, err := svc.CurrentNews(context.Background(), newsTestAccount, from, to)
	if err != nil {
		t.Fatal(err)
	}
	var hashes []string
	for _, d := range got {
		hashes = append(hashes, d.DocHash)
	}
	if !reflect.DeepEqual(hashes, []string{"newer", "old"}) {
		t.Fatalf("current news: %v", hashes)
	}
	if !reflect.DeepEqual(crawler.limits, []int32{0}) {
		t.Fatalf("must read all pages of the owned source only: %v", crawler.limits)
	}
	if got, err := svc.CurrentNews(context.Background(), newsTestAccount, to, from); err != nil || len(got) != 0 {
		t.Fatalf("empty window: %v %v", got, err)
	}
}
