package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func TestCropRecordsRejectReplacementAudio(t *testing.T) {
	ddb := dynamodb.New(dynamodb.Options{Region: "us-west-2", Credentials: aws.AnonymousCredentials{},
		HTTPClient: recoveryHTTPClient(func(r *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(r.Header.Get("X-Amz-Target"), "GetItem") {
				t.Fatal("crop guard must run before a write")
			}
			return recoveryResponse(200, `{"Item":{"PK":{"S":"USER#owner"},"SK":{"S":"MEETING#copy"},"userId":{"S":"owner"},"meetingId":{"S":"copy"},"status":{"S":"transcribing"},"audioCrop":{"M":{"state":{"S":"processing"}}}}}`), nil
		})})
	s3Client := s3.New(s3.Options{Region: "us-west-2", Credentials: aws.AnonymousCredentials{},
		HTTPClient: recoveryHTTPClient(func(*http.Request) (*http.Response, error) { t.Fatal("crop guard must not upload"); return nil, nil })})
	uploads := NewUploadService(s3Client, repository.NewDynamoDBRepository(ddb, "test"), "bucket", nil)
	_, err := uploads.GeneratePresignedUploadURL(context.Background(), "owner", &model.PresignedURLRequest{Category: "audio", MeetingID: "copy", FileName: "new.webm", FileType: "audio/webm"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("presign: %v", err)
	}
	err = uploads.CompleteUpload(context.Background(), "owner", &model.UploadCompleteRequest{Category: "audio", MeetingID: "copy", Key: "audio/owner/copy/new.webm"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("complete: %v", err)
	}
}

func TestCropRecordsCannotRecoverCheckpointsOrRediarize(t *testing.T) {
	meeting := &model.Meeting{UserID: "owner", MeetingID: "copy", Status: model.StatusError, AudioCrop: &model.AudioCrop{State: "failed"}}
	if CanRecoverRecording(meeting) {
		t.Fatal("crop is not an interrupted browser recording")
	}
	meeting.AudioKey = "audio/owner/copy/crop_result.wav"
	if _, err := validateRediarizeEligibility(meeting, "owner", 2); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("rediarize: %v", err)
	}
}
