package service

import (
	"context"
	"errors"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func markedSource() *model.CrawlerSource {
	return &model.CrawlerSource{SourceID: "alphagrid-news", SourceName: "AlphaGrid News", NewsQueries: []string{"AlphaGrid"},
		Schedule: "daily", Status: "active", GlobalNewsSource: true}
}

// Users cannot subscribe to an operator-curated global news source (R16).
func TestAddSource_GlobalNewsSourceRefused(t *testing.T) {
	repo := newMockCrawlerRepo()
	repo.sources["alphagrid-news"] = markedSource()
	svc := &CrawlerService{repo: repo}
	_, err := svc.AddSource(context.Background(), "user1", &model.AddCrawlerSourceRequest{
		SourceName: "AlphaGrid News", NewsQueries: []string{"private customer"}, CustomUrls: []string{"https://private.example"},
	})
	if !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("subscribe to a marked source: %v", err)
	}
	src := repo.sources["alphagrid-news"]
	if len(src.Subscribers) != 0 || len(src.CustomUrls) != 0 || len(src.NewsQueries) != 1 || src.UserTiedAt != "" {
		t.Fatalf("marked source changed: %+v", src)
	}
	if _, ok := repo.subscriptions[subKey("user1", "alphagrid-news")]; ok {
		t.Fatal("no subscription row for a refused subscribe")
	}
}

// markingRaceRepo reads a source as unmarked while the stored item is
// already marked, as if the operator marked it between read and write.
type markingRaceRepo struct{ *mockCrawlerRepo }

func (r markingRaceRepo) GetSource(ctx context.Context, id string) (*model.CrawlerSource, error) {
	src, err := r.mockCrawlerRepo.GetSource(ctx, id)
	if src != nil {
		src.GlobalNewsSource = false
	}
	return src, err
}

func TestAddSource_MarkedDuringMergeRefusedByCondition(t *testing.T) {
	repo := newMockCrawlerRepo()
	repo.sources["alphagrid-news"] = markedSource()
	svc := &CrawlerService{repo: markingRaceRepo{repo}}
	_, err := svc.AddSource(context.Background(), "user1", &model.AddCrawlerSourceRequest{SourceName: "AlphaGrid News", NewsQueries: []string{"x"}})
	if !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("the write condition must refuse a source marked mid-merge: %v", err)
	}
	if src := repo.sources["alphagrid-news"]; len(src.Subscribers) != 0 || len(src.NewsQueries) != 1 {
		t.Fatalf("marked source changed: %+v", src)
	}
}

// A stale subscription to a marked source cannot update, wipe or disable it.
func TestUserUpdateAndUnsubscribeCannotModifyMarkedSource(t *testing.T) {
	for _, race := range []bool{false, true} {
		repo := newMockCrawlerRepo()
		repo.sources["alphagrid-news"] = markedSource()
		repo.subscriptions[subKey("user1", "alphagrid-news")] = &model.CrawlerSubscription{SourceID: "alphagrid-news"}
		var svc *CrawlerService
		if race {
			svc = &CrawlerService{repo: markingRaceRepo{repo}}
		} else {
			svc = &CrawlerService{repo: repo}
		}
		err := svc.UpdateSource(context.Background(), "user1", "alphagrid-news", &model.UpdateCrawlerSourceRequest{
			NewsQueries: []string{"private"}, Aliases: []string{"Alpha"},
		})
		if !errors.Is(err, ErrSourceUnavailable) {
			t.Fatalf("race=%v update: %v", race, err)
		}
		if err := svc.Unsubscribe(context.Background(), "user1", "alphagrid-news"); !errors.Is(err, ErrSourceUnavailable) {
			t.Fatalf("race=%v unsubscribe: %v", race, err)
		}
		src := repo.sources["alphagrid-news"]
		if src.Status != "active" || len(src.NewsQueries) != 1 || len(src.Aliases) != 0 || src.UserTiedAt != "" {
			t.Fatalf("race=%v marked source changed: %+v", race, src)
		}
	}
}

// Every user write leaves the sticky taint, and unsubscribing does not clear it.
func TestUserWritesTaintSourceForGood(t *testing.T) {
	repo := newMockCrawlerRepo()
	repo.sources["k-culture"] = &model.CrawlerSource{SourceID: "k-culture", SourceName: "K-culture", Status: "active"}
	svc := &CrawlerService{repo: repo}
	if _, err := svc.AddSource(context.Background(), "user1", &model.AddCrawlerSourceRequest{SourceName: "K-culture", NewsQueries: []string{"q"}}); err != nil {
		t.Fatal(err)
	}
	if repo.sources["k-culture"].UserTiedAt == "" {
		t.Fatal("a subscribe must taint the source")
	}
	if err := svc.Unsubscribe(context.Background(), "user1", "k-culture"); err != nil {
		t.Fatal(err)
	}
	if src := repo.sources["k-culture"]; src.UserTiedAt == "" || len(src.Subscribers) != 0 {
		t.Fatalf("the taint survives the last unsubscribe: %+v", src)
	}
}
