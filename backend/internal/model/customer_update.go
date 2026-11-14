package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Customer update drafts: per-user rows live under PK = USER#{userId}. The
// shared URL cache lives at PK = CUURL#{urlHash}; PKUpdatePending is the
// partition of the pending-work index. TTL rows use PendingShareTTLAttribute.
const (
	PrefixUpdateSource   = "CUSRC#"
	PrefixUpdateItem     = "CUITEM#"
	PrefixUpdateDecision = "CUDEC#"
	PrefixUpdateDraft    = "CUDRAFT#"
	PrefixUpdateOpt      = "CUOPT#"
	PrefixUpdateURL      = "CUURL#"
	PKUpdatePending      = "CUPENDING"
	// SKUpdatePendingCursor is the run phases' rotation cursor row in the
	// pending partition (attribute lastUserId). "#" sorts before any user id
	// and is never a pending mark.
	SKUpdatePendingCursor = "#CURSOR"

	// SKUpdateURLMeta: the shared URL cache row is PK = CUURL#{urlHash}, SK = META,
	// shared across users (a Python runtime will read and write the same key).
	SKUpdateURLMeta = "META"
)

const (
	MaxUpdateSources        = 30
	MaxUpdateLinksPerUpload = 200
	MaxUpdateURLLength      = 2048
	MaxDraftBodyRunes       = 2000
	// MaxUpdateDraftsScan bounds how many of a caller's draft rows one list
	// request reads before ordering and paging them in memory.
	MaxUpdateDraftsScan = 1000

	UpdateDraftTTL    = 90 * 24 * time.Hour
	UpdateDecisionTTL = 180 * 24 * time.Hour
	UpdateItemTTL     = 30 * 24 * time.Hour
	UpdateURLTTL      = 30 * 24 * time.Hour
)

// Enumerated values.
const (
	UpdateSourceKindSlack   = "slack"
	UpdateSourceKindRSS     = "rss"
	UpdateSourceKindSite    = "site"
	UpdateSourceKindYouTube = "youtube"

	UpdateSourceActive   = "active"
	UpdateSourceDisabled = "disabled"

	UpdateItemNew     = "new"
	UpdateItemDecided = "decided"
	UpdateItemSkipped = "skipped"

	UpdateURLPending      = "pending"
	UpdateURLReady        = "ready"
	UpdateURLInsufficient = "insufficient"
	UpdateURLFailed       = "failed"

	UpdateChoiceNeeded    = "needed"
	UpdateChoiceNotNeeded = "not_needed"

	UpdateOutcomeDrafted   = "drafted"
	UpdateOutcomeDiscarded = "discarded"
	UpdateOutcomeDeferred  = "deferred"

	UpdateDraftNew       = "new"
	UpdateDraftAdopted   = "adopted"
	UpdateDraftDismissed = "dismissed"

	UpdateDismissNotRelevant  = "not_relevant"
	UpdateDismissAlreadyKnown = "already_known"
	UpdateDismissNotNow       = "not_now"
)

func UpdateSourceSK(sourceID string) string { return PrefixUpdateSource + sourceID }
func UpdateItemSK(urlHash string) string    { return PrefixUpdateItem + urlHash }
func UpdateDecisionSK(urlHash, accountID string) string {
	return PrefixUpdateDecision + urlHash + "#" + accountID
}
func UpdateDraftSK(accountID, urlHash string) string {
	return PrefixUpdateDraft + accountID + "#" + urlHash
}
func UpdateOptSK(accountID string) string { return PrefixUpdateOpt + accountID }
func UpdateURLPK(urlHash string) string   { return PrefixUpdateURL + urlHash }

// UpdateSource is a user's configured link source. ExpiresAt fields are epoch
// seconds stored under the table's TTL attribute (PendingShareTTLAttribute).
type UpdateSource struct {
	SourceID      string    `json:"sourceId" dynamodbav:"sourceId"`
	Kind          string    `json:"kind" dynamodbav:"kind"`
	Label         string    `json:"label" dynamodbav:"label"`
	URL           string    `json:"url" dynamodbav:"url"`
	LocalSourceID string    `json:"localSourceId" dynamodbav:"localSourceId"`
	Status        string    `json:"status" dynamodbav:"status"`
	CreatedAt     time.Time `json:"createdAt" dynamodbav:"createdAt"`
	BackfilledAt  time.Time `json:"backfilledAt" dynamodbav:"backfilledAt"`
	// FeedHash is the registry key of a non-Slack source, stored so a later
	// normalization change cannot orphan its registry row. Empty marks a legacy
	// row. It is not part of the API.
	FeedHash string `json:"-" dynamodbav:"feedHash,omitempty"`
	// FeedError is the feed's last fetch error while it keeps failing. It is
	// derived when sources are listed and never stored on the source row.
	FeedError string `json:"feedError,omitempty" dynamodbav:"-"`
}

