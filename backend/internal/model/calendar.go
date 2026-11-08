package model

import "time"

const (
	PrefixCalendarEvent  = "CALEVENT#"
	PrefixCalendarTime   = "CALTIME#"
	PrefixCalendarSource = "CALSOURCE#"
	PrefixCalendarBatch  = "CALBATCH#"
)

type CalendarEvent struct {
	EventID   string    `json:"eventId"`
	SourceID  string    `json:"sourceId"`
	Provider  string    `json:"provider"`
	Title     string    `json:"title"`
	StartsAt  time.Time `json:"startsAt"`
	EndsAt    time.Time `json:"endsAt"`
	TimeZone  string    `json:"timeZone"`
	IsAllDay  bool      `json:"isAllDay"`
	StartDate string    `json:"startDate,omitempty"`
	EndDate   string    `json:"endDate,omitempty"`
	Location  string    `json:"location,omitempty"`
	AccountID string    `json:"accountId,omitempty"`
}

type CalendarEventInput struct {
	ExternalID string    `json:"externalId"`
	Title      string    `json:"title"`
	StartsAt   time.Time `json:"startsAt"`
	EndsAt     time.Time `json:"endsAt"`
	TimeZone   string    `json:"timeZone"`
	IsAllDay   bool      `json:"isAllDay"`
	StartDate  string    `json:"startDate,omitempty"`
	EndDate    string    `json:"endDate,omitempty"`
	Location   string    `json:"location,omitempty"`
	AccountID  string    `json:"accountId,omitempty"`
}

type CalendarMutation struct {
	Op         string              `json:"op"`
	Event      *CalendarEventInput `json:"event,omitempty"`
	ExternalID string              `json:"externalId,omitempty"`
}

type CalendarSyncRequest struct {
	BatchID          string             `json:"batchId"`
	ExpectedRevision int64              `json:"expectedRevision"`
	WindowStart      time.Time          `json:"windowStart"`
	WindowEnd        time.Time          `json:"windowEnd"`
	Complete         bool               `json:"complete"`
	Mutations        []CalendarMutation `json:"mutations"`
}

type CalendarSyncReceipt struct {
	BatchID   string `json:"batchId" dynamodbav:"batchId"`
	Revision  int64  `json:"revision" dynamodbav:"revision"`
	Upserted  int    `json:"upserted" dynamodbav:"upserted"`
	Removed   int    `json:"removed" dynamodbav:"removed"`
	Completed bool   `json:"completed" dynamodbav:"completed"`
}

type CalendarSourceStatus struct {
	SourceID              string     `json:"sourceId" dynamodbav:"sourceId"`
	Provider              string     `json:"provider" dynamodbav:"provider"`
	Revision              int64      `json:"revision" dynamodbav:"revision"`
	LastAttemptAt         time.Time  `json:"lastAttemptAt" dynamodbav:"lastAttemptAt"`
	LastCompletedAt       *time.Time `json:"lastCompletedAt,omitempty" dynamodbav:"lastCompletedAt,omitempty"`
	LastCompletedRevision *int64     `json:"lastCompletedRevision,omitempty" dynamodbav:"lastCompletedRevision,omitempty"`
	CompletedWindowStart  *time.Time `json:"completedWindowStart,omitempty" dynamodbav:"completedWindowStart,omitempty"`
	CompletedWindowEnd    *time.Time `json:"completedWindowEnd,omitempty" dynamodbav:"completedWindowEnd,omitempty"`
}

type CalendarEventPage struct {
	Events     []CalendarEvent `json:"events"`
	NextCursor *string         `json:"nextCursor"`
}

type CalendarSourcePage struct {
	Sources    []CalendarSourceStatus `json:"sources"`
	NextCursor *string                `json:"nextCursor"`
}

type CalendarQuery struct {
	From      time.Time
	To        time.Time
	AccountID string
	Limit     int32
	Cursor    string
}

func CalendarEventSortKey(sourceID, eventID string) string {
	return PrefixCalendarEvent + sourceID + "#" + eventID
}

func CalendarTimeSortKey(startsAt time.Time, sourceID, eventID string) string {
	return PrefixCalendarTime + startsAt.UTC().Format("2006-01-02T15:04:05.000000000Z") + "#" + sourceID + "#" + eventID
}

func CalendarSourceSortKey(sourceID string) string {
	return PrefixCalendarSource + sourceID
}

func CalendarBatchSortKey(sourceID, batchID string) string {
	return PrefixCalendarBatch + sourceID + "#" + batchID
}
