package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestKBListFilesPaginatesAndPropagatesPageFailures(t *testing.T) {
	for _, failPage := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("failure-page-%d", failPage), func(t *testing.T) {
			calls := 0
			client := s3.New(s3.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
				HTTPClient: indexProviderHTTP(func(r *http.Request) (*http.Response, error) {
					calls++
					q := r.URL.Query()
					if q.Get("prefix") != "kb/owner/" || q.Get("list-type") != "2" {
						t.Fatalf("wrong KB list scope: %s", r.URL)
					}
					if calls == failPage {
						return indexHTTPResponse(403, `<Error><Code>AccessDenied</Code><Message>synthetic page failure</Message></Error>`, nil), nil
					}
					switch calls {
					case 1:
						if q.Get("continuation-token") != "" {
							t.Fatal("unexpected initial cursor")
						}
						return indexHTTPResponse(200, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><IsTruncated>true</IsTruncated><NextContinuationToken>next+/=</NextContinuationToken><Contents><Key>kb/owner/first.pdf</Key><Size>12</Size></Contents></ListBucketResult>`, nil), nil
					case 2:
						if q.Get("continuation-token") != "next+/=" {
							t.Fatalf("second page cursor lost: %s", r.URL)
						}
						return indexHTTPResponse(200, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><IsTruncated>false</IsTruncated><Contents><Key>kb/owner/second.md</Key><Size>34</Size></Contents></ListBucketResult>`, nil), nil
					default:
						t.Fatal("KB listing did not stop at the final page")
						return nil, nil
					}
				}),
			})
			result, err := NewKBService(client, nil, "knowledge", "", "").ListFiles(context.Background(), "owner")
			if failPage != 0 {
				if err == nil || result != nil || calls != failPage || !strings.Contains(err.Error(), "AccessDenied") {
					t.Fatalf("page failure reported partial success: result=%+v calls=%d err=%v", result, calls, err)
				}
				return
			}
			if err != nil || calls != 2 || len(result.Files) != 2 ||
				result.Files[0].FileID != "first.pdf" || result.Files[1].FileID != "second.md" ||
				result.Files[1].Size != 34 || result.Files[1].FileType != "text/markdown" {
				t.Fatalf("missing second KB page: result=%+v calls=%d err=%v", result, calls, err)
			}
		})
	}
}

func TestValidateAssetOwnership(t *testing.T) {
	const me = "user-123"
	cases := []struct {
		name      string
		sourceKey string
		userID    string
		wantErr   bool
	}{
		{"owned file", "files/user-123/meeting-1/deck.pdf", me, false},
		{"owned audio", "audio/user-123/meeting-1/clip.m4a", me, false},
		{"owned image", "images/user-123/meeting-1/shot.png", me, false},
		{"other user's file (IDOR)", "files/other-user/meeting-1/secret.pdf", me, true},
		{"other user's audio (IDOR)", "audio/other-user/meeting-1/secret.m4a", me, true},
		{"prefix-confusion not-quite-owner", "files/user-1234/meeting-1/x.pdf", me, true},
		{"path traversal", "files/user-123/../other-user/secret.pdf", me, true},
		{"unknown prefix", "kb/user-123/x.pdf", me, true},
		{"empty userID", "files/user-123/meeting-1/deck.pdf", "", true},
		{"empty source key", "", me, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAssetOwnership(tc.sourceKey, tc.userID)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !errors.Is(err, ErrForbidden) {
					t.Fatalf("expected ErrForbidden, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}
