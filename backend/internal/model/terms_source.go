package model

import (
	"crypto/sha256"
	"encoding/hex"
)

// Term extraction from KB documents and deep-research reports (layered
// dictionary, Task 10). A TermsSourceRun row, USER#{owner} /
// TERMS_SOURCE#{kind}#{ref}, records the content hash of the last succeeded
// run so a redelivered or repeated trigger does not call the model again.
const (
	PrefixTermsSource     = "TERMS_SOURCE#"
	EntityTypeTermsSource = "TERMS_SOURCE"
)

// TermsSourceRequested is the ttobak.analysis/TermsSourceRequested event
// detail. Kind is DictSourceKB (Ref = object key) or DictSourceResearch
// (Ref = research ID). AccountIDs is what the trigger saw; the worker re-reads
// the authoritative value (object metadata, research item) before merging.
type TermsSourceRequested struct {
	Kind       string   `json:"kind"`
	Ref        string   `json:"ref"`
	OwnerID    string   `json:"ownerId"`
	AccountIDs []string `json:"accountIds,omitempty"`
}

// TermsSourceRun is the idempotency row of one source. It stores a hash, never
// text.
type TermsSourceRun struct {
	PK          string `dynamodbav:"PK"`
	SK          string `dynamodbav:"SK"`
	OwnerID     string `dynamodbav:"ownerId"`
	Kind        string `dynamodbav:"kind"`
	Ref         string `dynamodbav:"ref"`
	ContentHash string `dynamodbav:"contentHash"`
	UpdatedAt   string `dynamodbav:"updatedAt"`
	EntityType  string `dynamodbav:"entityType"`
}

// TermsSourceSK returns the sort key of a source run row.
func TermsSourceSK(kind, ref string) string {
	return PrefixTermsSource + kind + "#" + ref
}

// DictObsSourceID is the source part of an observation sort key: the meeting
// ID for meeting evidence, else kind plus a digest of the ref, so an object
// key (any length, any characters) never becomes part of a key.
func DictObsSourceID(kind, ref string) string {
	if kind == DictSourceMeeting || kind == "" {
		return ref
	}
	sum := sha256.Sum256([]byte(ref))
	return kind + ":" + hex.EncodeToString(sum[:16])
}

// ObservationSK is the sort key an observation row must have: meeting rows
// are keyed by meetingId, other sources by DictObsSourceID.
func (o *DictObservation) ObservationSK() string {
	if o.MeetingID != "" {
		return DictObsSK(o.TermKey, o.MeetingID)
	}
	return DictObsSK(o.TermKey, DictObsSourceID(o.SourceKind, o.SourceRef))
}
