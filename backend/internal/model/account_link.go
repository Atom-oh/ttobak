package model

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Account link rows live in the account partition (PK ACCOUNT#{accountId}):
//
//	LINK#{fromKind}#{fromId}#{toKind}#{toId}     forward row (canonical)
//	LINKTO#{toKind}#{toId}#{fromKind}#{fromId}   reverse row, same transaction
//	LINKDENY#{fromKind}#{fromId}#{toKind}#{toId} a removed auto link; blocks re-creation
//
// News and tech IDs are {sourceId}:{docHash} (tech uses sourceId __tech__).
// Meeting IDs are {ownerUserId}:{meetingId}. Links are metadata only; readers
// revalidate each endpoint and never grant access from a link row.
const (
	PrefixAccountLink     = "LINK#"
	PrefixAccountLinkTo   = "LINKTO#"
	PrefixAccountLinkDeny = "LINKDENY#"

	LinkKindNews    = "news"
	LinkKindTech    = "tech"
	LinkKindEvent   = "event"
	LinkKindMeeting = "meeting"
	LinkKindProject = "project"

	LinkOriginManual = "manual"
	LinkOriginAuto   = "auto"

	// MaxAccountLinkIDBytes bounds each endpoint ID.
	MaxAccountLinkIDBytes = 200
)

// ErrInvalidAccountLink reports a disallowed kind pair or a malformed ID.
var ErrInvalidAccountLink = errors.New("invalid account link")

// AccountLink is a typed edge between two account-related items.
type AccountLink struct {
	AccountID string `dynamodbav:"accountId" json:"-"`
	FromKind  string `dynamodbav:"fromKind" json:"fromKind"`
	FromID    string `dynamodbav:"fromId" json:"fromId"`
	ToKind    string `dynamodbav:"toKind" json:"toKind"`
	ToID      string `dynamodbav:"toId" json:"toId"`
	Origin    string `dynamodbav:"origin" json:"origin"`
	CreatedBy string `dynamodbav:"createdBy,omitempty" json:"createdBy,omitempty"`
	CreatedAt string `dynamodbav:"createdAt" json:"createdAt"`
	Evidence  string `dynamodbav:"evidence,omitempty" json:"evidence,omitempty"`
	ModelID   string `dynamodbav:"modelId,omitempty" json:"-"`
}

// AccountLinkInput identifies a link by its two endpoints.
type AccountLinkInput struct {
	FromKind string `json:"fromKind"`
	FromID   string `json:"fromId"`
	ToKind   string `json:"toKind"`
	ToID     string `json:"toId"`
}

// Input returns the endpoint identity of the link.
func (l AccountLink) Input() AccountLinkInput {
	return AccountLinkInput{FromKind: l.FromKind, FromID: l.FromID, ToKind: l.ToKind, ToID: l.ToID}
}

// AccountLinksResponse is one page of an account's links.
type AccountLinksResponse struct {
	Links      []AccountLink `json:"links"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

// allowedAccountLinkPairs lists the only from→to kinds that can be linked.
var allowedAccountLinkPairs = map[[2]string]bool{
	{LinkKindNews, LinkKindProject}:  true,
	{LinkKindTech, LinkKindProject}:  true,
	{LinkKindEvent, LinkKindProject}: true,
	{LinkKindNews, LinkKindMeeting}:  true,
}

func validAccountLinkID(id string) bool {
	if id == "" || len(id) > MaxAccountLinkIDBytes || !utf8.ValidString(id) || strings.Contains(id, "#") {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// ValidateAccountLink checks the kind pair and the shape of both IDs. Kind
// specific formats and existence are checked by the service.
func ValidateAccountLink(in AccountLinkInput) error {
	if !allowedAccountLinkPairs[[2]string{in.FromKind, in.ToKind}] || !validAccountLinkID(in.FromID) || !validAccountLinkID(in.ToID) {
		return ErrInvalidAccountLink
	}
	return nil
}

// AccountLinkSortKey is the forward row sort key.
func AccountLinkSortKey(in AccountLinkInput) string {
	return PrefixAccountLink + in.FromKind + "#" + in.FromID + "#" + in.ToKind + "#" + in.ToID
}

// AccountLinkToSortKey is the reverse row sort key.
func AccountLinkToSortKey(in AccountLinkInput) string {
	return PrefixAccountLinkTo + in.ToKind + "#" + in.ToID + "#" + in.FromKind + "#" + in.FromID
}

// AccountLinkDenySortKey is the deny row sort key.
func AccountLinkDenySortKey(in AccountLinkInput) string {
	return PrefixAccountLinkDeny + in.FromKind + "#" + in.FromID + "#" + in.ToKind + "#" + in.ToID
}

// ParseAccountLinkSortKey decodes a forward row sort key; ok is false for any
// key that is not a valid link.
func ParseAccountLinkSortKey(sk string) (AccountLinkInput, bool) {
	if !strings.HasPrefix(sk, PrefixAccountLink) {
		return AccountLinkInput{}, false
	}
	parts := strings.Split(strings.TrimPrefix(sk, PrefixAccountLink), "#")
	if len(parts) != 4 {
		return AccountLinkInput{}, false
	}
	in := AccountLinkInput{FromKind: parts[0], FromID: parts[1], ToKind: parts[2], ToID: parts[3]}
	if ValidateAccountLink(in) != nil {
		return AccountLinkInput{}, false
	}
	return in, true
}

// ParseAccountLinkToSortKey decodes a reverse row sort key
// LINKTO#{toKind}#{toId}#{fromKind}#{fromId}; ok is false for any key that is
// not a valid link.
func ParseAccountLinkToSortKey(sk string) (AccountLinkInput, bool) {
	if !strings.HasPrefix(sk, PrefixAccountLinkTo) {
		return AccountLinkInput{}, false
	}
	parts := strings.Split(strings.TrimPrefix(sk, PrefixAccountLinkTo), "#")
	if len(parts) != 4 {
		return AccountLinkInput{}, false
	}
	in := AccountLinkInput{ToKind: parts[0], ToID: parts[1], FromKind: parts[2], FromID: parts[3]}
	if ValidateAccountLink(in) != nil {
		return AccountLinkInput{}, false
	}
	return in, true
}
