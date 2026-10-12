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

func TestKBDeleteFileMatchesExactNamesAcrossPages(t *testing.T) {
	page := func(truncated bool, token string, keys ...string) string {
		body := `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`
		body += fmt.Sprintf(`<IsTruncated>%t</IsTruncated>`, truncated)
		if token != "" {
			body += `<NextContinuationToken>` + token + `</NextContinuationToken>`
		}
		for _, key := range keys {
			body += `<Contents><Key>` + key + `</Key><Size>1</Size></Contents>`
		}
		return body + `</ListBucketResult>`
	}
	cases := []struct {
		name    string
		fileID  string
		pages   []string
		deleted string
		want    error
	}{
		{
			name:    "suffix collision deletes only the exact name",
			fileID:  "report.pdf",
			pages:   []string{page(false, "", "kb/owner/2026-report.pdf", "kb/owner/report.pdf", "kb/owner/old_report.pdf")},
			deleted: "kb/owner/report.pdf",
		},
		{
			name:    "match on a later page is found",
			fileID:  "1790_deck.pdf",
			pages:   []string{page(true, "p2", "kb/owner/a.pdf"), page(false, "", "kb/owner/1790_deck.pdf")},
			deleted: "kb/owner/1790_deck.pdf",
		},
		{
			name:   "two objects with the same file name are refused",
			fileID: "notes.md",
			pages:  []string{page(true, "p2", "kb/owner/notes.md"), page(false, "", "kb/owner/meetings/notes.md")},
			want:   ErrKBFileAmbiguous,
		},
		{name: "missing file", fileID: "gone.pdf", pages: []string{page(false, "", "kb/owner/gone.pdf.bak")}, want: ErrNotFound},
		{name: "path separator", fileID: "../other/secret.pdf", want: ErrInvalidInput},
		{name: "backslash", fileID: `a\b.pdf`, want: ErrInvalidInput},
		{name: "dot dot", fileID: "..", want: ErrInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lists, deletes := 0, []string{}
			client := s3.New(s3.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
				HTTPClient: indexProviderHTTP(func(r *http.Request) (*http.Response, error) {
					if r.Method == http.MethodDelete {
						deletes = append(deletes, strings.TrimPrefix(r.URL.Path, "/"))
						return indexHTTPResponse(204, "", nil), nil
					}
					if q := r.URL.Query(); q.Get("prefix") != "kb/owner/" || lists >= len(tc.pages) {
						t.Fatalf("unexpected list request %s", r.URL)
					}
					lists++
					return indexHTTPResponse(200, tc.pages[lists-1], nil), nil
				}),
			})
			err := NewKBService(client, nil, "knowledge", "", "").DeleteFile(context.Background(), "owner", tc.fileID)
			if tc.want != nil {
				if !errors.Is(err, tc.want) || len(deletes) != 0 {
					t.Fatalf("err=%v deletes=%v, want %v and no delete", err, deletes, tc.want)
				}
				return
			}
			if err != nil || len(deletes) != 1 || !strings.HasSuffix(deletes[0], tc.deleted) || lists != len(tc.pages) {
				t.Fatalf("err=%v deletes=%v lists=%d, want only %s after %d pages", err, deletes, lists, tc.deleted, len(tc.pages))
			}
		})
	}
}
