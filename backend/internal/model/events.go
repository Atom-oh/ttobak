package model

// EventBridgeS3Event represents an S3 event delivered via EventBridge
// This is different from the direct S3 notification (events.S3Event)
type EventBridgeS3Event struct {
	Version    string                   `json:"version"`
	Source     string                   `json:"source"`
	DetailType string                   `json:"detail-type"`
	Detail     EventBridgeS3EventDetail `json:"detail"`
}

// EventBridgeS3EventDetail contains the S3 event detail
type EventBridgeS3EventDetail struct {
	Bucket EventBridgeS3Bucket `json:"bucket"`
	Object EventBridgeS3Object `json:"object"`
}

// EventBridgeS3Bucket represents the bucket info in an EventBridge S3 event
type EventBridgeS3Bucket struct {
	Name string `json:"name"`
}

// EventBridgeS3Object represents the object info in an EventBridge S3 event
type EventBridgeS3Object struct {
	Key  string `json:"key"`
	Size int64  `json:"size"`
	ETag string `json:"etag"`
}
