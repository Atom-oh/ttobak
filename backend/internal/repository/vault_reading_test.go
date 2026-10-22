package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestVaultReadingAttachmentQueryHasCurrentBoundedMetadataProjection(t *testing.T) {
	calls := 0
	client := dynamodb.New(dynamodb.Options{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1, HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		var input struct {
			ConsistentRead           bool
			Limit                    int
			ExclusiveStartKey        map[string]map[string]string
			ExpressionAttributeNames map[string]string
		}
		if json.NewDecoder(req.Body).Decode(&input) != nil || !input.ConsistentRead || input.Limit != 10 || input.ExclusiveStartKey["PK"]["S"] != "MEETING#m" || input.ExclusiveStartKey["SK"]["S"] != "ATTACH#a" {
			t.Fatal("unbounded/foreign attachment query")
		}
		for _, name := range input.ExpressionAttributeNames {
			if name == "originalKey" || name == "processedContent" {
				t.Fatal("attachment reading hydrated unrelated private body/key")
			}
		}
		body := `{"Items":[],"LastEvaluatedKey":{"PK":{"S":"MEETING#m"},"SK":{"S":"ATTACH#b"}}}`
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	repo := NewDynamoDBRepository(client, "vault")
	page, err := repo.QueryVaultAttachments(context.Background(), "m", "ATTACH#a")
	if err != nil || calls != 1 || page.NextKey != "ATTACH#b" {
		t.Fatal("empty attachment window lost continuation", err)
	}
}
func TestVaultReadingTranscriptValidatesOwnershipAndRejectsOversizeWithoutClipping(t *testing.T) {
	calls := 0
	client := s3.New(s3.Options{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1, HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if !strings.HasSuffix(req.URL.Path, "/transcripts/m/transcriptA.txt") {
			t.Fatal("unexpected source path")
		}
		response := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("현재 원문😀")), ContentLength: int64(len("현재 원문😀"))}
		if calls == 2 {
			response.Header.Set("Content-Length", "67108865")
			response.ContentLength = MaxVaultTranscriptSourceBytes + 1
			response.Body = io.NopCloser(strings.NewReader(""))
		}
		return response, nil
	})})
	repo := NewDynamoDBRepositoryWithS3(nil, "vault", client, "fixture-bucket")
	text, err := repo.ReadVaultTranscriptField(context.Background(), "m", "transcriptA", "s3://fixture-bucket/transcripts/m/transcriptA.txt")
	if err != nil || text != "현재 원문😀" {
		t.Fatal("valid source lost", err)
	}
	if text, err = repo.ReadVaultTranscriptField(context.Background(), "m", "transcriptA", "s3://fixture-bucket/transcripts/m/transcriptA.txt"); text != "" || !errors.Is(err, ErrVaultSourceTooLarge) {
		t.Fatal("oversize source was clipped", err)
	}
	for _, value := range []string{"s3://foreign/transcripts/m/transcriptA.txt", "s3://fixture-bucket/transcripts/other/transcriptA.txt", "s3://fixture-bucket/transcripts/m/transcriptA.txt/../private"} {
		if _, err := repo.ReadVaultTranscriptField(context.Background(), "m", "transcriptA", value); err == nil {
			t.Fatal("foreign/traversal source accepted")
		}
	}
	if calls != 2 {
		t.Fatal("invalid key reached S3")
	}
}

// Regression: an accepted source must fit the 256 MiB API after markdown,
// Unicode pagination and the independent freshness read amplify its size.
func TestVaultReadingTranscriptSourceMemoryBoundary(t *testing.T) {
	for _, tc := range []struct {
		name     string
		text     string
		tooLarge bool
	}{
		{"exact_ascii", strings.Repeat("a", 1*1024*1024), false},
		{"over_ascii", strings.Repeat("a", 1*1024*1024+1), true},
		{"exact_unicode", strings.Repeat("😀", 256*1024), false},
		{"over_unicode", strings.Repeat("😀", 256*1024+1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := NewDynamoDBRepository(nil, "vault")
			got, err := repo.ReadVaultTranscriptField(context.Background(), "m", "transcriptA", tc.text)
			if tc.tooLarge {
				if !errors.Is(err, ErrVaultSourceTooLarge) || got != "" {
					t.Fatalf("oversized source returned %d bytes, err=%v", len(got), err)
				}
			} else if err != nil || got != tc.text {
				t.Fatalf("boundary source was rejected or clipped: bytes=%d err=%v", len(got), err)
			}
		})
	}
}

type vaultSourceReadCounter struct {
	reader *strings.Reader
	read   int
	closed bool
}

func (r *vaultSourceReadCounter) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += n
	return n, err
}

func (r *vaultSourceReadCounter) Close() error {
	r.closed = true
	return nil
}

func TestVaultReadingTranscriptRejectsOversizeBeforeUnboundedS3Read(t *testing.T) {
	for _, advertised := range []bool{true, false} {
		name := "unknown_length"
		if advertised {
			name = "advertised_length"
		}
		t.Run(name, func(t *testing.T) {
			body := &vaultSourceReadCounter{reader: strings.NewReader(strings.Repeat("a", 2*1024*1024))}
			client := s3.New(s3.Options{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1, HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
				if !strings.HasSuffix(req.URL.Path, "/transcripts/m/transcriptA.txt") {
					t.Fatal("unexpected source path")
				}
				response := &http.Response{StatusCode: 200, Header: http.Header{}, Body: body, ContentLength: -1}
				if advertised {
					response.Header.Set("Content-Length", "2097152")
					response.ContentLength = 9 * 1024 * 1024
				}
				return response, nil
			})})
			repo := NewDynamoDBRepositoryWithS3(nil, "vault", client, "fixture-bucket")
			got, err := repo.ReadVaultTranscriptField(context.Background(), "m", "transcriptA", "s3://fixture-bucket/transcripts/m/transcriptA.txt")
			if !errors.Is(err, ErrVaultSourceTooLarge) || got != "" {
				t.Fatalf("oversized source returned %d bytes, err=%v", len(got), err)
			}
			if !body.closed || body.read > 1*1024*1024+1 {
				t.Fatalf("unbounded or unclosed source: consumed=%d closed=%v", body.read, body.closed)
			}
			if advertised && body.read != 0 {
				t.Fatalf("advertised oversize was read: %d bytes", body.read)
			}
		})
	}
}
