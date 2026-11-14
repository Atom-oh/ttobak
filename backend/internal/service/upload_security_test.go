package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func TestCompleteUploadBindsOnlyOwnedMeetingAssets(t *testing.T) {
	cases := []struct {
		name, category, key string
		totalParts          int
		valid               bool
	}{
		{"audio", "audio", "audio/owner/meeting/녹음.webm", 0, true},
		{"multipart audio", "audio", "audio/owner/meeting/part_000_녹음.webm", 2, true},
		{"image", "image", "images/owner/meeting/사진.png", 0, true},
		{"audio foreign document", "audio", "docs/victim/private.pdf", 0, false},
		{"multipart foreign audio", "audio", "audio/victim/meeting/part.webm", 2, false},
		{"image foreign document", "image", "docs/victim/private.pdf", 0, false},
		{"image foreign user", "image", "images/victim/meeting/photo.png", 0, false},
		{"other meeting", "audio", "audio/owner/other/recording.webm", 0, false},
		{"wrong category", "image", "files/owner/meeting/file.pdf", 0, false},
		{"traversal", "audio", "audio/owner/meeting/../private.webm", 0, false},
		{"encoded traversal", "image", "images/owner/meeting/%252e%252e%252fprivate.png", 0, false},
		{"empty filename", "image", "images/owner/meeting/", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writes := 0
			transport := noteSourceHTTPClient(func(req *http.Request) (*http.Response, error) {
				if strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".GetItem") {
					return attachmentWireResponse(map[string]interface{}{"Item": attachmentWireItem(t, &model.Meeting{UserID: "owner", MeetingID: "meeting"})}), nil
				}
				writes++
				return attachmentWireResponse(map[string]interface{}{}), nil
			})
			cfg := aws.Config{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, HTTPClient: transport}
			storage := s3.NewFromConfig(cfg)
			repo := repository.NewDynamoDBRepositoryWithS3(dynamodb.NewFromConfig(cfg), "table", storage, "bucket")
			upload := NewUploadService(storage, repo, "bucket")
			err := upload.CompleteUpload(context.Background(), "owner", &model.UploadCompleteRequest{
				MeetingID: "meeting", Category: tc.category, Key: tc.key, TotalParts: tc.totalParts,
			})
			if tc.valid {
				if err != nil || writes == 0 {
					t.Fatalf("valid upload rejected: err=%v writes=%d", err, writes)
				}
			} else if !errors.Is(err, ErrInvalidInput) || writes != 0 {
				t.Fatalf("invalid upload was not rejected before writing: err=%v writes=%d", err, writes)
			}
		})
	}
}

func TestDownloadSignerRejectsUnsafePaths(t *testing.T) {
	upload := &UploadService{presignClient: newTestPresignClient(t), bucketName: "test-bucket"}
	for _, key := range []string{
		"docs/owner/../victim/private.pdf",
		"docs/owner/%252e%252e%252fvictim/private.pdf",
		"audio/owner/meeting/../victim.webm",
		"docs/owner/control\t.pdf",
		"docs/owner/control%257f.pdf",
		"docs/owner/invalid\xff.pdf",
		"docs/owner/invalid%ff.pdf",
	} {
		t.Run(key, func(t *testing.T) {
			if url, err := upload.GeneratePresignedDownloadURL(context.Background(), key); err == nil || url != "" {
				t.Fatalf("unsafe key was signed: url=%q err=%v", url, err)
			}
		})
	}
}

func TestDocumentOwnershipRejectsTraversal(t *testing.T) {
	for _, key := range []string{"docs/owner/../victim/private.pdf", "docs/owner/%252e%252e%252fvictim/private.pdf"} {
		if err := validateFileKeyOwnership("owner", key); !errors.Is(err, ErrForbidden) {
			t.Fatalf("unsafe document key %q accepted: %v", key, err)
		}
	}
	if err := validateFileKeyOwnership("owner", "docs/owner/고객 자료 50%.pdf"); err != nil {
		t.Fatalf("legitimate document rejected: %v", err)
	}
}

func TestPresignedUploadRejectsUnsafeNamesAndMeetingIDs(t *testing.T) {
	client := s3.NewFromConfig(aws.Config{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}})
	upload := NewUploadService(client, nil, "bucket")
	for _, req := range []model.PresignedURLRequest{
		{Category: "image", MeetingID: "other/meeting", FileName: "photo.png", FileType: "image/png"},
		{Category: "image", MeetingID: "meeting", FileName: "%252fphoto.png", FileType: "image/png"},
		{Category: "doc", FileName: "%2fprivate.pdf", FileType: "application/pdf"},
	} {
		if _, err := upload.GeneratePresignedUploadURL(context.Background(), "owner", &req); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("unsafe upload accepted: request=%+v err=%v", req, err)
		}
	}
}

func TestDownloadSigningProtectsBothBackends(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, useCloudFront := range []bool{false, true} {
		t.Run(fmt.Sprintf("cloudfront=%t", useCloudFront), func(t *testing.T) {
			upload := &UploadService{presignClient: newTestPresignClient(t), bucketName: "test-bucket"}
			if useCloudFront {
				upload.SetCloudFrontSigner(newCloudFrontSignerWithKey("https://example.com/media", "test", key))
			}
			for _, valid := range []string{
				"docs/owner/고객 자료.pdf", "docs-pdf/owner/deck.pptx.pdf", "files/owner/meeting/sim/chart.png",
				"audio-playback/owner/meeting/recording.webm",
				"skillpkg/org/" + strings.Repeat("a", 64) + ".zip",
				"skillpkg/user/owner/" + strings.Repeat("b", 64) + ".zip",
			} {
				if url, err := upload.GeneratePresignedDownloadURL(context.Background(), valid); err != nil || url == "" {
					t.Fatalf("valid key rejected: key=%q err=%v", valid, err)
				}
			}
			for _, invalid := range []string{"docs/owner/../victim/private.pdf", "docs/owner/%252e%252e%252fvictim.pdf", "transcripts/meeting.txt"} {
				if url, err := upload.GeneratePresignedDownloadURL(context.Background(), invalid); err == nil || url != "" {
					t.Fatalf("unsafe key signed: key=%q err=%v", invalid, err)
				}
			}
		})
	}
}
