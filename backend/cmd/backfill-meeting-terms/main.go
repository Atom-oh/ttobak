// backfill-meeting-terms is a manually operated CLI (not a Lambda) that
// requests dictionary term extraction for completed meetings, so meetings
// analysed under an older extractor (termsExtractorVersion) or never analysed
// feed the current prompt, filters and caps.
//
// It scans ttobak-main for MEETING items with status done and calls the
// production TermsAnalysisService.Enqueue (reason "backfill") for each owner
// and meeting. Enqueue applies the deployed rules itself: AUTO_TERMS_MODE and
// AUTO_TERMS_USERS gate it, an unchanged source reports UNCHANGED, a live run
// reports PENDING, and only a fresh claim publishes ttobak.analysis/
// TermsRequested for the summarize worker. A rerun is therefore safe.
//
// -dry-run defaults to true: meetings are counted and nothing is claimed or
// published. In apply mode -interval spaces queued requests to bound
// concurrent model calls. Output is outcome counts only, never titles or
// content.
//
// Requires TABLE_NAME and, in apply mode, BUCKET_NAME (the assets bucket, so
// spilled transcripts hydrate exactly as in the summarize worker and the
// source identity matches), AUTO_TERMS_MODE=personal and AUTO_TERMS_USERS
// copied from the deployed summarize function configuration, plus standard
// AWS credentials/region with events:PutEvents on the default bus.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

const maxScanPages = 100000

// meetingRef names one completed meeting.
type meetingRef struct {
	UserID    string `dynamodbav:"userId"`
	MeetingID string `dynamodbav:"meetingId"`
}

// meetingLister returns one page of done meetings after cursor.
type meetingLister func(ctx context.Context, cursor map[string]any) (refs []meetingRef, next map[string]any, err error)

// enqueuer claims and publishes one meeting's run; it returns an outcome code.
type enqueuer func(ctx context.Context, ownerID, meetingID string) string

type backfillOptions struct {
	limit    int
	interval time.Duration
	sleep    func(context.Context, time.Duration) error
}

type backfillResult struct {
	Scanned  int
	Outcomes map[string]int
}

// backfill enqueues every listed meeting. After each QUEUED outcome it waits
// interval; a FAILED outcome is counted and the run continues; a listing error
// stops it.
func backfill(ctx context.Context, list meetingLister, enqueue enqueuer, opts backfillOptions) (backfillResult, error) {
	result := backfillResult{Outcomes: map[string]int{}}
	var cursor map[string]any
	for pages := 0; ; pages++ {
		if pages >= maxScanPages {
			return result, errors.New("meeting scan page limit exceeded")
		}
		refs, next, err := list(ctx, cursor)
		if err != nil {
			return result, err
		}
		for _, ref := range refs {
			if opts.limit > 0 && result.Scanned >= opts.limit {
				return result, nil
			}
			if ref.UserID == "" || ref.MeetingID == "" {
				continue
			}
			result.Scanned++
			outcome := enqueue(ctx, ref.UserID, ref.MeetingID)
			result.Outcomes[outcome]++
			if outcome == service.TermsEnqueueQueued && opts.interval > 0 {
				if err := opts.sleep(ctx, opts.interval); err != nil {
					return result, err
				}
			}
		}
		if len(next) == 0 {
			return result, nil
		}
		cursor = next
	}
}

