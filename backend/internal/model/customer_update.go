package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
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
)

const (
	MaxUpdateSources        = 30
	MaxUpdateLinksPerUpload = 200
	MaxUpdateURLLength      = 2048
	MaxDraftBodyRunes       = 2000

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
type UpdateDecision struct {
	URLHash   string    `json:"urlHash" dynamodbav:"urlHash"`
	AccountID string    `json:"accountId" dynamodbav:"accountId"`
	Choice    string    `json:"choice" dynamodbav:"choice"`
	Score     float64   `json:"score" dynamodbav:"score"`
	Reason    string    `json:"reason" dynamodbav:"reason"`
	Backend   string    `json:"backend" dynamodbav:"backend"`
	RunID     string    `json:"runId" dynamodbav:"runId"`
	Outcome   string    `json:"outcome" dynamodbav:"outcome"`
	DecidedAt time.Time `json:"decidedAt" dynamodbav:"decidedAt"`
	ExpiresAt int64     `json:"expiresAt" dynamodbav:"pendingShareExpiresAt"`
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

func isUpdateInternalHost(host string) bool {
	for _, s := range updateInternalHostSuffixes {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
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
	if host == "" || !strings.Contains(host, ".") || net.ParseIP(host) != nil || strings.Contains(host, ":") {
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
			pairs = append(pairs, pair)
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		ki, _, _ := strings.Cut(pairs[i], "=")
		kj, _, _ := strings.Cut(pairs[j], "=")
		return ki < kj
	})

	out := url.URL{Scheme: "https", Host: host, Path: u.Path, RawPath: u.RawPath, RawQuery: strings.Join(pairs, "&")}
	if out.Path == "" {
		out.Path = "/"
	}
	normalized := out.String()
	if len(normalized) > MaxUpdateURLLength {
		return "", "", rejectUpdateURL("length")
	}
	sum := sha256.Sum256([]byte(normalized))
	return normalized, hex.EncodeToString(sum[:]), nil
}
