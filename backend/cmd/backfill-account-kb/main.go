// backfill-account-kb is a manually operated CLI (not a Lambda) that creates
// the canonical ACCOUNT#{accountId} / KBREF#{sha256(key)} ref for each KB
// upload whose S3 object carries x-amz-meta-account-id but predates refs.
//
// It lists kb/ objects, reads each object's metadata and, for a tagged
// kb/{owner}/{file} object of an existing account last modified before the
// -before cutoff, writes the ref only if absent (attribute_not_exists) and
// never untagged (no KBUNTAG# tombstone), so a rerun never duplicates refs.
// It never deletes or rewrites objects, refs or tombstones.
//
// Refs untagged before tombstones existed left no tombstone and cannot be told
// apart from pre-ref uploads. -before must therefore be the RFC 3339 time the
// account KB ref feature was first deployed (presign has written refs since
// then): only objects modified strictly before it are candidates, and later
// objects are counted as skipped by the cutoff. Apply mode refuses to run
// without -before; a dry run without it counts every candidate.
//
// -dry-run defaults to true. Output is counts only: no keys, file names,
// user IDs or account IDs are printed.
//
// Requires TABLE_NAME and KB_BUCKET_NAME (no fallbacks) and standard AWS
// credentials/region.
//
// Usage:
//
//	TABLE_NAME=ttobak-main KB_BUCKET_NAME=... go run ./cmd/backfill-account-kb -before 2026-11-01T00:00:00Z [-dry-run=false] [-limit N]
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
	Untagged int `json:"untagged"`
	Skipped  int `json:"skipped"`
	// Merging counts objects whose account is merging or merged; no ref is written.
	Merging int `json:"merging"`
	// AfterCutoff counts tagged objects modified at or after -before.
	AfterCutoff int `json:"afterCutoff"`
}

// errCutoffRequired means apply mode was requested without -before.
var errCutoffRequired = errors.New("-before is required unless -dry-run")

var uploadPrefix = regexp.MustCompile(`^[0-9]{1,20}_`)

const maxListPages = 100000

// backfill considers only objects modified strictly before cutoff; a zero
// cutoff is allowed only for a dry run.
func backfill(ctx context.Context, objects backfillObjects, repo backfillRepository, dryRun bool, limit int, cutoff time.Time) (backfillResult, error) {
	result := backfillResult{}
	if cutoff.IsZero() && !dryRun {
		return result, errCutoffRequired
	}
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
			if !cutoff.IsZero() && !modified.Before(cutoff) {
				// Possibly a ref-era upload whose ref was untagged before
				// tombstones existed; never re-share it.
				result.AfterCutoff++
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
				if errors.Is(err, repository.ErrAccountKBRefUntagged) {
					result.Untagged++
					continue
				}
				if errors.Is(err, repository.ErrAccountMerging) {
					result.Merging++
					continue
				}
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
	before := flag.String("before", "", "RFC 3339 account KB ref rollout time; only objects modified before it are backfilled (required unless -dry-run)")
	flag.Parse()
	if *limit < 0 {
		fmt.Fprintln(os.Stderr, "ERROR: -limit must be >= 0")
		os.Exit(1)
	}
	var cutoff time.Time
	if *before != "" {
		parsed, err := time.Parse(time.RFC3339, *before)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ERROR: -before must be an RFC 3339 time")
			os.Exit(1)
		}
		cutoff = parsed
	}
	if cutoff.IsZero() && !*dryRun {
		fmt.Fprintln(os.Stderr, "ERROR: -before is required unless -dry-run")
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
	result, err := backfill(ctx, objects, repo, *dryRun, *limit, cutoff)
	mode := "APPLY"
	if *dryRun {
		mode = "DRY RUN"
	}
	fmt.Printf("[%s] scanned=%d tagged=%d written=%d existing=%d untagged=%d merging=%d after_cutoff=%d skipped=%d\n", mode, result.Scanned, result.Tagged, result.Written, result.Existing, result.Untagged, result.Merging, result.AfterCutoff, result.Skipped)
	if err != nil {
		// Errors may embed object keys; print only that the run stopped.
		fmt.Fprintln(os.Stderr, "ERROR: backfill stopped before completion; a rerun skips existing and untagged refs")
		os.Exit(1)
	}
}
