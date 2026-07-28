package service

import (
	"crypto/rand"
	"crypto/rsa"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// newTestPresignClient builds a presign client with static fake credentials —
// presigning is pure local crypto, no network involved.
func newTestPresignClient(t *testing.T) *s3.PresignClient {
	t.Helper()
	client := s3.New(s3.Options{
		Region:      "ap-northeast-2",
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider("AKID", "SECRET", "")),
	})
	return s3.NewPresignClient(client)
}

func TestSignedURL(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer := newCloudFrontSignerWithKey("https://ttobak.example.com/media/", "KTESTKEYPAIRID", key)

	got, err := signer.SignedURL("docs/u1/1_a b.pptx", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "https://ttobak.example.com/media/docs/u1/1_a%20b.pptx?") {
		t.Errorf("unexpected URL prefix: %s", got)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("Key-Pair-Id") != "KTESTKEYPAIRID" {
		t.Errorf("Key-Pair-Id = %q", q.Get("Key-Pair-Id"))
	}
	if q.Get("Expires") == "" || q.Get("Signature") == "" {
		t.Errorf("missing Expires/Signature params: %s", got)
	}
}

// Guard: without a CloudFront signer, download URLs stay S3 presigns.
func TestGeneratePresignedDownloadURLWithoutSigner(t *testing.T) {
	svc := &UploadService{
		presignClient: newTestPresignClient(t),
		bucketName:    "test-bucket",
	}
	got, err := svc.GeneratePresignedDownloadURL(t.Context(), "audio/u1/m1/a.webm")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "test-bucket") || !strings.Contains(got, "audio/u1/m1/a.webm") {
		t.Errorf("expected S3 presigned URL, got %s", got)
	}
}