// UpdateItem is one normalized link seen from one or more sources.
type UpdateItem struct {
	URLHash     string    `json:"urlHash" dynamodbav:"urlHash"`
	URL         string    `json:"url" dynamodbav:"url"`
	SourceIDs   []string  `json:"sourceIds" dynamodbav:"sourceIds"`
	FirstSeenAt time.Time `json:"firstSeenAt" dynamodbav:"firstSeenAt"`
	State       string    `json:"state" dynamodbav:"state"`
	Attempts    int       `json:"attempts" dynamodbav:"attempts"`
	ExpiresAt   int64     `json:"expiresAt" dynamodbav:"pendingShareExpiresAt"`
	// HeldUntil keeps a new item out of runs until this time (a draft was
	// deferred by a day budget); zero means not held. Leaving state new clears it.
	HeldUntil time.Time `json:"heldUntil,omitzero" dynamodbav:"heldUntil,omitempty"`
}

// UpdateURL is the shared fetched-content cache row for a normalized URL.
type UpdateURL struct {
	URLHash     string    `json:"urlHash" dynamodbav:"urlHash"`
	URL         string    `json:"url" dynamodbav:"url"`
	Status      string    `json:"status" dynamodbav:"status"`
	Title       string    `json:"title" dynamodbav:"title"`
	Summary     string    `json:"summary" dynamodbav:"summary"`
	Services    []string  `json:"services" dynamodbav:"services"`
	Topics      []string  `json:"topics" dynamodbav:"topics"`
	PublishedAt time.Time `json:"publishedAt" dynamodbav:"publishedAt"`
	FetchedAt   time.Time `json:"fetchedAt" dynamodbav:"fetchedAt"`
	ExpiresAt   int64     `json:"expiresAt" dynamodbav:"pendingShareExpiresAt"`
}

// UpdateDecision records the relevance decision for one (URL, account).
// Reason and GateInput are stored sealed, never as plaintext attributes;
// GateInput is kept only for a needed choice.
type UpdateDecision struct {
	URLHash       string           `json:"urlHash" dynamodbav:"urlHash"`
	AccountID     string           `json:"accountId" dynamodbav:"accountId"`
	Choice        string           `json:"choice" dynamodbav:"choice"`
	Score         float64          `json:"score" dynamodbav:"score"`
	Reason        string           `json:"reason" dynamodbav:"-"`
	GateInput     *UpdateGateInput `json:"gateInput,omitempty" dynamodbav:"-"`
	Backend       string           `json:"backend" dynamodbav:"backend"`
	RunID         string           `json:"runId" dynamodbav:"runId"`
	Outcome       string           `json:"outcome" dynamodbav:"outcome"`
	DiscardReason string           `json:"discardReason" dynamodbav:"discardReason"`
	DecidedAt     time.Time        `json:"decidedAt" dynamodbav:"decidedAt"`
	ExpiresAt     int64            `json:"expiresAt" dynamodbav:"pendingShareExpiresAt"`
}

// UpdateGateOption is one option the relevance gate scored.
type UpdateGateOption struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// UpdateGateInput is the question, options and context the gate scored for a
// needed decision, as the agent runtime writes it (gate.py decide_pair).
type UpdateGateInput struct {
	Question string             `json:"question"`
	Options  []UpdateGateOption `json:"options"`
	Context  string             `json:"context"`
}

// UpdateDraftOppty is an opportunity reference attached to a draft.
type UpdateDraftOppty struct {
	ProjectID string `json:"projectId" dynamodbav:"projectId"`
	Name      string `json:"name" dynamodbav:"name"`
	Stage     string `json:"stage" dynamodbav:"stage"`
}

