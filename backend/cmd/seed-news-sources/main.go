// seed-news-sources is a manually operated CLI (not a Lambda) that creates or
// updates the code-managed operator-curated news sources in sources.go: Korean
// financial regulators and regulation topics, crawled daily by
// ttobak-news-daily and eligible for the Common dictionary as global sources
// (repository.IsGlobalNewsSource).
//
// Each source is a CONFIG row CRAWLER#{sourceId} with globalNewsSource = true,
// status active (set only on creation), no owner, subscribers or custom URLs,
// and an optional searchDomains allowlist. A dry run (the default) only reads
// and prints CREATE, UPDATE or UNCHANGED per source. -apply writes
// conditionally: a create never overwrites a row, an update changes only
// sourceName, aliases, newsQueries and searchDomains of a row that is still
// operator-managed, and a row a user, subscriber or account owns is REFUSED
// and left unchanged. A disabled seeded source stays disabled. The daily
// schedule picks rows up on its next run; nothing is crawled immediately.
//
// Requires TABLE_NAME (no fallback) and standard AWS credentials/region.
//
// Usage:
//
//	TABLE_NAME=ttobak-main go run ./cmd/seed-news-sources [-apply] [-source fss-kr]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/repository"
)

// upserter is the repository call the seed needs.
type upserter interface {
	UpsertOperatorNewsSource(ctx context.Context, src repository.OperatorNewsSource, apply bool) (repository.OperatorSourceOutcome, error)
}

// run seeds sources (all when only is empty) and reports one line per source.
// It continues past a refused or failed source and returns an error if any.
func run(ctx context.Context, repo upserter, sources []repository.OperatorNewsSource, only string, apply bool, out io.Writer) error {
	mode := "dry-run"
	if apply {
		mode = "apply"
	}
	var failed []string
	matched := false
	for _, src := range sources {
		if only != "" && src.SourceID != only {
			continue
		}
		matched = true
		outcome, err := repo.UpsertOperatorNewsSource(ctx, src, apply)
		switch {
		case errors.Is(err, repository.ErrNotOperatorSource):
			fmt.Fprintf(out, "%s %s REFUSED (row is not operator-managed)\n", mode, src.SourceID)
			failed = append(failed, src.SourceID)
		case err != nil:
			fmt.Fprintf(out, "%s %s FAILED: %v\n", mode, src.SourceID, err)
			failed = append(failed, src.SourceID)
		default:
			fmt.Fprintf(out, "%s %s %s\n", mode, src.SourceID, outcome)
		}
	}
	if !matched {
		return fmt.Errorf("no seeded source %q", only)
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d source(s) not seeded: %v", len(failed), failed)
	}
	return nil
}

func main() {
	apply := flag.Bool("apply", false, "write changes (default: dry run, reads only)")
	only := flag.String("source", "", "seed only this source ID")
	flag.Parse()

	tableName := os.Getenv("TABLE_NAME")
	if tableName == "" {
		log.Fatal("TABLE_NAME is required")
	}
	if err := validateSources(regulatorySources); err != nil {
		log.Fatalf("invalid seed definition: %v", err)
	}
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("load AWS config: %v", err)
	}
	repo := repository.NewCrawlerRepository(dynamodb.NewFromConfig(cfg), tableName)
	if err := run(ctx, repo, regulatorySources, *only, *apply, os.Stdout); err != nil {
		log.Fatal(err)
	}
}
