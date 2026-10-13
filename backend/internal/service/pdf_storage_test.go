package service

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func pdfStorageFixture(t *testing.T, failPut, failSign, failDelete bool) (*UploadService, *[]string) {
	t.Helper()
	var calls []string
	storage := s3.NewFromConfig(aws.Config{
		Region: "ap-northeast-2", Retryer: func() aws.Retryer { return aws.NopRetryer{} },
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture"}, nil
		}),
		HTTPClient: noteSourceHTTPClient(func(r *http.Request) (*http.Response, error) {
			calls = append(calls, r.Method+" "+r.URL.Path)
			status, body := 200, ""
			if r.Method == "PUT" {
				data, err := io.ReadAll(r.Body)
				if err != nil || string(data) != "%PDF-synthetic" ||
					r.Header.Get("Content-Type") != "application/pdf" ||
					r.Header.Get("Cache-Control") != "private, no-store" ||
					r.Header.Get("X-Amz-Tagging") != "ttobak-artifact=meeting-pdf-export" {
					t.Error("stored artifact lost its bytes, type or retention identity")
				}
				kind, params, err := mime.ParseMediaType(r.Header.Get("Content-Disposition"))
				if err != nil || kind != "attachment" || params["filename"] != "한국어 회의.pdf" {
					t.Error("Unicode download filename was corrupted")
				}
			}
			if (failPut && r.Method == "PUT") || (failDelete && r.Method == "DELETE") {
				status, body = 500, "<Error><Code>InternalError</Code><Message>SYNTHETIC_PRIVATE_DETAIL</Message></Error>"
			}
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
	})
	svc := NewUploadService(storage, nil, "test-bucket")
	if failSign {
		svc.presignClient = s3.NewPresignClient(s3.NewFromConfig(aws.Config{
			Region: "ap-northeast-2",
			Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{}, errors.New("SYNTHETIC_PRIVATE_CREDENTIAL_DETAIL")
			}),
		}))
	}
	return svc, &calls
}

func TestPDFStorageUsesFreshCallerKeysAndBoundedDownload(t *testing.T) {
	svc, calls := pdfStorageFixture(t, false, false, false)
	seen := map[string]bool{}
	for range 2 {
		result, err := svc.StorePDFExport(t.Context(), "reader", "shared-meeting", "한국어 회의.pdf", []byte("%PDF-synthetic"))
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(result)
		if err != nil || u.Query().Get("X-Amz-Expires") != "300" ||
			!strings.HasPrefix(u.Path, "/files/reader/shared-meeting/export_") || !strings.HasSuffix(u.Path, ".pdf") ||
			seen[u.Path] {
			t.Fatal("download capability lost lifetime, ownership or fresh identity")
		}
		seen[u.Path] = true
	}
	if len(*calls) != 2 {
		t.Fatal("successful export performed unexpected network operations")
	}
}

func TestPDFStorageFailuresNeverPublishCapabilitiesAndCleanOnlyTheirNewKey(t *testing.T) {
	for _, tc := range []struct{ put, sign, cleanup bool }{{true, false, false}, {false, true, false}, {false, true, true}} {
		svc, calls := pdfStorageFixture(t, tc.put, tc.sign, tc.cleanup)
		result, err := svc.StorePDFExport(t.Context(), "reader", "shared-meeting", "한국어 회의.pdf", []byte("%PDF-synthetic"))
		if err == nil || result != "" || strings.Contains(err.Error(), "SYNTHETIC_PRIVATE") {
			t.Fatal("failed export published a capability or exposed provider detail")
		}
		if tc.put {
			if len(*calls) != 1 {
				t.Fatal("failed upload deleted an unconfirmed object")
			}
		} else if len(*calls) != 2 || strings.TrimPrefix((*calls)[0], "PUT ") != strings.TrimPrefix((*calls)[1], "DELETE ") {
			t.Fatal("signing failure did not clean its exact new object")
		}
		if tc.cleanup && !strings.Contains(err.Error(), "cleanup failed") {
			t.Fatal("cleanup failure was hidden")
		}
	}
}

func TestPDFStorageRejectsTraversalBeforeWriting(t *testing.T) {
	svc, calls := pdfStorageFixture(t, false, false, false)
	for _, id := range []string{"", ".", "..", "foreign/meeting", "foreign\\meeting", "%2e%2e", "bad\nid", strings.Repeat("a", 129)} {
		if _, err := svc.StorePDFExport(t.Context(), "reader", id, "한국어 회의.pdf", []byte("%PDF-synthetic")); err == nil {
			t.Fatal("unsafe identifier accepted")
		}
	}
	if len(*calls) != 0 {
		t.Fatal("invalid identifier reached storage")
	}
}