// failedRun reports outcomes an operator must act on: a failed claim, or a
// claim whose TermsRequested publish failed (the row is then failed and no
// worker receives it, e.g. a shell without events:PutEvents).
func failedRun(outcomes map[string]int) bool {
	return outcomes[service.TermsEnqueueFailed] > 0 || outcomes[model.AnalysisPublishFailed] > 0
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

// dynamoLister scans done MEETING items, projecting only their keys.
func dynamoLister(client *dynamodb.Client, table string) (meetingLister, error) {
	filter := expression.Name("entityType").Equal(expression.Value("MEETING")).
		And(expression.Name("status").Equal(expression.Value(model.StatusDone)))
	expr, err := expression.NewBuilder().WithFilter(filter).
		WithProjection(expression.NamesList(expression.Name("userId"), expression.Name("meetingId"))).Build()
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, cursor map[string]any) ([]meetingRef, map[string]any, error) {
		in := &dynamodb.ScanInput{
			TableName: aws.String(table), FilterExpression: expr.Filter(), ProjectionExpression: expr.Projection(),
			ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
		}
		if len(cursor) > 0 {
			start, err := attributevalue.MarshalMap(cursor)
			if err != nil {
				return nil, nil, err
			}
			in.ExclusiveStartKey = start
		}
		out, err := client.Scan(ctx, in)
		if err != nil {
			return nil, nil, fmt.Errorf("scan meetings: %w", err)
		}
		var refs []meetingRef
		if err := attributevalue.UnmarshalListOfMaps(out.Items, &refs); err != nil {
			return nil, nil, fmt.Errorf("decode meetings: %w", err)
		}
		var next map[string]any
		if len(out.LastEvaluatedKey) > 0 {
			if err := attributevalue.UnmarshalMap(out.LastEvaluatedKey, &next); err != nil {
				return nil, nil, err
			}
		}
		return refs, next, nil
	}, nil
}

func main() {
	dryRun := flag.Bool("dry-run", true, "count done meetings without claiming or publishing (default true)")
	limit := flag.Int("limit", 0, "maximum meetings to process (0 = all)")
	interval := flag.Duration("interval", 15*time.Second, "wait after each queued request (apply mode)")
	flag.Parse()
	if *limit < 0 || *interval < 0 {
		fmt.Fprintln(os.Stderr, "ERROR: -limit and -interval must be >= 0")
		os.Exit(1)
	}
	tableName := os.Getenv("TABLE_NAME")
	if tableName == "" {
		fmt.Fprintln(os.Stderr, "ERROR: TABLE_NAME is required")
		os.Exit(1)
	}
	ctx := context.Background()
	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR: load AWS config")
		os.Exit(1)
	}
	client := dynamodb.NewFromConfig(awsCfg)
	list, err := dynamoLister(client, tableName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	mode := "APPLY"
	var enqueue enqueuer
	opts := backfillOptions{limit: *limit, sleep: sleepContext}
	if *dryRun {
		mode = "DRY RUN"
		enqueue = func(context.Context, string, string) string { return "DRY_RUN" }
	} else {
		cfg := service.TermsConfigFromEnv()
		if cfg.Mode != service.AutoTermsModePersonal {
			fmt.Fprintln(os.Stderr, "ERROR: AUTO_TERMS_MODE=personal is required in apply mode")
			os.Exit(1)
		}
		bucket := os.Getenv("BUCKET_NAME")
		if bucket == "" {
			fmt.Fprintln(os.Stderr, "ERROR: BUCKET_NAME is required in apply mode")
			os.Exit(1)
		}
		// Same hydration as the summarize worker: a spilled transcript must hash
		// as its text, not its s3:// reference, or every run ends SOURCE_CHANGED.
		repo := repository.NewDynamoDBRepositoryWithS3(client, tableName, s3.NewFromConfig(awsCfg), bucket)
		terms := service.NewTermsAnalysisService(repo, nil, nil, nil, nil, service.TermsPublisher(eventbridge.NewFromConfig(awsCfg)), cfg)
		enqueue = func(ctx context.Context, ownerID, meetingID string) string {
			return terms.Enqueue(ctx, ownerID, meetingID, "backfill")
		}
		opts.interval = *interval
	}
	result, err := backfill(ctx, list, enqueue, opts)
	codes := make([]string, 0, len(result.Outcomes))
	for code := range result.Outcomes {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	fmt.Printf("[%s] scanned=%d", mode, result.Scanned)
	for _, code := range codes {
		fmt.Printf(" %s=%d", code, result.Outcomes[code])
	}
	fmt.Println()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	if failedRun(result.Outcomes) {
		os.Exit(1)
	}
}
