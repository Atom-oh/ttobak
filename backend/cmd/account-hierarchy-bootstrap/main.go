package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type bootstrapRepository interface {
	ScanAccountHierarchyPage(context.Context, string) (*repository.AccountHierarchyPage, error)
	GetAccount(context.Context, string) (*model.Account, error)
	EnsureAccountChildReference(context.Context, string, string) error
	ListChildAccountIDsPage(context.Context, string, string, int32) ([]string, *string, error)
}
type bootstrapResult struct {
	Accounts          int `json:"accounts"`
	Links             int `json:"links"`
	Written           int `json:"written"`
	Verified          int `json:"verified"`
	ConcurrentChanges int `json:"concurrentChanges"`
}

func bootstrap(ctx context.Context, repo bootstrapRepository, apply, verify bool) (bootstrapResult, error) {
	result := bootstrapResult{}
	cursor := ""
	seen := map[string]bool{}
	for pages := 0; ; pages++ {
		if pages >= 10000 {
			return result, errors.New("account metadata scan page limit exceeded")
		}
		page, err := repo.ScanAccountHierarchyPage(ctx, cursor)
		if err != nil {
			return result, err
		}
		if page == nil || len(page.Accounts) > 100 {
			return result, errors.New("invalid account metadata scan page")
		}
		for _, a := range page.Accounts {
			result.Accounts++
			if a.ParentAccountID == "" {
				continue
			}
			result.Links++
			parent, err := repo.GetAccount(ctx, a.ParentAccountID)
			if err != nil {
				return result, err
			}
			if parent == nil || parent.AccountID != a.ParentAccountID || parent.PK != model.PrefixAccount+a.ParentAccountID || parent.SK != model.SKAccountMeta || parent.EntityType != model.EntityTypeAccount {
				return result, errors.New("canonical account parent is missing or invalid")
			}
			if apply {
				if err := repo.EnsureAccountChildReference(ctx, a.AccountID, a.ParentAccountID); err != nil {
					if errors.Is(err, repository.ErrConditionFailed) {
						result.ConcurrentChanges++
						continue
					}
					return result, err
				}
				result.Written++
			}
			if verify {
				child, err := repo.GetAccount(ctx, a.AccountID)
				if err != nil {
					return result, err
				}
				if child == nil || child.ParentAccountID != a.ParentAccountID {
					result.ConcurrentChanges++
					continue
				}
				refCursor := ""
				refCursors := map[string]bool{}
				found := false
				for n := 0; ; n++ {
					if n >= 10000 {
						return result, errors.New("child reference verification page limit exceeded")
					}
					ids, next, err := repo.ListChildAccountIDsPage(ctx, a.ParentAccountID, refCursor, 100)
					if err != nil {
						return result, err
					}
					for _, id := range ids {
						found = found || id == a.AccountID
					}
					if found || next == nil {
						break
					}
					if *next == "" || *next == refCursor || refCursors[*next] {
						return result, errors.New("invalid child reference verification continuation")
					}
					refCursors[*next] = true
					refCursor = *next
				}
				if !found {
					return result, errors.New("canonical account child reference missing")
				}
				current, err := repo.GetAccount(ctx, a.AccountID)
				if err != nil {
					return result, err
				}
				if current == nil || current.ParentAccountID != a.ParentAccountID {
					result.ConcurrentChanges++
					continue
				}
				result.Verified++
			}
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor || seen[page.NextCursor] {
			return result, errors.New("invalid account metadata scan continuation")
		}
		seen[page.NextCursor] = true
		cursor = page.NextCursor
	}
	if result.ConcurrentChanges > 0 {
		return result, errors.New("hierarchy changed concurrently; rerun bootstrap")
	}
	return result, nil
}

func main() {
	table := flag.String("table", "", "required DynamoDB table")
	region := flag.String("region", "ap-northeast-2", "AWS region")
	apply := flag.Bool("apply", false, "conditionally write missing canonical child references")
	verify := flag.Bool("verify", false, "strongly verify child references against canonical parents")
	flag.Parse()
	if *table == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "specify -table; default is read-only dry-run")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(*region))
	if err != nil {
		fmt.Fprintln(os.Stderr, "AWS configuration unavailable")
		os.Exit(1)
	}
	result, err := bootstrap(ctx, repository.NewDynamoDBRepository(dynamodb.NewFromConfig(cfg), *table), *apply, *verify)
	_ = json.NewEncoder(os.Stdout).Encode(result)
	if err != nil {
		// Keep operator receipts free of SDK request data or private item content.
		fmt.Fprintln(os.Stderr, "bootstrap failed; inspect configuration or rerun after concurrent changes")
		os.Exit(1)
	}
}
