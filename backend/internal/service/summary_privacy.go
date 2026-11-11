package service

import (
	"errors"
	"os"
	"strings"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

var ErrSummaryReadRestricted = errors.New("owner sharing-safe regeneration required for derived content")

const SummaryPrivacyCutoffEnv = "SUMMARY_PRIVACY_LEGACY_MEMBER_CUTOFF"
const SummaryPrivacyReaderEnv = "SUMMARY_PRIVACY_READER_ENABLED"

// Publishing a meeting to an account team shares its current summary and
// derived content with every member who can read it (ADR-029 source provenance, 2026-11-06
// amendment). Privacy provenance is still recorded for owner tooling, but it
// no longer hides published content from members.
//
// It is a variable so tests can exercise the restricted-read path, which is
// otherwise unreachable while the guard is disabled.
var accountMemberNeedsSummaryGuard = func(_ *model.AccountMember) bool {
	return false
}

func summaryPrivacyLegacyCutoff() time.Time {
	raw := os.Getenv(SummaryPrivacyCutoffEnv)
	if !strings.HasSuffix(raw, "Z") {
		return time.Time{}
	}
	cutoff, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return cutoff
}

// This is a view, never a stored mutation. Existing direct grants and owner
// reads do not enter this function. Unknown/new membership cannot read old
// generated context, but parent-owned raw transcript/notes remain usable.
func projectAccountMeeting(meeting *model.Meeting, member *model.AccountMember) *model.Meeting {
	if meeting == nil || !accountMemberNeedsSummaryGuard(member) ||
		meeting.AccountSummaryShareable() {
		return meeting
	}
	projected := *meeting
	projected.SummaryReadRestricted = true
	projected.Content, projected.ActionItems, projected.Insights, projected.Sentiment, projected.LiveSummary = "", "", "", "", ""
	projected.Tags, projected.LinkedMeetingIDs = nil, nil
	return &projected
}

// directTeamMember: the caller is the creator or a direct member of the
// meeting's account (inherited managers cannot republish).
func summarySharingStatus(meeting *model.Meeting, permission string, directTeamMember bool) *model.SummarySharingStatus {
	allowed := meeting.CanCreateSummaryGrant()
	// The approval path (OwnerAccountShareEnabled) is the creator's alone
	// (ADR-029, owner approval). A managing member only republishes the meeting to the account
	// it is already published to, which needs no approval.
	canShareAccount := permission == "owner" && (allowed || repository.OwnerAccountShareEnabled() &&
		(meeting.Status == model.StatusDone || meeting.Status == model.StatusError)) ||
		permission != "owner" && directTeamMember && meeting.SharedToAccount
	return &model.SummarySharingStatus{CanCreateGrant: allowed, CanShareToAccount: canShareAccount, DerivedReadRestricted: meeting.SummaryReadRestricted,
		RequiresRegeneration: !allowed, CanRegenerate: repository.SummaryPrivacyWritesEnabled() && permission == "owner" &&
			(meeting.Status == model.StatusDone || meeting.Status == model.StatusError)}
}
