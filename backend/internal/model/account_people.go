package model

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// People edges share the account partition with the project 1 link rows but
// use a 5-segment key, so the 4-segment parsers reject them:
//
//	LINK#meeting#{meetingLinkId}#{toKind}#{toId}#{role}      forward row
//	LINKTO#{toKind}#{toId}#meeting#{meetingLinkId}#{role}    reverse row
//	LINKDENY#meeting#{meetingLinkId}#{toKind}#{toId}#{role}  a removed auto edge
//	LINKDENY#mention#{hash}                                  an ignored mention
//	MENTION#{hash}#{meetingLinkId}#{role}                    an unresolved mention
//	PEOPLERUN#{meetingLinkId} | PEOPLERUN#account            resolver run state
//
// meetingLinkId is {ownerUserId}:{meetingId}. Rows hold IDs and the mention
// text only; contact fields are read from the contact row at read time.
const (
	LinkKindMember  = "member"
	LinkKindContact = "contact"
	LinkKindAccount = "account"
	LinkKindMention = "mention" // deny-only: LINKDENY#mention#{hash}

	PeopleRoleParticipant = "participant"
	PeopleRoleSpeaker     = "speaker"
	PeopleRoleAssignee    = "assignee"
	PeopleRoleMentions    = "mentions"

	PeopleRuleEmail   = "email"
	PeopleRuleName    = "name"
	PeopleRuleAlias   = "alias"
	PeopleRuleProject = "project"

	PeopleSourceParticipants = "participants"
	PeopleSourceSpeakerMap   = "speakerMap"
	PeopleSourceActionItems  = "actionItems"
	PeopleSourceInsights     = "insights"

	PrefixAccountMention   = "MENTION#"
	PrefixAccountPeopleRun = "PEOPLERUN#"
	PeopleRunScopeAccount  = "account"

	MaxPeopleMentionRunes       = 120
	MaxPeopleEvidenceRunes      = 200
	MaxPeopleSourceRefBytes     = 200
	MaxPeopleMentionsPerMeeting = 50
	MaxPeopleEdgesPerMeeting    = 100

	peopleLinkPrefix = PrefixAccountLink + LinkKindMeeting + "#"
)

// allowedPeopleRoleKinds lists the target kinds each role may point to.
var allowedPeopleRoleKinds = map[string]map[string]bool{
	PeopleRoleParticipant: {LinkKindMember: true, LinkKindContact: true},
	PeopleRoleSpeaker:     {LinkKindMember: true, LinkKindContact: true},
	PeopleRoleAssignee:    {LinkKindMember: true, LinkKindContact: true},
	PeopleRoleMentions:    {LinkKindMember: true, LinkKindContact: true, LinkKindAccount: true, LinkKindProject: true},
}

// PeopleLinkKey identifies one people edge. MeetingID is {ownerUserId}:{meetingId}.
type PeopleLinkKey struct {
	MeetingID string
	ToKind    string
	ToID      string
	Role      string
}

// PeopleLink is a typed, provenance-bearing edge from an account meeting to a
// member, contact, account or project.
type PeopleLink struct {
	AccountID   string `dynamodbav:"accountId" json:"-"`
	MeetingID   string `dynamodbav:"meetingId" json:"meetingId"`
	ToKind      string `dynamodbav:"toKind" json:"toKind"`
	ToID        string `dynamodbav:"toId" json:"toId"`
	Role        string `dynamodbav:"role" json:"role"`
	Origin      string `dynamodbav:"origin" json:"origin"`
	Rule        string `dynamodbav:"rule,omitempty" json:"rule,omitempty"`
	SourceField string `dynamodbav:"sourceField" json:"sourceField"`
	Mention     string `dynamodbav:"mention,omitempty" json:"mention,omitempty"`
	SourceRef   string `dynamodbav:"sourceRef,omitempty" json:"sourceRef,omitempty"`
	Evidence    string `dynamodbav:"evidence,omitempty" json:"evidence,omitempty"`
	CreatedBy   string `dynamodbav:"createdBy,omitempty" json:"createdBy,omitempty"`
	CreatedAt   string `dynamodbav:"createdAt" json:"createdAt"`
	// MentionAliases: hashes of more texts a member bound to this manual edge.
	MentionAliases []string `dynamodbav:"mentionAliases,stringset,omitempty" json:"-"`
}

// MaxPeopleMentionAliases bounds the alias hashes of one manual edge.
const MaxPeopleMentionAliases = 20

// Key returns the identity of the edge.
func (l PeopleLink) Key() PeopleLinkKey {
	return PeopleLinkKey{MeetingID: l.MeetingID, ToKind: l.ToKind, ToID: l.ToID, Role: l.Role}
}

// validMeetingLinkID accepts {owner}:{meetingId} with exactly one colon.
func validMeetingLinkID(id string) bool {
	owner, meeting, ok := strings.Cut(id, ":")
	return ok && validAccountLinkID(owner) && validAccountLinkID(meeting) &&
		!strings.Contains(meeting, ":") && validAccountLinkID(id)
}

