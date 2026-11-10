// backfill-terms-sources is a manually operated CLI (not a Lambda) that
// requests dictionary term extraction for KB documents and research reports
// uploaded before the source flags were turned on.
//
// Term sources are triggered by the KB bucket's Object Created events, so
// objects that predate activation are never processed. This CLI lists the
// kb/ and shared/research/ prefixes and passes every key to the production
// mapper (TermsAnalysisService.RequestSourceFromObject), which applies the
// same key ownership, file-type, account-metadata and research-item rules as
// the event path and publishes ttobak.analysis/TermsSourceRequested. The
// summarize worker then re-checks the deployed flags, re-reads the source and
// its Accounts, and skips content it already processed (TERMS_SOURCE run row),
// so a rerun is safe and a deployment with the flags off logs DISABLED.
//
// The mapper runs with both source modes on in this process; the deployed
// AUTO_TERMS_USERS canary list is honoured when set in the environment.
//
// -dry-run defaults to true: keys are mapped but nothing is published. In
// apply mode -interval spaces published requests to bound concurrent model
// calls. Output is counts only; the mapper's own log lines carry digests, not
// file names.
//
// Requires TABLE_NAME and KB_BUCKET_NAME (no fallbacks) and standard AWS
// credentials/region.
//
// Usage:
//
//	TABLE_NAME=ttobak-main KB_BUCKET_NAME=... go run ./cmd/backfill-terms-sources [-dry-run=false] [-limit N] [-interval 5s]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

// backfillPrefixes are the source prefixes the KB bucket rule matches.
var backfillPrefixes = []string{"kb/", "shared/research/"}

const maxListPages = 100000

// keyLister lists one page of keys under prefix after cursor.
type keyLister func(ctx context.Context, prefix, cursor string) (keys []string, next string, err error)

// sourceMapper maps one object key to a source request (RequestSourceFromObject).
type sourceMapper func(ctx context.Context, bucket, key string) error

type backfillResult struct {
	Scanned int
	Queued  int
	Failed  int
}

type backfillOptions struct {
	bucket   string
	limit    int
	interval time.Duration
	sleep    func(context.Context, time.Duration) error
	// queued reports how many requests the mapper has published so far.
	queued func() int
}

// backfill passes every listed key to mapper. A mapper error is counted and
// the run continues; listing errors stop it. After each newly queued request
// it waits interval (apply mode spacing).
func backfill(ctx context.Context, list keyLister, mapper sourceMapper, opts backfillOptions) (result backfillResult, err error) {
	// Every exit reports the requests already published.
	defer func() { result.Queued = opts.queued() }()
	for _, prefix := range backfillPrefixes {
		cursor := ""
		for pages := 0; ; pages++ {
			if pages >= maxListPages {
				return result, errors.New("object listing page limit exceeded")
			}
			keys, next, err := list(ctx, prefix, cursor)
			if err != nil {
				return result, err
			}
			for _, key := range keys {
				if opts.limit > 0 && result.Scanned >= opts.limit {
					return result, nil
				}
				result.Scanned++
				before := opts.queued()
				if err := mapper(ctx, opts.bucket, key); err != nil {
					result.Failed++
					continue
				}
				if opts.queued() > before && opts.interval > 0 {
					if err := opts.sleep(ctx, opts.interval); err != nil {
						return result, err
					}
				}
			}
			if next == "" {
				break
			}
			cursor = next
		}
	}
	return result, nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func s3Lister(client *s3.Client, bucket string) keyLister {
	return func(ctx context.Context, prefix, cursor string) ([]string, string, error) {
		in := &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String(prefix)}
		if cursor != "" {
			in.ContinuationToken = aws.String(cursor)
		}
		out, err := client.ListObjectsV2(ctx, in)
		if err != nil {
			return nil, "", fmt.Errorf("list %s: %w", prefix, err)
		}
		keys := make([]string, 0, len(out.Contents))
		for _, o := range out.Contents {
			keys = append(keys, aws.ToString(o.Key))
		}
		next := ""
		if aws.ToBool(out.IsTruncated) {
			next = aws.ToString(out.NextContinuationToken)
		}
		return keys, next, nil
	}
}

// countingPublisher wraps publish (nil in a dry run) and counts accepted
// requests.
func countingPublisher(publish func(context.Context, model.TermsSourceRequested) error) (func(context.Context, model.TermsSourceRequested) error, func() int) {
	n := 0
	return func(ctx context.Context, ev model.TermsSourceRequested) error {
			if publish != nil {
				if err := publish(ctx, ev); err != nil {
					return err
				}
			}
			n++
			return nil
		}, func() int {
			return n
		}
}

func main() {
	dryRun := flag.Bool("dry-run", true, "map keys without publishing requests (default true)")
	limit := flag.Int("limit", 0, "maximum objects to scan (0 = all)")
	interval := flag.Duration("interval", 5*time.Second, "wait after each published request (apply mode)")
	flag.Parse()
	if *limit < 0 || *interval < 0 {
		fmt.Fprintln(os.Stderr, "ERROR: -limit and -interval must be >= 0")
		os.Exit(1)
	}
	tableName, bucket := os.Getenv("TABLE_NAME"), os.Getenv("KB_BUCKET_NAME")
	if tableName == "" || bucket == "" {
		fmt.Fprintln(os.Stderr, "ERROR: TABLE_NAME and KB_BUCKET_NAME are required")
		os.Exit(1)
	}
	mode := "APPLY"
	if *dryRun {
		mode = "DRY RUN"
		log.SetPrefix("[DRY RUN] ")
	}
	ctx := context.Background()
	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR: load AWS config")
		os.Exit(1)
	}
	s3Client := s3.NewFromConfig(awsCfg)
	repo := repository.NewDynamoDBRepository(dynamodb.NewFromConfig(awsCfg), tableName)

	var publish func(context.Context, model.TermsSourceRequested) error
	if !*dryRun {
		publish = service.TermsSourcePublisher(eventbridge.NewFromConfig(awsCfg))
	}
	counted, queued := countingPublisher(publish)

	termsCfg := service.TermsConfigFromEnv()
	termsCfg.KBMode, termsCfg.ResearchMode = service.SourceTermsModeOn, service.SourceTermsModeOn
	terms := service.NewTermsAnalysisService(nil, nil, nil, nil, nil, nil, termsCfg)
	terms.SetSourceReader(service.TermsSourceDeps{Store: repo, Objects: s3Client, Bucket: bucket, Publish: counted})

	opts := backfillOptions{bucket: bucket, limit: *limit, sleep: sleepContext, queued: queued}
	if !*dryRun {
		opts.interval = *interval
	}
	result, err := backfill(ctx, s3Lister(s3Client, bucket), terms.RequestSourceFromObject, opts)
	fmt.Printf("[%s] scanned=%d queued=%d failed=%d\n", mode, result.Scanned, result.Queued, result.Failed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	if result.Failed > 0 {
		os.Exit(1)
	}
}
