package service

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func newKBUploadService(members map[string]bool) *KBService {
	client := s3.New(s3.Options{
		Region:      "ap-northeast-2",
		Credentials: aws.NewCredentialsCache(staticKBCreds{}),
	})
	svc := NewKBService(client, nil, "knowledge", "", "")
	svc.SetMembershipReader(func(_ context.Context, accountID, userID string) (bool, error) {
		return members[accountID+"/"+userID], nil
	})
	return svc
}

type staticKBCreds struct{}

func (staticKBCreds) Retrieve(context.Context) (aws.Credentials, error) {
	return aws.Credentials{AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "secret"}, nil
}

func TestKBUploadRejectsForeignAccount(t *testing.T) {
	svc := newKBUploadService(map[string]bool{"acct-a/owner": true})
	for _, id := range []string{"acct-b", "a#b", "a/b"} {
		_, err := svc.GetPresignedURL(context.Background(), "owner", "a.pdf", "application/pdf", id)
		if !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrKBAccountInvalid) {
			t.Fatalf("account %q: want forbidden or invalid, got %v", id, err)
		}
	}
	_, err := svc.GetPresignedURL(context.Background(), "owner", "a.pdf", "application/pdf", "acct-b")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-member: want ErrForbidden, got %v", err)
	}
	_, err = svc.GetPresignedURL(context.Background(), "owner", "a.pdf", "application/pdf", "a#b")
	if !errors.Is(err, ErrKBAccountInvalid) {
		t.Fatalf("malformed id: want ErrKBAccountInvalid, got %v", err)
	}
	// No reader configured refuses every Account.
	bare := NewKBService(svcClient(), nil, "knowledge", "", "")
	if _, err := bare.GetPresignedURL(context.Background(), "owner", "a.pdf", "application/pdf", "acct-a"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("no reader: want ErrForbidden, got %v", err)
	}
}

func svcClient() *s3.Client {
	return s3.New(s3.Options{Region: "ap-northeast-2", Credentials: aws.NewCredentialsCache(staticKBCreds{})})
}

func TestKBUploadSignsAccountMetadata(t *testing.T) {
	svc := newKBUploadService(map[string]bool{"acct-a/owner": true})
	got, err := svc.GetPresignedURL(context.Background(), "owner", "a.pdf", "application/pdf", "acct-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Headers["x-amz-meta-account-id"] != "acct-a" {
		t.Fatalf("headers = %v", got.Headers)
	}
	u, err := url.Parse(got.UploadURL)
	if err != nil {
		t.Fatal(err)
	}
	signed := u.Query().Get("X-Amz-SignedHeaders")
	if !strings.Contains(signed, "x-amz-meta-account-id") {
		t.Fatalf("metadata header is not signed: %q", signed)
	}

	personal, err := svc.GetPresignedURL(context.Background(), "owner", "a.pdf", "application/pdf", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(personal.Headers) != 0 || strings.Contains(personal.UploadURL, "x-amz-meta") {
		t.Fatalf("personal upload must carry no metadata: %v %s", personal.Headers, personal.UploadURL)
	}
}
