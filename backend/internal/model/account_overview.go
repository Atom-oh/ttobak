package model

import "time"

// Account signals state values.
const (
	NewsStateOK           = "OK"
	NewsStateNoSource     = "NO_SOURCE"
	NewsStateNoRecentNews = "NO_RECENT_NEWS"
)

// Stall reason codes.
const (
	StallStageStale = "STAGE_STALE"
	StallOpenAction = "OPEN_ACTIONS"
	StallNoUpcoming = "NO_UPCOMING"
)

// Default stall thresholds (spec "Default stall signals").
const (
	StallStageAfter  = 21 * 24 * time.Hour
	StallActionAfter = 14 * 24 * time.Hour
)

// AccountSignals is the account page's AWS signals section. It is derived
// read-only data; nothing here is written back as a link.
type AccountSignals struct {
	NewsState       string              `json:"newsState"`     // NewsState*
	News            []SignalNews        `json:"news"`          // at most 10, last 30 days
	Opportunities   []SignalOppty       `json:"opportunities"` // at most 20
	Insights        []AccountInsightDTO `json:"insights"`      // at most 10, need|opportunity|risk
	RestrictedCount int                 `json:"restrictedCount"`
}

// SignalNews is one recent news item of the account's own news sources. ID
// is the link form {sourceId}:{docHash}. Candidate marks unlinked news that
// looks like a new opportunity; it is a display flag only.
type SignalNews struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	URL              string   `json:"url,omitempty"`
	PubDate          string   `json:"pubDate,omitempty"`
	AWSRelevance     string   `json:"awsRelevance,omitempty"`
	QualityScore     float64  `json:"qualityScore"`
	LinkedProjectIDs []string `json:"linkedProjectIds"`
	Candidate        bool     `json:"candidate"`
}

// SignalOppty is one project linked to the account. DaysInStage is nil when
// the stage change time is unknown (legacy rows).
type SignalOppty struct {
	ProjectID   string        `json:"projectId"`
	Name        string        `json:"name"`
	Stage       string        `json:"stage"`
	StageMapped bool          `json:"stageMapped"`
	DaysInStage *int          `json:"daysInStage,omitempty"`
	Terminal    bool          `json:"terminal"`
	Stall       []StallReason `json:"stall"`
}

// StallReason explains why an opportunity looks stalled.
type StallReason struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}
