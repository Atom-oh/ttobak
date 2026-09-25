package model

type AudioCrop struct {
	SourceMeetingID string `dynamodbav:"sourceMeetingId" json:"sourceMeetingId"`
	SourceKey       string `dynamodbav:"sourceKey" json:"-"`
	SourceETag      string `dynamodbav:"sourceETag" json:"-"`
	StartSeconds    int    `dynamodbav:"startSeconds" json:"startSeconds"`
	EndSeconds      int    `dynamodbav:"endSeconds" json:"endSeconds"`
	State           string `dynamodbav:"state" json:"state"`
	RunID           string `dynamodbav:"runId,omitempty" json:"-"`
	ResultKey       string `dynamodbav:"resultKey,omitempty" json:"-"`
}

func (crop *AudioCrop) Active() bool {
	return crop != nil && (crop.State == "queued" || crop.State == "processing")
}

type AudioCropRequest struct {
	RequestID    string `json:"requestId"`
	StartSeconds int    `json:"startSeconds"`
	EndSeconds   int    `json:"endSeconds"`
}

func (crop *AudioCrop) QueuedFor(status string) bool {
	return crop != nil && crop.State == "queued" && status == StatusTranscribing
}