// UpdateDraft is a generated customer update draft for one (account, URL).
type UpdateDraft struct {
	AccountID     string             `json:"accountId" dynamodbav:"accountId"`
	URLHash       string             `json:"urlHash" dynamodbav:"urlHash"`
	SourceURL     string             `json:"sourceUrl" dynamodbav:"sourceUrl"`
	SourceTitle   string             `json:"sourceTitle" dynamodbav:"sourceTitle"`
	Why           string             `json:"why" dynamodbav:"why"`
	Body          string             `json:"body" dynamodbav:"body"`
	EditedBody    string             `json:"editedBody" dynamodbav:"editedBody"`
	Opportunities []UpdateDraftOppty `json:"opportunities" dynamodbav:"opportunities"`
	Links         []string           `json:"links" dynamodbav:"links"`
	Status        string             `json:"status" dynamodbav:"status"`
	DismissReason string             `json:"dismissReason" dynamodbav:"dismissReason"`
	Revision      int                `json:"revision" dynamodbav:"revision"`
	RunID         string             `json:"runId" dynamodbav:"runId"`
	CreatedAt     time.Time          `json:"createdAt" dynamodbav:"createdAt"`
	UpdatedAt     time.Time          `json:"updatedAt" dynamodbav:"updatedAt"`
	ExpiresAt     int64              `json:"expiresAt" dynamodbav:"pendingShareExpiresAt"`
	// Sealed is the at-rest sealed text of a listed, unopened draft; it is
	// never serialized.
	Sealed string `json:"-" dynamodbav:"-"`
}

// NormalizeSlices replaces nil Opportunities and Links with empty slices so a
// draft never stores DynamoDB NULL for them or serializes them as JSON null.
func (d *UpdateDraft) NormalizeSlices() {
	if d.Opportunities == nil {
		d.Opportunities = []UpdateDraftOppty{}
	}
	if d.Links == nil {
		d.Links = []string{}
	}
}

// ErrUpdateURLRejected is wrapped with one reason: scheme, userinfo, host,
// internal_host or length.
var ErrUpdateURLRejected = errors.New("update url rejected")

func rejectUpdateURL(reason string) error {
	return fmt.Errorf("%w: %s", ErrUpdateURLRejected, reason)
}

// updateInternalHostSuffixes mirrors contracts/customer-updates/internal-hosts-v1.json;
// TestInternalHostsMirrorContract keeps the two in sync.
var updateInternalHostSuffixes = []string{
	"slack.com",
	"slack-files.com",
	"amazon.dev",
	"aws.dev",
	"a2z.com",
	"corp.amazon.com",
	"w.amazon.com",
	"quip-amazon.com",
	"amazon.work",
	"awsapps.com",
	"sharepoint.com",
	"salesforce.com",
	"force.com",
	"sim.amazon.com",
	"issues.amazon.com",
	"code.amazon.com",
	"phonetool.amazon.com",
	"broadcast.amazon.com",
}

var updateDroppedQueryKeys = map[string]bool{
	"gclid": true, "fbclid": true, "sc_channel": true, "sc_campaign": true,
	"sc_publisher": true, "sc_medium": true, "sc_content": true,
	"sc_country": true, "sc_geo": true, "sc_outcome": true,
	"trk": true, "trkcampaign": true, "ref_": true, "si": true,
}

// IsUpdateInternalHost reports whether host (already lowercased, no port) equals
// an internal suffix or ends with "." plus one.
func IsUpdateInternalHost(host string) bool { return isUpdateInternalHost(host) }

