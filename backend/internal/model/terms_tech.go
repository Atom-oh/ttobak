package model

// Daily AWS technical document terms (ADR-008 L4). Like news, they feed only
// DICT#COMMON. The rows below live under DICT#COMMON, are read by the tech job
// only, and store day strings, public crawled-document hashes and a content
// hash; never text or a user ID.
const (
	// DictTechActor is the createdBy/updatedBy of rows the tech job writes
	// and the ownerId of its observations: a system writer, not a user.
	DictTechActor = "system:tech"

	PrefixTermsTech        = "TERMS_TECH#"
	PrefixTermsTechRun     = "TERMS_TECH_RUN#"
	EntityTypeTermsTech    = "TERMS_TECH"
	EntityTypeTermsTechRun = "TERMS_TECH_RUN"
)

// TermsTechHistory, DICT#COMMON / TERMS_TECH#{termKey}, records the days
// (YYYY-MM-DD, UTC) a term was a tech candidate and, as "{day}#{docHash}",
// which AWS technical documents named it on each, for the distinct-document
// threshold. It expires with the table TTL.
type TermsTechHistory struct {
	PK         string   `dynamodbav:"PK"`
	SK         string   `dynamodbav:"SK"`
	TermKey    string   `dynamodbav:"termKey"`
	Days       []string `dynamodbav:"days"`
	Documents  []string `dynamodbav:"documents,omitempty"`
	ExpiresAt  int64    `dynamodbav:"pendingShareExpiresAt"`
	UpdatedAt  string   `dynamodbav:"updatedAt"`
	EntityType string   `dynamodbav:"entityType"`
}

// TermsTechRun, DICT#COMMON / TERMS_TECH_RUN#{day}, is the idempotency row of
// one succeeded tech day: a repeat with the same documents skips the model
// calls.
type TermsTechRun struct {
	PK          string `dynamodbav:"PK"`
	SK          string `dynamodbav:"SK"`
	Day         string `dynamodbav:"day"`
	ContentHash string `dynamodbav:"contentHash"`
	ExpiresAt   int64  `dynamodbav:"pendingShareExpiresAt"`
	UpdatedAt   string `dynamodbav:"updatedAt"`
	EntityType  string `dynamodbav:"entityType"`
}

// TermsTechSK returns the sort key of a term's tech candidate history.
func TermsTechSK(termKey string) string {
	return PrefixTermsTech + termKey
}

// TermsTechRunSK returns the sort key of a day's tech run row.
func TermsTechRunSK(day string) string {
	return PrefixTermsTechRun + day
}
