// backfill-account-people is a manually operated CLI (not a Lambda) that runs
// the deterministic people resolver over existing account meetings.
//
// For each selected account (-account ID, or -all through the operator
// account scan) it pages the account's meeting refs newest first and, for each
// meeting dated strictly before -before, runs AccountPeopleService.ResolveMeeting:
// auto edges and unresolved mention rows are reconciled under the meeting's run
// lease, and manual edges, deny rows and ignores always win. At most -limit
// meetings per account are resolved.
//
// -prune also removes the people edges and unresolved mention rows of meetings
// whose row no longer exists. Only then are manual edges removed; each removal
// re-checks in its transaction that the meeting row is absent, so an
// unpublished but existing meeting keeps all of its rows.
//
// -dry-run defaults to true and writes nothing, not even run leases. Apply
// mode refuses to run without -before. A failed meeting or row is counted in
// errors and the run continues; the exit status is then non-zero. Output is
// JSON counts only: no IDs, names, mention text or contact fields.
//
// Requires TABLE_NAME (no fallback), KMS_KEY_ID for the sealed contacts, and
// standard AWS credentials/region.
//
// Usage:
//
//	TABLE_NAME=ttobak-main KMS_KEY_ID=... go run ./cmd/backfill-account-people -account ID|-all -before 2026-11-10T00:00:00Z [-dry-run=false] [-limit N] [-prune]
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/sealing"
	"github.com/ttobak/backend/internal/service"
)

type peopleResolver interface {
	ResolveMeetingForAccount(ctx context.Context, accountID, ownerID, meetingID string, opt service.PeopleRunOptions) (service.PeopleRunResult, error)
}

type backfillRepository interface {
	ScanAccountHierarchyPage(ctx context.Context, cursor string) (*repository.AccountHierarchyPage, error)
	GetAccount(ctx context.Context, accountID string) (*model.Account, error)
	ListMeetingRefsForAccountPage(ctx context.Context, accountID, cursor string, limit int32) ([]model.MeetingRef, *string, error)
	GetMeeting(ctx context.Context, ownerID, meetingID string) (*model.Meeting, error)
	ListPeopleLinksByTarget(ctx context.Context, accountID, toKind, afterSK string, maxRows int) ([]model.PeopleLink, string, error)
	ListPeopleLinksForMeeting(ctx context.Context, accountID, meetingID string, maxRows int) ([]model.PeopleLink, bool, error)
	ListUnresolvedMentions(ctx context.Context, accountID, afterSK string, maxRows int) ([]model.UnresolvedMention, string, error)
	PrunePeopleLink(ctx context.Context, accountID string, k model.PeopleLinkKey) error
	PruneUnresolvedMention(ctx context.Context, accountID, hash, meetingID, role string) error
}

type backfillOptions struct {
	AccountID string
	All       bool
	Before    time.Time
	DryRun    bool
	Limit     int
	Prune     bool
}

type backfillResult struct {
	Accounts   int `json:"accounts"`
	Meetings   int `json:"meetings"`
	Written    int `json:"written"`
	Removed    int `json:"removed"`
	Unresolved int `json:"unresolved"`
	Skipped    int `json:"skipped"`
	Busy       int `json:"busy"`
	Pruned     int `json:"pruned"`
	// Errors counts meetings or rows whose write failed; the run continues.
	Errors int `json:"errors"`
}

var (
	errAccountXorAll  = errors.New("exactly one of -account or -all is required")
	errBeforeRequired = errors.New("-before is required unless -dry-run")
)

const (
	refPageSize      = 50
	pruneListRows    = 200
	maxPruneRowsRead = 20000
	maxScanPages     = 100000
)

