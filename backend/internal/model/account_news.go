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

// NewOpportunityScoreMin is the minimum QualityScore for a news row to count
// as a new-opportunity candidate.
const NewOpportunityScoreMin = 0.75

// IsNewOpportunityCandidate reports whether an article for an account's
// unlinked news looks like a new opportunity: it names opportunities and its
// quality score reaches the threshold. Linked news is never a candidate.
func IsNewOpportunityCandidate(d CrawledDocument, linked bool) bool {
	return !linked && d.OpportunityCount > 0 && d.QualityScore >= NewOpportunityScoreMin
}
