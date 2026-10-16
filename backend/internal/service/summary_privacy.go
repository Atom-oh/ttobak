package service

import (
	"context"
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

func linkedSummaryContext(sources []model.LinkedSummarySource) string {
	if len(sources) == 0 {
		return ""
	}
	var text strings.Builder
	text.WriteString("이전 연관 회의의 승인된 요약입니다. 현재 회의의 발언과 별도 참고 자료로 취급하세요:\n\n")
	for _, source := range sources {
		title, content, items := []rune(source.Title), []rune(source.Content), []rune(source.ActionItems)
		text.WriteString("## " + string(title[:min(len(title), 256)]) + "\n")
		text.WriteString(string(content[:min(len(content), 2000)]) + "\n")
		text.WriteString(string(items[:min(len(items), 1000)]) + "\n\n")
	}
	return text.String()
}

func bindLinkedSummarySources(ctx context.Context, repo interface {
	BindLinkedSummarySources(context.Context, *model.SummarySnapshot) error
}, snapshot *model.SummarySnapshot) error {
	if repository.SummaryPrivacyWritesEnabled() {
		return repo.BindLinkedSummarySources(ctx, snapshot)
	}
	return nil
}

func accountMemberNeedsSummaryGuard(member *model.AccountMember) bool {
	if member == nil || member.SummaryPrivacyVersion != 0 {
		return true
	}
	switch os.Getenv(SummaryPrivacyReaderEnv) {
	case "", "0":
		// Reader installation only. This is not proof of legacy eligibility;
		// epoch enforcement is enabled only after the rollout fence is ready.
		return false
	case "1":
		return member.NeedsSummaryPrivacyGuard(summaryPrivacyLegacyCutoff())
	default:
		return true
	}
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
		meeting.SummaryPrivacyState() == model.SummaryPrivacySafe {
		return meeting
	}
	projected := *meeting
	projected.SummaryReadRestricted = true
	projected.Content, projected.ActionItems, projected.Insights, projected.Sentiment, projected.LiveSummary = "", "", "", "", ""
	projected.Tags, projected.LinkedMeetingIDs = nil, nil
	return &projected
}

func summarySharingStatus(meeting *model.Meeting, permission string) *model.SummarySharingStatus {
	allowed := meeting.CanCreateSummaryGrant()
	return &model.SummarySharingStatus{CanCreateGrant: allowed, DerivedReadRestricted: meeting.SummaryReadRestricted,
		RequiresRegeneration: !allowed, CanRegenerate: repository.SummaryPrivacyWritesEnabled() && permission == "owner" &&
			(meeting.Status == model.StatusDone || meeting.Status == model.StatusError)}
}
