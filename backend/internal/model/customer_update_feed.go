package model

import "time"

// Feed registry: one registry row per (feed, user, source) lives in the fixed
// partition PKUpdateFeeds so collectors and the fan-out worker enumerate feeds
// without scanning the table. Shared per-feed state and entries live under
// PK = CUFEED#{feedHash}. The attribute names are camelCase exactly as tagged
// because the Python collector reads and writes the same rows.
const (
	PKUpdateFeeds         = "CUFEEDS"
	SKUpdateFeedState     = "STATE"
	PrefixUpdateFeed      = "CUFEED#"
	PrefixUpdateFeedEntry = "ENTRY#"
)

// UpdateFeedPK is the partition of a feed's shared state and entries.
func UpdateFeedPK(feedHash string) string { return PrefixUpdateFeed + feedHash }

// UpdateFeedSubSK is the sort key of a registry row in PKUpdateFeeds.
func UpdateFeedSubSK(feedHash, userID, sourceID string) string {
	return feedHash + "#" + userID + "#" + sourceID
}

// UpdateFeedEntrySK is the sort key of one feed entry; it is not time ordered.
func UpdateFeedEntrySK(entryHash string) string { return PrefixUpdateFeedEntry + entryHash }

// UpdateFeedSub registers one user's non-Slack source as a subscriber of a feed.
// FeedHash is the urlHash of the normalized source URL.
type UpdateFeedSub struct {
	FeedHash string    `json:"feedHash" dynamodbav:"feedHash"`
	URL      string    `json:"url" dynamodbav:"url"`
	Kind     string    `json:"kind" dynamodbav:"kind"`
	UserID   string    `json:"userId" dynamodbav:"userId"`
	SourceID string    `json:"sourceId" dynamodbav:"sourceId"`
	Status   string    `json:"status" dynamodbav:"status"`
	FannedAt time.Time `json:"fannedAt" dynamodbav:"fannedAt"`
}

// UpdateFeedState is the shared fetch state of a feed.
type UpdateFeedState struct {
	FeedHash      string    `json:"feedHash" dynamodbav:"feedHash"`
	ResolvedURL   string    `json:"resolvedUrl" dynamodbav:"resolvedUrl"`
	UpdatedAt     time.Time `json:"updatedAt" dynamodbav:"updatedAt"`
	LastFetchedAt time.Time `json:"lastFetchedAt" dynamodbav:"lastFetchedAt"`
	Failures      int       `json:"failures" dynamodbav:"failures"`
	LastError     string    `json:"lastError" dynamodbav:"lastError"`
}

// UpdateFeedEntry is one entry seen in a feed. ExpiresAt is epoch seconds under
// the table's TTL attribute.
type UpdateFeedEntry struct {
	EntryHash   string    `json:"entryHash" dynamodbav:"entryHash"`
	URL         string    `json:"url" dynamodbav:"url"`
	Title       string    `json:"title" dynamodbav:"title"`
	PublishedAt time.Time `json:"publishedAt" dynamodbav:"publishedAt"`
	SeenAt      time.Time `json:"seenAt" dynamodbav:"seenAt"`
	ExpiresAt   int64     `json:"expiresAt" dynamodbav:"pendingShareExpiresAt"`
}
