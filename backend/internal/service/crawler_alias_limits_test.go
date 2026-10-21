package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type aliasRaceRepo struct {
	*mockCrawlerRepo
	raced bool
}

func (m *aliasRaceRepo) UpdateSourcePartial(ctx context.Context, id string, expected *model.CrawlerSource, fields repository.SourcePartialFields) error {
	if fields.Aliases != nil && !m.raced {
		m.raced = true
		m.sources[id].Aliases = append(m.sources[id].Aliases, "concurrent alias")
		return repository.ErrConditionFailed
	}
	return m.mockCrawlerRepo.UpdateSourcePartial(ctx, id, expected, fields)
}

func aliasFixture(count int) *mockCrawlerRepo {
	repo := newMockCrawlerRepo()
	aliases := make([]string, count)
	for i := range aliases {
		aliases[i] = fmt.Sprintf("Alias %d", i)
	}
	repo.sources["source"] = &model.CrawlerSource{
		SourceID: "source", SourceName: "Source", OwnerID: "owner",
		Subscribers: []string{"owner"}, Status: "idle", Aliases: aliases,
	}
	repo.subscriptions[subKey("owner", "source")] = &model.CrawlerSubscription{
		SourceID: "source", NewsQueries: []string{"previous query"},
	}
	return repo
}

func TestAliasesCombinedLimitAfterConcurrentAddition(t *testing.T) {
	for _, operation := range []string{"add", "update"} {
		t.Run(operation, func(t *testing.T) {
			repo := &aliasRaceRepo{mockCrawlerRepo: aliasFixture(19)}
			svc := &CrawlerService{repo: repo}
			var err error
			if operation == "add" {
				_, err = svc.AddSource(context.Background(), "new-user", &model.AddCrawlerSourceRequest{
					SourceName: "Source", Aliases: []string{"requested alias"},
				})
			} else {
				err = svc.UpdateSource(context.Background(), "owner", "source", &model.UpdateCrawlerSourceRequest{
					Aliases: []string{"requested alias"}, NewsQueries: []string{"new query"},
				})
			}
			if !errors.Is(err, ErrInvalidInput) || !repo.raced {
				t.Fatalf("combined limit not checked after race: %v", err)
			}
			source := repo.sources["source"]
			if len(source.Aliases) != 20 || source.OwnerID != "owner" || contains(source.Aliases, "requested alias") {
				t.Fatalf("concurrent source overwritten: %+v", source)
			}
			if repo.subscriptions[subKey("new-user", "source")] != nil ||
				!strSliceEqual(repo.subscriptions[subKey("owner", "source")].NewsQueries, []string{"previous query"}) {
				t.Fatal("subscription changed after rejected alias addition")
			}
		})
	}
}

func TestAliasesAtCapacityAllowDuplicateAndUnrelatedLegacyEdit(t *testing.T) {
	for _, count := range []int{20, 31} {
		repo := aliasFixture(count)
		svc := &CrawlerService{repo: repo}
		var additions []string
		if count == 20 {
			additions = []string{"ALIAS 0"}
		}
		if err := svc.UpdateSource(context.Background(), "owner", "source", &model.UpdateCrawlerSourceRequest{
			Aliases: additions, NewsQueries: []string{"updated query"},
		}); err != nil {
			t.Fatalf("existing aliases prevent unrelated edit at %d: %v", count, err)
		}
		if len(repo.sources["source"].Aliases) != count || repo.sources["source"].Aliases[0] != "Alias 0" {
			t.Fatal("existing aliases changed")
		}
	}
}

func TestAliasUnicodeValidation(t *testing.T) {
	for _, value := range []string{"invalid\xff", "display\u202eoverride", "zero\u200bwidth", strings.Repeat("𠮷", 101)} {
		if _, err := validateAliases([]string{value}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid alias accepted: %q, %v", value, err)
		}
	}
	if _, err := validateAliases([]string{strings.Repeat("𠮷", 100), "우리은행"}); err != nil {
		t.Fatalf("valid Unicode alias rejected: %v", err)
	}
}

func TestAliasUpdateMissingSourceDoesNotChangeSubscription(t *testing.T) {
	repo := aliasFixture(1)
	delete(repo.sources, "source")
	err := (&CrawlerService{repo: repo}).UpdateSource(context.Background(), "owner", "source", &model.UpdateCrawlerSourceRequest{
		Aliases: []string{"new"}, NewsQueries: []string{"new query"},
	})
	if !errors.Is(err, ErrNotFound) ||
		!strSliceEqual(repo.subscriptions[subKey("owner", "source")].NewsQueries, []string{"previous query"}) {
		t.Fatalf("missing source changed subscription: %v", err)
	}
}