func validateOptions(o backfillOptions) error {
	if (o.AccountID == "") == !o.All {
		return errAccountXorAll
	}
	if o.AccountID != "" && strings.ContainsAny(o.AccountID, "#/:") {
		return errors.New("invalid -account")
	}
	if o.Before.IsZero() && !o.DryRun {
		return errBeforeRequired
	}
	if o.Limit <= 0 {
		return errors.New("-limit must be > 0")
	}
	return nil
}

func backfill(ctx context.Context, repo backfillRepository, people peopleResolver, o backfillOptions) (backfillResult, error) {
	var result backfillResult
	if err := validateOptions(o); err != nil {
		return result, err
	}
	if !o.All {
		account, err := repo.GetAccount(ctx, o.AccountID)
		if err != nil {
			return result, err
		}
		if account == nil || account.AccountID != o.AccountID {
			return result, errors.New("account not found")
		}
		return result, backfillAccount(ctx, repo, people, o, o.AccountID, &result)
	}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages >= maxScanPages {
			return result, errors.New("account scan page limit exceeded")
		}
		page, err := repo.ScanAccountHierarchyPage(ctx, cursor)
		if err != nil {
			return result, err
		}
		for _, a := range page.Accounts {
			if err := backfillAccount(ctx, repo, people, o, a.AccountID, &result); err != nil {
				return result, err
			}
		}
		if page.NextCursor == "" {
			return result, nil
		}
		cursor = page.NextCursor
	}
}

func backfillAccount(ctx context.Context, repo backfillRepository, people peopleResolver, o backfillOptions, accountID string, result *backfillResult) error {
	result.Accounts++
	done := 0
	cursor := ""
	for done < o.Limit {
		refs, next, err := repo.ListMeetingRefsForAccountPage(ctx, accountID, cursor, refPageSize)
		if err != nil {
			return err
		}
		for _, ref := range refs {
			if done >= o.Limit {
				break
			}
			if ref.AccountID != accountID || (!o.Before.IsZero() && !ref.Date.Before(o.Before)) {
				continue
			}
			done++
			// The selected account binds the run: a meeting moved elsewhere
			// is skipped, never resolved for its new account.
			res, err := people.ResolveMeetingForAccount(ctx, accountID, ref.OwnerUserID, ref.MeetingID, service.PeopleRunOptions{DryRun: o.DryRun})
			if errors.Is(err, service.ErrInvalidInput) {
				result.Skipped++
				continue
			}
			if err != nil {
				result.Errors++
				continue
			}
			result.Meetings += res.Meetings
			result.Written += res.Written
			result.Removed += res.Removed
			result.Unresolved += res.Unresolved
			result.Skipped += res.Skipped
			if res.Busy {
				result.Busy++
			}
		}
		if next == nil || *next == "" {
			break
		}
		cursor = *next
	}
	if o.Prune {
		return prune(ctx, repo, o, accountID, result)
	}
	return nil
}

