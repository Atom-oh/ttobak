package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagent"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type indexProviderHTTP func(*http.Request) (*http.Response, error)

func (f indexProviderHTTP) Do(req *http.Request) (*http.Response, error) { return f(req) }

func indexHTTPResponse(status int, body string, headers map[string]string) *http.Response {
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}
}
func indexHTTPProvider(t *testing.T, wire indexProviderHTTP) *IndexAWSProvider {
	t.Helper()
	creds := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "test-key", SecretAccessKey: "test-secret"}, nil
	})
	objects := s3.New(s3.Options{Region: "us-east-1", Credentials: creds, HTTPClient: wire, UsePathStyle: true, RetryMaxAttempts: 1})
	ingest := bedrockagent.New(bedrockagent.Options{Region: "us-east-1", Credentials: creds, HTTPClient: wire, RetryMaxAttempts: 1})
	p, err := NewIndexAWSProvider(objects, ingest, "assets", "knowledge", "KB12345678", "DS12345678")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestIndexProviderPinsSourceBytesAndImmutableWrites(t *testing.T) {
	headCalls, getCalls, puts := 0, 0, 0
	p := indexHTTPProvider(t, func(req *http.Request) (*http.Response, error) {
		switch req.Method {
		case http.MethodHead:
			headCalls++
			if req.URL.Path != "/assets/docs/owner/file.pdf" {
				t.Fatal("wrong source bucket/key")
			}
			res := indexHTTPResponse(200, "", map[string]string{"ETag": `"etag-1"`, "Content-Length": "4", "X-Amz-Version-Id": "v1+/="})
			res.ContentLength = 4
			return res, nil
		case http.MethodGet:
			getCalls++
			if req.Header.Get("If-Match") != `"etag-1"` || req.URL.Query().Get("versionId") != "v1+/=" {
				t.Fatalf("unpinned read: %s %v", req.URL, req.Header)
			}
			return indexHTTPResponse(200, "data", map[string]string{"ETag": `"etag-1"`, "X-Amz-Version-Id": "v1+/="}), nil
		case http.MethodPut:
			puts++
			if !strings.HasPrefix(req.URL.Path, "/knowledge/canonical/v1/") || req.Header.Get("If-None-Match") != "*" {
				t.Fatal("projection can overwrite or escape configured bucket")
			}
			if _, err := io.ReadAll(req.Body); err != nil {
				t.Fatal(err)
			}
			return indexHTTPResponse(200, "", nil), nil
		default:
			t.Fatalf("unexpected HTTP: %s", req.Method)
			return nil, nil
		}
	})
	object, err := p.Head(context.Background(), "docs/owner/file.pdf")
	if err != nil {
		t.Fatal(err)
	}
	body, err := p.Read(context.Background(), object)
	if err != nil || string(body) != "data" {
		t.Fatalf("bad pinned read: %q %v", body, err)
	}
	if err := p.Put(context.Background(), "canonical/v1/personalDocument/hash/run/file.pdf", "application/pdf", body); err != nil {
		t.Fatal(err)
	}
	if headCalls != 1 || getCalls != 1 || puts != 1 {
		t.Fatal("unexpected extra calls")
	}
}

func TestIndexProviderUploadErrorsRemainUncertainWithoutSDKRetry(t *testing.T) {
	calls := 0
	p := indexHTTPProvider(t, func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("reply lost after upload")
	})
	err := p.Put(context.Background(), "canonical/v1/meeting/hash/run/meeting.md", "text/markdown", []byte("notes"))
	if !errors.Is(err, ErrIndexWriteUncertain) || calls != 1 {
		t.Fatalf("write uncertainty/retries: %v calls=%d", err, calls)
	}
}

func TestIndexProviderFullSyncTokenConflictAndCompletion(t *testing.T) {
	token := "00000000-0000-4000-8000-000000000001"
	starts := 0
	p := indexHTTPProvider(t, func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPut {
			starts++
			var body map[string]interface{}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["clientToken"] != token || req.URL.Path != "/knowledgebases/KB12345678/datasources/DS12345678/ingestionjobs/" {
				t.Fatalf("wrong submission: %s %+v", req.URL.Path, body)
			}
			if starts == 1 {
				return indexHTTPResponse(409, `{"message":"external job"}`, map[string]string{"Content-Type": "application/json", "X-Amzn-Errortype": "ConflictException"}), nil
			}
			return indexHTTPResponse(202, `{"ingestionJob":{"knowledgeBaseId":"KB12345678","dataSourceId":"DS12345678","ingestionJobId":"JOB1234567","status":"STARTING"}}`, map[string]string{"Content-Type": "application/json"}), nil
		}
		if req.Method != http.MethodGet || !strings.HasSuffix(req.URL.Path, "/JOB1234567") {
			t.Fatalf("unexpected provider call: %s %s", req.Method, req.URL)
		}
		return indexHTTPResponse(200, `{"ingestionJob":{"knowledgeBaseId":"KB12345678","dataSourceId":"DS12345678","ingestionJobId":"JOB1234567","status":"COMPLETE","statistics":{"numberOfDocumentsFailed":1}}}`, map[string]string{"Content-Type": "application/json"}), nil
	})
	if _, err := p.Start(context.Background(), token); !errors.Is(err, ErrIndexSyncBusy) {
		t.Fatalf("conflict became acceptance: %v", err)
	}
	id, err := p.Start(context.Background(), token)
	if err != nil || id != "JOB1234567" {
		t.Fatalf("idempotent retry: %q %v", id, err)
	}
	job, err := p.Get(context.Background(), id)
	if err != nil || job.Failed != 1 || job.Status != "COMPLETE" {
		t.Fatalf("partial failure hidden: %+v %v", job, err)
	}
}

func TestIndexProviderPaginatesSyncDiscoveryAndProjectionCleanup(t *testing.T) {
	lists, objects := 0, 0
	p := indexHTTPProvider(t, func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/ingestionjobs/") {
			lists++
			var body map[string]interface{}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if lists == 1 {
				return indexHTTPResponse(200, `{"ingestionJobSummaries":[],"nextToken":"next"}`, map[string]string{"Content-Type": "application/json"}), nil
			}
			if body["nextToken"] != "next" {
				t.Fatal("lost provider pagination")
			}
			return indexHTTPResponse(200, `{"ingestionJobSummaries":[{"status":"IN_PROGRESS"}]}`, map[string]string{"Content-Type": "application/json"}), nil
		}
		objects++
		if req.URL.Query().Get("prefix") != "canonical/v1/meeting/hash/" {
			t.Fatal("cleanup prefix escaped")
		}
		if objects == 1 {
			return indexHTTPResponse(200, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>next</NextContinuationToken><Contents><Key>canonical/v1/meeting/hash/one</Key></Contents></ListBucketResult>`, nil), nil
		}
		if req.URL.Query().Get("continuation-token") != "next" {
			t.Fatal("lost cleanup pagination")
		}
		return indexHTTPResponse(200, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>canonical/v1/meeting/hash/two</Key></Contents></ListBucketResult>`, nil), nil
	})
	busy, err := p.Busy(context.Background())
	if err != nil || !busy || lists != 2 {
		t.Fatalf("active provider job missed: %v %v %d", busy, err, lists)
	}
	keys, err := p.List(context.Background(), "canonical/v1/meeting/hash/")
	if err != nil || len(keys) != 2 || objects != 2 {
		t.Fatalf("cleanup skipped a page: %v %v", keys, err)
	}
}
