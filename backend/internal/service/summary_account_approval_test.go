package service

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/ttobak/backend/internal/model"
)

type ownerPublicationRepo struct {
	*mockMeetingRepo
	published int
}

func (r *ownerPublicationRepo) PublishMeetingToAccount(ctx context.Context, owner, meeting, account string) error {
	r.published++
	return r.UpdateMeetingFields(ctx, owner, meeting, map[string]interface{}{"accountId": account, "sharedToAccount": true})
}

func TestTeamShareUsesOwnerPublicationAndRejectsNonOwners(t *testing.T) {
	repo := &ownerPublicationRepo{mockMeetingRepo: newMockMeetingRepo()}
	repo.addMeeting(&model.Meeting{UserID: "owner", MeetingID: "meeting", Status: model.StatusDone, Content: "current"})
	repo.addMember("account", "owner", model.RoleSA)
	svc := newMeetingServiceWithRepo(repo)
	if _, err := svc.ShareMeetingToAccount(context.Background(), "owner", "owner@example.invalid", "meeting", "account"); err != nil {
		t.Fatal(err)
	}
	if repo.published != 1 {
		t.Fatal("team share did not invoke the owner disclosure writer")
	}
	if _, err := svc.ShareMeetingToAccount(context.Background(), "intruder", "", "meeting", "account"); !errors.Is(err, ErrNotFound) ||
		repo.published != 1 {
		t.Fatal("nonowner reached disclosure approval")
	}
}

func approvedAccountMeeting(t *testing.T) *model.Meeting {
	t.Helper()
	row := map[string]interface{}{
		"userId": "owner", "meetingId": "meeting", "accountId": "account", "sharedToAccount": true,
		"content": "현재 완성된 회의록", "actionItems": `[{"id":"task","completed":true}]`,
		"insights": `[{"text":"현재 Insight"}]`, "liveSummary": "existing draft",
		"summaryPrivacy": map[string]interface{}{"version": 1, "state": "private",
			"generationId": "generation", "contentSha256": model.SummaryPrivacyHash("현재 완성된 회의록")},
		"summaryAccountApproval": map[string]interface{}{"version": 1, "ownerUserId": "owner", "accountId": "account",
			"summaryGeneration": "generation", "contentSha256": model.SummaryPrivacyHash("현재 완성된 회의록"),
			"approvedAt": "2026-11-04T06:00:00Z"},
	}
	item, err := attributevalue.MarshalMap(row)
	if err != nil {
		t.Fatal(err)
	}
	var meeting model.Meeting
	if err := attributevalue.UnmarshalMap(item, &meeting); err != nil {
		t.Fatal(err)
	}
	return &meeting
}

func TestOwnerAccountApprovalPreservesCurrentPrivateOutputForProtectedReader(t *testing.T) {
	meeting := approvedAccountMeeting(t)
	projected := projectAccountMeeting(meeting, &model.AccountMember{SummaryPrivacyVersion: 1})
	if projected.SummaryReadRestricted || projected.Content != meeting.Content ||
		projected.Insights != meeting.Insights || projected.ActionItems != meeting.ActionItems || projected.LiveSummary != meeting.LiveSummary {
		t.Fatal("owner-approved current output was withheld or replaced")
	}
	if meeting.SummaryPrivacyState() != model.SummaryPrivacyPrivate || meeting.CanCreateSummaryGrant() {
		t.Fatal("Account disclosure was incorrectly promoted to safe-source or direct-grant clearance")
	}
}

func TestAccountProjectionNoLongerDependsOnApproval(t *testing.T) {
	for _, change := range []struct {
		name string
		edit func(*model.Meeting)
	}{
		{"content", func(m *model.Meeting) { m.Content += " changed" }},
		{"generation", func(m *model.Meeting) { m.SummaryPrivacy.GenerationID = "replaced" }},
		{"account", func(m *model.Meeting) { m.AccountID = "other" }},
		{"owner", func(m *model.Meeting) { m.UserID = "other" }},
		{"unpublished", func(m *model.Meeting) { m.SharedToAccount = false }},
	} {
		t.Run(change.name, func(t *testing.T) {
			meeting := approvedAccountMeeting(t)
			change.edit(meeting)
			// Account publication shares current content; approval staleness
			// no longer withholds it (ADR-049 2026-11-06 amendment).
			projected := projectAccountMeeting(meeting, &model.AccountMember{SummaryPrivacyVersion: 1})
			if projected.SummaryReadRestricted || projected.Content != meeting.Content ||
				projected.ActionItems != meeting.ActionItems || projected.Insights != meeting.Insights {
				t.Fatal("account projection withheld current derived content")
			}
		})
	}
}

func TestOwnerAccountApprovalPermitsCurrentInsightMetadataOnly(t *testing.T) {
	meeting := approvedAccountMeeting(t)
	meeting.PK, meeting.SK = "USER#owner", "MEETING#meeting"
	meeting.EntityType = "MEETING"
	meeting.InsightsRevision = "insights"
	meeting.Content = "" // Publication reads deliberately do not hydrate content.
	insight := model.AccountInsight{AccountID: "account", SourceID: "meeting", SourceUserID: "owner",
		SummaryGeneration: "generation", InsightsRevision: "insights"}
	member := &model.AccountMember{SummaryPrivacyVersion: 1}
	if !accountMeetingInsightAllowed(meeting, member, insight, "reader") {
		t.Fatal("current owner-approved Insight metadata was withheld")
	}
	insight.SummaryGeneration = "old"
	if accountMeetingInsightAllowed(meeting, member, insight, "reader") {
		t.Fatal("owner approval allowed a stale Insight generation")
	}
}

func TestOwnerAccountSharingStatusDoesNotRequireRegeneration(t *testing.T) {
	t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", "1")
	t.Setenv("SUMMARY_OWNER_ACCOUNT_SHARE_ENABLED", "1")
	status := summarySharingStatus(&model.Meeting{Status: model.StatusDone, Content: "current legacy notes"}, "owner", true)
	if !status.CanShareToAccount {
		t.Fatal("completed owner output still requires separate regeneration for team sharing")
	}
	if summarySharingStatus(&model.Meeting{Status: model.StatusDone, Content: "current"}, "edit", false).CanShareToAccount {
		t.Fatal("editor status advertised owner disclosure")
	}
}

func TestOwnerApprovedLegacyAccountInsightsRejectOldProjection(t *testing.T) {
	meeting := approvedAccountMeeting(t)
	meeting.PK, meeting.SK, meeting.EntityType = "USER#owner", "MEETING#meeting", "MEETING"
	meeting.SummaryPrivacy = nil
	meeting.SummaryAccountApproval.SummaryGeneration = ""
	meeting.InsightsRevision = "approval-bound-current-insights"
	member := &model.AccountMember{SummaryPrivacyVersion: 1}
	insight := model.AccountInsight{AccountID: "account", SourceUserID: "owner", SourceID: "meeting", Text: "old unapproved Insight"}
	if accountMeetingInsightAllowed(meeting, member, insight, "reader") {
		t.Fatal("approval of current legacy notes revived a stale unapproved Insight row")
	}
	insight.InsightsRevision = meeting.InsightsRevision
	if !accountMeetingInsightAllowed(meeting, member, insight, "reader") {
		t.Fatal("fresh current legacy Insight identity was rejected")
	}
}
