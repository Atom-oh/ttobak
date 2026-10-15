package model

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

const (
	SummaryPrivacyVersion = 1
	SummaryPrivacySafe    = "safe"
	SummaryPrivacyPrivate = "private"
	SummaryPrivacyUnknown = "unknown"
)

// SummaryPrivacy records only identities/hashes, never predecessor text.
// "Safe" means no inherited private predecessor evidence, not an assurance
// that user-supplied transcript/notes are free of sensitive information.
type SummaryPrivacy struct {
	Version        int                        `dynamodbav:"version"`
	State          string                     `dynamodbav:"state"`
	GenerationID   string                     `dynamodbav:"generationId"`
	ContentSHA256  string                     `dynamodbav:"contentSha256"`
	Dependencies   []SummaryPrivacyDependency `dynamodbav:"dependencies,omitempty"`
	InheritedState string                     `dynamodbav:"inheritedState,omitempty"`
}

type SummaryPrivacyDependency struct {
	MeetingID         string `dynamodbav:"meetingId"`
	ContentSHA256     string `dynamodbav:"contentSha256"`
	ActionItemsSHA256 string `dynamodbav:"actionItemsSha256"`
	TitleSHA256       string `dynamodbav:"titleSha256"`
}

// LinkedSummarySource exists only in an authorized generation snapshot.
type LinkedSummarySource struct {
	MeetingID, Title, Content, ActionItems string
}

type ActionItemContinuity struct {
	ID        string `dynamodbav:"id"`
	Completed bool   `dynamodbav:"completed"`
}

func SummaryPrivacyHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func (meeting *Meeting) SummaryPrivacyState() string {
	if meeting == nil || meeting.SummaryPrivacy == nil || meeting.SummaryPrivacy.Version != SummaryPrivacyVersion ||
		meeting.SummaryPrivacy.GenerationID == "" {
		return SummaryPrivacyUnknown
	}
	privacy := meeting.SummaryPrivacy
	if privacy.State == SummaryPrivacyPrivate {
		return SummaryPrivacyPrivate
	}
	if privacy.State == SummaryPrivacySafe && privacy.ContentSHA256 == SummaryPrivacyHash(meeting.Content) {
		return SummaryPrivacySafe
	}
	return SummaryPrivacyUnknown
}

func (meeting *Meeting) SummaryGeneration() string {
	if meeting == nil || meeting.SummaryPrivacy == nil {
		return ""
	}
	return meeting.SummaryPrivacy.GenerationID
}

func (privacy *SummaryPrivacy) SafeMetadata() bool {
	if privacy == nil || privacy.Version != SummaryPrivacyVersion || privacy.State != SummaryPrivacySafe || privacy.GenerationID == "" {
		return false
	}
	hash, err := hex.DecodeString(privacy.ContentSHA256)
	return err == nil && len(hash) == sha256.Size && hex.EncodeToString(hash) == privacy.ContentSHA256
}

func (meeting *Meeting) HasSummaryDerivatives() bool {
	return strings.TrimSpace(meeting.Content) != "" || strings.TrimSpace(meeting.LiveSummary) != "" ||
		(strings.TrimSpace(meeting.ActionItems) != "" && strings.TrimSpace(meeting.ActionItems) != "[]") ||
		(strings.TrimSpace(meeting.Insights) != "" && strings.TrimSpace(meeting.Insights) != "[]") ||
		len(meeting.Tags) > 0 || strings.TrimSpace(meeting.Sentiment) != ""
}

func (meeting *Meeting) CanCreateSummaryGrant() bool {
	if meeting == nil || meeting.SummaryReadRestricted {
		return false
	}
	if meeting.SummaryPrivacyState() == SummaryPrivacySafe {
		return true
	}
	return meeting.SummaryPrivacy == nil && !meeting.HasSummaryDerivatives()
}

type SummarySharingStatus struct {
	CanCreateGrant        bool `json:"canCreateGrant"`
	DerivedReadRestricted bool `json:"derivedReadRestricted"`
	RequiresRegeneration  bool `json:"requiresRegeneration"`
	CanRegenerate         bool `json:"canRegenerate"`
}

// An absent marker does not prove that a membership predates activation.
// The cutoff is operator-fixed after reader rollout and old-writer drain.
func (member *AccountMember) NeedsSummaryPrivacyGuard(legacyBefore time.Time) bool {
	return member == nil || member.SummaryPrivacyVersion != 0 || legacyBefore.IsZero() ||
		member.AddedAt.IsZero() || !member.AddedAt.Before(legacyBefore)
}

func ActionContinuityHash(text, assignee, dueDate string) string {
	normalize := func(value string) string { return strings.Join(strings.Fields(value), " ") }
	return SummaryPrivacyHash(normalize(text) + "\x00" + normalize(assignee) + "\x00" + dueDate)
}
