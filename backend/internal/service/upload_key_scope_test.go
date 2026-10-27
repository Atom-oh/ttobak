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

func TestValidMeetingUploadKey(t *testing.T) {
	tests := []struct {
		name, category, key string
		want                bool
	}{
		{"audio single", "audio", "audio/owner/m1/1700000000000_rec.webm", true},
		{"audio part", "audio", "audio/owner/m1/part_002_1700000000000_rec.webm", true},
		{"audio checkpoint", "audio", "audio/owner/m1/recording_progress.webm", true},
		{"image", "image", "images/owner/m1/1700000000000_photo.png", true},
		{"literal percent name", "image", "images/owner/m1/1700000000000_50%off.png", true},
		{"other user", "audio", "audio/victim/m1/rec.webm", false},
		{"other meeting", "audio", "audio/owner/m2/rec.webm", false},
		{"wrong category prefix", "image", "audio/owner/m1/photo.png", false},
		{"files prefix as image", "image", "files/owner/m1/photo.png", false},
		{"nested segment", "audio", "audio/owner/m1/x/rec.webm", false},
		{"traversal", "audio", "audio/owner/m1/../../victim/m9/rec.webm", false},
		{"encoded traversal", "audio", "audio/owner/m1/%2e%2e", false},
		{"encoded slash", "image", "images/owner/m1/a%2fb.png", false},
		{"empty name", "audio", "audio/owner/m1/", false},
		{"unsupported category", "file", "files/owner/m1/doc.pdf", false},
		{"oversized", "audio", "audio/owner/m1/" + strings.Repeat("a", 1024), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validMeetingUploadKey(tt.category, "owner", "m1", tt.key); got != tt.want {
				t.Fatalf("validMeetingUploadKey(%q, %q) = %v, want %v", tt.category, tt.key, got, tt.want)
			}
		})
	}
}

func TestCompleteUploadRejectsForeignKeysBeforeWriting(t *testing.T) {
	for _, req := range []model.UploadCompleteRequest{
		{Category: "audio", MeetingID: "mine", Key: "audio/victim/theirs/1700000000000_rec.webm"},
		{Category: "audio", MeetingID: "mine", Key: "audio/victim/theirs/part_000_rec.webm", PartIndex: 0, TotalParts: 2},
		{Category: "image", MeetingID: "mine", Key: "images/victim/theirs/1700000000000_photo.png"},
	} {
		t.Run(req.Category+"/"+req.Key, func(t *testing.T) {
			ddb := dynamodb.New(dynamodb.Options{Region: "us-west-2", Credentials: aws.AnonymousCredentials{},
				HTTPClient: recoveryHTTPClient(func(r *http.Request) (*http.Response, error) {
					if !strings.HasSuffix(r.Header.Get("X-Amz-Target"), "GetItem") {
						t.Fatalf("foreign key reached %s", r.Header.Get("X-Amz-Target"))
					}
					return recoveryResponse(200, `{"Item":{"PK":{"S":"USER#owner"},"SK":{"S":"MEETING#mine"},"userId":{"S":"owner"},"meetingId":{"S":"mine"},"status":{"S":"recording"}}}`), nil
				})})
			s3Client := s3.New(s3.Options{Region: "us-west-2", Credentials: aws.AnonymousCredentials{},
				HTTPClient: recoveryHTTPClient(func(*http.Request) (*http.Response, error) {
					t.Fatal("foreign key must not touch S3")
					return nil, nil
				})})
			uploads := NewUploadService(s3Client, repository.NewDynamoDBRepository(ddb, "test"), "bucket", nil)
			if err := uploads.CompleteUpload(context.Background(), "owner", &req); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("CompleteUpload = %v, want ErrInvalidInput", err)
			}
		})
	}
}
