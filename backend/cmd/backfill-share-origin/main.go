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
// collision at Origin==""). Every candidate is printed for manual review;
// --apply must be passed explicitly, and running one --account-id at a time
// is the recommended, safer rollout.
//
// Usage:
//
//	go run ./cmd/backfill-share-origin --account-id <id> [--apply]
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func main() {
	accountID := flag.String("account-id", "", "account to backfill (required)")
	apply := flag.Bool("apply", false, "actually write changes (default: dry-run, prints candidates only)")
	flag.Parse()

	if *accountID == "" {
		fmt.Fprintln(os.Stderr, "ERROR: --account-id is required (run one account at a time)")
		os.Exit(1)
	}

	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("failed to load AWS config: %v", err)
	}
	tableName := os.Getenv("TABLE_NAME")
	if tableName == "" {
		tableName = "ttobak-main"
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
	for _, ref := range refs {
		meeting, err := repo.GetMeetingByID(ctx, ref.MeetingID)
		if err != nil {
			log.Printf("skip meeting %s: get meeting: %v", ref.MeetingID, err)
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
				continue
			}
			if share == nil || share.Origin != "" {
				continue // no share, or already tagged (account or otherwise) -- nothing to do
			}
			candidates++
			fmt.Printf("  CANDIDATE: member=%s (%s) meeting=%s (%q) -- untaggable ambiguity: this heuristic cannot tell a true legacy\n"+
				"    account-share apart from a direct share that happens to exist on a meeting also shared to this account.\n"+
				"    Review before trusting this tag.\n", m.UserID, m.Email, ref.MeetingID, meeting.Title)
			if !*apply {
				continue
			}
			if err := repo.BackfillShareOrigin(ctx, m.UserID, ref.MeetingID); err != nil {
				log.Printf("    FAILED to tag: %v", err)
				continue
			}
			tagged++
			fmt.Printf("    tagged origin=account\n")
		}
	}

	fmt.Printf("[%s] done: %d candidate(s) found, %d tagged\n", mode, candidates, tagged)
	if !*apply && candidates > 0 {
		fmt.Println("Re-run with --apply after reviewing the candidates above.")
	}
}
