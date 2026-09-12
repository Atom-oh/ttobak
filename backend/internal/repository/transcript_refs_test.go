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
	"github.com/ttobak/backend/internal/model"
)

const transcriptRefVersion = "0123456789abcdef0123456789abcdef"

func TestTranscriptRefFormatsAndBindings(t *testing.T) {
	for _, field := range []string{"transcriptA", "transcriptB", "transcriptSegments"} {
		for _, suffix := range []string{".txt", "." + transcriptRefVersion + ".txt"} {
			key := "transcripts/m1/" + field + suffix
			bucket, got, err := validateTranscriptRef("synthetic", "m1", field, "s3://synthetic/"+key)
			if err != nil || bucket != "synthetic" || got != key {
				t.Fatalf("valid ref %s: bucket=%q key=%q err=%v", key, bucket, got, err)
			}
		}
	}
	base := "s3://synthetic/transcripts/m1/transcriptA"
	invalid := []string{
		"synthetic/transcripts/m1/transcriptA.txt", "S3://synthetic/transcripts/m1/transcriptA.txt",
		"s3:/synthetic/transcripts/m1/transcriptA.txt", "https://synthetic/transcripts/m1/transcriptA.txt",
		"s3://other/transcripts/m1/transcriptA.txt",
		"s3://synthetic.evil/transcripts/m1/transcriptA." + transcriptRefVersion + ".txt",
		"s3://synthetic/transcripts/m10/transcriptA." + transcriptRefVersion + ".txt",
		"s3://synthetic/transcripts/m1/transcriptB." + transcriptRefVersion + ".txt",
		"s3://synthetic/transcripts/m1/../m2/transcriptA.txt",
		"s3://synthetic/transcripts/%6d1/transcriptA.txt",
		"s3://synthetic/transcripts/m1/%74ranscriptA.txt",
		base + "." + strings.Repeat("a", 31) + ".txt",
		base + "." + strings.Repeat("a", 33) + ".txt",
		base + "." + strings.Repeat("A", 32) + ".txt",
		base + "." + strings.Repeat("g", 32) + ".txt",
		base + ".01234567-89ab-cdef-0123-456789abcdef.txt",
		base + ".%30" + transcriptRefVersion[1:] + ".txt",
		base + "..txt", base + "." + transcriptRefVersion + "/other.txt",
		base + "." + transcriptRefVersion + ".txt.bak",
		base + ".txt?versionId=old", base + ".txt#fragment",
		base + "." + transcriptRefVersion + ".txt?versionId=old",
		base + "." + transcriptRefVersion + ".txt#fragment",
		base + "." + transcriptRefVersion + ".txt\n",
	}
	for _, ref := range invalid {
		t.Run(ref, func(t *testing.T) {
			bucket, key, err := validateTranscriptRef("synthetic", "m1", "transcriptA", ref)
			if !errors.Is(err, ErrInvalidTranscriptRef) || bucket != "" || key != "" {
				t.Fatalf("invalid ref accepted: bucket=%q key=%q err=%v", bucket, key, err)
			}
		})
	}
	for _, meetingID := range []string{"", "../m1", "m1/..", "%6d1", "m1?x", "m1#x", "m1\n", strings.Repeat("m", 129)} {
		_, _, err := validateTranscriptRef("synthetic", meetingID, "transcriptA", "s3://synthetic/transcripts/"+meetingID+"/transcriptA.txt")
		if !errors.Is(err, ErrInvalidTranscriptRef) {
			t.Fatalf("invalid meeting ID accepted: %q", meetingID)
		}
	}
	for _, field := range []string{"", "notes", "../transcriptA", "transcriptA.txt", "TranscriptA"} {
		_, _, err := validateTranscriptRef("synthetic", "m1", field, "s3://synthetic/transcripts/m1/"+field+".txt")
		if !errors.Is(err, ErrInvalidTranscriptRef) {
			t.Fatalf("invalid field accepted: %q", field)
		}
	}
	if _, _, err := validateTranscriptRef("", "m1", "transcriptA", base+".txt"); !errors.Is(err, ErrInvalidTranscriptRef) {
		t.Fatal("missing bucket configuration accepted")
	}
}

type transcriptRefBody struct {
	io.Reader
	closed bool
}

func (b *transcriptRefBody) Close() error { b.closed = true; return nil }

