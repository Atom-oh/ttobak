// backfill-account-kb is a manually operated CLI (not a Lambda) that creates
// the canonical ACCOUNT#{accountId} / KBREF#{sha256(key)} ref for each KB
// upload whose S3 object carries x-amz-meta-account-id but predates refs.
//
// It lists kb/ objects, reads each object's metadata and, for a tagged
// kb/{owner}/{file} object of an existing account, writes the ref only if
// absent (attribute_not_exists), so reruns are idempotent. It never deletes
// or rewrites objects or refs.
//
// -dry-run defaults to true. Output is counts only: no keys, file names,
// user IDs or account IDs are printed.
//
// Requires TABLE_NAME and KB_BUCKET_NAME (no fallbacks) and standard AWS
// credentials/region.
//
// Usage:
//
//	TABLE_NAME=ttobak-main KB_BUCKET_NAME=... go run ./cmd/backfill-account-kb [-dry-run=false] [-limit N]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

type backfillObjects interface {
	ListKBKeys(ctx context.Context, cursor string) ([]string, string, error)
	HeadDetail(ctx context.Context, key string) (accountID string, modified time.Time, found bool, err error)
}

type backfillRepository interface {
	GetAccount(ctx context.Context, accountID string) (*model.Account, error)
	PutAccountKBRef(ctx context.Context, ref *model.AccountKBRef) error
}

type backfillResult struct {
	Scanned  int `json:"scanned"`
	Tagged   int `json:"tagged"`
	Written  int `json:"written"`
	Existing int `json:"existing"`
	Skipped  int `json:"skipped"`
}

var uploadPrefix = regexp.MustCompile(`^[0-9]{1,20}_`)

const maxListPages = 100000

func backfill(ctx context.Context, objects backfillObjects, repo backfillRepository, dryRun bool, limit int) (backfillResult, error) {
	result := backfillResult{}
	accounts := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages >= maxListPages {
			return result, errors.New("KB object listing page limit exceeded")
		}
		keys, next, err := objects.ListKBKeys(ctx, cursor)
		if err != nil {
			return result, err
		}
		for _, key := range keys {
			if limit > 0 && result.Scanned >= limit {
				return result, nil
			}
			result.Scanned++
			res, ok := model.KnowledgeIndexResource(key)
			if !ok || res.Kind != model.IndexManualKind {
				result.Skipped++
				continue
			}
			accountID, modified, found, err := objects.HeadDetail(ctx, key)
			if err != nil {
				return result, err
			}
			if !found || accountID == "" {
				continue
			}
			if strings.ContainsAny(accountID, "#/") {
				result.Skipped++
				continue
			}
			exists, seen := accounts[accountID]
			if !seen {
				account, err := repo.GetAccount(ctx, accountID)
				if err != nil {
					return result, err
				}
				exists = account != nil && account.AccountID == accountID
				accounts[accountID] = exists
			}
			if !exists {
				result.Skipped++
				continue
			}
			result.Tagged++
			if dryRun {
				continue
			}
			parts := strings.Split(key, "/")
			ref := &model.AccountKBRef{AccountID: accountID, RefID: model.AccountKBRefID(key), S3Key: key,
				FileName: uploadPrefix.ReplaceAllString(parts[2], ""), UploadedBy: parts[1], CreatedAt: modified.UTC()}
			if err := repo.PutAccountKBRef(ctx, ref); err != nil {
				if errors.Is(err, repository.ErrConditionFailed) {
					result.Existing++
					continue
				}
				return result, err
			}
			result.Written++
		}
		if next == "" {
			return result, nil
		}
		cursor = next
	}
}

func main() {
	dryRun := flag.Bool("dry-run", true, "count candidates without writing refs (default true)")
	limit := flag.Int("limit", 0, "maximum kb/ objects to scan (0 = all)")
	flag.Parse()
	if *limit < 0 {
		fmt.Fprintln(os.Stderr, "ERROR: -limit must be >= 0")
		os.Exit(1)
	}
	tableName, bucket := os.Getenv("TABLE_NAME"), os.Getenv("KB_BUCKET_NAME")
	if tableName == "" || bucket == "" {
		fmt.Fprintln(os.Stderr, "ERROR: TABLE_NAME and KB_BUCKET_NAME are required")
		os.Exit(1)
	}
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR: load AWS config")
		os.Exit(1)
	}
	repo := repository.NewDynamoDBRepository(dynamodb.NewFromConfig(cfg), tableName)
	objects := service.NewKBObjectStore(s3.NewFromConfig(cfg), bucket)
	result, err := backfill(ctx, objects, repo, *dryRun, *limit)
	mode := "APPLY"
	if *dryRun {
		mode = "DRY RUN"
	}
	fmt.Printf("[%s] scanned=%d tagged=%d written=%d existing=%d skipped=%d\n", mode, result.Scanned, result.Tagged, result.Written, result.Existing, result.Skipped)
	if err != nil {
		// Errors may embed object keys; print only that the run stopped.
		fmt.Fprintln(os.Stderr, "ERROR: backfill stopped before completion; rerun is safe")
		os.Exit(1)
	}
}