// prune finds meetings referenced by people or mention rows, and removes the
// rows of each meeting whose row no longer exists.
func prune(ctx context.Context, repo backfillRepository, o backfillOptions, accountID string, result *backfillResult) error {
	mentions := map[string][]model.UnresolvedMention{}
	meetings := map[string]bool{}
	read := 0
	for _, kind := range []string{model.LinkKindMember, model.LinkKindContact, model.LinkKindAccount, model.LinkKindProject} {
		after := ""
		for {
			links, last, err := repo.ListPeopleLinksByTarget(ctx, accountID, kind, after, pruneListRows)
			if err != nil {
				return err
			}
			for _, l := range links {
				meetings[l.MeetingID] = true
			}
			if read += pruneListRows; read > maxPruneRowsRead {
				return errors.New("prune row limit exceeded")
			}
			if last == "" {
				break
			}
			after = last
		}
	}
	after := ""
	for {
		rows, last, err := repo.ListUnresolvedMentions(ctx, accountID, after, pruneListRows)
		if err != nil {
			return err
		}
		for _, m := range rows {
			meetings[m.MeetingID] = true
			mentions[m.MeetingID] = append(mentions[m.MeetingID], m)
		}
		if read += pruneListRows; read > maxPruneRowsRead {
			return errors.New("prune row limit exceeded")
		}
		if last == "" {
			break
		}
		after = last
	}
	for linkID := range meetings {
		owner, meetingID, ok := strings.Cut(linkID, ":")
		if !ok {
			continue
		}
		meeting, err := repo.GetMeeting(ctx, owner, meetingID)
		if err != nil {
			result.Errors++
			continue
		}
		if meeting != nil {
			continue
		}
		links, _, err := repo.ListPeopleLinksForMeeting(ctx, accountID, linkID, 300)
		if err != nil {
			result.Errors++
			continue
		}
		// A dry run counts candidates; apply mode counts only removed rows.
		// A condition failure means the meeting row exists again: kept.
		count := func(err error) {
			switch {
			case err == nil:
				result.Pruned++
			case errors.Is(err, repository.ErrConditionFailed):
				result.Skipped++
			default:
				result.Errors++
			}
		}
		for _, l := range links {
			if o.DryRun {
				result.Pruned++
				continue
			}
			count(repo.PrunePeopleLink(ctx, accountID, l.Key()))
		}
		for _, m := range mentions[linkID] {
			if o.DryRun {
				result.Pruned++
				continue
			}
			count(repo.PruneUnresolvedMention(ctx, accountID, m.Hash, m.MeetingID, m.Role))
		}
	}
	return nil
}

func writeResult(w io.Writer, mode string, r backfillResult) {
	raw, _ := json.Marshal(struct {
		Mode string `json:"mode"`
		backfillResult
	}{mode, r})
	fmt.Fprintln(w, string(raw))
}

func main() {
	accountID := flag.String("account", "", "account ID to backfill")
	all := flag.Bool("all", false, "backfill every account")
	before := flag.String("before", "", "RFC 3339 cutoff; only meetings dated strictly before it (required unless -dry-run)")
	dryRun := flag.Bool("dry-run", true, "plan without writing (default true)")
	limit := flag.Int("limit", 200, "meetings per account")
	pruneMissing := flag.Bool("prune", false, "remove people rows of meetings whose row no longer exists")
	flag.Parse()
	o := backfillOptions{AccountID: *accountID, All: *all, DryRun: *dryRun, Limit: *limit, Prune: *pruneMissing}
	if *before != "" {
		parsed, err := time.Parse(time.RFC3339, *before)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ERROR: -before must be an RFC 3339 time")
			os.Exit(1)
		}
		o.Before = parsed
	}
	if err := validateOptions(o); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
	tableName, keyID := os.Getenv("TABLE_NAME"), os.Getenv("KMS_KEY_ID")
	if tableName == "" || keyID == "" {
		fmt.Fprintln(os.Stderr, "ERROR: TABLE_NAME and KMS_KEY_ID are required")
		os.Exit(1)
	}
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR: load AWS config")
		os.Exit(1)
	}
	repo := repository.NewDynamoDBRepository(dynamodb.NewFromConfig(cfg), tableName).MetadataView()
	sealer := sealing.NewEnvelope(kms.NewFromConfig(cfg), keyID)
	if sealer == nil {
		fmt.Fprintln(os.Stderr, "ERROR: contact sealer unavailable")
		os.Exit(1)
	}
	repo.SetRecordSealer(sealer)
	result, err := backfill(ctx, repo, service.NewAccountPeopleService(repo), o)
	mode := "apply"
	if o.DryRun {
		mode = "dry-run"
	}
	writeResult(os.Stdout, mode, result)
	if err == nil && result.Errors > 0 {
		err = errors.New("some writes failed")
	}
	if err != nil {
		// Error codes only: messages may carry IDs from wrapped SDK errors.
		fmt.Fprintln(os.Stderr, "ERROR: backfill failed")
		os.Exit(1)
	}
}
