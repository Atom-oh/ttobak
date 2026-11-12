package model

import "time"

// PrefixAccountNewsSource keys an account-owned news source ref:
// PK ACCOUNT#{accountId}, SK NEWSSRC#{sourceId}. The crawler CONFIG row
// (CRAWLER#{sourceId} / CONFIG) carries the same accountId; readers require both.
const PrefixAccountNewsSource = "NEWSSRC#"

// SKAccountNewsSourceCount keys the per-account source counter row
// (PK ACCOUNT#{accountId}). Its attribute sourceCount is incremented in the
// create transaction (conditioned on the cap) and decremented in the delete
// transaction. It deliberately does not start with PrefixAccountNewsSource.
const SKAccountNewsSourceCount = "NEWSSRC_COUNT"

// AccountNewsSourceRef records that an account owns a crawler source.
type AccountNewsSourceRef struct {
	AccountID  string    `dynamodbav:"accountId" json:"accountId"`
	SourceID   string    `dynamodbav:"sourceId" json:"sourceId"`
	SourceName string    `dynamodbav:"sourceName" json:"sourceName"`
	CreatedBy  string    `dynamodbav:"createdBy" json:"createdBy"`
	CreatedAt  time.Time `dynamodbav:"createdAt" json:"createdAt"`
}

// CreateAccountNewsSourceRequest creates a new account-owned news source.
// CopyFromSourceID copies queries, aliases and custom URLs from a per-user
// source the caller subscribes to; the original is never modified.
type CreateAccountNewsSourceRequest struct {
	SourceName       string   `json:"sourceName"`
	NewsQueries      []string `json:"newsQueries,omitempty"`
	Aliases          []string `json:"aliases,omitempty"`
	CopyFromSourceID string   `json:"copyFromSourceId,omitempty"`
}

// AccountNewsSourcesResponse lists an account's news sources.
type AccountNewsSourcesResponse struct {
	Sources []CrawlerSource `json:"sources"`
}

// UpdateAccountNewsSourceRequest edits an account-owned news source. An
// absent or null field is unchanged; a present field replaces the stored value.
type UpdateAccountNewsSourceRequest struct {
	SourceName  *string   `json:"sourceName,omitempty"`
	NewsQueries *[]string `json:"newsQueries,omitempty"`
	Aliases     *[]string `json:"aliases,omitempty"`
}

// News source kinds in NewsSourceItem.Kind.
const (
	NewsSourceKindCommon  = "common"
	NewsSourceKindAccount = "account"
)

// NewsSourceItem is one of the caller's news sources: a subscribed common
// source or a source owned by an account the caller is a direct member of.
// Internal CONFIG fields (owner, subscribers, operator markers) are omitted.
type NewsSourceItem struct {
	SourceID      string   `json:"sourceId"`
	SourceName    string   `json:"sourceName"`
	Kind          string   `json:"kind"`
	AccountID     string   `json:"accountId,omitempty"`
	AccountName   string   `json:"accountName,omitempty"`
	NewsQueries   []string `json:"newsQueries"`
	Aliases       []string `json:"aliases"`
	CustomUrls    []string `json:"customUrls"`
	NewsSources   []string `json:"newsSources"`
	Status        string   `json:"status"`
	LastCrawledAt string   `json:"lastCrawledAt"`
	DocumentCount int      `json:"documentCount"`
	CanEdit       bool     `json:"canEdit"`
	// ListsTruncated reports that a list or string was cut to item bounds.
	ListsTruncated bool `json:"listsTruncated,omitempty"`
	// NextCollectAllowedAt (account kind only) is when "collect now" may be
	// requested again; omitted when it may be requested now.
	NextCollectAllowedAt string `json:"nextCollectAllowedAt,omitempty"`
}

// NewsCollectRequested is the EventBridge detail of a "collect now" request
// (source ttobak.crawler). It carries identifiers only; the crawler re-reads
// the CONFIG row and refuses anything that does not match.
type NewsCollectRequested struct {
	SourceID  string `json:"sourceId"`
	AccountID string `json:"accountId"`
	RequestID string `json:"requestId"`
}

// NewsCollectResponse acknowledges an accepted "collect now" request. The run
// itself is asynchronous; RequestedAt and NextAllowedAt are UTC RFC3339.
type NewsCollectResponse struct {
	RequestID     string `json:"requestId"`
	RequestedAt   string `json:"requestedAt"`
	NextAllowedAt string `json:"nextAllowedAt"`
}

// NewsSourcesResponse is one page of the caller's news sources. Truncated
// reports that a membership or subscription cap left sources out.
type NewsSourcesResponse struct {
	Sources    []NewsSourceItem `json:"sources"`
	NextCursor string           `json:"nextCursor,omitempty"`
	Truncated  bool             `json:"truncated,omitempty"`
}

// NewOpportunityScoreMin is the minimum QualityScore for a news row to count
// as a new-opportunity candidate.
const NewOpportunityScoreMin = 0.75

// IsNewOpportunityCandidate reports whether an article for an account's
// unlinked news looks like a new opportunity: it names opportunities and its
// quality score reaches the threshold. Linked news is never a candidate.
func IsNewOpportunityCandidate(d CrawledDocument, linked bool) bool {
	return !linked && d.OpportunityCount > 0 && d.QualityScore >= NewOpportunityScoreMin
}
