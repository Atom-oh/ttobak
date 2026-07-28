// backfill-share-origin is a one-time, manually-operated CLI (not a Lambda)
// that tags pre-existing account-origin Share records with the Origin (and
// AccountID) fields (added by ADR's account-team-members fix round 2).
// Shares written by ShareMeetingToAccount before those fields existed have
// Origin=="" and are therefore treated as direct grants by RemoveMember's
// cleanup, permanently un-revocable -- this backfill closes that gap.
//
// Candidates are found by enumerating each account-linked meeting's Share
// rows directly (ListSharesForMeeting), NOT by joining through
// ListAccountMembers -- this deliberately covers shares belonging to users
// who have ALREADY been removed from the account by the time this CLI runs,
// not just current members. An earlier version of this tool only checked
// current members, which meant a member removed before backfill ran had no
// remediation path at all (see ADR-023's "Known Limitation" for why that gap
// existed and this fix).
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
// ORPHANED CANDIDATES (reported, never auto-tagged): a ref in this
// account's ListMeetingRefsForAccount whose meeting is no longer
// SharedToAccount, or whose AccountID has since changed to a different
// account, is printed as an ORPHANED line but is NEVER tagged, even under
// --apply. Tagging it would require deciding which account it belongs to
// now -- this account (its original grantor, per the stale ref) or the
// meeting's current AccountID (if any) -- and this tool cannot safely infer
// that. A share left in this state stays Origin=="" indefinitely and is
// honored by resolveSharedAccess as an unconditional direct grant (no
// membership re-verification applies to it at all, unlike a tagged
// account-origin share) -- see ADR-023 §5 for the manual remediation
// procedure (the owner must use RevokeShare after manually confirming the
// share is not a genuine direct grant).
//
// Requires the TABLE_NAME env var (and standard AWS credentials/region via
// the default credential chain) -- there is no hardcoded fallback, since a
// data-mutating tool guessing the wrong table under --apply is worse than
// failing outright.
//
// Also see ADR-023 for the full migration procedure, operational sequencing
// ("run this before removing a member from a legacy account"), and rollback.
//
// Usage:
//
//	TABLE_NAME=ttobak-main /usr/local/go/bin/go run ./cmd/backfill-share-origin --account-id <id> [--apply] [--verbose] [--exclude userId1:meetingId1,userId2:meetingId2,...]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/sharebackfill"
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

	excluded, err := sharebackfill.ParseExclude(*excludeFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
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

	refs, err := repo.ListMeetingRefsForAccount(ctx, *accountID)
	if err != nil {
		log.Fatalf("list meeting refs: %v", err)
	}

	mode := "DRY RUN"
	if *apply {
		mode = "APPLY"
	}
	fmt.Printf("[%s] account %s (%s): %d meeting ref(s)\n", mode, *accountID, account.Name, len(refs))

	candidates := 0
	tagged := 0
	failed := 0
	skipped := 0
	orphaned := 0
	for _, ref := range refs {
		meeting, err := repo.GetMeetingByID(ctx, ref.MeetingID)
		if err != nil {
			log.Printf("skip meeting %s: get meeting: %v", ref.MeetingID, err)
			failed++
			continue
		}
		if meeting == nil {
			continue // ref exists but the meeting was deleted
		}
		// Enumerate this meeting's Share rows directly instead of joining
		// through ListAccountMembers -- this is what covers a user who has
		// ALREADY been removed from the account by the time this CLI runs:
		// their legacy share row still exists (RemoveMember never touches an
		// ambiguous Origin=="" share) but they no longer appear in
		// ListAccountMembers, so a members-first join would silently skip
		// them forever.
		shares, err := repo.ListSharesForMeeting(ctx, ref.MeetingID)
		if err != nil {
			log.Printf("skip meeting %s: list shares: %v", ref.MeetingID, err)
			failed++
			continue
		}
		refState := sharebackfill.MeetingRefState{
			SharedToAccount: meeting.SharedToAccount,
			AccountID:       meeting.AccountID,
			UploaderUserID:  meeting.UserID,
		}
		for _, sh := range shares {
			verdict := sharebackfill.Classify(*accountID, refState, sharebackfill.ShareState{
				SharedToID: sh.SharedToID,
				Origin:     sh.Origin,
			})
			detail := ""
			if *verbose {
				detail = fmt.Sprintf(" (%s / %q)", sh.Email, meeting.Title)
			}
			switch verdict {
			case sharebackfill.VerdictSkip:
				continue
			case sharebackfill.VerdictOrphaned:
				// The meeting was un-shared from this account (or moved to a
				// different one) since this ref was written. This is
				// detect-only: never tagged, under --apply or otherwise. See
				// the package doc comment for why auto-tagging here is unsafe.
				orphaned++
				fmt.Printf("  ORPHANED: sharedTo=%s meeting=%s%s -- meeting is no longer SharedToAccount to %s"+
					" (current AccountID=%q). This share is un-taggable by this tool and is honored as an"+
					" unconditional direct grant by resolveSharedAccess. Manual remediation: confirm with the"+
					" meeting owner whether this is a genuine direct grant; if not, the owner must call"+
					" RevokeShare.\n", sh.SharedToID, ref.MeetingID, detail, *accountID, meeting.AccountID)
				continue
			}
			candidates++
			pairKey := sh.SharedToID + ":" + ref.MeetingID
			fmt.Printf("  CANDIDATE: sharedTo=%s meeting=%s%s -- untaggable ambiguity: this heuristic cannot tell a true legacy\n"+
				"    account-share apart from a direct share that happens to exist on a meeting also shared to this account.\n"+
				"    Review before trusting this tag. To skip it, pass --exclude %s\n", sh.SharedToID, ref.MeetingID, detail, pairKey)
			if !*apply {
				continue
			}
			if excluded[pairKey] {
				fmt.Printf("    skipped (--exclude %s)\n", pairKey)
				continue
			}
			if err := repo.BackfillShareOrigin(ctx, *accountID, meeting.UserID, sh.SharedToID, ref.MeetingID); err != nil {
				if errors.Is(err, repository.ErrConditionFailed) {
					// Benign: the row's origin changed (or the row itself
					// was deleted) between the ListSharesForMeeting read
					// above and BackfillShareOrigin's conditional write --
					// something else (a concurrent RemoveMember cleanup, or
					// a fresh direct share) already resolved this pair, so
					// tagging is correctly skipped rather than failed.
					fmt.Printf("    skipped (row changed concurrently -- no longer an untagged candidate)\n")
					skipped++
					continue
				}
				log.Printf("    FAILED to tag: %v", err)
				failed++
				continue
			}
			tagged++
			fmt.Printf("    tagged origin=account accountId=%s\n", *accountID)
		}
	}

	fmt.Printf("[%s] done: %d candidate(s) found, %d tagged, %d skipped (concurrent change), %d orphaned (un-taggable, see above), %d failed\n", mode, candidates, tagged, skipped, orphaned, failed)
	if !*apply && candidates > 0 {
		fmt.Println("Re-run with --apply after reviewing the candidates above.")
	}
	if orphaned > 0 {
		fmt.Println("ORPHANED shares require manual remediation -- see ADR-023 §5. They are never tagged by this tool.")
	}
	if failed > 0 {
		// A partial-failure run left some candidates untagged -- exit non-zero
		// so an operator running this from a script notices instead of
		// assuming the account is now fully backfilled.
		os.Exit(1)
	}
}