func isUpdateInternalHost(host string) bool {
	for _, s := range updateInternalHostSuffixes {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
}

// numericLastLabel reports whether the host's last label is only ASCII digits.
// No public top-level domain is numeric, and such hosts include IPv4 shorthand
// forms (127.1, 0x7f.0.0.1, 1.2.3) that resolvers may treat as addresses.
func numericLastLabel(host string) bool {
	last := host[strings.LastIndex(host, ".")+1:]
	if last == "" {
		return false
	}
	for i := 0; i < len(last); i++ {
		if last[i] < '0' || last[i] > '9' {
			return false
		}
	}
	return true
}

// allLabelsInteger reports whether every dot-separated label parses as an
// integer in base 0 (decimal, 0x hex, leading-0 octal). Such hosts are IPv4
// spellings (0x7f.0x1, 0177.0.0.0x1) that resolvers may treat as addresses.
// Out-of-range integers still count as integers.
func allLabelsInteger(host string) bool {
	for _, label := range strings.Split(host, ".") {
		if _, err := strconv.ParseUint(label, 0, 64); err != nil && !errors.Is(err, strconv.ErrRange) {
			return false
		}
	}
	return true
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// upperPercentHex uppercases the two hex digits of every %XX escape.
func upperPercentHex(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	b := []byte(s)
	for i := 0; i+2 < len(b); i++ {
		if b[i] == '%' && isHexDigit(b[i+1]) && isHexDigit(b[i+2]) {
			b[i+1] = toUpperASCII(b[i+1])
			b[i+2] = toUpperASCII(b[i+2])
			i += 2
		}
	}
	return string(b)
}

func toUpperASCII(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - 'a' + 'A'
	}
	return c
}

// NormalizeUpdateURL canonicalizes a link (https only, lowercase host, no
// default port or fragment, tracking parameters dropped, sorted query) and
// returns it with the lowercase hex SHA-256 of the normalized form. Failures
// wrap ErrUpdateURLRejected with a reason. Callers must not log the URL.
func NormalizeUpdateURL(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > MaxUpdateURLLength {
		return "", "", rejectUpdateURL("length")
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") {
		return "", "", rejectUpdateURL("scheme")
	}
	if u.User != nil {
		return "", "", rejectUpdateURL("userinfo")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || !strings.Contains(host, ".") || net.ParseIP(host) != nil || strings.Contains(host, ":") || numericLastLabel(host) || allLabelsInteger(host) {
		return "", "", rejectUpdateURL("host")
	}
	if isUpdateInternalHost(host) {
		return "", "", rejectUpdateURL("internal_host")
	}
	if p := u.Port(); p != "" && p != "443" {
		host = host + ":" + p
	}

	youtubeWatch := (host == "youtube.com" || strings.HasSuffix(host, ".youtube.com")) && u.Path == "/watch"
	var pairs []string
	if u.RawQuery != "" {
		for _, pair := range strings.Split(u.RawQuery, "&") {
			if pair == "" {
				continue
			}
			key, _, _ := strings.Cut(pair, "=")
			if k, err := url.QueryUnescape(key); err == nil {
				key = k
			}
			lk := strings.ToLower(key)
			if strings.HasPrefix(lk, "utm_") || updateDroppedQueryKeys[lk] {
				continue
			}
			if youtubeWatch && key != "v" {
				continue
			}
			pairs = append(pairs, upperPercentHex(pair))
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		ki, _, _ := strings.Cut(pairs[i], "=")
		kj, _, _ := strings.Cut(pairs[j], "=")
		return ki < kj
	})

	// EscapedPath keeps a valid escaped form as given and percent-encodes
	// anything else (non-ASCII as UTF-8); escape hex is then uppercased so
	// %c3%a9 and %C3%A9 normalize alike. Query pairs are kept verbatim apart
	// from the same hex uppercasing.
	path := upperPercentHex(u.EscapedPath())
	if path == "" {
		path = "/"
	}
	normalized := "https://" + host + path
	if len(pairs) > 0 {
		normalized += "?" + strings.Join(pairs, "&")
	}
	if len(normalized) > MaxUpdateURLLength {
		return "", "", rejectUpdateURL("length")
	}
	sum := sha256.Sum256([]byte(normalized))
	return normalized, hex.EncodeToString(sum[:]), nil
}

// AddUpdateSourceRequest creates a source. Slack sources carry LocalSourceID
// and no URL; other kinds carry a URL and no LocalSourceID.
type AddUpdateSourceRequest struct {
	Kind          string `json:"kind"`
	Label         string `json:"label"`
	URL           string `json:"url"`
	LocalSourceID string `json:"localSourceId"`
}

// PatchUpdateSourceRequest changes the label and/or status of a source.
type PatchUpdateSourceRequest struct {
	Label  *string `json:"label,omitempty"`
	Status *string `json:"status,omitempty"`
}

// UpdateUploadLink is one link uploaded from a local Slack source.
type UpdateUploadLink struct {
	URL    string `json:"url"`
	SeenAt string `json:"seenAt"`
}

// UploadUpdateLinksRequest uploads links for one slack source.
type UploadUpdateLinksRequest struct {
	SourceID string             `json:"sourceId"`
	Links    []UpdateUploadLink `json:"links"`
}

// UploadUpdateLinksResponse counts the outcome of each uploaded link.
type UploadUpdateLinksResponse struct {
	Accepted  int `json:"accepted"`
	Duplicate int `json:"duplicate"`
	Rejected  int `json:"rejected"`
}

// Draft actions for PatchUpdateDraftRequest.
const (
	UpdateDraftActionEdit    = "edit"
	UpdateDraftActionAdopt   = "adopt"
	UpdateDraftActionDismiss = "dismiss"
	UpdateDraftActionReopen  = "reopen"
)

// PatchUpdateDraftRequest applies one action to a draft at an expected revision.
type PatchUpdateDraftRequest struct {
	Action   string `json:"action"`
	Body     string `json:"body"`
	Reason   string `json:"reason"`
	Revision int    `json:"revision"`
}

// UpdateDraftsPage is one page of drafts.
type UpdateDraftsPage struct {
	Drafts     []UpdateDraft `json:"drafts"`
	NextCursor string        `json:"nextCursor"`
	// Truncated reports that the all-accounts scan stopped at
	// MaxUpdateDraftsScan rows with more rows remaining.
	Truncated bool `json:"truncated,omitempty"`
}
