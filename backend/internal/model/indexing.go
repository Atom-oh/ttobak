package model

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

const (
	IndexJobsPK        = "KBINDEX#JOBS"
	IndexControlPK     = "KBINDEX#CONTROL"
	IndexPrefix        = "canonical/v1/"
	IndexPending       = "PENDING"
	IndexPreparing     = "PREPARING"
	IndexWaitingSync   = "WAITING_SYNC"
	IndexWaitingSource = "WAITING_SOURCE"
	IndexIndexed       = "INDEXED"
	IndexDeleted       = "DELETED"
	IndexFailed        = "FAILED"
)

var indexID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

type IndexResource struct {
	PK   string `json:"pk" dynamodbav:"pk"`
	SK   string `json:"sk" dynamodbav:"sk"`
	Kind string `json:"kind" dynamodbav:"kind"`
	ID   string `json:"id" dynamodbav:"id"`
}

func CanonicalIndexResource(pk, sk string) (IndexResource, bool) {
	r := IndexResource{PK: pk, SK: sk}
	switch {
	case strings.HasPrefix(pk, "USER#") && strings.HasPrefix(sk, "MEETING#"):
		r.Kind, r.ID = "meeting", strings.TrimPrefix(sk, "MEETING#")
	case strings.HasPrefix(pk, "USER#") && strings.HasPrefix(sk, "DOC#"):
		r.Kind, r.ID = "personalDocument", strings.TrimPrefix(sk, "DOC#")
	case strings.HasPrefix(pk, "ACCOUNT#") && strings.HasPrefix(sk, "DOC#"):
		r.Kind, r.ID = "accountDocument", strings.TrimPrefix(sk, "DOC#")
	default:
		return IndexResource{}, false
	}
	_, owner, _ := strings.Cut(pk, "#")
	return r, indexID.MatchString(owner) && indexID.MatchString(r.ID)
}

func (r IndexResource) Hash() string {
	hash := sha256.Sum256([]byte(r.PK + "\x00" + r.SK))
	return hex.EncodeToString(hash[:])
}
func (r IndexResource) Prefix() string { return IndexPrefix + r.Kind + "/" + r.Hash() + "/" }

// IndexRecord contains only projection-relevant source attributes. An absent
// record is represented by nil; nil map values are real DynamoDB NULL values.
type IndexRecord struct {
	Resource IndexResource
	Fields   map[string]interface{}
}

func IndexSourceFields(kind string) []string {
	if kind == "meeting" {
		return []string{"meetingId", "userId", "title", "date", "status", "notes", "content", "selectedTranscript", "transcriptA", "transcriptB", "actionItems", "updatedAt"}
	}
	return []string{"docId", "accountId", "sourceUserId", "title", "docType", "content", "fileKey", "fileName", "mimeType", "updatedAt"}
}

type IndexJob struct {
	Resource        IndexResource `dynamodbav:"resource" json:"resource"`
	Version         int64         `dynamodbav:"version" json:"version"`
	State           string        `dynamodbav:"state" json:"state"`
	DesiredRevision string        `dynamodbav:"desiredRevision" json:"desiredRevision,omitempty"`
	Revision        string        `dynamodbav:"revision" json:"revision,omitempty"`
	RunID           string        `dynamodbav:"runId" json:"runId,omitempty"`
	LeaseUntil      int64         `dynamodbav:"leaseUntil" json:"leaseUntil,omitempty"`
	RetryAfter      int64         `dynamodbav:"retryAfter" json:"retryAfter,omitempty"`
	Keys            []string      `dynamodbav:"keys" json:"-"`
	PendingKeys     []string      `dynamodbav:"pendingKeys" json:"-"`
	Outcome         string        `dynamodbav:"outcome" json:"-"`
	ErrorCode       string        `dynamodbav:"errorCode" json:"errorCode,omitempty"`
	SyncID          string        `dynamodbav:"syncId" json:"syncId,omitempty"`
	UpdatedAt       int64         `dynamodbav:"updatedAt" json:"updatedAt"`
}

type IndexMember struct {
	Resource IndexResource `dynamodbav:"resource"`
	Version  int64         `dynamodbav:"version"`
	RunID    string        `dynamodbav:"runId"`
	Revision string        `dynamodbav:"revision"`
}

// A frozen batch stays EXPORTING until every partial projection is cleaned or
// fully staged. PREPARED persists ClientToken before any provider submission.
type IndexControl struct {
	Version           int64         `dynamodbav:"version"`
	Owner             string        `dynamodbav:"owner"`
	LeaseUntil        int64         `dynamodbav:"leaseUntil"`
	Phase             string        `dynamodbav:"phase"`
	Batch             []IndexMember `dynamodbav:"batch"`
	ClientToken       string        `dynamodbav:"clientToken"`
	ProviderJobID     string        `dynamodbav:"providerJobId"`
	ProviderSucceeded bool          `dynamodbav:"providerSucceeded"`
	ErrorCode         string        `dynamodbav:"errorCode"`
	SourceCursor      string        `dynamodbav:"sourceCursor"`
	JobCursor         string        `dynamodbav:"jobCursor"`
	LegacyCursor      string        `dynamodbav:"legacyCursor"`
}
