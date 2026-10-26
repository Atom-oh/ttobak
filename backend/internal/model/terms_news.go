package model

// Daily news terms (layered dictionary, Task 11). News is the only source of
// automatic Common terms. The rows below live under DICT#COMMON, are read by
// the news job only, and store day strings, global crawler source IDs and a
// content hash; never text or a user ID.
const (
	// DictNewsActor is the createdBy/updatedBy of rows the news job writes
	// and the ownerId of its observations: a system writer, not a user.
	DictNewsActor = "system:news"

	PrefixTermsNews        = "TERMS_NEWS#"
	PrefixTermsNewsRun     = "TERMS_NEWS_RUN#"
	EntityTypeTermsNews    = "TERMS_NEWS"
	EntityTypeTermsNewsRun = "TERMS_NEWS_RUN"
)

// TermsNewsHistory, DICT#COMMON / TERMS_NEWS#{termKey}, records the days
// (YYYY-MM-DD, UTC) a term was a news candidate and, as "{day}#{sourceId}",
// which global crawler sources named it on each, for the repeat threshold.
// It expires with the table TTL.
type TermsNewsHistory struct {
	PK         string   `dynamodbav:"PK"`
	SK         string   `dynamodbav:"SK"`
	TermKey    string   `dynamodbav:"termKey"`
	Days       []string `dynamodbav:"days"`
	Sources    []string `dynamodbav:"sources,omitempty"`
	ExpiresAt  int64    `dynamodbav:"pendingShareExpiresAt"`
	UpdatedAt  string   `dynamodbav:"updatedAt"`
	EntityType string   `dynamodbav:"entityType"`
}

// TermsNewsRun, DICT#COMMON / TERMS_NEWS_RUN#{day}, is the idempotency row of
// one succeeded day: a repeat with the same documents skips the model calls.
type TermsNewsRun struct {
	PK          string `dynamodbav:"PK"`
	SK          string `dynamodbav:"SK"`
	Day         string `dynamodbav:"day"`
	ContentHash string `dynamodbav:"contentHash"`
	ExpiresAt   int64  `dynamodbav:"pendingShareExpiresAt"`
	UpdatedAt   string `dynamodbav:"updatedAt"`
	EntityType  string `dynamodbav:"entityType"`
}

// TermsNewsSK returns the sort key of a term's news candidate history.
func TermsNewsSK(termKey string) string {
	return PrefixTermsNews + termKey
}

// TermsNewsRunSK returns the sort key of a day's news run row.
func TermsNewsRunSK(day string) string {
	return PrefixTermsNewsRun + day
}
