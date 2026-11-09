package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const accountSourceID = "acct_0f8c1d2e-3a4b-4c5d-8e6f-7a8b9c0d1e2f_0000abcd"

func accountOwnedSource() *model.CrawlerSource {
	return &model.CrawlerSource{SourceID: accountSourceID, SourceName: "Hanbit", OwnerID: "creator", AccountID: "0f8c1d2e-3a4b-4c5d-8e6f-7a8b9c0d1e2f",
		Subscribers: []string{"creator"}, NewsQueries: []string{"Hanbit"}, CustomUrls: []string{"https://example.com"}, Schedule: "daily", Status: "idle"}
}

// accountUnmarkedRepo reads an account source without its accountId, as if a
// stale read raced the write; the repository condition must still refuse it.
type accountUnmarkedRepo struct{ *mockCrawlerRepo }

func (r accountUnmarkedRepo) GetSource(ctx context.Context, id string) (*model.CrawlerSource, error) {
	src, err := r.mockCrawlerRepo.GetSource(ctx, id)
	if src != nil {
		src.AccountID = ""
	}
	return src, err
}

// sourceWriteCounter records source/subscription writes so a fresh read is
// shown to be refused by the service before any write is attempted.
type sourceWriteCounter struct {
	*mockCrawlerRepo
	writes *int
}

func (r sourceWriteCounter) UpdateSourcePartial(ctx context.Context, id string, expected *model.CrawlerSource, fields repository.SourcePartialFields) error {
	*r.writes++
	return r.mockCrawlerRepo.UpdateSourcePartial(ctx, id, expected, fields)
}

func (r sourceWriteCounter) PutSubscription(ctx context.Context, userID string, sub *model.CrawlerSubscription) error {
	*r.writes++
	return r.mockCrawlerRepo.PutSubscription(ctx, userID, sub)
}

// crawlerService returns a service over repo. A fresh read must be refused by
// the service with no write attempt; a stale read (accountId missing) must be
// refused by the repository write condition.
func crawlerService(t *testing.T, repo *mockCrawlerRepo, staleRead bool) *CrawlerService {
	if staleRead {
		return &CrawlerService{repo: accountUnmarkedRepo{repo}}
	}
	writes := 0
	t.Cleanup(func() {
		if writes != 0 {
			t.Errorf("service attempted %d writes to an account source", writes)
		}
	})
	return &CrawlerService{repo: sourceWriteCounter{repo, &writes}}
}

func requireAccountSourceUnchanged(t *testing.T, repo *mockCrawlerRepo, id string) {
	t.Helper()
	want := accountOwnedSource()
	want.SourceID = id
	if got := repo.sources[id]; !reflect.DeepEqual(got, want) {
		t.Fatalf("account source changed: %+v", got)
	}
}

func seedAccountSource(repo *mockCrawlerRepo, id string) {
	src := accountOwnedSource()
	src.SourceID = id
	repo.sources[id] = src
}

func TestUnsubscribe_RefusesAccountSource(t *testing.T) {
	for _, stale := range []bool{false, true} {
		for _, user := range []string{"stranger", "creator"} {
			repo := newMockCrawlerRepo()
			seedAccountSource(repo, accountSourceID)
			if err := crawlerService(t, repo, stale).Unsubscribe(context.Background(), user, accountSourceID); !errors.Is(err, ErrSourceUnavailable) {
				t.Fatalf("stale=%v %s: %v", stale, user, err)
			}
			requireAccountSourceUnchanged(t, repo, accountSourceID)
		}
	}
}

func TestUpdateSource_RefusesAccountSource(t *testing.T) {
	for _, stale := range []bool{false, true} {
		repo := newMockCrawlerRepo()
		seedAccountSource(repo, accountSourceID)
		repo.subscriptions[subKey("stranger", accountSourceID)] = &model.CrawlerSubscription{SourceID: accountSourceID}
		err := crawlerService(t, repo, stale).UpdateSource(context.Background(), "stranger", accountSourceID, &model.UpdateCrawlerSourceRequest{
			NewsQueries: []string{"other"}, CustomUrls: []string{"https://attacker.example"}, Aliases: []string{"x"},
		})
		if !errors.Is(err, ErrSourceUnavailable) {
			t.Fatalf("stale=%v: %v", stale, err)
		}
		requireAccountSourceUnchanged(t, repo, accountSourceID)
		if !stale {
			if got := repo.subscriptions[subKey("stranger", accountSourceID)]; len(got.NewsQueries) != 0 || len(got.CustomUrls) != 0 {
				t.Fatalf("subscription rewritten: %+v", got)
			}
		}
	}
}

func TestAddSource_DoesNotMergeIntoAccountSource(t *testing.T) {
	// Same display name: normalizeSourceID never yields an acct_ ID (it drops
	// "_"), so the user gets a separate per-user source.
	repo := newMockCrawlerRepo()
	seedAccountSource(repo, accountSourceID)
	resp, err := (&CrawlerService{repo: repo}).AddSource(context.Background(), "stranger", &model.AddCrawlerSourceRequest{
		SourceName: "Hanbit", NewsQueries: []string{"other"}, CustomUrls: []string{"https://attacker.example"},
	})
	if err != nil || resp.Source.SourceID == accountSourceID || resp.Source.AccountID != "" {
		t.Fatalf("separate source: %+v %v", resp, err)
	}
	requireAccountSourceUnchanged(t, repo, accountSourceID)

	// Defense in depth: an account-owned row at a name-derived ID is refused,
	// including when the read misses accountId (repository condition).
	for _, stale := range []bool{false, true} {
		repo := newMockCrawlerRepo()
		seedAccountSource(repo, "hanbit")
		_, err := crawlerService(t, repo, stale).AddSource(context.Background(), "stranger", &model.AddCrawlerSourceRequest{
			SourceName: "Hanbit", NewsQueries: []string{"other"}, CustomUrls: []string{"https://attacker.example"},
		})
		if !errors.Is(err, ErrSourceUnavailable) {
			t.Fatalf("stale=%v: %v", stale, err)
		}
		requireAccountSourceUnchanged(t, repo, "hanbit")
		if _, ok := repo.subscriptions[subKey("stranger", "hanbit")]; ok {
			t.Fatalf("stale=%v: subscription created for an account source", stale)
		}
	}
}