// Use real SDK serialization/hydration with synthetic HTTP transports only.
func transcriptRefReadFixture(t *testing.T, storedID, field, ref string, read func(*http.Request) *http.Response) *DynamoDBRepository {
	t.Helper()
	item := map[string]any{"meetingId": map[string]string{"S": storedID}, field: map[string]string{"S": ref}}
	db := dynamodb.New(dynamodb.Options{
		Region: "us-east-1", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			var response any
			switch req.Header.Get("X-Amz-Target") {
			case "DynamoDB_20120810.GetItem":
				response = map[string]any{"Item": item}
			case "DynamoDB_20120810.Query":
				response = map[string]any{"Items": []any{item}}
			default:
				t.Fatalf("unexpected DynamoDB operation: %s", req.Header.Get("X-Amz-Target"))
			}
			body, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
		}),
	})
	objects := s3.New(s3.Options{
		Region: "us-east-1", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1, UsePathStyle: true,
		HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) { return read(req), nil }),
	})
	return NewDynamoDBRepositoryWithS3(db, "synthetic", objects, "synthetic")
}

func TestMeetingReadsLegacyAndVersionedTranscripts(t *testing.T) {
	for _, byID := range []bool{false, true} {
		for _, field := range []string{"transcriptA", "transcriptB", "transcriptSegments"} {
			for _, suffix := range []string{".txt", "." + transcriptRefVersion + ".txt"} {
				key := "transcripts/m1/" + field + suffix
				body := &transcriptRefBody{Reader: strings.NewReader("원문 payload")}
				calls := 0
				repo := transcriptRefReadFixture(t, "m1", field, "s3://synthetic/"+key, func(req *http.Request) *http.Response {
					calls++
					if req.Method != http.MethodGet || req.URL.Path != "/synthetic/"+key {
						t.Fatalf("wrong S3 read: %s %s", req.Method, req.URL)
					}
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: body}
				})
				var meeting *model.Meeting
				var err error
				if byID {
					meeting, err = repo.GetMeetingByID(context.Background(), "m1")
				} else {
					meeting, err = repo.GetMeeting(context.Background(), "owner", "m1")
				}
				if err != nil || meeting == nil {
					t.Fatalf("read %s: %v", key, err)
				}
				values := map[string]string{"transcriptA": meeting.TranscriptA, "transcriptB": meeting.TranscriptB, "transcriptSegments": meeting.TranscriptSegments}
				if values[field] != "원문 payload" || calls != 1 || !body.closed {
					t.Fatalf("ref not hydrated/closed: field=%s value=%q calls=%d closed=%v", field, values[field], calls, body.closed)
				}
			}
		}
	}
}

func TestVersionedTranscriptReadDoesNotFallBackToLegacy(t *testing.T) {
	key := "transcripts/m1/transcriptA." + transcriptRefVersion + ".txt"
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		calls := 0
		repo := transcriptRefReadFixture(t, "m1", "transcriptA", "s3://synthetic/"+key, func(req *http.Request) *http.Response {
			calls++
			if req.URL.Path != "/synthetic/"+key {
				t.Fatalf("read substituted another key: %s", req.URL.Path)
			}
			code := "InternalError"
			if status == http.StatusNotFound {
				code = "NoSuchKey"
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/xml"}}, Body: io.NopCloser(strings.NewReader("<Error><Code>" + code + "</Code></Error>"))}
		})
		meeting, err := repo.GetMeeting(context.Background(), "owner", "m1")
		if calls != 1 {
			t.Fatalf("unexpected fallback/retry count: %d", calls)
		}
		if status == http.StatusNotFound {
			if err != nil || meeting == nil || meeting.TranscriptA != "" {
				t.Fatalf("missing pinned object must retain existing degraded-read policy: meeting=%+v err=%v", meeting, err)
			}
		} else if err == nil {
			t.Fatal("transient read failure was hidden")
		}
	}
}

func TestMeetingRefReadNeverUsesForeignOrMalformedKey(t *testing.T) {
	for _, test := range []struct{ storedID, ref string }{
		{"m1", "s3://other/transcripts/m1/transcriptA." + transcriptRefVersion + ".txt"},
		{"m1", "s3://synthetic/transcripts/m2/transcriptA." + transcriptRefVersion + ".txt"},
		{"m1", "s3://synthetic/transcripts/m1/transcriptB." + transcriptRefVersion + ".txt"},
		{"m1", "s3://synthetic/transcripts/m1/transcriptA." + strings.Repeat("A", 32) + ".txt"},
		{"m1", "s3://synthetic/transcripts/m1/transcriptA." + transcriptRefVersion + ".txt?versionId=old"},
		{"other", "s3://synthetic/transcripts/other/transcriptA.txt"},
		{"other", "s3://synthetic/transcripts/other/transcriptA." + transcriptRefVersion + ".txt"},
	} {
		repo := transcriptRefReadFixture(t, test.storedID, "transcriptA", test.ref, func(*http.Request) *http.Response {
			t.Fatalf("unauthorized ref reached S3: %s", test.ref)
			return nil
		})
		meeting, err := repo.GetMeeting(context.Background(), "owner", "m1")
		if err != nil || meeting == nil || meeting.TranscriptA != "" {
			t.Fatalf("poison ref should be degraded without an S3 read: meeting=%+v err=%v", meeting, err)
		}
	}
}
