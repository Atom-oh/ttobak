package model

import "time"

// Customer brief: one row per (account, member) at ACCOUNT#{accountId} /
// CUSTBRIEF#{userId}. Run-state fields stay plaintext for conditions; the
// last good brief is sealed in "sealed" and survives failed runs.
const (
	PrefixCustomerBrief     = "CUSTBRIEF#"
	EntityTypeCustomerBrief = "CUSTOMER_BRIEF"
	CustomerBriefTTL        = 30 * 24 * time.Hour
	CustomerBriefMaxBytes   = 24000
)

func CustomerBriefSK(userID string) string { return PrefixCustomerBrief + userID }

// CustomerBriefState is the stored row. ExpiresAt is the table's swept TTL
// attribute (epoch seconds); readers treat an expired row as absent.
type CustomerBriefState struct {
	PK          string    `dynamodbav:"PK"`
	SK          string    `dynamodbav:"SK"`
	AccountID   string    `dynamodbav:"accountId"`
	UserID      string    `dynamodbav:"userId"`
	RunID       string    `dynamodbav:"runId"`
	Status      string    `dynamodbav:"status"`
	RequestedAt time.Time `dynamodbav:"requestedAt"`
	LeaseUntil  int64     `dynamodbav:"leaseUntil"`
	InputHash   string    `dynamodbav:"inputHash"`
	ErrorCode   string    `dynamodbav:"errorCode"`
	UpdatedAt   time.Time `dynamodbav:"updatedAt"`
	Sealed      string    `dynamodbav:"sealed,omitempty"`
	GeneratedAt time.Time `dynamodbav:"generatedAt,omitempty"`
	ResultHash  string    `dynamodbav:"resultHash,omitempty"`
	ModelID     string    `dynamodbav:"modelId,omitempty"`
	ExpiresAt   int64     `dynamodbav:"pendingShareExpiresAt,omitempty"`
	// BriefExpiresAt (epoch seconds) bounds the sealed brief's readability
	// independently of the row TTL, which a queued run may extend.
	BriefExpiresAt int64  `dynamodbav:"briefExpiresAt,omitempty"`
	EntityType     string `dynamodbav:"entityType"`
}

func (s *CustomerBriefState) Pending() bool {
	return s != nil && (s.Status == AnalysisQueued || s.Status == AnalysisRunning)
}

// Expired reports whether the row's TTL has passed; the sweep can lag.
func (s *CustomerBriefState) Expired(now time.Time) bool {
	return s != nil && s.ExpiresAt != 0 && s.ExpiresAt <= now.Unix()
}

// BriefExpiry is the sealed brief's own expiry. Rows written before
// briefExpiresAt existed carry none; their row TTL is the brief expiry,
// because only a completion set that TTL to the brief's 30 days.
func (s *CustomerBriefState) BriefExpiry() int64 {
	if s == nil || s.Sealed == "" {
		return 0
	}
	if s.BriefExpiresAt != 0 {
		return s.BriefExpiresAt
	}
	return s.ExpiresAt
}

// BriefReadable reports whether the sealed brief may still be returned.
func (s *CustomerBriefState) BriefReadable(now time.Time) bool {
	expiry := s.BriefExpiry()
	return expiry != 0 && now.Unix() < expiry && !s.Expired(now)
}

// CustomerBriefRequested carries identifiers only, never customer text.
type CustomerBriefRequested struct {
	AccountID string `json:"accountId"`
	UserID    string `json:"userId"`
	RunID     string `json:"runId"`
}

// CustomerBriefSource is filled by the worker from its own inputs; Kind is
// meeting, insight, news or event.
type CustomerBriefSource struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Label string `json:"label"`
}

type CustomerBriefNextMeeting struct {
	Title    string              `json:"title"`
	StartsAt string              `json:"startsAt"`
	Source   CustomerBriefSource `json:"source"`
}

type CustomerBriefStatement struct {
	Text    string                `json:"text"`
	Sources []CustomerBriefSource `json:"sources"`
}

type CustomerBriefNews struct {
	Text    string                `json:"text"`
	Why     string                `json:"why"`
	Sources []CustomerBriefSource `json:"sources"`
}

type CustomerBriefTerm struct {
	Term         string                `json:"term"`
	Gloss        string                `json:"gloss"`
	InDictionary bool                  `json:"inDictionary"`
	Sources      []CustomerBriefSource `json:"sources"`
}

// CustomerBrief is the plain-text stored content (at most
// CustomerBriefMaxBytes encoded); nothing in it is interpreted as HTML.
type CustomerBrief struct {
	NextMeeting *CustomerBriefNextMeeting `json:"nextMeeting"`
	Situation   *CustomerBriefStatement   `json:"situation"`
	SinceLast   []CustomerBriefStatement  `json:"sinceLast"`
	News        []CustomerBriefNews       `json:"news"`
	Questions   []CustomerBriefStatement  `json:"questions"`
	Terms       []CustomerBriefTerm       `json:"terms"`
}

// CustomerBriefResponseMaxBytes bounds the encoded GET/POST response: the
// stored content (at most CustomerBriefMaxBytes) plus fixed-size run fields.
// It stays well under the 64 KiB budget of the other account reads.
const CustomerBriefResponseMaxBytes = CustomerBriefMaxBytes + 1024

// CustomerBriefRun is the requester's run state. Status is none, queued,
// running, succeeded or failed; ErrorCode is set only for failed.
type CustomerBriefRun struct {
	Status      string     `json:"status"`
	RunID       string     `json:"runId,omitempty"`
	RequestedAt *time.Time `json:"requestedAt,omitempty"`
	ErrorCode   string     `json:"errorCode,omitempty"`
}

type CustomerBriefView struct {
	GeneratedAt time.Time      `json:"generatedAt"`
	Content     *CustomerBrief `json:"content"`
}

// CustomerBriefResponse is the API shape; Brief is null when there is no
// readable brief.
type CustomerBriefResponse struct {
	Run   CustomerBriefRun   `json:"run"`
	Brief *CustomerBriefView `json:"brief"`
}
