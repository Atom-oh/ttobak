// backfill-share-origin is a one-time, manually-operated CLI (not a Lambda)
// that tags pre-existing account-origin Share records with the Origin field
// (added by ADR's account-team-members fix round 2). Shares written by
// ShareMeetingToAccount before that field existed have Origin=="" and are
// therefore treated as direct grants by RemoveMember's cleanup, permanently
// un-revocable -- this backfill closes that gap for shares belonging to
// members who are STILL in the account today.
//
// IMPORTANT — the discriminator this tool uses cannot distinguish a true
// legacy account-share from a direct share that happens to exist on a
// meeting that's ALSO shared to the account (both collapse into the same
// Share row, since CreateShare's clobber-guard always leaves such a
// collision at Origin==""). It CANNOT tell which candidates are ambiguous:
// every candidate goes through the identical heuristic, so --apply tags ALL
// of them with no exceptions -- there is no "skip the ambiguous ones
// automatically" behavior. Review the dry-run CANDIDATE list first; any
// candidate you don't trust must be named in --exclude before --apply, or it
// WILL be tagged (and later auto-revoked by RemoveMember if the member is
// ever removed, even if the share was actually a direct grant). Running one
// --account-id at a time is the recommended, safer rollout.
//
// Requires the TABLE_NAME env var (and standard AWS credentials/region via
// the default credential chain) -- there is no hardcoded fallback, since a
// data-mutating tool guessing the wrong table under --apply is worse than
// failing outright.
//
// Usage:
//
//	TABLE_NAME=ttobak-main go run ./cmd/backfill-share-origin --account-id <id> [--apply] [--verbose] [--exclude userId1:meetingId1,userId2:meetingId2,...]
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func main() {
	accountID := flag.String("account-id", "", "account to backfill (required)")
	apply := flag.Bool("apply", false, "actually write changes (default: dry-run, prints candidates only)")
	excludeFlag := flag.String("exclude", "", "comma-separated userId:meetingId pairs to skip (candidates reviewed as ambiguous / actually direct grants)")
	verbose := flag.Bool("verbose", false, "print member email and meeting title in CANDIDATE lines (PII -- omitted by default to avoid leaking into terminal history / CI logs)")
	flag.Parse()

	if *accountID == "" {
		fmt.Fprintln(os.Stderr, "ERROR: --account-id is required (run one account at a time)")
		os.Exit(1)
	}

	excluded := make(map[string]bool)
	for _, pair := range strings.Split(*excludeFlag, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		if !strings.Contains(pair, ":") {
			fmt.Fprintf(os.Stderr, "ERROR: --exclude entry %q must be userId:meetingId\n", pair)
			os.Exit(1)
		}
		excluded[pair] = true
	}

	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("failed to load AWS config: %v", err)
	}
	tableName := os.Getenv("TABLE_NAME")
	if tableName == "" {
		// Unlike a read-only tool, this one writes with --apply -- silently
		// defaulting to a guessed table name risks tagging the wrong table's
		// data. Fail explicitly instead.
		fmt.Fprintln(os.Stderr, "ERROR: TABLE_NAME environment variable is required")
		os.Exit(1)
	}
	repo := repository.NewDynamoDBRepository(dynamodb.NewFromConfig(cfg), tableName)

	account, err := repo.GetAccount(ctx, *accountID)
	if err != nil {
		log.Fatalf("get account: %v", err)
	}
	if account == nil {
		log.Fatalf("account %s not found", *accountID)
	}

	members, err := repo.ListAccountMembers(ctx, *accountID)
	if err != nil {
		log.Fatalf("list account members: %v", err)
	}
	refs, err := repo.ListMeetingRefsForAccount(ctx, *accountID)
	if err != nil {
		log.Fatalf("list meeting refs: %v", err)
	}

	mode := "DRY RUN"
	if *apply {
		mode = "APPLY"
	}
	fmt.Printf("[%s] account %s (%s): %d current member(s), %d meeting ref(s)\n", mode, *accountID, account.Name, len(members), len(refs))

	candidates := 0
	tagged := 0
	failed := 0
	for _, ref := range refs {
		meeting, err := repo.GetMeetingByID(ctx, ref.MeetingID)
		if err != nil {
			log.Printf("skip meeting %s: get meeting: %v", ref.MeetingID, err)
			failed++
			continue
		}
		if meeting == nil || !meeting.SharedToAccount || meeting.AccountID != *accountID {
			continue // ref exists but the meeting no longer matches ShareMeetingToAccount's invariants
		}
		for _, m := range members {
			if m.Role == model.RoleOwner {
				continue // ShareMeetingToAccount never shares to the owner
			}
			share, err := repo.GetShare(ctx, m.UserID, ref.MeetingID)
			if err != nil {
				log.Printf("skip %s / meeting %s: get share: %v", m.UserID, ref.MeetingID, err)
				failed++
				continue
			}
			if share == nil || share.Origin != "" {
				continue // no share, or already tagged (account or otherwise) -- nothing to do
			}
			candidates++
			pairKey := m.UserID + ":" + ref.MeetingID
			detail := ""
			if *verbose {
				detail = fmt.Sprintf(" (%s / %q)", m.Email, meeting.Title)
			}
			fmt.Printf("  CANDIDATE: member=%s meeting=%s%s -- untaggable ambiguity: this heuristic cannot tell a true legacy\n"+
				"    account-share apart from a direct share that happens to exist on a meeting also shared to this account.\n"+
				"    Review before trusting this tag. To skip it, pass --exclude %s\n", m.UserID, ref.MeetingID, detail, pairKey)
			if !*apply {
				continue
			}
			if excluded[pairKey] {
				fmt.Printf("    skipped (--exclude %s)\n", pairKey)
				continue
			}
			if err := repo.BackfillShareOrigin(ctx, m.UserID, ref.MeetingID); err != nil {
				log.Printf("    FAILED to tag: %v", err)
				failed++
				continue
			}
			tagged++
			fmt.Printf("    tagged origin=account\n")
		}
	}

	fmt.Printf("[%s] done: %d candidate(s) found, %d tagged, %d failed\n", mode, candidates, tagged, failed)
	if !*apply && candidates > 0 {
		fmt.Println("Re-run with --apply after reviewing the candidates above.")
	}
	if failed > 0 {
		// A partial-failure run left some candidates untagged -- exit non-zero
		// so an operator running this from a script notices instead of
		// assuming the account is now fully backfilled.
		os.Exit(1)
	}
}
