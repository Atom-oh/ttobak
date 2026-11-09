package model

import "time"

// PrefixAccountNewsSource keys an account-owned news source ref:
// PK ACCOUNT#{accountId}, SK NEWSSRC#{sourceId}. The crawler CONFIG row
// (CRAWLER#{sourceId} / CONFIG) carries the same accountId; readers require both.
const PrefixAccountNewsSource = "NEWSSRC#"

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
