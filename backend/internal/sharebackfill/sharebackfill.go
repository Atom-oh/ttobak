// Package sharebackfill holds cmd/backfill-share-origin's pure classification
// logic, extracted so it's unit-testable under go test ./internal/... without
// pulling in that command's init() (AWS config load + TABLE_NAME fail-fast,
// unsuitable to run in a test binary). This CLI is not one of the Lambda
// binaries test-backend.yml builds, so this package is the only thing that
// gets exercised by CI for it -- keep any new decision logic here, not in
// cmd/backfill-share-origin/main.go, or CI will never catch a regression in it.
package sharebackfill

import (
	"fmt"
	"strings"
)

// Verdict is what Classify decided about one Share row on one meeting ref.
type Verdict int

const (
	// VerdictSkip means the pair needs no action: already tagged, or the
	// share belongs to the meeting's own uploader (never a
	// ShareMeetingToAccount recipient).
	VerdictSkip Verdict = iota
	// VerdictCandidate means the meeting is still genuinely SharedToAccount
	// to this exact account and the share is an untagged (Origin=="")
	// row -- eligible for --apply to tag, subject to --exclude.
	VerdictCandidate
	// VerdictOrphaned means the meeting has moved on (un-shared, or
	// re-shared to a different account) since this MeetingRef was written,
	// and the share is still untagged. Never eligible for tagging by this
	// tool -- see the package doc on cmd/backfill-share-origin/main.go for
	// why guessing an account here is unsafe.
	VerdictOrphaned
)

// MeetingRefState is the subset of a meeting's fields Classify needs to
// decide whether a MeetingRef still points at a genuine ShareMeetingToAccount
// grant for accountID.
type MeetingRefState struct {
	SharedToAccount bool
	AccountID       string
	UploaderUserID  string // Meeting.UserID -- ShareMeetingToAccount skips this user, not the account owner role
}

// ShareState is the subset of a Share row's fields Classify needs.
type ShareState struct {
	SharedToID string
	Origin     string
}

// Classify decides what to do with one (meeting, share) pair, mirroring
// exactly the predicate cmd/backfill-share-origin/main.go's loop applies
// inline. Kept here as a pure function (no AWS calls, no I/O) so this
// decision logic -- the actual "is this safe to tag" judgment call -- has
// unit test coverage independent of the CLI's DynamoDB plumbing.
func Classify(accountID string, meeting MeetingRefState, share ShareState) Verdict {
	if !meeting.SharedToAccount || meeting.AccountID != accountID {
		if share.Origin != "" {
			return VerdictSkip // already tagged -- not an orphan, nothing to report
		}
		return VerdictOrphaned
	}
	if share.SharedToID == meeting.UploaderUserID {
		return VerdictSkip
	}
	if share.Origin != "" {
		return VerdictSkip
	}
	return VerdictCandidate
}

// ExcludeFormatError reports a malformed --exclude entry (missing the
// required "userId:meetingId" colon separator).
type ExcludeFormatError struct{ Entry string }

func (e *ExcludeFormatError) Error() string {
	return fmt.Sprintf("exclude entry %q must be userId:meetingId", e.Entry)
}

// ParseExclude parses the --exclude flag's "userId1:meetingId1,userId2:meetingId2"
// syntax into a set of "userId:meetingId" pair keys. Returns an error
// (rather than the CLI's os.Exit) so this parsing is independently testable;
// the caller decides how to report a malformed entry.
func ParseExclude(raw string) (map[string]bool, error) {
	excluded := make(map[string]bool)
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		if !strings.Contains(pair, ":") {
			return nil, &ExcludeFormatError{Entry: pair}
		}
		excluded[pair] = true
	}
	return excluded, nil
}