// ValidatePeopleLinkKey checks the role/kind pair and the shape of both IDs.
// Existence and access are checked by the repository transaction.
func ValidatePeopleLinkKey(k PeopleLinkKey) error {
	if !allowedPeopleRoleKinds[k.Role][k.ToKind] || !validMeetingLinkID(k.MeetingID) ||
		!validAccountLinkID(k.ToID) || strings.Contains(k.ToID, ":") {
		return ErrInvalidAccountLink
	}
	return nil
}

// PeopleLinkSortKey is the forward row sort key.
func PeopleLinkSortKey(k PeopleLinkKey) string {
	return peopleLinkPrefix + k.MeetingID + "#" + k.ToKind + "#" + k.ToID + "#" + k.Role
}

// PeopleLinkToSortKey is the reverse row sort key.
func PeopleLinkToSortKey(k PeopleLinkKey) string {
	return PrefixAccountLinkTo + k.ToKind + "#" + k.ToID + "#" + LinkKindMeeting + "#" + k.MeetingID + "#" + k.Role
}

// PeopleLinkDenySortKey is the deny row sort key for a removed auto edge.
func PeopleLinkDenySortKey(k PeopleLinkKey) string {
	return PrefixAccountLinkDeny + LinkKindMeeting + "#" + k.MeetingID + "#" + k.ToKind + "#" + k.ToID + "#" + k.Role
}

// IsPeopleLinkSortKey reports whether sk is in the people forward range.
func IsPeopleLinkSortKey(sk string) bool {
	return strings.HasPrefix(sk, peopleLinkPrefix)
}

// ParsePeopleLinkSortKey decodes a forward people key; ok is false for any key
// that is not a valid people edge.
func ParsePeopleLinkSortKey(sk string) (PeopleLinkKey, bool) {
	if !IsPeopleLinkSortKey(sk) {
		return PeopleLinkKey{}, false
	}
	parts := strings.Split(strings.TrimPrefix(sk, peopleLinkPrefix), "#")
	if len(parts) != 4 {
		return PeopleLinkKey{}, false
	}
	k := PeopleLinkKey{MeetingID: parts[0], ToKind: parts[1], ToID: parts[2], Role: parts[3]}
	if ValidatePeopleLinkKey(k) != nil {
		return PeopleLinkKey{}, false
	}
	return k, true
}

// ParsePeopleLinkToSortKey decodes a reverse people key; ok is false for any
// key that is not a valid people edge.
func ParsePeopleLinkToSortKey(sk string) (PeopleLinkKey, bool) {
	if !strings.HasPrefix(sk, PrefixAccountLinkTo) {
		return PeopleLinkKey{}, false
	}
	parts := strings.Split(strings.TrimPrefix(sk, PrefixAccountLinkTo), "#")
	if len(parts) != 5 || parts[2] != LinkKindMeeting {
		return PeopleLinkKey{}, false
	}
	k := PeopleLinkKey{ToKind: parts[0], ToID: parts[1], MeetingID: parts[3], Role: parts[4]}
	if ValidatePeopleLinkKey(k) != nil {
		return PeopleLinkKey{}, false
	}
	return k, true
}

// MentionHash is the first 32 lowercase hex characters of sha256(normalized).
func MentionHash(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])[:32]
}

// MentionSortKey is the unresolved mention row sort key.
func MentionSortKey(hash, meetingID, role string) string {
	return PrefixAccountMention + hash + "#" + meetingID + "#" + role
}

// MentionIgnoreSortKey is the deny row for an ignored mention.
func MentionIgnoreSortKey(hash string) string {
	return PrefixAccountLinkDeny + LinkKindMention + "#" + hash
}

// UnresolvedMention is a mention the resolver could not bind to one target.
type UnresolvedMention struct {
	AccountID   string `dynamodbav:"accountId" json:"-"`
	Hash        string `dynamodbav:"hash" json:"hash"`
	MeetingID   string `dynamodbav:"meetingId" json:"meetingId"`
	Role        string `dynamodbav:"role" json:"role"`
	SourceField string `dynamodbav:"sourceField" json:"sourceField"`
	Mention     string `dynamodbav:"mention" json:"mention"`
	SourceRef   string `dynamodbav:"sourceRef,omitempty" json:"sourceRef,omitempty"`
	CreatedAt   string `dynamodbav:"createdAt" json:"createdAt"`
}

// PeopleRun is the resolver run state for one meeting or the whole account.
type PeopleRun struct {
	AccountID  string   `dynamodbav:"accountId" json:"-"`
	Scope      string   `dynamodbav:"scope" json:"scope"` // meetingLinkId or "account"
	Mentions   []string `dynamodbav:"mentions,omitempty" json:"-"`
	LeaseToken string   `dynamodbav:"leaseToken,omitempty" json:"-"`
	LeaseUntil int64    `dynamodbav:"leaseUntil,omitempty" json:"-"` // unix seconds
	Rerun      bool     `dynamodbav:"rerun,omitempty" json:"-"`
	RunAt      string   `dynamodbav:"runAt,omitempty" json:"runAt,omitempty"`
	Resolved   int      `dynamodbav:"resolved" json:"resolved"`
	Unresolved int      `dynamodbav:"unresolved" json:"unresolved"`
	Truncated  bool     `dynamodbav:"truncated,omitempty" json:"truncated,omitempty"`
}
